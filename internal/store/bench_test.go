package store

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/record"
	"estopo/internal/sim"
)

// TestIngestScale is the plan's step 6b micro-benchmark on the reference
// cluster (SPEC §14: a full capture of 250k shards in < 30 s). Run with
// ESTOPO_BENCH=1; the data directory must be on a local Linux filesystem.
func TestIngestScale(t *testing.T) {
	if os.Getenv("ESTOPO_BENCH") == "" {
		t.Skip("set ESTOPO_BENCH=1")
	}
	ctx := context.Background()
	dir := t.TempDir()
	st, err := Open(ctx, dir, clock.NewManual(t0))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in := NewIngester(st, Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	start := time.Now()
	c := sim.Build(sim.PresetA("8.19.22"), t0)
	t.Logf("build: %v", time.Since(start).Round(time.Millisecond))

	measure := func(label string, at time.Time, kind record.Kind) {
		start := time.Now()
		r := c.CaptureAt(at.UnixMilli(), "clu-a", kind, "bench")
		capture := time.Since(start)
		start = time.Now()
		res, err := in.Ingest(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		t.Logf("%-28s shards=%d capture(render+parse)=%v ingest=%v opened=%d closed=%d events=%d heap=%dMB",
			label, len(r.Shards), capture.Round(time.Millisecond), time.Since(start).Round(time.Millisecond),
			res.ShardsOpened, res.ShardsClosed, res.Events, m.HeapAlloc>>20)
	}
	measure("first full capture", t0, record.Full)
	measure("probe", t0.Add(5*time.Minute), record.Light)
	measure("full, 1 h later", t0.Add(time.Hour), record.Full)
	measure("full, after midnight moves", time.Date(2026, 10, 1, 0, 30, 0, 0, time.UTC), record.Full)
	measure("keyframe (new week)", time.Date(2026, 10, 5, 0, 10, 0, 0, time.UTC), record.Full)

	var total int64
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	t.Logf("data directory: %d MB", total>>20)
}
