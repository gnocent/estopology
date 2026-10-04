package store

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/record"
	"estopo/internal/sim"
)

// retentionEnv ingests one full capture per day for days days, starting on
// Monday 2026-08-31, on a manual clock.
func retentionEnv(t *testing.T, days int) (*Store, *clock.Manual, *Ingester, *sim.Cluster, string) {
	t.Helper()
	ctx := context.Background()
	start := time.Date(2026, 8, 31, 6, 0, 0, 0, time.UTC)
	clk := clock.NewManual(start)
	dir := t.TempDir()
	st, err := Open(ctx, dir, clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	in := NewIngester(st, Options{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	spec := sim.PresetSmall("8.19.22")
	spec.RetentionDays = 3
	c := sim.Build(spec, start)
	for d := 0; d < days; d++ {
		at := start.Add(time.Duration(d) * 24 * time.Hour)
		clk.Set(at)
		if _, err := in.Ingest(ctx, c.CaptureAt(at.UnixMilli(), "small", record.Full, "t")); err != nil {
			t.Fatal(err)
		}
	}
	return st, clk, in, c, dir
}

// B13: partitions entirely out of retention are deleted; the others stay
// readable alone.
func TestRetentionByAge(t *testing.T) {
	ctx := context.Background()
	st, _, _, _, dir := retentionEnv(t, 40) // W36 .. W41
	parts, _ := st.Partitions(ctx, "small")
	if len(parts) != 6 {
		t.Fatalf("%d partitions", len(parts))
	}
	rep, err := st.ApplyRetention(ctx, Retention{MaxAgeDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	// Now = 2026-10-09 (W41). Limit = 2026-09-09: W36 (ends 09-07) goes,
	// W37 (ends 09-14) is only partly out of retention and stays.
	if len(rep.DeletedByAge) != 1 || rep.DeletedByAge[0].Week != "2026-W36" {
		t.Fatalf("deleted %+v", rep.DeletedByAge)
	}
	if _, err := os.Stat(filepath.Join(dir, "clusters", "small", "2026-W36.db")); !os.IsNotExist(err) {
		t.Error("W36 file still there")
	}
	left, _ := st.Partitions(ctx, "small")
	if len(left) != 5 || left[0].Week != "2026-W37" {
		t.Fatalf("left %+v", left)
	}
	// The oldest remaining partition is readable alone (keyframe).
	p, err := st.OpenExisting(ctx, left[0])
	if err != nil {
		t.Fatal(err)
	}
	var open int
	p.DB().QueryRowContext(ctx, `SELECT count(*) FROM shard_iv WHERE valid_to IS NULL`).Scan(&open)
	if open == 0 {
		t.Error("oldest partition has no open interval")
	}
}

// B14: above the size limit, the oldest partitions go until it fits; an
// alert is raised.
func TestRetentionBySize(t *testing.T) {
	ctx := context.Background()
	st, _, _, _, _ := retentionEnv(t, 20) // W36 .. W38
	parts, _ := st.Partitions(ctx, "")
	var total int64
	for _, p := range parts {
		pp, _ := st.OpenExisting(ctx, p)
		size, _ := st.RefreshSize(ctx, pp)
		total += size
	}
	// The limit applies to the whole data directory (meta.db and spool too).
	total += pathSize(filepath.Join(st.Dir(), "meta.db")) + pathSize(filepath.Join(st.Dir(), "meta.db-wal")) +
		pathSize(filepath.Join(st.Dir(), "spool"))
	limit := total * 3 / 4 // the current week (never deleted) + meta.db stay below it
	rep, err := st.ApplyRetention(ctx, Retention{MaxAgeDays: 365, MaxBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.DeletedBySize) == 0 || rep.DeletedBySize[0].Week != "2026-W36" || rep.TotalBytes > limit {
		t.Errorf("report %+v (limit %d)", rep, limit)
	}
	alerts, _ := st.Alerts(ctx)
	if len(alerts) != 1 || alerts[0].Kind != "retention_size" {
		t.Errorf("alerts %+v", alerts)
	}
	// An impossible limit never deletes the current week.
	rep, _ = st.ApplyRetention(ctx, Retention{MaxAgeDays: 365, MaxBytes: 1})
	left, _ := st.Partitions(ctx, "")
	if len(left) != 1 || left[0].Week != WeekOf(st.clock.Now().UnixMilli()).Key || !rep.OverLimit {
		t.Errorf("left %+v, over %v", left, rep.OverLimit)
	}
}

func TestPurgeAndSettings(t *testing.T) {
	ctx := context.Background()
	st, _, _, _, _ := retentionEnv(t, 8)
	n, err := st.Purge(ctx, "small")
	if err != nil || n != 2 {
		t.Errorf("purge: %d %v", n, err)
	}
	if err := st.SetSetting(ctx, "retention", Retention{MaxAgeDays: 400, MaxBytes: 1 << 40}); err != nil {
		t.Fatal(err)
	}
	pol, err := st.RetentionPolicy(ctx)
	if err != nil || pol.MaxAgeDays != 400 || pol.MaxBytes != 1<<40 {
		t.Errorf("policy %+v %v", pol, err)
	}
}

// The size limit covers the data directory: meta.db and the spool count,
// only partitions are deleted.
func TestRetentionSizeCountsMetaAndSpool(t *testing.T) {
	ctx := context.Background()
	st, _, _, _, _ := retentionEnv(t, 3)
	spool := filepath.Join(st.Dir(), "spool", "small")
	os.MkdirAll(spool, 0o750)
	os.WriteFile(filepath.Join(spool, "big.rec.zst"), make([]byte, 1<<20), 0o600)
	rep, err := st.ApplyRetention(ctx, Retention{MaxAgeDays: 365})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OtherBytes < 1<<20 || rep.TotalBytes <= rep.OtherBytes {
		t.Errorf("report %+v", rep)
	}
}
