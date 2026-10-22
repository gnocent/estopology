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

// startCollector collects essim (small preset) with a collector reading clk,
// driven by m; prepare injects faults before the first probe. It returns
// once the worker is parked after its first probe.
func startCollector(t *testing.T, m *clock.Manual, clk clock.Clock, prepare func(*sim.Server)) (*memSink, *Collector) {
	t.Helper()
	c := sim.Build(sim.PresetSmall("8.19.22"), t0)
	srv := &sim.Server{Cluster: c, Clock: m, Auth: sim.Auth{Disabled: true}}
	if prepare != nil {
		prepare(srv)
	}
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)
	sink := &memSink{}
	col := New(clk, slog.New(slog.NewTextHandler(io.Discard, nil)), sink, &memEvents{}, 3)
	sched := DefaultSchedule()
	sched.Jitter = 0
	if err := col.Add(ClusterConfig{ID: "small", Name: "Small", Schedule: sched,
		ES: esclient.Config{URLs: []string{hs.URL}, Auth: esclient.AuthNone, Timeout: 500 * time.Millisecond}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(col.Stop)
	m.BlockUntil(1)
	return sink, col
}

// _nodes/stats failing for good: no probe can trigger a full capture, the
// maximum interval still does, counted from the start of the worker.
func TestMaxIntervalWithoutAnyFullCapture(t *testing.T) {
	m := clock.NewManual(t0)
	sink, _ := startCollector(t, m, m, func(s *sim.Server) {
		s.SetFault("/_nodes/stats", sim.Fault{Status: http.StatusInternalServerError})
	})
	if n := len(sink.fulls()); n != 0 {
		t.Fatalf("%d full captures at start", n)
	}
	for i := 0; i < 36; i++ { // 3 hours
		m.Advance(5 * time.Minute)
		m.BlockUntil(1)
	}
	fulls := sink.fulls()
	if len(fulls) != 3 || fulls[0].Trigger != record.TriggerMaxInterval || fulls[0].StartMs != t0.Add(time.Hour).UnixMilli() {
		t.Fatalf("full captures in 3 hours: %v", kinds(fulls))
	}
}

// steppingClock is a manual clock whose wall time can step back, as the
// system clock does (NTP, restored VM); its monotonic reading cannot.
type steppingClock struct {
	*clock.Manual
	mu     sync.Mutex
	offset time.Duration
}

func (s *steppingClock) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Manual.Now().Add(s.offset)
}

func (s *steppingClock) step(d time.Duration) {
	s.mu.Lock()
	s.offset += d
	s.mu.Unlock()
}

func (s *steppingClock) monotonic() time.Duration { return s.Manual.Now().Sub(t0) }

// The wall clock stepping back an hour does not hold the probes back for an
// hour; the records keep the wall time.
func TestWallClockStepBackDoesNotStallProbes(t *testing.T) {
	m := clock.NewManual(t0)
	sc := &steppingClock{Manual: m}
	sink, _ := startCollector(t, m, sc, nil)
	sc.step(-time.Hour)
	m.Advance(5 * time.Minute)
	m.BlockUntil(1)
	var probes []*record.Record
	for _, r := range sink.all() {
		if r.Kind == record.Light {
			probes = append(probes, r)
		}
	}
	if len(probes) != 2 {
		t.Fatalf("%d probes 5 minutes after the clock stepped back, want 2", len(probes))
	}
	if want := t0.Add(5*time.Minute - time.Hour).UnixMilli(); probes[1].StartMs != want {
		t.Errorf("probe at %d, want the wall time %d", probes[1].StartMs, want)
	}
}

// Side calls failing in a full capture leave out their section only: the
// structure is kept, and a missing section is nil, not empty, so that the
// ingestion does not close the remotes or forget the settings.
func TestFailedSideCallsKeepTheStructure(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/_nodes/stats", "/_cat/allocation", "/_cluster/settings", "/_remote/info"} {
		e.srv.SetFault(p, sim.Fault{Status: http.StatusInternalServerError})
	}
	e.col.CaptureNow("small", true)
	waitFor(t, func() bool { return len(e.sink.fulls()) == 2 })
	r := e.sink.fulls()[1]
	if !r.HasStructure() || r.Nodes == nil || r.Indices == nil || r.Shards == nil {
		t.Fatalf("structure dropped: status %s, error %q", r.Status, r.Error)
	}
	if r.NodesStats != nil || r.Allocation != nil || r.ClusterSettings != nil || r.Remotes != nil || r.RemotesUnavailable {
		t.Errorf("failed sections not left out: %+v", r)
	}
	for _, call := range []string{"nodes_stats", "cat_allocation", "cluster_settings", "remote_info"} {
		if !strings.Contains(r.Error, call) {
			t.Errorf("error %q does not name %s", r.Error, call)
		}
	}

	// A probe with the same failures is partial.
	e.clk.BlockUntil(1)
	e.advance(5 * time.Minute)
	last := e.sink.all()[len(e.sink.all())-1]
	if last.Kind != record.Light || last.Status != record.Partial {
		t.Errorf("probe: %s %s", last.Kind, last.Status)
	}
}
