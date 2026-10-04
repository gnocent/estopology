package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"estopo/internal/derive"
	"estopo/internal/esclient"
	"estopo/internal/record"
)

// The ingestion turns records into validity intervals (SPEC §6.3, §6.4):
// only changes are written. Each cluster's open intervals are mirrored in
// memory (cstate); the database stays the reference: after any error the
// mirror is dropped and reloaded from the current partition.

// DeadBand configures when a metric is rewritten (SPEC §4, §6.3).
type DeadBand struct {
	Rel      float64 // relative change of sizes and doc counts (default 5 %)
	AbsBytes int64   // absolute size change always written (default 1 GiB)
	CPUPts   int     // CPU points (default 5)
	HeapPts  int     // heap points (default 5)
	DiskRel  float64 // node disk usage relative change (default 1 %)
}

// DefaultDeadBand returns the SPEC defaults.
func DefaultDeadBand() DeadBand {
	return DeadBand{Rel: 0.05, AbsBytes: 1 << 30, CPUPts: 5, HeapPts: 5, DiskRel: 0.01}
}

// Options configures the ingestion.
type Options struct {
	// MachineAttr returns the node attribute naming the machine of a
	// cluster ("" = the node host, SPEC §5.4).
	MachineAttr func(cluster string) string
	// Family returns the family of an index (default: automatic detection).
	Family   func(cluster, index string) string
	DeadBand DeadBand
}

// Ingester writes records into the store. It is safe for concurrent use;
// records of one cluster must be ingested in capture order.
type Ingester struct {
	st  *Store
	opt Options
	log *slog.Logger

	mu       sync.Mutex
	clusters map[string]*cstate
	locks    map[string]*sync.Mutex
}

// NewIngester returns an ingester writing into st.
func NewIngester(st *Store, opt Options, log *slog.Logger) *Ingester {
	if opt.MachineAttr == nil {
		opt.MachineAttr = func(string) string { return "" }
	}
	if opt.Family == nil {
		opt.Family = func(_, index string) string { return derive.DetectFamily(index) }
	}
	if opt.DeadBand == (DeadBand{}) {
		opt.DeadBand = DefaultDeadBand()
	}
	return &Ingester{st: st, opt: opt, log: log, clusters: map[string]*cstate{}, locks: map[string]*sync.Mutex{}}
}

// --- in-memory mirror ---------------------------------------------------------

type nodeKey struct{ name, esID string }

type nodeInfoVals struct {
	roles, ip, transport, http, host, machine, version, osName, kernel, arch, jvm string
	processors                                                                    int
	startMs                                                                       int64
	attrs                                                                         string // blob hash of the attributes
	attrMap                                                                       map[string]string
}

type nodeMetricVals struct {
	procCPU, osCPU, heap                              int
	load1                                             float64
	cgroupNs, diskTotal, diskUsed, diskAvail, diskIdx int64
	shards                                            int64
	ioROps, ioWOps, ioRKB, ioWKB                      int64
}

type nodeState struct {
	key          nodeKey
	gen          int
	firstSeen    int64
	id           int64 // row id in the current partition
	present      bool
	presenceRow  int64
	presentSince int64
	info         *nodeInfoVals
	prevInfo     *nodeInfoVals // last known info of a node absent from the last full capture
	infoRow      int64
	infoSince    int64
	metric       *nodeMetricVals
	metricTs     int64
}

type indexIV struct {
	status      string
	pri, rep    int
	settings    string // blob hash ("" = none)
	settingsMap map[string]string
}

type indexMetricVals struct{ docs, store, priStore int64 }

type indexState struct {
	uuid, name, family string
	creation           int64
	versionCreated     string
	hidden             bool
	id                 int64
	iv                 *indexIV
	ivRow              int64
	ivSince            int64
	metric             *indexMetricVals
	metricTs           int64
}

type shardKey struct {
	uuid    string
	shard   int
	primary bool
	node    nodeKey // zero when unassigned
	slot    int
}

type shardVals struct {
	state   string
	target  nodeKey // relocation target
	reason  string
	at      int64
	details string // blob hash
}

