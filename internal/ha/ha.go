// Package ha implements the simple high availability of SPEC §11: a primary
// collects; a passive standby never contacts the Elasticsearch clusters and
// pulls from the primary (HTTPS + shared token) the configuration part of
// meta.db and the capture records, which it ingests with the same code. A
// standby with no history, or too far behind for the spool, copies the
// partitions first. It records the instance id of its primary: a primary
// that is another instance, or that comes back without history, is not
// followed (it would empty the standby) until an administrator rebuilds the
// standby. Promotion is manual; an old primary that sees a more
// recent promotion on its peer stops collecting ("fenced") until an
// administrator demotes it.
package ha

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"estopo/internal/clock"
	"estopo/internal/record"
	"estopo/internal/store"
)

// Roles.
const (
	RolePrimary = "primary"
	RoleStandby = "standby"
	RoleFenced  = "fenced" // old primary: a more recent promotion exists on its peer
)

// Hooks connect the manager to the rest of the application.
type Hooks struct {
	Ingest  func(r *record.Record) error // spool + ingestion queue
	Idle    func() bool                  // everything queued is ingested
	LastTs  func(ctx context.Context, cluster string) (int64, error)
	Forget  func(cluster string)            // drop the ingester state of a cluster
	Changed func(ctx context.Context) error // configuration or role changed
}

// Options for New.
type Options struct {
	Store         *store.Store
	Spool         *record.Spool
	Clock         clock.Clock
	Log           *slog.Logger
	Hooks         Hooks
	StandbyOf     string        // ESTOPO_STANDBY_OF: first start as a standby of this primary
	TokenFile     string        // ESTOPO_REPLICATION_TOKEN_FILE
	CAFile        string        // ESTOPO_REPLICATION_CA: CA of the primary's certificate (default: system CAs)
	AllowInsecure bool          // replication token over plain HTTP to a non-loopback host
	Interval      time.Duration // replication and peer check period (default 30 s)
	TmpDir        string        // partition snapshots in transit
}

// State is the local HA state (meta.db ha_state, never replicated).
type State struct {
	Role         string `json:"role"`
	StandbyOf    string `json:"standby_of,omitempty"`  // standby: URL of the primary
	Peer         string `json:"peer,omitempty"`        // primary: URL of the standby, watched for promotions
	PromotedMs   int64  `json:"promoted_ms,omitempty"` // this instance was promoted at
	FencedReason string `json:"fenced_reason,omitempty"`
}

// ReplicationStatus describes the standby side.
type ReplicationStatus struct {
	LastSyncMs  int64         `json:"last_sync_ms,omitempty"` // last complete synchronisation
	LastError   string        `json:"last_error,omitempty"`
	LastErrorMs int64         `json:"last_error_ms,omitempty"`
	LagMs       int64         `json:"lag_ms"` // newest record on the primary − newest ingested here (max over clusters)
	Rebuilding  bool          `json:"rebuilding,omitempty"`
	Clusters    []ClusterRepl `json:"clusters"`
	// PrimaryID is the instance id of the primary this standby follows,
	// recorded at the first synchronisation.
	PrimaryID string `json:"primary_id,omitempty"`
	// Blocked: replication is stopped, and nothing is copied nor purged,
	// until an administrator asks for a rebuild (or demotes the instance
	// towards another primary). The reason is given: the primary is not the
	// instance followed so far, or it holds no history while this one does.
	Blocked string `json:"blocked,omitempty"`
}

// ClusterRepl is the replication state of one cluster.
type ClusterRepl struct {
	Cluster   string `json:"cluster"`
	CursorMs  int64  `json:"cursor_ms"`  // newest record ingested here
	PrimaryMs int64  `json:"primary_ms"` // newest record in the primary's spool
	Records   int    `json:"records"`    // records received since start
	Rebuilds  int    `json:"rebuilds"`   // partition copies since start
}

