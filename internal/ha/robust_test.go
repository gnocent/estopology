package ha

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/esclient"
	"estopo/internal/record"
	"estopo/internal/sim"
	"estopo/internal/store"
)

const testToken = "a-shared-replication-token"

var t0 = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) // Wednesday, 2026-W40

// node is an instance with a real store, spool and ingester behind its
// manager.
type node struct {
	t    *testing.T
	dir  string
	st   *store.Store
	sp   *record.Spool
	in   *store.Ingester
	m    *Manager
	busy atomic.Bool // what Hooks.Idle reports
}

func newNode(t *testing.T, standbyOf string) *node {
	t.Helper()
	ctx := context.Background()
	n := &node{t: t, dir: t.TempDir()}
	var err error
	if n.st, err = store.Open(ctx, n.dir, clock.NewManual(t0)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.st.Close() })
	n.sp, _ = record.OpenSpool(filepath.Join(n.dir, "spool"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	n.in = store.NewIngester(n.st, store.Options{}, log)
	n.start(standbyOf)
	return n
}

// start creates the manager (again: a restart of the instance).
func (n *node) start(standbyOf string) {
	n.t.Helper()
	token := filepath.Join(n.dir, "token")
	os.WriteFile(token, []byte(testToken), 0o600)
	m, err := New(context.Background(), Options{Store: n.st, Spool: n.sp, Clock: clock.NewManual(t0),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), StandbyOf: standbyOf, TokenFile: token, TmpDir: n.st.TmpDir(),
		Hooks: Hooks{Ingest: n.ingest, Idle: func() bool { return !n.busy.Load() }, LastTs: n.in.LastTs, Forget: n.in.Forget,
			Changed: func(context.Context) error { return nil }}})
	if err != nil {
		n.t.Fatal(err)
	}
	n.m = m
}

func (n *node) ingest(r *record.Record) error {
	if _, err := n.sp.Write(r); err != nil {
		return err
	}
	_, err := n.in.Ingest(context.Background(), r)
	return err
}

// capture records a full capture of c, as the collector of a primary would.
func (n *node) capture(c *sim.Cluster, id string, d time.Duration) {
	n.t.Helper()
	if err := n.ingest(c.CaptureAt(t0.Add(d).UnixMilli(), id, record.Full, record.TriggerSchedule)); err != nil {
		n.t.Fatal(err)
	}
}

func (n *node) configure(ids ...string) {
	n.t.Helper()
	for _, id := range ids {
		if err := n.st.SaveCluster(context.Background(), store.Cluster{ID: id, Name: id, Enabled: true,
			ES: esclient.Config{URLs: []string{"https://es.example.org:9200"}, Auth: esclient.AuthNone}}); err != nil {
			n.t.Fatal(err)
		}
	}
}

func (n *node) partitions(cluster string) []store.PartitionInfo {
	n.t.Helper()
	parts, err := n.st.Partitions(context.Background(), cluster)
	if err != nil {
		n.t.Fatal(err)
	}
	return parts
}

// captures counts the captures of a cluster over its partitions.
func (n *node) captures(cluster string) int {
	n.t.Helper()
	total := 0
	for _, info := range n.partitions(cluster) {
		p, err := n.st.OpenExisting(context.Background(), info)
		if err != nil {
			n.t.Fatal(err)
		}
		var c int
		if err := p.DB().QueryRow(`SELECT count(*) FROM capture`).Scan(&c); err != nil {
			n.t.Fatal(err)
		}
		total += c
	}
	return total
}

func (n *node) lastTs(cluster string) int64 {
	n.t.Helper()
	ts, err := n.in.LastTs(context.Background(), cluster)
	if err != nil {
		n.t.Fatal(err)
	}
	return ts
}

func (n *node) clusters() int {
	n.t.Helper()
	cs, err := n.st.Clusters(context.Background(), true)
	if err != nil {
		n.t.Fatal(err)
	}
	return len(cs)
}

// swapServer serves whatever handler is current: the primary behind a URL
// can be replaced, or made to fail.
type swapServer struct {
	*httptest.Server
	h atomic.Value
}

func newSwapServer(t *testing.T, h http.Handler) *swapServer {
	s := &swapServer{}
	s.h.Store(&h)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		(*s.h.Load().(*http.Handler)).ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *swapServer) serve(h http.Handler) { s.h.Store(&h) }