type shardState struct {
	key      shardKey
	vals     shardVals
	row      int64
	since    int64
	docs     int64 // last written metric, esclient.Missing if none
	store    int64
	metricTs int64
}

type remoteVals struct {
	mode      string
	connected bool
	nodes     int
	config    string // blob hash (seeds / proxy address)
}

type remoteState struct {
	vals  remoteVals
	row   int64
	since int64
}

type cstate struct {
	id   string
	part *Partition

	lastTs         int64 // last record
	lastPresenceTs int64 // last probe with node presence
	lastFullTs     int64 // last structural capture
	lastHealth     *esclient.Health
	lastHealthTs   int64

	nodes   map[nodeKey]*nodeState
	nodeGen map[string]int // highest generation per node name
	nodeBy  map[int64]*nodeState
	indices map[string]*indexState // by uuid
	shards  map[shardKey]*shardState

	settingsHash  string
	settingsMap   map[string]string // "p:key" / "t:key"
	settingsRow   int64
	settingsSince int64
	defaultsHash  string

	remotes map[string]*remoteState
}

func newCState(id string) *cstate {
	return &cstate{id: id, nodes: map[nodeKey]*nodeState{}, nodeGen: map[string]int{}, nodeBy: map[int64]*nodeState{},
		indices: map[string]*indexState{}, shards: map[shardKey]*shardState{}, remotes: map[string]*remoteState{}}
}

// --- ingestion ------------------------------------------------------------------

// Result summarizes one ingestion.
type Result struct {
	Keyframe     bool
	Events       int
	ShardsOpened int
	ShardsClosed int
}

func (in *Ingester) clusterLock(id string) *sync.Mutex {
	in.mu.Lock()
	defer in.mu.Unlock()
	l, ok := in.locks[id]
	if !ok {
		l = &sync.Mutex{}
		in.locks[id] = l
	}
	return l
}

// Ingest writes one record in a single transaction.
func (in *Ingester) Ingest(ctx context.Context, r *record.Record) (res Result, err error) {
	if r.Format != record.FormatVersion {
		return res, record.ErrFormat
	}
	l := in.clusterLock(r.Cluster)
	l.Lock()
	defer l.Unlock()

	cs, err := in.state(ctx, r.Cluster)
	if err != nil {
		return res, err
	}
	if r.StartMs < cs.lastTs {
		return res, fmt.Errorf("record of %s at %d is older than the last ingested (%d)", r.Cluster, r.StartMs, cs.lastTs)
	}
	defer func() {
		if err != nil { // the mirror may be half-updated: rebuild it from the database next time
			in.mu.Lock()
			delete(in.clusters, r.Cluster)
			in.mu.Unlock()
		}
	}()

	part, err := in.st.Partition(ctx, r.Cluster, r.StartMs)
	if err != nil {
		return res, err
	}
	var old *Partition
	if cs.part != part {
		old = cs.part
		res.Keyframe = cs.part != nil || cs.lastTs > 0
		cs.part = part
	}
	if res.Keyframe {
		in.pruneNodes(ctx, cs, part)
	}

	tx, err := part.db.BeginTx(ctx, nil)
	if err != nil {
		return res, err
	}
	w := &writer{in: in, ctx: ctx, tx: tx, p: part, cs: cs, ts: r.StartMs, stmts: map[string]*sql.Stmt{}, res: &res,
		createdIdx: map[string]bool{}, deletedIdx: map[string]bool{}, repDecreased: map[string]bool{}}
	defer w.closeStmts()
	if err = w.ingest(r, res.Keyframe, old); err != nil {
		tx.Rollback()
		part.blobs = map[string]bool{}
		return res, err
	}
	if err = tx.Commit(); err != nil {
		part.blobs = map[string]bool{}
		return res, err
	}
	cs.lastTs = r.StartMs
	if old != nil {
		if serr := in.st.Seal(ctx, old); serr != nil {
			in.log.Warn("seal partition", "path", old.Path, "err", serr)
		}
	}
	if r.Kind == record.Full {
		in.st.RefreshSize(ctx, part)
	}
	return res, nil
}

