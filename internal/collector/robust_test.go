package collector

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/esclient"
	"estopo/internal/record"
	"estopo/internal/sim"
)

// A 200 answer of _nodes/stats or _nodes that lacks nodes (one failed, or an
// empty object from a proxy) makes the capture partial: no node list reaches
// the ingestion, which would close the presence of the missing ones.
func TestIncompleteNodesAnswerIsPartial(t *testing.T) {
	clk := clock.NewManual(t0)
	c := sim.Build(sim.PresetSmall("8.19.22"), t0)
	srv := &sim.Server{Cluster: c, Clock: clk, Auth: sim.Auth{Disabled: true}}
	var mode atomic.Value // "", "failed", "empty"
	mode.Store("")
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := mode.Load().(string)
		if m == "" || !strings.HasPrefix(r.URL.Path, "/_nodes") {
			srv.ServeHTTP(w, r)
			return
		}
		if m == "empty" {
			w.Write([]byte(`{}`))
			return
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		w.Write(bytes.Replace(rec.Body.Bytes(), []byte(`"failed":0`), []byte(`"failed":1`), 1))
	}))
	defer hs.Close()
	sink := &memSink{}
	col := New(clk, slog.New(slog.NewTextHandler(io.Discard, nil)), sink, &memEvents{}, 3)
	sched := DefaultSchedule()
	sched.Jitter = 0
	if err := col.Add(ClusterConfig{ID: "small", Name: "Small", Schedule: sched,
		ES: esclient.Config{URLs: []string{hs.URL}, Auth: esclient.AuthNone, Timeout: 500 * time.Millisecond}}); err != nil {
		t.Fatal(err)
	}
	defer col.Stop()
	clk.BlockUntil(1)
	if first := sink.fulls()[0]; first.Status != record.OK || len(first.NodesStats) != len(c.Nodes) {
		t.Fatalf("first capture: %s, %d nodes", first.Status, len(first.NodesStats))
	}

	mode.Store("failed")
	clk.Advance(5 * time.Minute)
	clk.BlockUntil(1)
	probe := sink.all()[len(sink.all())-1]
	if probe.Kind != record.Light || probe.Status != record.Partial || probe.NodesStats != nil ||
		!strings.Contains(probe.Error, "nodes_stats") || !strings.Contains(probe.Error, "did not answer") {
		t.Errorf("probe with a failed node: %s, node stats %v, error %q", probe.Status, probe.NodesStats != nil, probe.Error)
	}
	if probe.Health == nil {
		t.Error("probe with a failed node: the health was dropped too")
	}

	mode.Store("empty")
	n := len(sink.fulls())
	col.CaptureNow("small", true)
	waitFor(t, func() bool { return len(sink.fulls()) == n+1 })
	full := sink.fulls()[n]
	if full.Status != record.Partial || full.NodesStats != nil || full.Nodes != nil || full.Shards != nil || full.HasStructure() {
		t.Errorf("full capture with empty node answers: %s, error %q", full.Status, full.Error)
	}
}

// Resume while a full capture is running: the capture that ends afterwards
// does not undo it, the next capture starts over (trigger "first").
func TestResumeDuringCapture(t *testing.T) {
	e := newEnv(t)
	e.srv.SetFault("/_cat/shards", sim.Fault{Delay: 300 * time.Millisecond})
	e.col.CaptureNow("small", true)
	time.Sleep(100 * time.Millisecond) // the worker waits for _cat/shards
	if err := e.col.Resume("small", e.c.UUID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(e.sink.fulls()) == 2 })
	e.srv.ClearFaults()
	e.clk.BlockUntil(1)
	e.advance(5 * time.Minute)  // probe: a full capture is due, deferred by the minimum interval
	e.advance(10 * time.Minute) // minimum interval over
	fulls := e.sink.fulls()
	if len(fulls) != 3 || fulls[2].Trigger != record.TriggerFirst {
		t.Errorf("full captures after the resume: %v", kinds(fulls))
	}
}

// A full capture whose _nodes/stats call failed still serves as the
// reference of the probes: the next probe does not take the cluster for
// never captured (trigger "first") and start another full capture.
func TestFullCaptureWithoutNodesStatsStaysTheReference(t *testing.T) {
	e := newEnv(t)
	e.srv.SetFault("/_nodes/stats", sim.Fault{Status: http.StatusInternalServerError})
	e.col.CaptureNow("small", true)
	waitFor(t, func() bool { return len(e.sink.fulls()) == 2 })
	if r := e.sink.fulls()[1]; !r.HasStructure() || r.NodesStats != nil {
		t.Fatalf("manual capture: status %s, error %q", r.Status, r.Error)
	}
	e.srv.ClearFaults()
	e.clk.BlockUntil(1)
	e.advance(5 * time.Minute)
	e.advance(10 * time.Minute)
	if fulls := e.sink.fulls(); len(fulls) != 2 {
		t.Errorf("full captures after the probes: %v", kinds(fulls))
	}
}

// Reset (history purged): the next probe triggers a full capture with the
// trigger "first", once the minimum interval is over.
func TestResetStartsOverWithFullCapture(t *testing.T) {
	e := newEnv(t)
	if err := e.col.Reset("small"); err != nil {
		t.Fatal(err)
	}
	e.advance(5 * time.Minute)
	e.advance(10 * time.Minute)
	fulls := e.sink.fulls()
	if len(fulls) != 2 || fulls[1].Trigger != record.TriggerFirst {
		t.Errorf("full captures after the reset: %v", kinds(fulls))
	}
	if err := e.col.Reset("other"); err != ErrUnknownCluster {
		t.Errorf("reset of an unknown cluster: %v", err)
	}
}
