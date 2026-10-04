package sim

import (
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

type countWriter struct{ n int64 }

func (w *countWriter) Write(p []byte) (int, error) { w.n += int64(len(p)); return len(p), nil }

// TestPresetAScale measures the reference cluster (plan step 4). Run with
// ESTOPO_SIM_PERF=1; it takes tens of seconds.
func TestPresetAScale(t *testing.T) {
	if os.Getenv("ESTOPO_SIM_PERF") == "" {
		t.Skip("set ESTOPO_SIM_PERF=1")
	}
	var m0 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)

	start := time.Now()
	c := Build(PresetA("8.19.22"), t0)
	build := time.Since(start)
	total, started, unassigned, _ := counts(c)

	runtime.GC()
	var m1 runtime.MemStats
	runtime.ReadMemStats(&m1)

	start = time.Now()
	w := &countWriter{}
	if err := c.renderCatShards(w, strings.Split("index,shard,prirep,state,docs,store,id,node,unassigned.reason,unassigned.at,unassigned.details", ",")); err != nil {
		t.Fatal(err)
	}
	render := time.Since(start)

	start = time.Now()
	iw := &countWriter{}
	c.renderCatIndices(iw, strings.Split("uuid,index,status,health,pri,rep,docs.count,store.size,pri.store.size,creation.date", ","))
	c.renderIndexSettings(io.Discard, nil)
	renderIdx := time.Since(start)

	start = time.Now()
	c.AdvanceTo(t0.Add(24 * time.Hour))
	day := time.Since(start)
	_, _, _, relocating := counts(c)

	t.Logf("nodes=%d indices=%d copies=%d started=%d unassigned=%d", len(c.Nodes), len(c.Indices), total, started, unassigned)
	t.Logf("build %v, heap %d MB", build.Round(time.Millisecond), (m1.HeapAlloc-m0.HeapAlloc)>>20)
	t.Logf("_cat/shards %d MB in %v; _cat/indices %d MB + settings in %v", w.n>>20, render.Round(time.Millisecond), iw.n>>20, renderIdx.Round(time.Millisecond))
	t.Logf("one simulated day in %v (relocating at end: %d), health %s", day.Round(time.Millisecond), relocating, c.health().status)
}