// pair returns a primary holding two weeks of history of "small", and a
// standby synchronised with it.
func pair(t *testing.T) (primary, standby *node, srv *swapServer, c *sim.Cluster) {
	t.Helper()
	primary = newNode(t, "")
	primary.configure("small")
	c = sim.Build(sim.PresetSmall("8.19.22"), t0)
	primary.capture(c, "small", 0)
	primary.capture(c, "small", 5*24*time.Hour) // W41
	srv = newSwapServer(t, primary.m.Handler())
	standby = newNode(t, srv.URL)
	if err := standby.m.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(standby.partitions("small")) != 2 || standby.captures("small") != primary.captures("small") ||
		standby.lastTs("small") != primary.lastTs("small") || standby.clusters() != 1 {
		t.Fatalf("standby not synchronised: %+v", standby.m.Status().Replication)
	}
	return primary, standby, srv, c
}

// A primary that lost its data volume restarts empty behind the same URL
// with the same token: the standby keeps everything and says why it
// stopped, until an administrator rebuilds it.
func TestStandbyRefusesEmptiedPrimary(t *testing.T) {
	ctx := context.Background()
	primary, standby, srv, _ := pair(t)
	if id := standby.m.Status().Replication.PrimaryID; id == "" || id != primary.m.Status().InstanceID {
		t.Fatalf("primary id recorded by the standby: %q, primary is %q", id, primary.m.Status().InstanceID)
	}
	captures, last := standby.captures("small"), standby.lastTs("small")

	emptied := newNode(t, "")
	if emptied.m.Status().InstanceID == primary.m.Status().InstanceID {
		t.Fatal("two data directories with the same instance id")
	}
	srv.serve(emptied.m.Handler())
	check := func(when string) {
		t.Helper()
		err := standby.m.SyncOnce(ctx)
		rs := standby.m.Status().Replication
		if err == nil || !strings.Contains(rs.Blocked, "another instance") || rs.LastError == "" {
			t.Fatalf("%s: err %v, status %+v", when, err, rs)
		}
		if standby.clusters() != 1 || len(standby.partitions("small")) != 2 || standby.captures("small") != captures ||
			standby.lastTs("small") != last {
			t.Fatalf("%s: the standby lost data: %d clusters, %d partitions, %d captures", when, standby.clusters(),
				len(standby.partitions("small")), standby.captures("small"))
		}
	}
	check("emptied primary")
	check("next cycle")
	standby.start(srv.URL) // the id of the primary followed survives a restart
	check("after a restart of the standby")

	// The administrator accepts the new primary: the standby becomes what it is.
	if err := standby.m.Rebuild(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := standby.m.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	rs := standby.m.Status().Replication
	if rs.Blocked != "" || rs.PrimaryID != emptied.m.Status().InstanceID || standby.clusters() != 0 || len(standby.partitions("")) != 0 {
		t.Errorf("after the rebuild: %+v, %d clusters, %d partitions", rs, standby.clusters(), len(standby.partitions("")))
	}
}

// The purge of a cluster on the primary reaches the standby, but a primary
// that announces no cluster at all does not empty a standby that has some.
func TestStandbyKeepsHistoryWhenPrimaryHasNone(t *testing.T) {
	ctx := context.Background()
	primary, standby, _, c := pair(t)
	primary.configure("other")
	primary.capture(c, "other", 6*24*time.Hour)
	if err := standby.m.SyncOnce(ctx); err != nil || len(standby.partitions("other")) != 1 {
		t.Fatalf("second cluster: %v, %d partitions", err, len(standby.partitions("other")))
	}
	purge := func(id string) {
		t.Helper()
		if _, err := primary.st.Purge(ctx, id); err != nil {
			t.Fatal(err)
		}
		os.RemoveAll(filepath.Join(primary.sp.Dir(), id))
	}
	purge("other")
	if err := standby.m.SyncOnce(ctx); err != nil || len(standby.partitions("other")) != 0 || len(standby.partitions("small")) != 2 {
		t.Fatalf("purge of one cluster: %v, %d/%d partitions", err, len(standby.partitions("other")), len(standby.partitions("small")))
	}
	purge("small")
	err := standby.m.SyncOnce(ctx)
	if rs := standby.m.Status().Replication; err == nil || !strings.Contains(rs.Blocked, "holds no history") {
		t.Fatalf("primary without history: err %v, status %+v", err, rs)
	}
	if len(standby.partitions("small")) != 2 {
		t.Fatalf("the standby purged its history: %d partitions", len(standby.partitions("small")))
	}
	if err := standby.m.Rebuild(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := standby.m.SyncOnce(ctx); err != nil || len(standby.partitions("")) != 0 || standby.m.Status().Replication.Blocked != "" {
		t.Errorf("after the rebuild: %v, %d partitions, %+v", err, len(standby.partitions("")), standby.m.Status().Replication)
	}
}

// The primary dies while the standby copies its partitions: the standby
// keeps the history it had.
func TestRebuildKeepsHistoryWhenPrimaryFails(t *testing.T) {
	ctx := context.Background()
	primary, standby, srv, c := pair(t)
	captures, last := standby.captures("small"), standby.lastTs("small")
	primary.capture(c, "small", 5*24*time.Hour+time.Hour)

	// The second partition is cut in the middle of the transfer.
	h := primary.m.Handler()
	srv.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/partitions/small/2026-W41") {
			h.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		w.Header().Set("Content-Length", rec.Header().Get("Content-Length"))
		w.Write(rec.Body.Bytes()[:rec.Body.Len()/2])
		panic(http.ErrAbortHandler)
	}))
	if err := standby.m.Rebuild(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := standby.m.SyncOnce(ctx); err == nil {
		t.Fatal("the synchronisation was expected to fail")
	}
	if n := len(standby.partitions("small")); n != 2 {
		t.Fatalf("%d partitions left after the failed copy", n)
	}
	if standby.captures("small") != captures || standby.lastTs("small") != last {
		t.Errorf("history after the failed copy: %d captures (want %d), last %d (want %d)",
			standby.captures("small"), captures, standby.lastTs("small"), last)
	}
	if left, _ := filepath.Glob(filepath.Join(standby.st.TmpDir(), "*")); len(left) != 0 {
		t.Errorf("files left in tmp: %v", left)
	}

	// The primary is back: the rebuild asked for is done.
	srv.serve(h)
	if err := standby.m.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if standby.captures("small") != primary.captures("small") || standby.lastTs("small") != primary.lastTs("small") {
		t.Errorf("after the rebuild: %d captures, primary has %d", standby.captures("small"), primary.captures("small"))
	}
	if rs := standby.m.Status().Replication; len(rs.Clusters) != 1 || rs.Clusters[0].Rebuilds != 2 {
		t.Errorf("replication status: %+v", rs)
	}
}

