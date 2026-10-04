package api

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

	"estopo/internal/auth"
	"estopo/internal/auth/ldaptest"
	"estopo/internal/clock"
	"estopo/internal/query"
	"estopo/internal/store"
)

type authEnv struct {
	t   *testing.T
	srv *Server
	st  *store.Store
}

func newAuthEnv(t *testing.T, transport Transport) *authEnv {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewManual(t0)
	st, err := store.Open(ctx, t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := auth.NewService(st, clk, log, false)
	return &authEnv{t: t, st: st, srv: New(Options{Log: log, Clock: clk, Store: st, Engine: query.New(st, log, 2), Auth: svc, Transport: transport})}
}

type call struct {
	method, path, body string
	cookie             *http.Cookie
	csrf               string
	// header replaces the headers that the UI sends with a state-changing
	// request (JSON content type, X-Requested-With) when not nil.
	header map[string]string
	addr   string // client address (default 10.0.0.9)
	ctx    context.Context
}

func (e *authEnv) do(c call) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
	req.RemoteAddr = "10.0.0.9:1234"
	if c.addr != "" {
		req.RemoteAddr = c.addr + ":1234"
	}
	if c.ctx != nil {
		req = req.WithContext(c.ctx)
	}
	if c.header == nil && c.method != "GET" {
		c.header = map[string]string{"Content-Type": "application/json", "X-Requested-With": RequestedWith}
	}
	for k, v := range c.header {
		req.Header.Set(k, v)
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	if c.csrf != "" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func (e *authEnv) login(name, pw string) (*http.Cookie, string, int) {
	rec := e.do(call{method: "POST", path: "/api/v1/login", body: `{"name":"` + name + `","password":"` + pw + `"}`})
	var info SessionInfo
	json.Unmarshal(rec.Body.Bytes(), &info)
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c, info.CSRF, rec.Code
		}
	}
	return nil, "", rec.Code
}

