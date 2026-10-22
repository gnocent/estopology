package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"estopo/internal/collector"
	"estopo/internal/record"
	"estopo/internal/sim"
	"estopo/internal/store"
)

// fakeCollector reports the state of the small cluster without collecting.
type fakeCollector struct{}

func (fakeCollector) Status() []collector.Status {
	return []collector.Status{{ID: "small", Name: "Small cluster"}}
}
func (fakeCollector) CaptureNow(string, bool) error { return nil }
func (fakeCollector) Resume(string, string) error   { return nil }

// The clock jumped 3 hours ahead and came back: the record ingested in
// between puts the history ahead of the clock, which /metrics and the state
// of the collectors report (SPEC §6.7); within an hour, they report nothing.
// The manual clock cannot go back: it stays at t0 while the record is dated
// 3 hours later, as a clock ahead would have dated it.
func TestHistoryAheadOfTheClock(t *testing.T) {
	e, clk := newSecEnv(t, Options{Transport: Transport{LoopbackOnly: true}, Collector: fakeCollector{}})
	in := store.NewIngester(e.st, store.Options{MachineAttr: func(string) string { return "machine" }}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	c := sim.Build(sim.PresetSmall("8.19.22"), t0)
	ahead := t0.Add(3 * time.Hour)
	if _, err := in.Ingest(context.Background(), c.CaptureAt(ahead.UnixMilli(), "small", record.Full, record.TriggerSchedule)); err != nil {
		t.Fatal(err)
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/metrics", body: `{"enabled":true}`}); rec.Code != 200 {
		t.Fatalf("enable metrics: %d", rec.Code)
	}
	check := func(what string, wantSec int64) {
		t.Helper()
		out := e.do(call{method: "GET", path: "/metrics"}).Body.String()
		if want := `estopo_history_ahead_seconds{cluster="small"} ` + jsonNum(wantSec) + "\n"; !strings.Contains(out, want) {
			t.Errorf("%s: metrics lack %q", what, want)
		}
		rec := e.do(call{method: "GET", path: "/api/v1/admin/collectors"})
		var sts []struct {
			ID             string `json:"id"`
			HistoryUntilMs int64  `json:"history_until_ms"`
			HistoryAheadMs int64  `json:"history_ahead_ms"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &sts); err != nil || len(sts) != 1 {
			t.Fatalf("%s: collectors %d %s", what, rec.Code, rec.Body.String())
		}
		until := int64(0)
		if wantSec > 0 {
			until = ahead.UnixMilli()
		}
		if s := sts[0]; s.ID != "small" || s.HistoryAheadMs != wantSec*1000 || s.HistoryUntilMs != until {
			t.Errorf("%s: collector state %+v, want ahead %d s until %d", what, s, wantSec, until)
		}
	}
	check("clock back", 3*3600)
	clk.Set(ahead.Add(-30 * time.Minute))
	check("within an hour", 0)
}
