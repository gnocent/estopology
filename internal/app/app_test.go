package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"estopo/internal/api"
	"estopo/internal/auth"
	"estopo/internal/sim"
)

// End to end: essim as a real HTTP cluster, configured through the admin
// API, collected, ingested, read back through the API; then a restart
// replays nothing twice.
func TestEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := sim.Build(sim.PresetSmall("9.5.4"), time.Now().UTC())
	es := httptest.NewServer(&sim.Server{Cluster: c, Auth: sim.Auth{Disabled: true}})
	defer es.Close()

	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(ctx, Options{DataDir: dir, Log: log, Collect: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"Small","enabled":true,"es":{"URLs":["` + es.URL + `"],"Auth":"none","Timeout":5000000000}}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/clusters/small", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("save cluster: %d %s", rec.Code, rec.Body.String())
	}

	// The worker probes and captures at once; wait for the ingestion.
	var home []api.ClusterSummary
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		rec = httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/clusters", nil))
		json.Unmarshal(rec.Body.Bytes(), &home)
		if len(home) == 1 && home[0].Counts != nil && home[0].Counts.Shards > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(home) != 1 || home[0].Counts == nil || home[0].Counts.Nodes != len(c.Nodes) {
		t.Fatalf("home after collection: %+v", home)
	}
	cl, _ := a.Store().Cluster(ctx, "small")
	if cl.BoundUUID != c.UUID {
		t.Errorf("bound uuid %q, want %q", cl.BoundUUID, c.UUID)
	}
	rec = httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !strings.Contains(rec.Body.String(), `"clusters":1`) {
		t.Errorf("healthz %s", rec.Body.String())
	}
	cancel()
	a.Close()

	// Restart: the spool holds records already ingested; nothing is replayed twice.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	b, err := New(ctx2, Options{DataDir: dir, Log: log, Collect: false})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.Start(ctx2); err != nil {
		t.Fatalf("restart: %v", err)
	}
	rec = httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/clusters/small/state", nil))
	if rec.Code != 200 {
		t.Errorf("state after restart: %d %s", rec.Code, rec.Body.String())
	}
}

// B17: an instance configured for local authentication refuses to start on
// plain HTTP on a non-loopback address.
func TestRefuseInsecureAuthentication(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(ctx, Options{DataDir: dir, Log: log, LoopbackOnly: true, AdminPassword: "initial-password-1"})
	if err != nil {
		t.Fatal(err)
	}
	a.Store().SetSetting(ctx, "auth", map[string]string{"mode": "local"})
	a.Close()
	if _, err := New(ctx, Options{DataDir: dir, Log: log}); err == nil || !strings.Contains(err.Error(), "ESTOPO_ALLOW_INSECURE_AUTH") {
		t.Fatalf("started on plain HTTP: %v", err)
	}
	b, err := New(ctx, Options{DataDir: dir, Log: log, TLS: true, AdminPassword: "initial-password-1"})
	if err != nil {
		t.Fatalf("TLS: %v", err)
	}
	defer b.Close()
	users, _ := b.Store().Users(ctx)
	if len(users) != 1 || users[0].Name != "admin" || users[0].Role != "admin" {
		t.Errorf("first admin not created: %+v", users)
	}
}

// ESTOPO_ADMIN_PASSWORD at the first start creates the account "admin" and
// enables local authentication; afterwards it is ignored.
func TestBootstrapAdmin(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	const pw = "initial-password-1"
	open := func(t *testing.T, o Options) (*App, error) {
		o.Log = log
		a, err := New(ctx, o)
		if err == nil {
			t.Cleanup(func() { a.Close() })
		}
		return a, err
	}
	get := func(a *App, path string) int {
		rec := httptest.NewRecorder()
		a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}

	dir := t.TempDir()
	a, err := open(t, Options{DataDir: dir, LoopbackOnly: true, AdminPassword: pw})
	if err != nil {
		t.Fatal(err)
	}
	if code := get(a, "/api/v1/clusters"); code != http.StatusUnauthorized {
		t.Errorf("first start with ESTOPO_ADMIN_PASSWORD: unauthenticated read %d", code)
	}
	u, err := a.Store().User(ctx, "admin")
	if err != nil || u.Role != auth.RoleAdmin || u.Source != "local" || u.Disabled {
		t.Fatalf("admin account: %+v %v", u, err)
	}
	if ok, _ := auth.VerifyPassword(u.Password, pw); !ok {
		t.Error("the account does not have the given password")
	}
	a.Close()
	// Later starts: the variable is ignored, even changed or too short.
	for _, later := range []string{"another-password-2", "short", ""} {
		b, err := open(t, Options{DataDir: dir, LoopbackOnly: true, AdminPassword: later})
		if later == "short" {
			if err == nil || !strings.Contains(err.Error(), "ESTOPO_ADMIN_PASSWORD") {
				t.Errorf("short password: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("start with %q: %v", later, err)
		}
		u2, _ := b.Store().User(ctx, "admin")
		if users, _ := b.Store().Users(ctx); len(users) != 1 || u2.Password != u.Password {
			t.Errorf("start with %q changed the accounts", later)
		}
		b.Close()
	}

	// Refused rather than applied over plain HTTP on a non-loopback address.
	dir = t.TempDir()
	if _, err := open(t, Options{DataDir: dir, AdminPassword: pw}); err == nil || !strings.Contains(err.Error(), "ESTOPO_ALLOW_INSECURE_AUTH") {
		t.Fatalf("plain HTTP: %v", err)
	}
	c, err := open(t, Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if users, _ := c.Store().Users(ctx); len(users) != 0 || get(c, "/api/v1/clusters") != 200 {
		t.Errorf("a refused start left something behind: %+v", users)
	}
	// Mode none chosen by an administrator is a configuration: not replaced.
	c.Store().SetSetting(ctx, "auth", auth.Config{Mode: auth.ModeNone})
	c.Close()
	d, err := open(t, Options{DataDir: dir, TLS: true, AdminPassword: pw})
	if err != nil {
		t.Fatal(err)
	}
	if users, _ := d.Store().Users(ctx); len(users) != 0 || get(d, "/api/v1/clusters") != 200 {
		t.Errorf("configured instance bootstrapped: %+v", users)
	}

	// Without the variable nothing is created, and no password is generated.
	e, err := open(t, Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if users, _ := e.Store().Users(ctx); len(users) != 0 || get(e, "/api/v1/clusters") != 200 {
		t.Errorf("accounts without ESTOPO_ADMIN_PASSWORD: %+v", users)
	}
}

// Unreadable authentication settings stop the start.
func TestRefuseCorruptedAuthSettings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(ctx, Options{DataDir: dir, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	a.Store().SetSetting(ctx, "auth", map[string]string{"mode": "locl"})
	a.Close()
	if b, err := New(ctx, Options{DataDir: dir, Log: log, TLS: true}); err == nil {
		b.Close()
		t.Fatal("started with an unknown authentication mode")
	}
}

func TestHTTPServerLimits(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NotFoundHandler(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if srv.ReadTimeout != 30*time.Second || srv.IdleTimeout != 120*time.Second || srv.ReadHeaderTimeout <= 0 || srv.MaxHeaderBytes <= 0 {
		t.Errorf("limits: %+v", srv)
	}
	// Exports and partition snapshots take time.
	if srv.WriteTimeout < 5*time.Minute {
		t.Errorf("write timeout %v", srv.WriteTimeout)
	}
}