func TestLocalAuthentication(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	// Mode none: everybody is admin.
	if rec := e.do(call{method: "GET", path: "/api/v1/admin/users"}); rec.Code != 200 {
		t.Fatalf("mode none: %d", rec.Code)
	}
	// Local mode needs a local admin first.
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: `{"mode":"local"}`}); rec.Code != http.StatusConflict {
		t.Errorf("local mode without admin: %d", rec.Code)
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/users/alice", body: `{"role":"admin","password":"short"}`}); rec.Code != 400 {
		t.Errorf("short password accepted: %d", rec.Code)
	}
	for _, u := range []string{`alice","role":"admin`, `bob","role":"reader`} {
		name := strings.Split(u, `"`)[0]
		role := "admin"
		if name == "bob" {
			role = "reader"
		}
		if rec := e.do(call{method: "PUT", path: "/api/v1/admin/users/" + name, body: `{"role":"` + role + `","password":"a-long-enough-pw"}`}); rec.Code != 204 {
			t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: `{"mode":"local"}`}); rec.Code != 200 {
		t.Fatalf("switch to local: %d %s", rec.Code, rec.Body.String())
	}

	// Now authentication is required.
	if rec := e.do(call{method: "GET", path: "/api/v1/clusters"}); rec.Code != 401 {
		t.Errorf("unauthenticated read: %d", rec.Code)
	}
	if _, _, code := e.login("alice", "wrong-password!"); code != 401 {
		t.Errorf("wrong password: %d", code)
	}
	cookie, csrf, code := e.login("alice", "a-long-enough-pw")
	if code != 200 || cookie == nil || csrf == "" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("login: %d %+v", code, cookie)
	}
	if rec := e.do(call{method: "GET", path: "/api/v1/clusters", cookie: cookie}); rec.Code != 200 {
		t.Errorf("authenticated read: %d", rec.Code)
	}
	// State-changing requests need the CSRF token.
	body := `{"max_age_days":60,"max_bytes":0}`
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/retention", body: body, cookie: cookie}); rec.Code != 403 {
		t.Errorf("no CSRF token: %d", rec.Code)
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/retention", body: body, cookie: cookie, csrf: "forged"}); rec.Code != 403 {
		t.Errorf("forged CSRF token: %d", rec.Code)
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/retention", body: body, cookie: cookie, csrf: csrf}); rec.Code != 200 {
		t.Errorf("with CSRF token: %d %s", rec.Code, rec.Body.String())
	}
	// A reader reads but does not administer.
	rc, rcsrf, _ := e.login("bob", "a-long-enough-pw")
	if rec := e.do(call{method: "GET", path: "/api/v1/clusters", cookie: rc}); rec.Code != 200 {
		t.Errorf("reader read: %d", rec.Code)
	}
	if rec := e.do(call{method: "GET", path: "/api/v1/admin/users", cookie: rc}); rec.Code != 403 {
		t.Errorf("reader on admin: %d", rec.Code)
	}
	// Own password change.
	if rec := e.do(call{method: "PUT", path: "/api/v1/me/password", body: `{"old":"a-long-enough-pw","new":"another-long-pw"}`, cookie: rc, csrf: rcsrf}); rec.Code != 204 {
		t.Errorf("password change: %d %s", rec.Code, rec.Body.String())
	}
	if _, _, code := e.login("bob", "another-long-pw"); code != 200 {
		t.Errorf("login with the new password: %d", code)
	}
	// Logout invalidates the session.
	if rec := e.do(call{method: "POST", path: "/api/v1/logout", cookie: cookie, csrf: csrf}); rec.Code != 204 {
		t.Errorf("logout: %d", rec.Code)
	}
	if rec := e.do(call{method: "GET", path: "/api/v1/clusters", cookie: cookie}); rec.Code != 401 {
		t.Errorf("after logout: %d", rec.Code)
	}
	// Audit trail.
	log, _ := e.st.AuditLog(context.Background(), 100)
	actions := map[string]bool{}
	for _, a := range log {
		actions[a.Action] = true
	}
	for _, want := range []string{"auth.mode", "user.save", "login", "login.failure", "retention.save", "user.password"} {
		if !actions[want] {
			t.Errorf("audit lacks %s: %v", want, actions)
		}
	}
}

func TestLoginThrottling(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	e.do(call{method: "PUT", path: "/api/v1/admin/users/alice", body: `{"role":"admin","password":"a-long-enough-pw"}`})
	e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: `{"mode":"local"}`})
	for i := 0; i < 5; i++ {
		e.login("alice", "wrong-password!")
	}
	if _, _, code := e.login("alice", "a-long-enough-pw"); code != http.StatusTooManyRequests {
		t.Errorf("6th attempt: %d", code)
	}
}

// B17 through the API: switching to local authentication on a plain HTTP,
// non-loopback listener is refused.
func TestAuthModeRefusedOnPlainHTTP(t *testing.T) {
	e := newAuthEnv(t, Transport{})
	e.do(call{method: "PUT", path: "/api/v1/admin/users/alice", body: `{"role":"admin","password":"a-long-enough-pw"}`})
	rec := e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: `{"mode":"local"}`})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "insecure_transport") {
		t.Errorf("%d %s", rec.Code, rec.Body.String())
	}
	e2 := newAuthEnv(t, Transport{AllowInsecureAuth: true})
	e2.do(call{method: "PUT", path: "/api/v1/admin/users/alice", body: `{"role":"admin","password":"a-long-enough-pw"}`})
	if rec := e2.do(call{method: "PUT", path: "/api/v1/admin/auth", body: `{"mode":"local"}`}); rec.Code != 200 {
		t.Errorf("explicitly allowed: %d", rec.Code)
	}
	_ = time.Second
}

