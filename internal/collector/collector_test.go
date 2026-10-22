package collector

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/esclient"
	"estopo/internal/record"
	"estopo/internal/sim"
)

var t0 = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

type memSink struct {
	mu   sync.Mutex
	recs []*record.Record
}

func (s *memSink) Record(r *record.Record, _ <-chan struct{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, r)
	return nil
}

func (s *memSink) all() []*record.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*record.Record(nil), s.recs...)
}

func (s *memSink) fulls() []*record.Record {
	var out []*record.Record
	for _, r := range s.all() {
		if r.Kind == record.Full {
			out = append(out, r)
		}
	}
	return out
}

type memEvents struct {
	mu        sync.Mutex
	bound     string
	suspended []string
}

func (e *memEvents) Bound(id, uuid string) { e.mu.Lock(); e.bound = uuid; e.mu.Unlock() }
func (e *memEvents) Suspended(id, bound, seen string) {
	e.mu.Lock()
	e.suspended = append(e.suspended, bound+"->"+seen)
	e.mu.Unlock()
}

type env struct {
	t      *testing.T
	clk    *clock.Manual
	c      *sim.Cluster
	srv    *sim.Server
	col    *Collector
	sink   *memSink
	events *memEvents
}

// newEnv starts essim (small preset) and a collector on a shared manual
// clock, and waits for the initial probe and full capture.
func newEnv(t *testing.T) *env {
	t.Helper()
	clk := clock.NewManual(t0)
	c := sim.Build(sim.PresetSmall("8.19.22"), t0)
	srv := &sim.Server{Cluster: c, Clock: clk, Auth: sim.Auth{Disabled: true}}
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	e := &env{t: t, clk: clk, c: c, srv: srv, sink: &memSink{}, events: &memEvents{}}
	e.col = New(clk, slog.New(slog.NewTextHandler(io.Discard, nil)), e.sink, e.events, 3)
	sched := DefaultSchedule()
	sched.Jitter = 0
	err := e.col.Add(ClusterConfig{ID: "small", Name: "Small", Schedule: sched,
		ES: esclient.Config{URLs: []string{hs.URL}, Auth: esclient.AuthNone, Timeout: 500 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.col.Stop)
	clk.BlockUntil(1)
	return e
}

// advance moves the clock and waits until the worker is parked again.
func (e *env) advance(d time.Duration) {
	e.clk.Advance(d)
	e.clk.BlockUntil(1)
}

func TestFirstCaptureBindsAndCollectsEverything(t *testing.T) {
	e := newEnv(t)
	recs := e.sink.all()
	if len(recs) != 2 || recs[0].Kind != record.Light || recs[1].Kind != record.Full || recs[1].Trigger != record.TriggerFirst {
		t.Fatalf("records: %+v", kinds(recs))
	}
	full := recs[1]
	if !full.HasStructure() || full.Status != record.OK {
		t.Fatalf("full capture: status %s err %q", full.Status, full.Error)
	}
	var copies int
	for _, ix := range e.c.Indices {
		copies += ix.Pri * (1 + ix.Rep)
	}
	if len(full.Shards) != copies || len(full.Indices) != len(e.c.Indices) || len(full.IndexSettings) != len(e.c.Indices) {
		t.Errorf("shards %d/%d, indices %d/%d, settings %d", len(full.Shards), copies, len(full.Indices), len(e.c.Indices), len(full.IndexSettings))
	}
	if full.ClusterSettings.Defaults == nil || recs[0].ClusterSettings.Defaults != nil {
		t.Error("defaults must be read by full captures only")
	}
	if len(full.Calls) != 10 {
		t.Errorf("full capture made %d calls", len(full.Calls))
	}
	if e.events.bound != e.c.UUID {
		t.Errorf("bound to %q, want %q", e.events.bound, e.c.UUID)
	}
}

// B1 (collector side): a stable cluster only gets probes, plus the full
// capture forced by max_interval.
func TestStableClusterOnlyProbes(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 11; i++ {
		e.advance(5 * time.Minute)
	}
	if n := len(e.sink.fulls()); n != 1 {
		t.Fatalf("%d full captures in 55 min, want 1", n)
	}
	e.advance(5 * time.Minute) // t0+60m
	fulls := e.sink.fulls()
	if len(fulls) != 2 || fulls[1].Trigger != record.TriggerMaxInterval || fulls[1].StartMs != t0.Add(time.Hour).UnixMilli() {
		t.Errorf("fulls: %+v", kinds(fulls))
	}
}

// B2: a node leaving triggers a full capture, deferred by min_interval.
func TestNodeLeftTriggersDeferredFull(t *testing.T) {
	e := newEnv(t)
	e.c.Do(func(c *sim.Cluster) { c.NodeLeave("small-hot-dc1-m01-1") })
	e.advance(5 * time.Minute) // probe sees it; last full at t0 < 10 min ago
	if n := len(e.sink.fulls()); n != 1 {
		t.Fatalf("full capture not deferred: %d", n)
	}
	if st := e.col.Status()[0]; st.PendingTrigger != record.TriggerHealth && st.PendingTrigger != record.TriggerNodes {
		t.Errorf("pending trigger %q", st.PendingTrigger)
	}
	e.advance(5 * time.Minute) // t0+10m: deferred capture runs
	fulls := e.sink.fulls()
	if len(fulls) != 2 || fulls[1].StartMs != t0.Add(10*time.Minute).UnixMilli() {
		t.Fatalf("fulls: %v", kinds(fulls))
	}
	if tr := fulls[1].Trigger; tr != record.TriggerHealth && tr != record.TriggerNodes {
		t.Errorf("trigger %s", tr)
	}
	var onLeft int
	for _, s := range fulls[1].Shards {
		if s.UnassignedReason == "NODE_LEFT" {
			onLeft++
		}
	}
	if onLeft == 0 && fulls[1].Health.Status != "green" {
		t.Error("no NODE_LEFT copy in the capture")
	}
}

// B15: continuous agitation never yields more than one full capture per
// min_interval.
func TestAgitationRespectsMinInterval(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 36; i++ { // 3 hours, a cluster setting changes before every probe
		v := "all"
		if i%2 == 0 {
			v = "primaries"
		}
		e.c.Do(func(c *sim.Cluster) { c.SetClusterSetting(true, "cluster.routing.allocation.enable", v) })
		e.advance(5 * time.Minute)
	}
	fulls := e.sink.fulls()
	if len(fulls) < 10 {
		t.Errorf("only %d full captures under agitation", len(fulls))
	}
	for i := 1; i < len(fulls); i++ {
		if d := fulls[i].StartMs - fulls[i-1].StartMs; d < (10 * time.Minute).Milliseconds() {
			t.Fatalf("full captures %d ms apart", d)
		}
	}
}

// B7: _cat/shards times out → partial capture, nothing structural, metrics kept.
func TestShardsTimeoutGivesPartialCapture(t *testing.T) {
	e := newEnv(t)
	e.srv.SetFault("/_cat/shards", sim.Fault{Delay: 2 * time.Second})
	e.col.CaptureNow("small", true)
	e.clk.BlockUntil(1)
	waitFor(t, func() bool { return len(e.sink.fulls()) == 2 })
	r := e.sink.fulls()[1]
	if r.Status != record.Partial || r.HasStructure() || r.Shards != nil || r.Indices != nil || r.Nodes != nil {
		t.Fatalf("status %s, shards %v", r.Status, r.Shards != nil)
	}
	if !strings.Contains(r.Error, "cat_shards") || r.NodesStats == nil || r.Health == nil {
		t.Errorf("error %q, metrics kept: %v", r.Error, r.NodesStats != nil)
	}
	if st := e.col.Status()[0]; st.LastFull.Status != record.Partial || len(st.RecentErrors) == 0 {
		t.Errorf("status %+v", st.LastFull)
	}

	// No master: the probe fails.
	e.srv.ClearFaults()
	e.srv.SetFault("/_cluster/health", sim.Fault{Status: http.StatusServiceUnavailable})
	e.advance(5 * time.Minute)
	last := e.sink.all()[len(e.sink.all())-1]
	if last.Kind != record.Light || last.Status != record.Failed || !strings.Contains(last.Error, "master_not_discovered") {
		t.Errorf("probe without master: %s %q", last.Status, last.Error)
	}
}

// B8: a new cluster_uuid behind the URL suspends collection; nothing more is
// recorded until an admin resumes it.
func TestIdentityChangeSuspends(t *testing.T) {
	e := newEnv(t)
	old := e.c.UUID
	e.c.Do(func(c *sim.Cluster) { c.ChangeUUID() })
	n := len(e.sink.all())
	e.advance(5 * time.Minute)
	e.advance(5 * time.Minute)
	if got := len(e.sink.all()); got != n {
		t.Errorf("%d records written after the identity change", got-n)
	}
	st := e.col.Status()[0]
	if !st.Suspended || !strings.Contains(st.SuspendReason, old) || len(e.events.suspended) != 1 {
		t.Fatalf("status %+v, events %v", st, e.events.suspended)
	}
	e.col.Resume("small", e.c.UUID)
	e.clk.BlockUntil(1)
	waitFor(t, func() bool { return len(e.sink.fulls()) == 2 })
	if f := e.sink.fulls()[1]; f.Trigger != record.TriggerFirst || f.Root.ClusterUUID != e.c.UUID {
		t.Errorf("after resume: %s %s", f.Trigger, f.Root.ClusterUUID)
	}
}

func TestRemoteInfo400IsNotAFailure(t *testing.T) {
	e := newEnv(t)
	e.srv.SetFault("/_remote/info", sim.Fault{Status: http.StatusBadRequest})
	e.advance(5 * time.Minute)
	last := e.sink.all()[len(e.sink.all())-1]
	if last.Status != record.OK || !last.RemotesUnavailable || last.Remotes != nil {
		t.Errorf("status %s, unavailable %v", last.Status, last.RemotesUnavailable)
	}
}

func TestCaptureNowHonoursMinInterval(t *testing.T) {
	e := newEnv(t)
	e.col.CaptureNow("small", false)
	e.clk.BlockUntil(1)
	time.Sleep(50 * time.Millisecond)
	if n := len(e.sink.fulls()); n != 1 {
		t.Fatalf("manual capture not deferred: %d", n)
	}
	e.advance(10 * time.Minute)
	fulls := e.sink.fulls()
	if len(fulls) != 2 || fulls[1].Trigger != record.TriggerManual {
		t.Errorf("fulls %v", kinds(fulls))
	}
	if err := e.col.CaptureNow("nope", true); err != ErrUnknownCluster {
		t.Errorf("unknown cluster: %v", err)
	}
}

func kinds(rs []*record.Record) []string {
	var out []string
	for _, r := range rs {
		out = append(out, string(r.Kind)+"/"+r.Trigger+"/"+string(r.Status))
	}
	return out
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}
