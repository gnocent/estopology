// Package bench holds the plan step 8 benchmarks: a 30-day backfill of the
// reference clusters through the real collector (HTTP) and ingestion, then
// the SPEC §14 query measurements on the resulting history.
//
//	ESTOPO_BACKFILL=1 go test ./internal/bench -run TestBackfill -timeout 4h -v
//	ESTOPO_BENCH_QUERIES=1 go test ./internal/bench -run TestQueries -v
//
// ESTOPO_BENCH_DIR (default ~/estopo-bench) must be on a local Linux
// filesystem. Results are written as JSON next to the data directory.
package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"estopo/internal/app"
	"estopo/internal/backfill"
	"estopo/internal/clock"
	"estopo/internal/query"
	"estopo/internal/sim"
	"estopo/internal/store"
)

var start = time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC) // Monday

func benchDir(t *testing.T) string {
	d := os.Getenv("ESTOPO_BENCH_DIR")
	if d == "" {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, "estopo-bench")
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	return d
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

type clusterResult struct {
	ID          string `json:"id"`
	Version     string `json:"version"`
	Nodes       int    `json:"nodes"`
	Copies      int    `json:"copies"`
	Indices     int    `json:"indices"`
	DiskBytes   int64  `json:"disk_bytes"`
	Partitions  int    `json:"partitions"`
	ShardRows   int64  `json:"shard_iv_rows"`
	MetricRows  int64  `json:"shard_metric_rows"`
	Events      int64  `json:"events"`
	FullCapture int64  `json:"full_captures"`
	Probes      int64  `json:"probes"`
}

type backfillResult struct {
	Days        int                `json:"days"`
	WallSeconds float64            `json:"wall_seconds"`
	Clusters    []clusterResult    `json:"clusters"`
	Ingest      []app.IngestStat   `json:"ingest"`
	HeapMB      uint64             `json:"heap_mb_end"`
	SysMB       uint64             `json:"sys_mb_max"`
	DaysWall    map[string]float64 `json:"wall_seconds_per_day"`
}

func TestBackfill(t *testing.T) {
	if os.Getenv("ESTOPO_BACKFILL") == "" {
		t.Skip("set ESTOPO_BACKFILL=1")
	}
	days := envInt("ESTOPO_BACKFILL_DAYS", 30)
	root := benchDir(t)
	dir := filepath.Join(root, "data")
	os.RemoveAll(dir)
	ctx := context.Background()

	logf, err := os.Create(filepath.Join(root, "backfill.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logf.Close()
	log := slog.New(slog.NewTextHandler(logf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	specs := []sim.Spec{sim.PresetA("7.17.29"), sim.PresetB("8.19.22"), sim.PresetC("9.5.4")}
	var clusters []backfill.Cluster
	for _, spec := range specs {
		clusters = append(clusters, backfill.Cluster{ID: strings.TrimPrefix(spec.Name, "clu-"), Spec: spec})
	}
	res := backfillResult{Days: days, DaysWall: map[string]float64{}}
	out, err := backfill.Run(ctx, backfill.Options{Dir: dir, Start: start, Days: days, Clusters: clusters, Log: log,
		Progress: func(day string, dayWall, total time.Duration, heap uint64) {
			res.DaysWall[day] = dayWall.Seconds()
			t.Logf("%s done in %.0fs (total %.0fs), heap %d MB", day, dayWall.Seconds(), total.Seconds(), heap>>20)
		}})
	if err != nil {
		t.Fatal(err)
	}
	res.WallSeconds = out.Wall.Seconds()
	res.Ingest = out.Ingest
	sort.Slice(res.Ingest, func(i, j int) bool {
		return res.Ingest[i].Cluster+res.Ingest[i].Kind < res.Ingest[j].Cluster+res.Ingest[j].Kind
	})
	res.HeapMB, res.SysMB = out.Heap>>20, out.MaxSys>>20

	st, err := store.Open(ctx, dir, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, cl := range clusters {
		s := out.Sims[cl.ID]
		cr := clusterResult{ID: cl.ID, Version: cl.Spec.Version, Nodes: len(s.Nodes), Indices: len(s.Indices)}
		for _, ix := range s.Indices {
			cr.Copies += ix.Pri * (1 + ix.Rep)
		}
		parts, _ := st.Partitions(ctx, cl.ID)
		cr.Partitions = len(parts)
		for _, p := range parts {
			pp, err := st.OpenExisting(ctx, p)
			if err != nil {
				continue
			}
			db := pp.DB()
			db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
			for q, dst := range map[string]*int64{
				"SELECT count(*) FROM shard_iv":                     &cr.ShardRows,
				"SELECT count(*) FROM shard_metric":                 &cr.MetricRows,
				"SELECT count(*) FROM event":                        &cr.Events,
				"SELECT count(*) FROM capture WHERE kind = 'full'":  &cr.FullCapture,
				"SELECT count(*) FROM capture WHERE kind = 'light'": &cr.Probes,
			} {
				var n int64
				db.QueryRowContext(ctx, q).Scan(&n)
				*dst += n
			}
		}
		cr.DiskBytes = dirSize(filepath.Join(dir, "clusters", cl.ID))
		res.Clusters = append(res.Clusters, cr)
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	os.WriteFile(filepath.Join(root, "backfill.json"), b, 0o644)
	t.Logf("%s", b)
}

func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// --- queries (SPEC §14) --------------------------------------------------------------

type timing struct {
	Name   string    `json:"name"`
	Target string    `json:"target"`
	P50ms  float64   `json:"p50_ms"`
	P95ms  float64   `json:"p95_ms"`
	Maxms  float64   `json:"max_ms"`
	Runs   int       `json:"runs"`
	All    []float64 `json:"-"`
}

func measure(name, target string, runs int, fn func(i int) error) (timing, error) {
	tm := timing{Name: name, Target: target, Runs: runs}
	for i := 0; i < runs; i++ {
		t0 := time.Now()
		if err := fn(i); err != nil {
			return tm, fmt.Errorf("%s: %w", name, err)
		}
		tm.All = append(tm.All, float64(time.Since(t0).Microseconds())/1000)
	}
	s := append([]float64(nil), tm.All...)
	sort.Float64s(s)
	tm.P50ms, tm.P95ms, tm.Maxms = s[len(s)/2], s[min(len(s)-1, int(float64(len(s))*0.95))], s[len(s)-1]
	return tm, nil
}

func TestQueries(t *testing.T) {
	if os.Getenv("ESTOPO_BENCH_QUERIES") == "" {
		t.Skip("set ESTOPO_BENCH_QUERIES=1")
	}
	root := benchDir(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	cluster := os.Getenv("ESTOPO_BENCH_CLUSTER")
	if cluster == "" {
		cluster = "a"
	}
	st, err := store.Open(ctx, filepath.Join(root, "data"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	eng := query.New(st, log, 8)
	last, err := eng.LastCaptureTs(ctx, cluster)
	if err != nil || last == 0 {
		t.Fatalf("no history for %s: %v", cluster, err)
	}
	first := start.UnixMilli() + (8 * 24 * time.Hour).Milliseconds() // leave 7 days before for diffs
	rng := rand.New(rand.NewPCG(1, 2))
	dates := make([]int64, 20)
	for i := range dates {
		dates[i] = first + rng.Int64N(last-first)
	}
	var out []timing
	add := func(tm timing, err error) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, tm)
		t.Logf("%-32s p50 %8.1f ms  p95 %8.1f ms  max %8.1f ms  (target %s)", tm.Name, tm.P50ms, tm.P95ms, tm.Maxms, tm.Target)
	}
	// Cold: a fresh engine per date (no cached structure).
	add(measure("state at T (cold)", "< 2 s p95", len(dates), func(i int) error {
		_, err := query.New(st, log, 8).Overview(ctx, cluster, dates[i], nil)
		return err
	}))
	for _, d := range dates { // warm the cache
		eng.Overview(ctx, cluster, d, nil)
	}
	add(measure("state at T (warm)", "< 300 ms p95", len(dates), func(i int) error {
		_, err := eng.Overview(ctx, cluster, dates[i], nil)
		return err
	}))
	// The measure above cycles through 20 dates with an LRU of 8 structures:
	// every call misses. These two measure a really warm cache (8 dates
	// already visited), as a user moving around a few dates would see.
	cached := dates[:8]
	for _, d := range cached {
		eng.Overview(ctx, cluster, d, nil)
	}
	add(measure("state at T (warm, 8 cached dates)", "< 300 ms p95", 40, func(i int) error {
		_, err := eng.Overview(ctx, cluster, cached[i%len(cached)], nil)
		return err
	}))
	add(measure("filtered shard page (cached)", "< 500 ms", 40, func(i int) error {
		st, err := eng.StateAt(ctx, cluster, cached[i%len(cached)])
		if err != nil {
			return err
		}
		fam := st.Indices[rng.IntN(len(st.Indices))].Family
		n := 0
		for _, sh := range st.Shards {
			if st.Indices[sh.Index].Family == fam {
				n++
			}
		}
		_ = n
		return nil
	}))
	s, err := eng.StateAt(ctx, cluster, last)
	if err != nil {
		t.Fatal(err)
	}
	add(measure("node detail (7 days)", "< 300 ms", 20, func(i int) error {
		n := s.Nodes[rng.IntN(len(s.Nodes))]
		if _, err := eng.NodeHistory(ctx, cluster, n.Name, last-(7*24*time.Hour).Milliseconds(), last, 500); err != nil {
			return err
		}
		_, err := eng.StateAt(ctx, cluster, last)
		return err
	}))
	add(measure("filtered shard page", "< 500 ms", 20, func(i int) error {
		st, err := eng.StateAt(ctx, cluster, dates[i])
		if err != nil {
			return err
		}
		fam := st.Indices[rng.IntN(len(st.Indices))].Family
		n := 0
		for _, sh := range st.Shards {
			if st.Indices[sh.Index].Family == fam {
				n++
			}
		}
		_ = n
		return nil
	}))
	add(measure("diff A/B over 7 days (cold)", "< 3 s", 5, func(i int) error {
		b := dates[i]
		_, err := query.New(st, log, 8).Diff(ctx, cluster, b-(7*24*time.Hour).Milliseconds(), b)
		return err
	}))
	add(measure("losses over 24 h", "< 2 s", 5, func(i int) error {
		b := dates[i]
		_, err := eng.Losses(ctx, cluster, b-(24*time.Hour).Milliseconds(), b)
		return err
	}))
	eng.Search(ctx, []string{cluster}, "x", 20) // build the catalogue
	prefixes := []string{"clu-a-hot", "logs-1", "logs-042-2026.09", "10.1.", "clu-a-cold-dc2", "logs-3"}
	add(measure("search completion", "< 150 ms", len(prefixes)*5, func(i int) error {
		_, err := eng.Search(ctx, []string{cluster}, prefixes[i%len(prefixes)], 20)
		return err
	}))
	b, _ := json.MarshalIndent(out, "", "  ")
	os.WriteFile(filepath.Join(root, "queries-"+cluster+".json"), b, 0o644)
}
