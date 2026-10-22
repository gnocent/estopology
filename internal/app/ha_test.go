package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"estopo/internal/api"
	"estopo/internal/ha"
	"estopo/internal/record"
	"estopo/internal/sim"
	"estopo/internal/store"
)

type haNode struct {
	t      *testing.T
	a      *App
	srv    *httptest.Server
	cancel context.CancelFunc
}

func startNode(t *testing.T, dir, token, standbyOf string) *haNode {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(ctx, Options{DataDir: dir, Log: log, Collect: true, LoopbackOnly: true,
		ReplicationTokenFile: token, StandbyOf: standbyOf, HAInterval: 200 * time.Millisecond})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := a.Start(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	n := &haNode{t: t, a: a, srv: httptest.NewServer(a.Handler()), cancel: cancel}
	t.Cleanup(n.stop)
	return n
}

func (n *haNode) stop() {
	if n.a == nil {
		return
	}
	n.cancel()
	n.srv.Close()
	n.a.Close()
	n.a = nil
}

func (n *haNode) do(method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json") // what the UI sends
	req.Header.Set("X-Requested-With", api.RequestedWith)
	n.a.Handler().ServeHTTP(rec, req)
	return rec
}

func (n *haNode) lastTs() int64 {
	ts, err := n.a.ingester.LastTs(context.Background(), "small")
	if err != nil {
		n.t.Fatal(err)
	}
	return ts
}

func (n *haNode) collecting() int {
	n.a.syncMu.Lock()
	defer n.a.syncMu.Unlock()
	return len(n.a.running)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

// captureTimes lists the capture dates of a data directory.
func captureTimes(t *testing.T, dir string) map[int64]bool {
	t.Helper()
	out := map[int64]bool{}
	files, _ := filepath.Glob(filepath.Join(dir, "clusters", "small", "*.db"))
	for _, f := range files {
		db, err := sql.Open("sqlite", "file:"+f+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		rows, err := db.Query(`SELECT ts FROM capture`)
		if err != nil { // partition being created: not ready yet
			db.Close()
			continue
		}
		for rows.Next() {
			var ts int64
			rows.Scan(&ts)
			out[ts] = true
		}
		rows.Close()
		db.Close()
	}
	return out
}

// B18 and the rest of SPEC §11: a standby replicates configuration and
// history without contacting the cluster; after the primary stops it is
// promoted and collects, with every capture received so far; the old
// primary coming back sees the promotion, stops collecting, and once
// demoted rebuilds its history from the new primary.
func TestHighAvailability(t *testing.T) {
	c := sim.Build(sim.PresetSmall("9.5.4"), time.Now().UTC())
	var esCalls atomic.Int64
	es := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		esCalls.Add(1)
		(&sim.Server{Cluster: c, Auth: sim.Auth{Disabled: true}}).ServeHTTP(w, r)
	}))
	defer es.Close()
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("a-shared-replication-token\n"), 0o600)
	dirA, dirB := t.TempDir(), t.TempDir()

	// Primary A collects.
	a := startNode(t, dirA, token, "")
	body := `{"name":"Small","enabled":true,"es":{"URLs":["` + es.URL + `"],"Auth":"none","Timeout":5000000000}}`
	if rec := a.do("PUT", "/api/v1/admin/clusters/small", body); rec.Code != 200 {
		t.Fatalf("save cluster: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "first capture on A", func() bool { return len(captureTimes(t, dirA)) > 0 && a.a.Idle() })

	// Standby B: configuration, partitions, then records.
	callsBefore := esCalls.Load()
	b := startNode(t, dirB, token, a.srv.URL)
	waitFor(t, "B catches up", func() bool { return b.lastTs() == a.lastTs() && b.a.Idle() })
	if n := esCalls.Load() - callsBefore; n != 0 {
		t.Errorf("%d cluster calls while the standby synchronised", n)
	}
	if b.a.HA().Role() != ha.RoleStandby || b.collecting() != 0 {
		t.Fatalf("B: role %s, %d workers", b.a.HA().Role(), b.collecting())
	}
	if cl, err := b.a.Store().Cluster(context.Background(), "small"); err != nil || cl.Name != "Small" {
		t.Errorf("configuration not replicated: %+v %v", cl, err)
	}
	if rec := b.do("GET", "/api/v1/clusters/small/state", ""); rec.Code != 200 {
		t.Errorf("state on B: %d", rec.Code)
	}
	if rec := b.do("PUT", "/api/v1/admin/clusters/small", body); rec.Code != http.StatusConflict {
		t.Errorf("B accepted a configuration change: %d", rec.Code)
	}
	if !strings.Contains(b.do("GET", "/healthz", "").Body.String(), `"role":"standby"`) {
		t.Error("healthz does not report standby")
	}
	st := b.a.HA().Status()
	if st.Replication == nil || len(st.Replication.Clusters) != 1 || st.Replication.Clusters[0].Rebuilds != 1 {
		t.Errorf("replication status: %+v", st.Replication)
	}

	// A new capture on A reaches B through the records.
	if rec := a.do("POST", "/api/v1/admin/clusters/small/capture?force=true", ""); rec.Code != 200 && rec.Code != 202 {
		t.Fatalf("capture now: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "second capture on A", func() bool { return len(captureTimes(t, dirA)) >= 2 && a.a.Idle() })
	waitFor(t, "B receives it", func() bool { return b.lastTs() == a.lastTs() && b.a.Idle() })
	if rec := a.do("POST", "/api/v1/admin/ha/peer", `{"url":"`+b.srv.URL+`"}`); rec.Code != 200 {
		t.Fatalf("peer on A: %d %s", rec.Code, rec.Body.String())
	}

	// A stops; B is promoted.
	a.stop()
	callsStop := esCalls.Load()
	time.Sleep(500 * time.Millisecond) // a few replication attempts against the stopped primary
	if esCalls.Load() != callsStop {
		t.Fatalf("the standby contacted the cluster: %d calls", esCalls.Load()-callsStop)
	}
	onA := captureTimes(t, dirA)
	if rec := b.do("POST", "/api/v1/admin/ha/promote", ""); rec.Code != 200 {
		t.Fatalf("promote: %d %s", rec.Code, rec.Body.String())
	}
	onB := captureTimes(t, dirB)
	for ts := range onA {
		if !onB[ts] {
			t.Errorf("capture %d of the old primary missing after promotion", ts)
		}
	}
	lastA := lastCapture(t, dirA)
	waitFor(t, "B collects", func() bool { return b.collecting() == 1 && b.lastTs() > lastA && b.a.Idle() })

	// The old primary comes back: it sees the more recent promotion and
	// does not collect.
	a2 := startNode(t, dirA, token, "")
	if a2.a.HA().Role() != ha.RoleFenced || a2.collecting() != 0 {
		t.Fatalf("old primary: role %s, %d workers", a2.a.HA().Role(), a2.collecting())
	}
	var sess struct {
		Instance string `json:"instance"`
	}
	json.Unmarshal(a2.do("GET", "/api/v1/session", "").Body.Bytes(), &sess)
	if sess.Instance != ha.RoleFenced {
		t.Errorf("session instance: %q", sess.Instance)
	}
	if rec := a2.do("POST", "/api/v1/admin/ha/resume", ""); rec.Code != http.StatusConflict {
		t.Errorf("resume while the peer is primary: %d", rec.Code)
	}
	// Demoted, it rebuilds its history from B.
	if rec := a2.do("POST", "/api/v1/admin/ha/demote", `{"url":"`+b.srv.URL+`"}`); rec.Code != 200 {
		t.Fatalf("demote: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "A follows B", func() bool { return a2.lastTs() == b.lastTs() && a2.a.Idle() })
	for ts := range captureTimes(t, dirB) {
		if !captureTimes(t, dirA)[ts] {
			t.Errorf("capture %d of B missing on the demoted A", ts)
		}
	}
	if a2.collecting() != 0 {
		t.Error("demoted instance collects")
	}
}

// The purge of a cluster (admin) removes its spooled records too: the
// standby purges it at its next cycle, and neither instance brings it back
// from its spool at the next start.
func TestPurgeReachesStandbyAndSurvivesRestart(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("a-shared-replication-token\n"), 0o600)
	dirA, dirB := t.TempDir(), t.TempDir()
	a := startNode(t, dirA, token, "")
	now := time.Now().UTC()
	c := sim.Build(sim.PresetSmall("9.5.4"), now)
	for _, id := range []string{"small", "other"} { // a primary left without any cluster blocks the standby
		if err := a.a.pipeline.Record(c.CaptureAt(now.UnixMilli(), id, record.Full, record.TriggerSchedule)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "ingestion on A", func() bool { return a.a.Idle() && len(partitions(t, a.a, "other")) == 1 })
	b := startNode(t, dirB, token, a.srv.URL)
	waitFor(t, "B catches up", func() bool { return len(partitions(t, b.a, "other")) == 1 && b.a.Idle() })

	if rec := a.do("POST", "/api/v1/admin/clusters/other/purge", ""); rec.Code != 200 {
		t.Fatalf("purge: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dirA, "spool", "other")); !os.IsNotExist(err) {
		t.Errorf("spool of the purged cluster on A: %v", err)
	}
	waitFor(t, "B purges", func() bool { return len(partitions(t, b.a, "other")) == 0 })
	if _, err := os.Stat(filepath.Join(dirB, "spool", "other")); !os.IsNotExist(err) {
		t.Errorf("spool of the purged cluster on B: %v", err)
	}

	b.stop()
	a.stop()
	a = startNode(t, dirA, token, "")
	b = startNode(t, dirB, token, a.srv.URL)
	if n, m := len(partitions(t, a.a, "other")), len(partitions(t, b.a, "other")); n != 0 || m != 0 {
		t.Errorf("purged cluster back after a restart: %d partitions on A, %d on B", n, m)
	}
	if len(partitions(t, a.a, "small")) != 1 || len(partitions(t, b.a, "small")) != 1 {
		t.Error("the other cluster lost its history")
	}
}

func partitions(t *testing.T, a *App, cluster string) []store.PartitionInfo {
	t.Helper()
	parts, err := a.Store().Partitions(context.Background(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	return parts
}

// Close waits for the replication and the retention to end before the
// store is closed: a synchronisation is not left running on a closed store.
func TestCloseWaitsForReplication(t *testing.T) {
	token := filepath.Join(t.TempDir(), "token")
	os.WriteFile(token, []byte("a-shared-replication-token\n"), 0o600)
	var calls atomic.Int64
	held, release := make(chan struct{}, 1), make(chan struct{})
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 { // the first status request waits
			held <- struct{}{}
			<-release
		}
		w.Write([]byte(`{"role":"primary","instance_id":"0123456789abcdef"}`))
	}))
	defer primary.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, Options{DataDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), LoopbackOnly: true,
		ReplicationTokenFile: token, StandbyOf: primary.URL, HAInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	<-held
	a.Close() // ctx still alive, as when Close is deferred before the cancel
	release <- struct{}{}
	time.Sleep(300 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Errorf("the replication went on after Close: %d requests", n)
	}
}

// lastCapture reads the last capture date of a data directory.
func lastCapture(t *testing.T, dir string) int64 {
	var last int64
	for ts := range captureTimes(t, dir) {
		last = max(last, ts)
	}
	return last
}