// A partition copy waits for the ingestion queue to be empty; if it never
// is, the copy fails with an explicit error and replaces nothing.
func TestRebuildNeedsIdleIngestion(t *testing.T) {
	ctx := context.Background()
	_, standby, _, _ := pair(t)
	captures := standby.captures("small")
	standby.busy.Store(true)
	standby.m.idleWait = 200 * time.Millisecond
	if err := standby.m.Rebuild(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	err := standby.m.SyncOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), "still busy") {
		t.Fatalf("rebuild with a busy ingestion: %v", err)
	}
	if standby.captures("small") != captures {
		t.Error("partitions replaced under a busy ingestion")
	}
	standby.busy.Store(false)
	if err := standby.m.SyncOnce(ctx); err != nil {
		t.Errorf("rebuild once idle: %v", err)
	}
}

// A promotion does not wait for a primary that stopped answering in the
// middle of a partition copy.
func TestPromoteDoesNotWaitForFrozenPrimary(t *testing.T) {
	ctx := context.Background()
	primary, standby, srv, _ := pair(t)
	h := primary.m.Handler()
	frozen, release := make(chan struct{}, 1), make(chan struct{})
	defer close(release)
	srv.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/partitions/") {
			frozen <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return
		}
		h.ServeHTTP(w, r)
	}))
	if err := standby.m.Rebuild(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	synced := make(chan error, 1)
	go func() { synced <- standby.m.SyncOnce(ctx) }()
	<-frozen

	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := standby.m.Promote(pctx, "admin"); err != nil {
		t.Fatalf("promotion behind a frozen primary: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("promotion took %s", d)
	}
	if err := <-synced; err == nil {
		t.Error("the interrupted synchronisation reported no error")
	}
	if standby.m.Role() != RolePrimary || len(standby.partitions("small")) != 2 {
		t.Errorf("after the promotion: role %s, %d partitions", standby.m.Role(), len(standby.partitions("small")))
	}
}

// A partition download that stops receiving bytes is abandoned.
func TestStalledPartitionDownload(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		w.Write(make([]byte, 1000))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	n := newNode(t, "")
	n.m.stall = 200 * time.Millisecond
	start := time.Now()
	err := n.m.client(srv.URL).partition(context.Background(), "small", "2026-W40", filepath.Join(t.TempDir(), "p.db"))
	if err == nil || !strings.Contains(err.Error(), "nothing received") || time.Since(start) > 5*time.Second {
		t.Errorf("stalled download: %v after %s", err, time.Since(start))
	}
}

// A record whose kind or cluster would escape the spool directory is
// refused by the standby.
func TestStandbyRefusesForgedRecord(t *testing.T) {
	primary, standby, _, c := pair(t)
	r := c.CaptureAt(t0.Add(6*24*time.Hour).UnixMilli(), "small", record.Light, record.TriggerSchedule)
	r.Kind = "x/../../../../escaped"
	f, err := os.Create(filepath.Join(primary.sp.Dir(), "small", "1791288000000-001-light.rec.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if err := record.Encode(f, r); err != nil {
		t.Fatal(err)
	}
	f.Close()
	last := standby.lastTs("small")
	if err := standby.m.SyncOnce(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid kind") {
		t.Errorf("forged record: %v", err)
	}
	if standby.lastTs("small") != last {
		t.Error("the forged record was ingested")
	}
	var outside []string
	filepath.WalkDir(standby.dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && strings.Contains(d.Name(), "escaped") {
			outside = append(outside, p)
		}
		return nil
	})
	if _, err := os.Stat(filepath.Join(filepath.Dir(standby.dir), "escaped.rec.zst")); err == nil || len(outside) != 0 {
		t.Errorf("file written by the forged record: %v", outside)
	}
}

// The replication client trusts only the configured CA, does not follow
// redirects (its token would follow) and bounds what it reads in memory.
func TestReplicationClientLimits(t *testing.T) {
	ctx := context.Background()
	n := newNode(t, "")
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.pem")
	os.WriteFile(empty, nil, 0o600)
	garbage := filepath.Join(dir, "garbage.pem")
	os.WriteFile(garbage, []byte("not a certificate"), 0o600)
	for _, ca := range []string{filepath.Join(dir, "missing.pem"), empty, garbage} {
		if _, err := New(ctx, Options{Store: n.st, Log: n.m.o.Log, CAFile: ca}); err == nil || !strings.Contains(err.Error(), "replication CA") {
			t.Errorf("start with CA %s: %v", filepath.Base(ca), err)
		}
		n.m.o.CAFile = ca
		if _, err := n.m.client("https://127.0.0.1:1").status(ctx); err == nil || !strings.Contains(err.Error(), "replication CA") {
			t.Errorf("request with CA %s: %v", filepath.Base(ca), err)
		}
	}
	n.m.o.CAFile = ""

	var leaked atomic.Value
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Store(r.Header.Get("Authorization"))
		w.Write([]byte(`{"role":"primary"}`))
	}))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	if st, err := n.m.client(redirect.URL).status(ctx); err == nil || leaked.Load() != nil {
		t.Errorf("redirect followed: %+v %v, token sent: %v", st, err, leaked.Load())
	}

	srv := httptest.NewServer(n.m.Handler())
	defer srv.Close()
	c := n.m.client(srv.URL)
	if _, _, _, err := c.meta(ctx, ""); err != nil {
		t.Fatal(err)
	}
	defer func(j, m int64) { maxJSONBytes, maxMetaBytes = j, m }(maxJSONBytes, maxMetaBytes)
	maxJSONBytes, maxMetaBytes = 10, 10
	if _, _, _, err := c.meta(ctx, ""); err == nil || !strings.Contains(err.Error(), "more than 10 bytes") {
		t.Errorf("configuration snapshot over the limit: %v", err)
	}
	if _, err := c.status(ctx); err == nil || !strings.Contains(err.Error(), "more than 10 bytes") {
		t.Errorf("status over the limit: %v", err)
	}
}

