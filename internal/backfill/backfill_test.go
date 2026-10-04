package backfill

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/query"
	"estopo/internal/sim"
	"estopo/internal/store"
)

// Two days of the small cluster with the demo scenario: the history holds
// the incident in progress at the end, the configuration is disabled.
func TestRunWithScenario(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	sc, err := sim.LoadScenario("demo")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	days := 0
	res, err := Run(ctx, Options{Dir: dir, Start: start, Days: 2, Scenario: sc, DisableAtEnd: true, Log: log,
		Clusters: []Cluster{{ID: "small", Spec: sim.PresetSmall("8.19.22")}},
		Progress: func(string, time.Duration, time.Duration, uint64) { days++ }})
	if err != nil {
		t.Fatal(err)
	}
	if days != 1 || res.Sims["small"] == nil || len(res.Ingest) == 0 {
		t.Errorf("result: days %d, %+v", days, res.Ingest)
	}

	st, err := store.Open(ctx, dir, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if c, _ := st.Cluster(ctx, "small"); c.Enabled {
		t.Error("cluster still enabled")
	}
	e := query.New(st, log, 4)
	end := start.Add(48 * time.Hour)
	page, err := e.Events(ctx, "small", query.EventFilter{From: start.UnixMilli(), To: end.UnixMilli(), Types: []string{"node_left"}, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range page.Events {
		if strings.HasSuffix(ev.Ref, "warm-dc1-m01-1") && ev.TsHi >= end.Add(-3*time.Hour).UnixMilli() {
			found = true
		}
	}
	if !found {
		t.Errorf("node_left of the scenario not in the history: %+v", page.Events)
	}
	last, _ := e.LastCaptureTs(ctx, "small")
	if last < end.Add(-10*time.Minute).UnixMilli() {
		t.Errorf("history ends at %s", time.UnixMilli(last).UTC())
	}
}
