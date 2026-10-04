package app

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"estopo/internal/sim"
)

// /metrics is off by default; once enabled it exposes the collection,
// ingestion and storage state in the Prometheus text format (SPEC §12.3).
func TestMetrics(t *testing.T) {
	c := sim.Build(sim.PresetSmall("9.5.4"), time.Now().UTC())
	es := httptest.NewServer(&sim.Server{Cluster: c, Auth: sim.Auth{Disabled: true}})
	defer es.Close()
	n := startNode(t, t.TempDir(), "", "")
	body := `{"name":"Small","enabled":true,"es":{"URLs":["` + es.URL + `"],"Auth":"none","Timeout":5000000000}}`
	if rec := n.do("PUT", "/api/v1/admin/clusters/small", body); rec.Code != 200 {
		t.Fatalf("save cluster: %d", rec.Code)
	}
	waitFor(t, "first capture", func() bool { return n.lastTs() > 0 && n.a.Idle() })

	if rec := n.do("GET", "/metrics", ""); rec.Code != 404 {
		t.Errorf("metrics served while disabled: %d", rec.Code)
	}
	if rec := n.do("PUT", "/api/v1/admin/metrics", `{"enabled":true}`); rec.Code != 200 {
		t.Fatalf("enable metrics: %d", rec.Code)
	}
	rec := n.do("GET", "/metrics", "")
	out := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("metrics: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, want := range []string{
		`estopo_ha_role{role="primary"} 1`,
		`estopo_capture_last_ok{cluster="small"} 1`,
		`estopo_probe_last_ok{cluster="small"} 1`,
		`estopo_cluster_suspended{cluster="small"} 0`,
		`estopo_ingest_records_total{cluster="small",kind="full"} 1`,
		`# TYPE estopo_ingest_records_total counter`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics lack %q", want)
		}
	}
	if !regexp.MustCompile(`estopo_storage_bytes\{cluster="small"\} [1-9]`).MatchString(out) {
		t.Error("storage size missing")
	}
	// Every line is a comment or "name{labels} value".
	line := regexp.MustCompile(`^(# (HELP|TYPE) .+|[a-z_]+(\{[a-z_]+="[^"]*"(,[a-z_]+="[^"]*")*\})? [-+0-9.e]+)$`)
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if !line.MatchString(l) {
			t.Errorf("malformed line %q", l)
		}
	}
}