// A role change that cannot be written leaves the role in memory as it was.
func TestStateWrittenBeforeApplied(t *testing.T) {
	_, standby, _, _ := pair(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := standby.m.Promote(cancelled, "admin"); err == nil {
		t.Fatal("promotion with a state that cannot be written")
	}
	if kv, _ := standby.st.HAState(context.Background()); standby.m.Role() != RoleStandby || kv["role"] != RoleStandby {
		t.Errorf("after the failed promotion: role %s in memory, %s on disk", standby.m.Role(), kv["role"])
	}

	primary := newNode(t, "")
	if err := primary.m.SetPeer(cancelled, "admin", "https://peer.example.org"); err == nil || primary.m.Status().Peer != "" {
		t.Errorf("peer after a failed write: %q, %v", primary.m.Status().Peer, err)
	}
	if err := primary.m.Demote(cancelled, "admin", "https://peer.example.org"); err == nil || primary.m.Role() != RolePrimary {
		t.Errorf("role after a failed demotion: %s, %v", primary.m.Role(), err)
	}
}

// A demotion asks for the history to be copied again from the new primary.
// An instance that restarts before its first synchronisation still does it,
// instead of appending the primary's records to its own (the readings of
// the split brain would stay, those of the primary would be missing). The
// same holds for a rebuild asked for by an administrator.
func TestRebuildRequestSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	primary, standby, srv, c := pair(t)
	// Split brain: the standby is promoted and collects on its own, while
	// the primary goes on collecting.
	if err := standby.m.Promote(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	standby.capture(c, "small", 6*24*time.Hour)
	primary.capture(c, "small", 6*24*time.Hour+time.Hour)
	if err := standby.m.Demote(ctx, "admin", srv.URL); err != nil {
		t.Fatal(err)
	}
	standby.start(srv.URL) // restart before the first synchronisation
	if err := standby.m.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if standby.captures("small") != primary.captures("small") || standby.lastTs("small") != primary.lastTs("small") {
		t.Fatalf("after the demotion and a restart: %d captures (primary %d), last %d (primary %d)",
			standby.captures("small"), primary.captures("small"), standby.lastTs("small"), primary.lastTs("small"))
	}
	// Done: the next start does not copy again.
	standby.start(srv.URL)
	if standby.m.rebuild {
		t.Error("rebuild still pending after a successful synchronisation")
	}

	if err := standby.m.Rebuild(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	standby.start(srv.URL)
	if !standby.m.rebuild {
		t.Error("the rebuild asked for was lost by a restart")
	}
}

// A primary whose spool holds nothing more for a cluster (pruned, or the
// cluster stopped being collected while the standby was down) still
// announces how far its history goes: a standby behind it rebuilds the
// cluster instead of staying behind without an error nor a lag.
func TestStandbyCatchesUpWithoutSpool(t *testing.T) {
	ctx := context.Background()
	primary, standby, _, c := pair(t)
	primary.capture(c, "small", 5*24*time.Hour+time.Hour)
	primary.capture(c, "small", 5*24*time.Hour+2*time.Hour)
	if err := os.RemoveAll(filepath.Join(primary.sp.Dir(), "small")); err != nil {
		t.Fatal(err)
	}
	if err := standby.m.SyncOnce(ctx); err != nil {
		t.Fatal(err)
	}
	rs := standby.m.Status().Replication
	if standby.lastTs("small") != primary.lastTs("small") || standby.captures("small") != primary.captures("small") {
		t.Fatalf("standby behind the primary: last %d (primary %d), %d captures (primary %d), status %+v",
			standby.lastTs("small"), primary.lastTs("small"), standby.captures("small"), primary.captures("small"), rs)
	}
	if len(rs.Clusters) != 1 || rs.Clusters[0].PrimaryMs != primary.lastTs("small") || rs.LagMs != 0 {
		t.Errorf("replication status: %+v", rs)
	}
}

// A blocked replication stays blocked while the primary does not answer:
// only a primary that passes the check lifts the block.
func TestBlockedSurvivesUnreachablePrimary(t *testing.T) {
	ctx := context.Background()
	_, standby, srv, _ := pair(t)
	emptied := newNode(t, "")
	srv.serve(emptied.m.Handler())
	if err := standby.m.SyncOnce(ctx); err == nil || standby.m.Status().Replication.Blocked == "" {
		t.Fatalf("emptied primary: %v, %+v", err, standby.m.Status().Replication)
	}
	srv.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "maintenance", http.StatusServiceUnavailable)
	}))
	err := standby.m.SyncOnce(ctx)
	if rs := standby.m.Status().Replication; err == nil || rs.Blocked == "" || !strings.Contains(rs.LastError, "503") {
		t.Fatalf("unreachable primary: %v, %+v", err, rs)
	}
	srv.serve(emptied.m.Handler())
	if err := standby.m.Rebuild(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := standby.m.SyncOnce(ctx); err != nil || standby.m.Status().Replication.Blocked != "" {
		t.Errorf("after the rebuild: %v, %+v", err, standby.m.Status().Replication)
	}
}

