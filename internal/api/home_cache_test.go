package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/query"
	"estopo/internal/record"
	"estopo/internal/sim"
	"estopo/internal/store"
)

// The home screen reuses the computed summary of a cluster until a new
// capture arrives; the configuration fields are always fresh.
func TestHomeCache(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewManual(t0)
	st, err := store.Open(ctx, t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	in := store.NewIngester(st, store.Options{MachineAttr: func(string) string { return "machine" }}, log)
	st.SaveCluster(ctx, store.Cluster{ID: "small", Name: "Small", Enabled: true})
	c := sim.Build(sim.PresetSmall("8.19.22"), t0)
	ingest := func(d time.Duration, kind record.Kind) {
		if _, err := in.Ingest(ctx, c.CaptureAt(t0.Add(d).UnixMilli(), "small", kind, "t")); err != nil {
			t.Fatal(err)
		}
		clk.Set(t0.Add(d + time.Second))
	}
	srv := New(Options{Log: log, Clock: clk, Store: st, Engine: query.New(st, log, 4)})
	home := func() ClusterSummary {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/clusters", nil))
		var out []ClusterSummary
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out) != 1 {
			t.Fatalf("home: %s", rec.Body.String())
		}
		return out[0]
	}

	ingest(0, record.Full)
	first := home()
	if first.Counts == nil || len(first.Missing) != 0 {
		t.Fatalf("first: %+v", first)
	}
	st.SaveCluster(ctx, store.Cluster{ID: "small", Name: "Renamed", Enabled: true})
	if h := home(); h.Name != "Renamed" || h.Counts == nil {
		t.Errorf("configuration not fresh: %+v", h.Name)
	}
	// A node leaves: the next probe changes the summary (within the TTL).
	c.Do(func(c *sim.Cluster) { c.NodeLeave("small-hot-dc1-m01-1") })
	ingest(30*time.Second, record.Light)
	if h := home(); !slices.Contains(h.Missing, "small-hot-dc1-m01-1") || h.LastCaptureMs != t0.Add(30*time.Second).UnixMilli() {
		t.Errorf("after a new capture: missing %v, last %d", h.Missing, h.LastCaptureMs)
	}
}