// PeerCheck describes the primary side watch of its peer.
type PeerCheck struct {
	LastMs    int64  `json:"last_ms,omitempty"`
	PeerRole  string `json:"peer_role,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// Status is what the admin screen shows.
type Status struct {
	State
	InstanceID      string             `json:"instance_id"` // random, created with the data directory
	TokenConfigured bool               `json:"token_configured"`
	Replication     *ReplicationStatus `json:"replication,omitempty"`
	PeerCheck       *PeerCheck         `json:"peer_check,omitempty"`
}

// Manager holds the role of the instance and runs the replication.
type Manager struct {
	o     Options
	token string

	// instanceID identifies this data directory (ha_state, created once): a
	// primary that lost its data comes back with another one.
	instanceID string
	stall      time.Duration // partition download: longest wait for a byte
	idleWait   time.Duration // longest wait for the ingestion queue to drain

	mu         sync.Mutex
	st         State
	primaryID  string // standby: instance id of the primary followed ("" = not recorded yet)
	repl       ReplicationStatus
	peer       PeerCheck
	repls      map[string]*ClusterRepl
	cursors    map[string]int64
	rebuild    bool
	metaETag   string
	configHash string // of the configuration tables last applied
	// A promotion cancels the synchronisation in progress: it must not wait
	// for a primary that no longer answers.
	syncCancel context.CancelFunc
	promoting  bool

	syncMu sync.Mutex // one synchronisation (or promotion) at a time
	kick   chan struct{}
}

// New loads the local state. ESTOPO_STANDBY_OF only applies to the first
// start: afterwards the state in meta.db wins (a promoted standby keeps its
// role even if the variable is still set).
func New(ctx context.Context, o Options) (*Manager, error) {
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	if o.Interval <= 0 {
		o.Interval = 30 * time.Second
	}
	m := &Manager{o: o, repls: map[string]*ClusterRepl{}, cursors: map[string]int64{}, kick: make(chan struct{}, 1),
		stall: stallTimeout, idleWait: time.Minute}
	if o.CAFile != "" {
		if _, err := loadCA(o.CAFile); err != nil {
			return nil, err
		}
	}
	if o.TokenFile != "" {
		b, err := os.ReadFile(o.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("replication token: %w", err)
		}
		m.token = strings.TrimSpace(string(b))
		if len(m.token) < 16 {
			return nil, fmt.Errorf("replication token %s: at least 16 characters are required", o.TokenFile)
		}
	}
	kv, err := o.Store.HAState(ctx)
	if err != nil {
		return nil, err
	}
	m.st = State{Role: kv["role"], StandbyOf: kv["standby_of"], Peer: kv["peer"], FencedReason: kv["fenced_reason"]}
	m.st.PromotedMs, _ = strconv.ParseInt(kv["promoted_ms"], 10, 64)
	m.instanceID, m.primaryID = kv["instance_id"], kv["primary_id"]
	if m.instanceID == "" {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		m.instanceID = hex.EncodeToString(b)
		if err := o.Store.SetHAState(ctx, map[string]string{"instance_id": m.instanceID}); err != nil {
			return nil, err
		}
	}
	switch {
	case m.st.Role == "" && o.StandbyOf != "":
		st := State{Role: RoleStandby, StandbyOf: strings.TrimRight(o.StandbyOf, "/")}
		if err := m.persist(ctx, st, ""); err != nil {
			return nil, err
		}
		m.st, m.rebuild = st, true
	case m.st.Role == "":
		m.st.Role = RolePrimary
	case o.StandbyOf != "" && m.st.Role != RoleStandby:
		o.Log.Warn("ESTOPO_STANDBY_OF ignored: the local HA state says this instance is "+m.st.Role, "standby_of", o.StandbyOf)
	}
	if m.st.Role == RoleStandby {
		if m.token == "" {
			return nil, errors.New("a standby needs ESTOPO_REPLICATION_TOKEN_FILE")
		}
		if err := checkURL(m.st.StandbyOf, o.AllowInsecure); err != nil {
			return nil, fmt.Errorf("primary URL: %w", err)
		}
	}
	return m, nil
}

// checkURL accepts https URLs, and http ones only towards loopback (or when
// explicitly allowed): the replication token must not travel in clear.
func checkURL(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid URL %q", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) || allowInsecure {
			return nil
		}
		return fmt.Errorf("%s: the replication token would travel in clear, use https (or ESTOPO_ALLOW_INSECURE_AUTH=true)", raw)
	}
	return fmt.Errorf("%s: the scheme must be https", raw)
}

// persist writes a state, with the id of the primary followed, to meta.db.
// The state in memory changes only once this has succeeded: memory and disk
// must not disagree on the role.
func (m *Manager) persist(ctx context.Context, st State, primaryID string) error {
	promoted := ""
	if st.PromotedMs > 0 {
		promoted = strconv.FormatInt(st.PromotedMs, 10)
	}
	return m.o.Store.SetHAState(ctx, map[string]string{"role": st.Role, "standby_of": st.StandbyOf, "peer": st.Peer,
		"promoted_ms": promoted, "fenced_reason": st.FencedReason, "primary_id": primaryID})
}

// Role returns the current role.
func (m *Manager) Role() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st.Role
}

// Collecting reports whether this instance may contact the clusters.
func (m *Manager) Collecting() bool { return m.Role() == RolePrimary }

// Status returns the state for the admin screen.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := Status{State: m.st, InstanceID: m.instanceID, TokenConfigured: m.token != ""}
	switch m.st.Role {
	case RoleStandby:
		r := m.repl
		r.PrimaryID = m.primaryID
		r.Clusters = []ClusterRepl{}
		for _, c := range m.repls {
			r.Clusters = append(r.Clusters, *c)
		}
		sortRepl(r.Clusters)
		s.Replication = &r
	default:
		if m.st.Peer != "" {
			p := m.peer
			s.PeerCheck = &p
		}
	}
	return s
}

// Run synchronises (standby) or watches the peer (primary) until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	for {
		switch m.Role() {
		case RoleStandby:
			if err := m.SyncOnce(ctx); err != nil && ctx.Err() == nil {
				m.o.Log.Warn("replication", "err", err)
			}
		case RolePrimary:
			m.CheckPeer(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-m.kick:
		case <-m.o.Clock.After(m.o.Interval):
		}
	}
}

func (m *Manager) wake() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Manager) audit(ctx context.Context, actor, action, target, details string) {
	if err := m.o.Store.Audit(ctx, actor, action, target, details); err != nil {
		m.o.Log.Error("audit", "err", err)
	}
}

// ErrRole is returned when an action does not apply to the current role.
var ErrRole = errors.New("not possible in the current role")

// Promote turns a standby into the primary (SPEC §11: manual promotion).
// The history holds every record received so far.
func (m *Manager) Promote(ctx context.Context, actor string) error {
	m.mu.Lock()
	if m.st.Role != RoleStandby {
		m.mu.Unlock()
		return fmt.Errorf("%w: only a standby can be promoted", ErrRole)
	}
	// The synchronisation in progress is abandoned: it may be waiting for a
	// primary that is gone, and what it was copying is not installed.
	m.promoting = true
	if m.syncCancel != nil {
		m.syncCancel()
	}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.promoting = false
		m.mu.Unlock()
	}()
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	m.mu.Lock()
	old, role := m.st.StandbyOf, m.st.Role
	m.mu.Unlock()
	if role != RoleStandby { // promoted meanwhile by another request
		return fmt.Errorf("%w: only a standby can be promoted", ErrRole)
	}
	if err := m.waitIdle(ctx); err != nil {
		// Not a reason to refuse a failover: the queue keeps the order, what
		// was received is ingested before anything collected from now on.
		m.o.Log.Warn("promotion while records received from the primary are still being ingested", "err", err)
	}
	st := State{Role: RolePrimary, Peer: old, PromotedMs: m.o.Clock.Now().UnixMilli()}
	if err := m.persist(ctx, st, ""); err != nil {
		return err
	}
	m.mu.Lock()
	m.st, m.primaryID = st, ""
	m.mu.Unlock()
	m.audit(ctx, actor, "ha.promote", old, "")
	m.o.Log.Warn("promoted to primary: collection starts", "old_primary", old)
	return m.o.Hooks.Changed(ctx)
}

// Demote turns this instance (primary or fenced) into a standby of
// primaryURL (the peer by default). Its history is rebuilt from the new
// primary: records it holds alone are dropped.
func (m *Manager) Demote(ctx context.Context, actor, primaryURL string) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	m.mu.Lock()
	if m.st.Role == RoleStandby {
		m.mu.Unlock()
		return fmt.Errorf("%w: already a standby", ErrRole)
	}
	if primaryURL == "" {
		primaryURL = m.st.Peer
	}
	primaryURL = strings.TrimRight(primaryURL, "/")
	m.mu.Unlock()
	if m.token == "" {
		return errors.New("a standby needs ESTOPO_REPLICATION_TOKEN_FILE")
	}
	if err := checkURL(primaryURL, m.o.AllowInsecure); err != nil {
		return err
	}
	st := State{Role: RoleStandby, StandbyOf: primaryURL}
	if err := m.persist(ctx, st, ""); err != nil {
		return err
	}
	m.mu.Lock()
	m.st, m.primaryID = st, "" // the identity of the new primary is recorded at the first synchronisation
	m.rebuild, m.metaETag, m.configHash = true, "", ""
	m.cursors, m.repls, m.repl = map[string]int64{}, map[string]*ClusterRepl{}, ReplicationStatus{}
	m.mu.Unlock()
	m.audit(ctx, actor, "ha.demote", primaryURL, "")
	m.o.Log.Warn("demoted to standby: collection stops, history rebuilt from the primary", "primary", primaryURL)
	err := m.o.Hooks.Changed(ctx)
	m.wake()
	return err
}

// Rebuild asks a standby to copy the partitions again (SPEC §11). It is
// also how an administrator accepts a primary that is not the instance
// followed so far, or that holds no history: the standby then becomes what
// that primary is.
func (m *Manager) Rebuild(ctx context.Context, actor string) error {
	m.mu.Lock()
	st := m.st
	m.mu.Unlock()
	if st.Role != RoleStandby {
		return fmt.Errorf("%w: only a standby is rebuilt", ErrRole)
	}
	if err := m.persist(ctx, st, ""); err != nil {
		return err
	}
	m.mu.Lock()
	m.rebuild, m.primaryID = true, ""
	m.mu.Unlock()
	m.audit(ctx, actor, "ha.rebuild", "", "")
	m.wake()
	return nil
}

// SetPeer sets the URL of the standby a primary watches.
func (m *Manager) SetPeer(ctx context.Context, actor, peer string) error {
	peer = strings.TrimRight(strings.TrimSpace(peer), "/")
	if peer != "" {
		if err := checkURL(peer, m.o.AllowInsecure); err != nil {
			return err
		}
	}
	m.mu.Lock()
	if m.st.Role == RoleStandby {
		m.mu.Unlock()
		return fmt.Errorf("%w: a standby follows ESTOPO_STANDBY_OF", ErrRole)
	}
	st := m.st
	st.Peer = peer
	err := m.persist(ctx, st, "")
	if err == nil {
		m.st, m.peer = st, PeerCheck{}
	}
	m.mu.Unlock()
	if err == nil {
		m.audit(ctx, actor, "ha.peer", peer, "")
		m.wake()
	}
	return err
}

// Resume lets a fenced instance collect again, when its peer no longer
// claims a more recent promotion.
func (m *Manager) Resume(ctx context.Context, actor string) error {
	m.mu.Lock()
	if m.st.Role != RoleFenced {
		m.mu.Unlock()
		return fmt.Errorf("%w: only a fenced instance resumes", ErrRole)
	}
	st := m.st
	m.mu.Unlock()
	if st.Peer != "" {
		if ps, err := m.client(st.Peer).status(ctx); err == nil && ps.Role == RolePrimary && ps.PromotedMs > st.PromotedMs {
			return fmt.Errorf("%s is still the primary (promoted at %s): demote it first", st.Peer,
				time.UnixMilli(ps.PromotedMs).UTC().Format(time.RFC3339))
		}
	}
	st.Role, st.FencedReason = RolePrimary, ""
	if err := m.persist(ctx, st, ""); err != nil {
		return err
	}
	m.mu.Lock()
	m.st = st
	m.mu.Unlock()
	m.audit(ctx, actor, "ha.resume", "", "")
	return m.o.Hooks.Changed(ctx)
}

// CheckPeer is the primary side watch: a peer promoted more recently than
// this instance means this one is an old primary; it stops collecting
// (SPEC §11). The most recent promotion wins, so two instances never fence
// each other.
func (m *Manager) CheckPeer(ctx context.Context) {
	m.mu.Lock()
	st := m.st
	m.mu.Unlock()
	if st.Role != RolePrimary || st.Peer == "" || m.token == "" {
		return
	}
	ps, err := m.client(st.Peer).status(ctx)
	now := m.o.Clock.Now().UnixMilli()
	m.mu.Lock()
	m.peer.LastMs = now
	if err != nil {
		m.peer.LastError = err.Error()
		m.mu.Unlock()
		return
	}
	m.peer.LastError, m.peer.PeerRole = "", ps.Role
	if ps.Role != RolePrimary || ps.PromotedMs <= st.PromotedMs || m.st.Role != RolePrimary {
		m.mu.Unlock()
		return
	}
	// Fenced in memory even if it cannot be written: collecting beside a more
	// recent primary is the worse outcome, and the next start checks again.
	m.st.Role = RoleFenced
	m.st.FencedReason = fmt.Sprintf("%s was promoted at %s", st.Peer, time.UnixMilli(ps.PromotedMs).UTC().Format(time.RFC3339))
	err = m.persist(ctx, m.st, "")
	reason := m.st.FencedReason
	m.mu.Unlock()
	if err != nil {
		m.o.Log.Error("save HA state", "err", err)
	}
	m.o.Log.Error("a more recent primary exists: collection suspended until this instance is demoted", "reason", reason)
	m.audit(ctx, "system", "ha.fenced", st.Peer, reason)
	if err := m.o.Hooks.Changed(ctx); err != nil {
		m.o.Log.Error("stop collection", "err", err)
	}
}

// CheckToken compares a presented replication token (constant time).
func (m *Manager) CheckToken(got string) bool {
	return m.token != "" && subtle.ConstantTimeCompare([]byte(got), []byte(m.token)) == 1
}

// waitIdle waits until every record handed to the ingestion is ingested.
func (m *Manager) waitIdle(ctx context.Context) error {
	deadline := time.Now().Add(m.idleWait)
	for !m.o.Hooks.Idle() {
		if time.Now().After(deadline) {
			return fmt.Errorf("the ingestion is still busy after %s", m.idleWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}