// LastTs returns the date of the last record ingested for a cluster (0 if
// none): the spool is replayed from there after a restart.
func (in *Ingester) LastTs(ctx context.Context, cluster string) (int64, error) {
	l := in.clusterLock(cluster)
	l.Lock()
	defer l.Unlock()
	cs, err := in.state(ctx, cluster)
	if err != nil {
		return 0, err
	}
	return cs.lastTs, nil
}

// Note writes an event that does not come from a record (e.g. the
// collector suspended a cluster whose identity changed, SPEC §5.5) into the
// partition of `at`.
func (in *Ingester) Note(ctx context.Context, cluster string, at int64, typ string, severity int, payload any) error {
	l := in.clusterLock(cluster)
	l.Lock()
	defer l.Unlock()
	part, err := in.st.Partition(ctx, cluster, at)
	if err != nil {
		return err
	}
	tx, err := part.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	w := &writer{in: in, ctx: ctx, tx: tx, p: part, cs: newCState(cluster), ts: at, stmts: map[string]*sql.Stmt{}, res: &Result{}}
	defer w.closeStmts()
	if err := w.event(typ, severity, "cluster", cluster, "", 0, 0, payload); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// EventClusterUUIDChanged is the type of the event written by Note when a
// cluster changed identity.
const EventClusterUUIDChanged = evClusterUUIDChanged

// state returns the in-memory mirror of a cluster, loading it from its
// latest partition if needed.
func (in *Ingester) state(ctx context.Context, id string) (*cstate, error) {
	in.mu.Lock()
	cs, ok := in.clusters[id]
	in.mu.Unlock()
	// A mirror whose partition the store closed (purge, retention, partition
	// replaced by a copy) describes a history that is gone: reload.
	if ok && (cs.part == nil || !cs.part.closed.Load()) {
		return cs, nil
	}
	cs, err := in.load(ctx, id)
	if err != nil {
		return nil, err
	}
	in.mu.Lock()
	in.clusters[id] = cs
	in.mu.Unlock()
	return cs, nil
}

// writer holds one ingestion transaction.
type writer struct {
	in    *Ingester
	ctx   context.Context
	tx    *sql.Tx
	p     *Partition
	cs    *cstate
	ts    int64
	stmts map[string]*sql.Stmt
	res   *Result

	// Index changes of this capture (shard events ignore them).
	createdIdx, deletedIdx, repDecreased map[string]bool
}

func (w *writer) exec(query string, args ...any) (sql.Result, error) {
	st, ok := w.stmts[query]
	if !ok {
		var err error
		st, err = w.tx.PrepareContext(w.ctx, query)
		if err != nil {
			return nil, fmt.Errorf("prepare %q: %w", query, err)
		}
		w.stmts[query] = st
	}
	return st.ExecContext(w.ctx, args...)
}

func (w *writer) insert(query string, args ...any) (int64, error) {
	r, err := w.exec(query, args...)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (w *writer) closeStmts() {
	for _, s := range w.stmts {
		s.Close()
	}
}

// blobJSON stores v as canonical JSON and returns its hash.
func (w *writer) blobJSON(v any) (string, error) {
	b, err := json.Marshal(v) // maps are marshalled with sorted keys
	if err != nil {
		return "", err
	}
	return w.p.putBlob(w.ctx, w.tx, b)
}

func (w *writer) ingest(r *record.Record, keyframe bool, old *Partition) error {
	if err := w.ingestBody(r, keyframe, old); err != nil {
		return err
	}
	return w.saveMarkers()
}

func (w *writer) ingestBody(r *record.Record, keyframe bool, old *Partition) error {
	if keyframe {
		if err := w.keyframe(old); err != nil {
			return fmt.Errorf("keyframe: %w", err)
		}
	}
	if err := w.capture(r); err != nil {
		return err
	}
	if r.Status == record.Failed {
		return w.event(evCaptureFailed, sevMajor, "capture", w.cs.id, "", w.cs.lastTs, 0,
			map[string]any{"kind": r.Kind, "error": r.Error})
	}
	if r.Status == record.Partial && r.Kind == record.Full {
		if err := w.event(evCaptureFailed, sevMinor, "capture", w.cs.id, "", w.cs.lastTs, 0,
			map[string]any{"kind": r.Kind, "status": r.Status, "error": r.Error}); err != nil {
			return err
		}
	}
	if r.Health != nil {
		if err := w.health(r.Health); err != nil {
			return err
		}
	}
	if r.NodesStats != nil {
		if err := w.presence(r.NodesStats); err != nil {
			return err
		}
		if err := w.nodeMetrics(r.NodesStats, r.Allocation); err != nil {
			return err
		}
	}
	if r.ClusterSettings != nil {
		if err := w.clusterSettings(r.ClusterSettings); err != nil {
			return err
		}
	}
	if r.Remotes != nil && !r.RemotesUnavailable {
		if err := w.remotes(r.Remotes); err != nil {
			return err
		}
	}
	if r.HasStructure() {
		if err := w.structure(r); err != nil {
			return err
		}
		w.cs.lastFullTs = w.ts
	}
	return nil
}

func (w *writer) capture(r *record.Record) error {
	stats, _ := json.Marshal(r.Calls)
	_, err := w.exec(`INSERT INTO capture(ts, end_ts, kind, trigger, status, duration_ms, api_stats, error) VALUES (?,?,?,?,?,?,?,?)`,
		r.StartMs, r.EndMs, string(r.Kind), r.Trigger, string(r.Status), r.EndMs-r.StartMs, string(stats), nullStr(r.Error))
	return err
}

func (w *writer) health(h *esclient.Health) error {
	_, err := w.exec(`INSERT OR REPLACE INTO health(ts, status, nodes, data_nodes, active_primary, active, relocating, initializing,
		unassigned, unassigned_primary, delayed_unassigned, pending_tasks) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		w.ts, h.Status, h.Nodes, h.DataNodes, h.ActivePrimary, h.Active, h.Relocating, h.Initializing, h.Unassigned,
		nullMissing(int64(h.UnassignedPrimary)), h.DelayedUnassigned, h.PendingTasks)
	if err != nil {
		return err
	}
	cs := w.cs
	if cs.lastHealth != nil && cs.lastHealth.Status != h.Status {
		sev := sevInfo
		switch h.Status {
		case "red":
			sev = sevCritical
		case "yellow":
			sev = sevMajor
		}
		if err := w.event(evHealthChanged, sev, "cluster", cs.id, "", cs.lastHealthTs, 0,
			map[string]any{"from": cs.lastHealth.Status, "to": h.Status}); err != nil {
			return err
		}
	}
	hc := *h
	cs.lastHealth, cs.lastHealthTs = &hc, w.ts
	return nil
}

// node returns the state of a node, creating its dimension row (and a new
// generation when the name is known with another ES id, SPEC §5.4).
func (w *writer) node(k nodeKey) (*nodeState, bool, error) {
	cs := w.cs
	if ns, ok := cs.nodes[k]; ok {
		return ns, false, nil
	}
	gen := cs.nodeGen[k.name] + 1
	ns := &nodeState{key: k, gen: gen, firstSeen: w.ts}
	id, err := w.insert(`INSERT INTO node(name, es_id, generation, first_seen) VALUES (?,?,?,?)`, k.name, k.esID, gen, w.ts)
	if err != nil {
		return nil, false, err
	}
	ns.id = id
	cs.nodes[k] = ns
	cs.nodeBy[id] = ns
	cs.nodeGen[k.name] = gen
	return ns, true, nil
}

// presence opens and closes node presence intervals (5-minute resolution).
func (w *writer) presence(stats []esclient.NodeStats) error {
	cs := w.cs
	if err := w.duplicateNames(stats); err != nil {
		return err
	}
	seen := map[nodeKey]bool{}
	for _, s := range stats {
		k := nodeKey{s.Name, s.ID}
		seen[k] = true
		ns, created, err := w.node(k)
		if err != nil {
			return err
		}
		if ns.present {
			continue
		}
		row, err := w.insert(`INSERT INTO node_presence(node_id, valid_from) VALUES (?,?)`, ns.id, w.ts)
		if err != nil {
			return err
		}
		ns.present, ns.presenceRow, ns.presentSince = true, row, w.ts
		typ := evNodeJoined
		payload := map[string]any{"es_id": k.esID, "generation": ns.gen}
		if created && ns.gen > 1 {
			typ = evNodeNewGeneration
		}
		if cs.lastPresenceTs > 0 || ns.gen > 1 { // not for the very first probe
			if err := w.event(typ, sevForNodeEvent(typ), "node", k.name, "", cs.lastPresenceTs, s.TimestampMs-uptimeOr0(s), payload); err != nil {
				return err
			}
		}
	}
	var left []*nodeState
	for k, ns := range cs.nodes {
		if ns.present && !seen[k] {
			left = append(left, ns)
		}
	}
	sort.Slice(left, func(i, j int) bool { return left[i].key.name+left[i].key.esID < left[j].key.name+left[j].key.esID })
	for _, ns := range left {
		if _, err := w.exec(`UPDATE node_presence SET valid_to = ? WHERE rowid = ?`, w.ts, ns.presenceRow); err != nil {
			return err
		}
		ns.present = false
		if err := w.event(evNodeLeft, sevMajor, "node", ns.key.name, machineOf(ns), cs.lastPresenceTs, 0,
			map[string]any{"es_id": ns.key.esID, "generation": ns.gen}); err != nil {
			return err
		}
	}
	if err := w.machinesLost(left); err != nil { // before moving lastPresenceTs (ts_lo)
		return err
	}
	cs.lastPresenceTs = w.ts
	return nil
}

// uptimeOr0 is the node uptime, so that timestamp - uptime gives the exact
// start date; 0 when unknown (no exact date).
func uptimeOr0(s esclient.NodeStats) int64 {
	if s.UptimeMs <= 0 || s.TimestampMs <= 0 {
		return s.TimestampMs
	}
	return s.UptimeMs
}

func machineOf(ns *nodeState) string {
	if ns.info != nil {
		return ns.info.machine
	}
	return ""
}

func (w *writer) nodeMetrics(stats []esclient.NodeStats, alloc []esclient.Allocation) error {
	byName := map[string]esclient.Allocation{}
	for _, a := range alloc {
		byName[a.Node] = a
	}
	db := w.in.opt.DeadBand
	for _, s := range stats {
		ns := w.cs.nodes[nodeKey{s.Name, s.ID}]
		a, hasAlloc := byName[s.Name]
		v := nodeMetricVals{
			procCPU: s.ProcCPU, osCPU: s.OSCPU, heap: s.HeapPct, load1: s.Load1, cgroupNs: s.CgroupCPUUsageNanos,
			diskTotal: s.FSTotal, diskUsed: esclient.Missing, diskAvail: s.FSAvail, diskIdx: esclient.Missing, shards: esclient.Missing,
			ioROps: s.IOReadOps, ioWOps: s.IOWriteOps, ioRKB: s.IOReadKB, ioWKB: s.IOWriteKB,
		}
		if s.FSTotal > 0 && s.FSFree >= 0 {
			v.diskUsed = s.FSTotal - s.FSFree
		}
		if hasAlloc {
			v.diskIdx, v.shards = a.DiskIndices, int64(a.Shards)
			if a.DiskUsed >= 0 {
				v.diskUsed = a.DiskUsed
			}
		}
		if ns.metric != nil && !nodeMetricChanged(*ns.metric, v, db) {
			continue
		}
		if _, err := w.exec(`INSERT OR REPLACE INTO node_metric(node_id, ts, proc_cpu, os_cpu, load1, cgroup_cpu_ns, heap_pct,
			disk_total, disk_used, disk_avail, disk_indices, shards, io_r_ops, io_w_ops, io_r_kb, io_w_kb)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ns.id, w.ts,
			nullMissing(int64(v.procCPU)), nullMissing(int64(v.osCPU)), nullMissingF(v.load1), nullMissing(v.cgroupNs),
			nullMissing(int64(v.heap)), nullMissing(v.diskTotal), nullMissing(v.diskUsed), nullMissing(v.diskAvail),
			nullMissing(v.diskIdx), nullMissing(v.shards), nullMissing(v.ioROps), nullMissing(v.ioWOps),
			nullMissing(v.ioRKB), nullMissing(v.ioWKB)); err != nil {
			return err
		}
		vv := v
		ns.metric, ns.metricTs = &vv, w.ts
	}
	return nil
}

// nodeMetricChanged applies the dead band. Cumulative I/O and cgroup
// counters always grow: they are written with the other values, never
// compared.
func nodeMetricChanged(a, b nodeMetricVals, db DeadBand) bool {
	switch {
	case absInt(a.procCPU-b.procCPU) >= db.CPUPts, absInt(a.osCPU-b.osCPU) >= db.CPUPts,
		absInt(a.heap-b.heap) >= db.HeapPts, a.shards != b.shards, a.diskTotal != b.diskTotal:
		return true
	}
	return relChanged(a.diskUsed, b.diskUsed, db.DiskRel, db.AbsBytes) || relChanged(a.diskIdx, b.diskIdx, db.DiskRel, db.AbsBytes)
}

// relChanged reports a change beyond rel (relative) or abs (absolute), or a
// value appearing/disappearing.
func relChanged(old, cur int64, rel float64, abs int64) bool {
	if (old < 0) != (cur < 0) {
		return true
	}
	if old < 0 {
		return false
	}
	d := cur - old
	if d < 0 {
		d = -d
	}
	if abs > 0 && d >= abs {
		return true
	}
	if old == 0 {
		return cur != 0
	}
	return float64(d)/float64(old) > rel
}

func (w *writer) clusterSettings(s *esclient.ClusterSettings) error {
	cs := w.cs
	flat := map[string]string{}
	for k, v := range s.Persistent {
		flat["p:"+k] = v
	}
	for k, v := range s.Transient {
		flat["t:"+k] = v
	}
	h, err := w.blobJSON(map[string]map[string]string{"persistent": nonNil(s.Persistent), "transient": nonNil(s.Transient)})
	if err != nil {
		return err
	}
	if s.Defaults != nil {
		d := map[string]string{}
		for k, v := range s.Defaults {
			if strings.HasPrefix(k, "cluster.routing.") {
				d[k] = v
			}
		}
		dh, err := w.blobJSON(d)
		if err != nil {
			return err
		}
		if dh != cs.defaultsHash {
			if _, err := w.exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('cluster_defaults', ?)`, dh); err != nil {
				return err
			}
			cs.defaultsHash = dh
		}
	}
	if h == cs.settingsHash {
		return nil
	}
	if cs.settingsRow != 0 {
		if _, err := w.exec(`UPDATE cluster_settings_iv SET valid_to = ? WHERE rowid = ?`, w.ts, cs.settingsRow); err != nil {
			return err
		}
		if err := w.event(evClusterSettingsChanged, sevMinor, "cluster", cs.id, "", cs.lastTs, 0,
			map[string]any{"changes": diffMaps(cs.settingsMap, flat)}); err != nil {
			return err
		}
	}
	row, err := w.insert(`INSERT INTO cluster_settings_iv(valid_from, blob) VALUES (?,?)`, w.ts, h)
	if err != nil {
		return err
	}
	cs.settingsHash, cs.settingsMap, cs.settingsRow, cs.settingsSince = h, flat, row, w.ts
	return nil
}

func (w *writer) remotes(rs []esclient.Remote) error {
	cs := w.cs
	seen := map[string]bool{}
	for _, r := range rs {
		seen[r.Alias] = true
		cfg, err := w.blobJSON(map[string]any{"seeds": r.Seeds, "proxy_address": r.ProxyAddress, "skip_unavailable": r.SkipUnavailable})
		if err != nil {
			return err
		}
		v := remoteVals{mode: r.Mode, connected: r.Connected, nodes: r.NodesConnected, config: cfg}
		old := cs.remotes[r.Alias]
		if old != nil && old.vals.mode == v.mode && old.vals.connected == v.connected && old.vals.config == v.config {
			continue // node counts alone do not open a new interval
		}
		if old != nil {
			if _, err := w.exec(`UPDATE remote_iv SET valid_to = ? WHERE rowid = ?`, w.ts, old.row); err != nil {
				return err
			}
		}
		row, err := w.insert(`INSERT INTO remote_iv(alias, valid_from, mode, connected, nodes_connected, blob) VALUES (?,?,?,?,?,?)`,
			r.Alias, w.ts, r.Mode, boolInt(r.Connected), r.NodesConnected, cfg)
		if err != nil {
			return err
		}
		if old != nil && old.vals.connected != v.connected {
			typ, sev := evRemoteConnected, sevInfo
			if !v.connected {
				typ, sev = evRemoteDisconnected, sevMajor
			}
			if err := w.event(typ, sev, "remote", r.Alias, "", cs.lastTs, 0, map[string]any{"mode": r.Mode}); err != nil {
				return err
			}
		} else if old == nil && !r.Connected && cs.lastTs > 0 {
			if err := w.event(evRemoteDisconnected, sevMajor, "remote", r.Alias, "", cs.lastTs, 0, map[string]any{"mode": r.Mode}); err != nil {
				return err
			}
		}
		cs.remotes[r.Alias] = &remoteState{vals: v, row: row, since: w.ts}
	}
	var gone []string
	for alias := range cs.remotes {
		if !seen[alias] {
			gone = append(gone, alias)
		}
	}
	sort.Strings(gone)
	for _, alias := range gone {
		if _, err := w.exec(`UPDATE remote_iv SET valid_to = ? WHERE rowid = ?`, w.ts, cs.remotes[alias].row); err != nil {
			return err
		}
		delete(cs.remotes, alias)
	}
	return nil
}

// --- helpers -----------------------------------------------------------------

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullMissing(v int64) any {
	if v == esclient.Missing {
		return nil
	}
	return v
}

func nullMissingF(v float64) any {
	if v == esclient.Missing {
		return nil
	}
	return v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// diffMaps lists the changed keys between two flat maps.
func diffMaps(old, cur map[string]string) []map[string]string {
	var keys []string
	for k := range old {
		keys = append(keys, k)
	}
	for k := range cur {
		if _, ok := old[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []map[string]string
	for _, k := range keys {
		o, oo := old[k]
		n, no := cur[k]
		if oo && no && o == n {
			continue
		}
		d := map[string]string{"key": k}
		if oo {
			d["from"] = o
		}
		if no {
			d["to"] = n
		}
		out = append(out, d)
	}
	return out
}

// Forget drops the in-memory state of a cluster: it is reloaded from the
// partitions at the next use (after a standby replaced them).
func (in *Ingester) Forget(cluster string) {
	l := in.clusterLock(cluster)
	l.Lock()
	defer l.Unlock()
	in.mu.Lock()
	delete(in.clusters, cluster)
	in.mu.Unlock()
}

// duplicateNames reports the node names carried by several ES nodes at once
// (SPEC §5.4: the key is (name, id), the case is signalled). The event is
// written when the situation starts, compared with the presence before
// this probe, so it is neither repeated nor lost across a restart.
func (w *writer) duplicateNames(stats []esclient.NodeStats) error {
	before := map[string]int{}
	for _, ns := range w.cs.nodes {
		if ns.present {
			before[ns.key.name]++
		}
	}
	ids := map[string][]string{}
	for _, s := range stats {
		ids[s.Name] = append(ids[s.Name], s.ID)
	}
	names := make([]string, 0, len(ids))
	for n, l := range ids {
		if len(l) > 1 && before[n] < 2 {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		l := ids[n]
		sort.Strings(l)
		if err := w.event(evNodeNameDuplicate, sevMajor, "node", n, "", w.cs.lastPresenceTs, 0,
			map[string]any{"es_ids": l, "count": len(l)}); err != nil {
			return err
		}
	}
	return nil
}