func TestLDAPAuthentication(t *testing.T) {
	srv := ldaptest.Start(t)
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	cfg := auth.LDAPConfig{URLs: []string{srv.URL}, CAFile: srv.CAFile, BindDN: ldaptest.ServiceDN,
		BindPasswordFile: srv.BindPasswordFile, UserBase: ldaptest.PeopleBase, GroupBase: ldaptest.GroupBase,
		AdminGroups: []string{ldaptest.AdminGroupDN}, ReaderGroups: []string{ldaptest.ReaderGroupDN}}
	body := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}

	// The configuration test works before anything is saved.
	rec := e.do(call{method: "POST", path: "/api/v1/admin/auth/ldap-test", body: body(map[string]any{"ldap": cfg, "user": "bob", "password": ldaptest.BobPassword})})
	var out struct {
		OK     bool             `json:"ok"`
		Steps  []auth.LDAPStep  `json:"steps"`
		Result *auth.LDAPResult `json:"result"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || !out.OK || out.Result == nil || out.Result.Role != auth.RoleReader || len(out.Steps) != 6 {
		t.Fatalf("ldap test: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), ldaptest.BobPassword) || strings.Contains(rec.Body.String(), ldaptest.ServicePass) {
		t.Error("a password is echoed")
	}

	// LDAP mode needs a valid configuration and a local admin (the way back in).
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: `{"mode":"ldap"}`}); rec.Code != 400 {
		t.Errorf("ldap without configuration: %d", rec.Code)
	}
	bad := cfg
	bad.URLs = []string{"ldap://127.0.0.1:389"}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: body(auth.Config{Mode: auth.ModeLDAP, LDAP: &bad})}); rec.Code != 400 {
		t.Errorf("plain LDAP accepted: %d", rec.Code)
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: body(auth.Config{Mode: auth.ModeLDAP, LDAP: &cfg})}); rec.Code != http.StatusConflict {
		t.Errorf("ldap without a local admin: %d", rec.Code)
	}
	e.do(call{method: "PUT", path: "/api/v1/admin/users/root", body: `{"role":"admin","password":"a-long-enough-pw"}`})
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: body(auth.Config{Mode: auth.ModeLDAP, LDAP: &cfg})}); rec.Code != 200 {
		t.Fatalf("switch to ldap: %d %s", rec.Code, rec.Body.String())
	}

	// A reader from the directory can read, not administer.
	cookie, csrf, code := e.login("bob", ldaptest.BobPassword)
	if code != 200 || cookie == nil {
		t.Fatalf("bob login: %d", code)
	}
	if rec := e.do(call{method: "GET", path: "/api/v1/clusters", cookie: cookie}); rec.Code != 200 {
		t.Errorf("bob reads: %d", rec.Code)
	}
	if rec := e.do(call{method: "GET", path: "/api/v1/admin/users", cookie: cookie, csrf: csrf}); rec.Code != 403 {
		t.Errorf("bob administers: %d", rec.Code)
	}
	if _, _, code := e.login("carol", ldaptest.CarolPassword); code != 401 {
		t.Errorf("carol (no group): %d", code)
	}
	admin, adminCSRF, code := e.login("root", "a-long-enough-pw")
	if code != 200 {
		t.Fatalf("local admin in LDAP mode: %d", code)
	}
	// Back to local mode: directory sessions end.
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: `{"mode":"local"}`, cookie: admin, csrf: adminCSRF}); rec.Code != 200 {
		t.Fatalf("back to local: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(call{method: "GET", path: "/api/v1/clusters", cookie: cookie}); rec.Code != 401 {
		t.Errorf("LDAP session after leaving LDAP mode: %d", rec.Code)
	}
	if _, _, code := e.login("bob", ldaptest.BobPassword); code != 401 {
		t.Errorf("LDAP login in local mode: %d", code)
	}

	// Directory down: 503, not counted as a failed attempt.
	down := cfg
	down.URLs = []string{"ldaps://127.0.0.1:1"}
	e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: body(auth.Config{Mode: auth.ModeLDAP, LDAP: &down}), cookie: admin, csrf: adminCSRF})
	for i := 0; i < 6; i++ {
		if _, _, code := e.login("bob", ldaptest.BobPassword); code != http.StatusServiceUnavailable {
			t.Fatalf("directory down, attempt %d: %d", i+1, code)
		}
	}
}