// listClusters calls the clusters endpoint of n within a second.
func (n *node) listClusters() (int, []remoteCluster) {
	n.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req := httptest.NewRequest("GET", "/api/v1/replication/clusters", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	n.m.Handler().ServeHTTP(rec, req)
	var out []remoteCluster
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			n.t.Fatal(err)
		}
	}
	return rec.Code, out
}

// The clusters endpoint does not go through the ingestion: an ingestion in
// progress (here, one that never ends) does not hold the answer back.
func TestClustersListDoesNotWaitForIngestion(t *testing.T) {
	n := newNode(t, "")
	n.configure("small")
	c := sim.Build(sim.PresetSmall("8.19.22"), t0)
	n.capture(c, "small", 0)
	n.capture(c, "small", time.Hour)
	n.m.o.Hooks.LastTs = func(ctx context.Context, _ string) (int64, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	code, cs := n.listClusters()
	if code != http.StatusOK || len(cs) != 1 || cs[0].LastMs != t0.Add(time.Hour).UnixMilli() {
		t.Fatalf("clusters: %d %+v", code, cs)
	}
}

// A cluster whose last partition cannot be read does not stop the
// replication of the others: it is still listed (not taken for purged),
// without its last ingested date.
func TestClustersListSurvivesUnreadablePartition(t *testing.T) {
	ctx := context.Background()
	n := newNode(t, "")
	n.configure("small", "broken")
	c := sim.Build(sim.PresetSmall("8.19.22"), t0)
	n.capture(c, "small", 0)
	w := store.WeekOf(t0.UnixMilli())
	rel := filepath.Join("clusters", "broken", w.Key+".db")
	os.MkdirAll(filepath.Join(n.dir, "clusters", "broken"), 0o750)
	if err := os.WriteFile(filepath.Join(n.dir, rel), []byte(strings.Repeat("not a database ", 512)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := n.st.Meta.ExecContext(ctx, `INSERT INTO partition(cluster_id, week, from_ms, to_ms, path, created_ms)
		VALUES ('broken', ?, ?, ?, ?, 0)`, w.Key, w.FromMs, w.ToMs, filepath.ToSlash(rel)); err != nil {
		t.Fatal(err)
	}
	code, cs := n.listClusters()
	if code != http.StatusOK || len(cs) != 2 {
		t.Fatalf("clusters: %d %+v", code, cs)
	}
	if cs[0].Cluster != "broken" || cs[0].LastMs != 0 || cs[1].Cluster != "small" || cs[1].LastMs != t0.UnixMilli() {
		t.Errorf("clusters: %+v", cs)
	}
}

// A rebuild asked for while a rebuild is copying the partitions is not
// lost when that copy ends: the next synchronisation copies again, even
// after a restart.
func TestRebuildAskedDuringRebuild(t *testing.T) {
	ctx := context.Background()
	primary, standby, srv, _ := pair(t)
	h := primary.m.Handler()
	frozen, release := make(chan struct{}, 1), make(chan struct{})
	srv.serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/partitions/") {
			select {
			case frozen <- struct{}{}:
			default:
			}
			<-release
		}
		h.ServeHTTP(w, r)
	}))
	if err := standby.m.Rebuild(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	synced := make(chan error, 1)
	go func() { synced <- standby.m.SyncOnce(ctx) }()
	<-frozen
	if err := standby.m.Rebuild(ctx, "admin"); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-synced; err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	standby.m.mu.Lock()
	pending := standby.m.rebuild
	standby.m.mu.Unlock()
	if !pending {
		t.Error("the rebuild asked for during a rebuild was cleared")
	}
	standby.start(srv.URL)
	if !standby.m.rebuild {
		t.Error("the rebuild asked for during a rebuild was lost by a restart")
	}
}
