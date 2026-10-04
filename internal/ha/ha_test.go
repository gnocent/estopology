package ha

import (
	"context"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/record"
	"estopo/internal/store"
)

func newManager(t *testing.T, token, standbyOf string) *Manager {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	clk := clock.NewManual(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	st, err := store.Open(ctx, dir, clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sp, _ := record.OpenSpool(filepath.Join(dir, "spool"))
	os.MkdirAll(filepath.Join(dir, "spool", "small"), 0o750)
	os.WriteFile(filepath.Join(dir, "spool", "small", "0001790000000000-001-light.rec.zst"), []byte("x"), 0o600)
	var tokenFile string
	if token != "" {
		tokenFile = filepath.Join(dir, "token")
		os.WriteFile(tokenFile, []byte(token), 0o600)
	}
	m, err := New(ctx, Options{Store: st, Spool: sp, Clock: clk, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		StandbyOf: standbyOf, TokenFile: tokenFile, TmpDir: filepath.Join(dir, "tmp"),
		Hooks: Hooks{Idle: func() bool { return true }, Changed: func(context.Context) error { return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func get(h http.Handler, path, token string) int {
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestReplicationAPIAccess(t *testing.T) {
	const token = "a-shared-replication-token"
	if code := get(newManager(t, "", "").Handler(), "/api/v1/replication/status", token); code != 404 {
		t.Errorf("replication without a token configured: %d", code)
	}
	h := newManager(t, token, "").Handler()
	for path, want := range map[string]int{
		"/api/v1/replication/status":                                           200,
		"/api/v1/replication/meta":                                             200,
		"/api/v1/replication/clusters":                                         200,
		"/api/v1/replication/records/small?after=0":                            200,
		"/api/v1/replication/records/small/0001790000000000-001-light.rec.zst": 200,
		"/api/v1/replication/records/small/..%2F..%2Fmeta.db":                  404,
		"/api/v1/replication/records/..%2Fclusters/x.rec.zst":                  404,
		"/api/v1/replication/records/small/.tmp-1":                             404,
		"/api/v1/replication/partitions/small/2026-W40":                        404,
		"/api/v1/replication/partitions/..%2F..%2Fmeta/2026-W40":               404,
	} {
		if code := get(h, path, token); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	for _, bad := range []string{"", "wrong-token", token + "x"} {
		if code := get(h, "/api/v1/replication/meta", bad); code != 401 {
			t.Errorf("token %q: %d", bad, code)
		}
	}
}

func TestReplicationSettings(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://primary.example.org:8443": true,
		"http://127.0.0.1:8080":            true,
		"http://localhost:8080":            true,
		"http://primary.example.org:8080":  false, // the token would travel in clear
		"ftp://primary":                    false,
		"primary:8443":                     false,
	} {
		if err := checkURL(raw, false); (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
	if checkURL("http://primary.example.org:8080", true) != nil {
		t.Error("ESTOPO_ALLOW_INSECURE_AUTH not honoured")
	}

	// A standby needs a token; the first start records the role, which then
	// wins over the environment.
	ctx := context.Background()
	dir := t.TempDir()
	st, _ := store.Open(ctx, dir, clock.Real{})
	defer st.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := New(ctx, Options{Store: st, Log: log, StandbyOf: "https://p"}); err == nil {
		t.Error("standby without a token")
	}
	short := filepath.Join(dir, "short")
	os.WriteFile(short, []byte("abc"), 0o600)
	if _, err := New(ctx, Options{Store: st, Log: log, TokenFile: short}); err == nil {
		t.Error("short token accepted")
	}
	st.SetHAState(ctx, map[string]string{"role": RolePrimary, "promoted_ms": "1000"})
	tok := filepath.Join(dir, "tok")
	os.WriteFile(tok, []byte("a-shared-replication-token"), 0o600)
	m, err := New(ctx, Options{Store: st, Log: log, StandbyOf: "https://p", TokenFile: tok})
	if err != nil || m.Role() != RolePrimary || m.Status().PromotedMs != 1000 {
		t.Errorf("promoted instance restarted with ESTOPO_STANDBY_OF: %v %+v", err, m.Status())
	}
}

// The standby reaches its primary over HTTPS with ESTOPO_REPLICATION_CA,
// and refuses a certificate it cannot verify.
func TestReplicationOverTLS(t *testing.T) {
	const token = "a-shared-replication-token"
	primary := newManager(t, token, "")
	srv := httptest.NewTLSServer(primary.Handler())
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)

	standby := newManager(t, token, srv.URL)
	if _, err := standby.client(srv.URL).status(context.Background()); err == nil {
		t.Error("unknown certificate accepted")
	}
	standby.o.CAFile = ca
	st, err := standby.client(srv.URL).status(context.Background())
	if err != nil || st.Role != RolePrimary {
		t.Errorf("status over TLS: %+v %v", st, err)
	}
}
