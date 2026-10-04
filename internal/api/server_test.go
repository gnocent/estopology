package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"estopo/internal/clock"
	"estopo/internal/query"
	"estopo/internal/record"
	"estopo/internal/sim"
	"estopo/internal/store"
)

var t0 = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

type aenv struct {
	t   *testing.T
	srv *Server
	clk *clock.Manual
	c   *sim.Cluster
}

// newAEnv ingests one hour of history of the small simulated cluster: a
// node leaves at +20 min, an index is deleted at +40 min.
func newAEnv(t *testing.T) *aenv {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewManual(t0)
	st, err := store.Open(ctx, t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	in := store.NewIngester(st, store.Options{MachineAttr: func(string) string { return "machine" }}, log)
	if err := st.SaveCluster(ctx, store.Cluster{ID: "small", Name: "Small cluster", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	c := sim.Build(sim.PresetSmall("8.19.22"), t0)
	for i := 0; i <= 12; i++ {
		d := time.Duration(i) * 5 * time.Minute
		switch i {
		case 4:
			c.Do(func(c *sim.Cluster) { c.NodeLeave("small-hot-dc1-m01-1") })
		case 8:
			c.Do(func(c *sim.Cluster) { c.DeleteIndex(c.Indices[2].Name) })
		}
		kind := record.Light
		if i%2 == 0 {
			kind = record.Full
		}
		if _, err := in.Ingest(ctx, c.CaptureAt(t0.Add(d).UnixMilli(), "small", kind, "t")); err != nil {
			t.Fatal(err)
		}
	}
	clk.Set(t0.Add(time.Hour))
	srv := New(Options{Log: log, Clock: clk, Store: st, Engine: query.New(st, log, 4)})
	return &aenv{t: t, srv: srv, clk: clk, c: c}
}

func (e *aenv) get(path string, v any) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if v != nil {
		if rec.Code != 200 {
			e.t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			e.t.Fatalf("GET %s: %v", path, err)
		}
	}
	return rec
}

func ms(d time.Duration) string { return strings.TrimSpace(jsonNum(t0.Add(d).UnixMilli())) }
func jsonNum(n int64) string    { b, _ := json.Marshal(n); return string(b) }

func TestHealthz(t *testing.T) {
	clk := clock.NewManual(t0)
	srv := New(Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Clock: clk})
	clk.Advance(90 * time.Second)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var got healthz
	json.Unmarshal(rec.Body.Bytes(), &got)
	if rec.Code != 200 || got.Status != "ok" || got.Uptime != "1m30s" || got.Role != "primary" {
		t.Errorf("%d %+v", rec.Code, got)
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz: %d", rec.Code)
	}
}

func TestReadEndpoints(t *testing.T) {
	e := newAEnv(t)
	var home []ClusterSummary
	e.get("/api/v1/clusters", &home)
	if len(home) != 1 || home[0].Name != "Small cluster" || home[0].Counts == nil || len(home[0].Timeline) != 84 || home[0].Assessment == nil {
		t.Fatalf("home %+v", home)
	}
	if home[0].Assessment.Level != 2 || len(home[0].Missing) != 1 {
		t.Errorf("missing node not reported: %+v", home[0].Assessment)
	}

	var st StateView
	e.get("/api/v1/clusters/small/state?at="+ms(10*time.Minute), &st)
	if st.Counts.Nodes != len(e.c.Nodes) || st.Hierarchy == nil || len(st.Hierarchy.Children) != 2 || st.Counts.Shards == 0 {
		t.Errorf("state %+v", st.Counts)
	}
	e.get("/api/v1/clusters/small/state?at="+ms(time.Hour), &st)
	var missing *NodeView
	for i := range st.Nodes {
		if st.Nodes[i].Missing {
			missing = &st.Nodes[i]
		}
	}
	if missing == nil || missing.Name != "small-hot-dc1-m01-1" || missing.Machine != "small-hot-dc1-m01" {
		t.Errorf("missing node placeholder %+v", missing)
	}

	var m Matrix
	e.get("/api/v1/clusters/small/matrix?top=3", &m)
	if len(m.Families) != 4 || m.Families[3] != "" || len(m.Counts) != 4 || len(m.Nodes) == 0 {
		t.Errorf("matrix %+v", m)
	}

	// Filter: only the families that match, by their name or by an index name.
	one := m.Families[0]
	e.get("/api/v1/clusters/small/matrix?top=3&q="+strings.ToUpper(one), &m)
	if m.Total < 1 || m.Total > 3 || m.Families[0] != one || m.Families[len(m.Families)-1] != "" {
		t.Errorf("filtered matrix: total %d, families %q", m.Total, m.Families)
	}
	for _, f := range m.Families[:len(m.Families)-1] {
		if !strings.Contains(f, one) {
			t.Errorf("family %q does not match %q", f, one)
		}
	}
	// An index name is searched only when no family name matches.
	var first page[IndexView]
	e.get("/api/v1/clusters/small/indices?limit=1", &first)
	if len(first.Items) == 1 && first.Items[0].Name != first.Items[0].Family {
		e.get("/api/v1/clusters/small/matrix?q="+first.Items[0].Name, &m)
		if m.Total != 1 || m.Families[0] != first.Items[0].Family {
			t.Errorf("matrix by index name %q: total %d, families %q", first.Items[0].Name, m.Total, m.Families)
		}
	} else {
		t.Errorf("no index with a date or counter suffix to test the search by index name: %+v", first.Items)
	}
	// One row per index: names and UUIDs side by side, the same copies in all.
	var byFamily, byIndex Matrix
	e.get("/api/v1/clusters/small/matrix?top=500", &byFamily)
	e.get("/api/v1/clusters/small/matrix?top=500&rows=index", &byIndex)
	sum := func(m Matrix) (n int) {
		for _, row := range m.Counts {
			for _, c := range row {
				n += int(c)
			}
		}
		return n
	}
	if len(byIndex.UUIDs) != byIndex.Total || len(byIndex.Families) != byIndex.Total+1 || byIndex.Total <= byFamily.Total ||
		sum(byIndex) == 0 || sum(byIndex) != sum(byFamily) {
		t.Errorf("matrix by index: %d rows, %d uuids, total %d (families %d), copies %d vs %d",
			len(byIndex.Families), len(byIndex.UUIDs), byIndex.Total, byFamily.Total, sum(byIndex), sum(byFamily))
	}
	if len(first.Items) == 1 {
		e.get("/api/v1/clusters/small/matrix?rows=index&q="+first.Items[0].Name, &m)
		if m.Total != 1 || m.Families[0] != first.Items[0].Name || m.UUIDs[0] != first.Items[0].UUID {
			t.Errorf("matrix by index, filtered: %+v", m.Families)
		}
	}
	e.get("/api/v1/clusters/small/matrix?q=no-such-family", &m)
	if m.Total != 0 || len(m.Families) != 1 {
		t.Errorf("matrix without match: total %d, families %q", m.Total, m.Families)
	}

	var shards page[ShardView]
	e.get("/api/v1/clusters/small/shards?limit=5", &shards)
	if shards.Total == 0 || len(shards.Items) != 5 {
		t.Errorf("shards %+v", shards)
	}
	var indices page[IndexView]
	e.get("/api/v1/clusters/small/indices?sort=size", &indices)
	if indices.Total == 0 || indices.Items[0].StoreBytes < indices.Items[len(indices.Items)-1].StoreBytes {
		t.Errorf("indices %+v", indices.Total)
	}
	uuid := indices.Items[0].UUID
	var withSizes page[ShardView]
	e.get("/api/v1/clusters/small/shards?index="+uuid, &withSizes)
	if withSizes.Total == 0 || withSizes.Items[0].StoreBytes <= 0 {
		t.Errorf("shard sizes %+v", withSizes.Items)
	}
	var fams page[FamilyView]
	e.get("/api/v1/clusters/small/families", &fams)
	if fams.Total != 5 {
		t.Errorf("families %d", fams.Total)
	}
	var idx IndexDetail
	e.get("/api/v1/clusters/small/indices/"+uuid+"?from="+ms(0), &idx)
	if idx.Index == nil || len(idx.Shards) == 0 || len(idx.History.Periods) == 0 {
		t.Errorf("index detail %+v", idx)
	}
	var node NodeDetail
	e.get("/api/v1/clusters/small/nodes/small-hot-dc1-m01-1?at="+ms(5*time.Minute)+"&from="+ms(0), &node)
	if node.Node == nil || len(node.Families) == 0 || len(node.History.Generations) != 1 {
		t.Errorf("node detail %+v", node)
	}

	var diff struct {
		Diff   query.Diff
		Totals map[string]int
	}
	e.get("/api/v1/clusters/small/diff?a="+ms(0)+"&b="+ms(time.Hour), &diff)
	if diff.Diff.Summary.NodesRemoved != 1 || diff.Diff.Summary.IndicesRemoved != 1 {
		t.Errorf("diff %+v", diff.Diff.Summary)
	}
	var losses query.Losses
	e.get("/api/v1/clusters/small/losses?a="+ms(0)+"&b="+ms(time.Hour), &losses)
	if len(losses.Groups) != 1 || len(losses.Deleted) != 1 {
		t.Errorf("losses %+v", losses.Summary)
	}
	// The lists are capped; the summary still counts everything.
	if n := len(losses.Groups[0].Copies); n > 1 {
		var capped query.Losses
		e.get("/api/v1/clusters/small/losses?limit=1&a="+ms(0)+"&b="+ms(time.Hour), &capped)
		if len(capped.Groups[0].Copies) != 1 || capped.Groups[0].More != n-1 || capped.Summary != losses.Summary {
			t.Errorf("capped losses: %d copies, %d more, summary %+v", len(capped.Groups[0].Copies), capped.Groups[0].More, capped.Summary)
		}
	} else {
		t.Errorf("the node loss should involve several copies: %d", n)
	}
	var events query.EventPage
	e.get("/api/v1/clusters/small/events?from="+ms(0)+"&types=node_left,index_deleted", &events)
	if len(events.Events) != 2 {
		t.Errorf("events %+v", events.Events)
	}
	var cons struct {
		Counts map[string]int
		Page   page[ConstraintView]
	}
	e.get("/api/v1/clusters/small/constraints", &cons)
	if cons.Page.Total == 0 || len(cons.Counts) == 0 {
		t.Errorf("constraints %+v", cons.Counts)
	}
	var hits []query.Hit
	e.get("/api/v1/search?q=small-warm", &hits)
	if len(hits) == 0 {
		t.Error("no search hit")
	}
	var flows Flows
	e.get("/api/v1/clusters/small/flows?a="+ms(0)+"&b="+ms(time.Hour)+"&level=tier", &flows)
	if len(flows.Histogram) != 48 {
		t.Errorf("flows %+v", flows)
	}

	if rec := e.get("/api/v1/clusters/nope/state", nil); rec.Code != 404 {
		t.Errorf("unknown cluster: %d", rec.Code)
	}
	if rec := e.get("/api/v1/clusters/small/state?at=yesterday", nil); rec.Code != 400 {
		t.Errorf("bad date: %d", rec.Code)
	}
}

// B22: the XLSX export of a diff opens, one sheet per category, labels in
// the requested language.
func TestExportXLSX(t *testing.T) {
	e := newAEnv(t)
	for _, tc := range []struct{ lang, sheet, name, change, settings string }{{"fr", "Synthèse", "Nom", "Changement", "Settings d’index"}, {"en", "Summary", "Name", "Change", "Index settings"}} {
		rec := e.get("/api/v1/clusters/small/export/diff?format=xlsx&lang="+tc.lang+"&a="+ms(0)+"&b="+ms(time.Hour), nil)
		if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), ".xlsx") {
			t.Fatalf("%s: %d %s", tc.lang, rec.Code, rec.Body.String())
		}
		f, err := excelize.OpenReader(bytes.NewReader(rec.Body.Bytes()))
		if err != nil {
			t.Fatalf("%s: the file does not open: %v", tc.lang, err)
		}
		sheets := f.GetSheetList()
		if len(sheets) != 7 || sheets[0] != tc.sheet || sheets[4] != tc.settings {
			t.Errorf("%s: sheets %v", tc.lang, sheets)
		}
		rows, _ := f.GetRows(sheets[1]) // nodes
		if len(rows) < 2 || rows[0][0] != tc.name || rows[0][2] != tc.change {
			t.Errorf("%s: nodes sheet %v", tc.lang, rows)
		}
		sum, _ := f.GetRows(sheets[0])
		if len(sum) < 12 {
			t.Errorf("%s: summary %v", tc.lang, sum)
		}
		f.Close()
	}
	for _, what := range []string{"events", "losses", "constraints"} {
		for _, format := range []string{"csv", "json", "xlsx"} {
			rec := e.get("/api/v1/clusters/small/export/"+what+"?format="+format+"&from="+ms(0)+"&a="+ms(0)+"&b="+ms(time.Hour), nil)
			if rec.Code != 200 || rec.Body.Len() == 0 {
				t.Errorf("%s/%s: %d", what, format, rec.Code)
			}
		}
	}
}

func TestAdminClusters(t *testing.T) {
	e := newAEnv(t)
	body := `{"name":"Other","enabled":true,"es":{"URLs":["https://es.example:9200"],"Auth":"basic","Username":"u","PasswordFile":"/etc/pw"}}`
	// What the UI sends with a state-changing request.
	send := func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", RequestedWith)
		return req
	}
	req := send(http.MethodPut, "/api/v1/admin/clusters/other-1", body)
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	var list []store.Cluster
	e.get("/api/v1/admin/clusters", &list)
	if len(list) != 2 {
		t.Errorf("clusters %+v", list)
	}
	rec = httptest.NewRecorder()
	e.srv.ServeHTTP(rec, send(http.MethodPut, "/api/v1/admin/clusters/Bad..ID", body))
	if rec.Code != 400 {
		t.Errorf("invalid id accepted: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	e.srv.ServeHTTP(rec, send(http.MethodPut, "/api/v1/admin/clusters/x", `{"bogus":1}`))
	if rec.Code != 400 {
		t.Errorf("unknown field accepted: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	e.srv.ServeHTTP(rec, send(http.MethodDelete, "/api/v1/admin/clusters/other-1", ""))
	if rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
}
