package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"estopo/internal/auth/ldaptest"
	"estopo/internal/clock"
	"estopo/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	clk := clock.NewManual(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	st, err := store.Open(context.Background(), t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return NewService(st, clk, discardLog(), false), st
}

func loginFrom(addr string) *http.Request {
	r := httptest.NewRequest("POST", "/api/v1/login", nil)
	r.RemoteAddr = addr + ":1234"
	return r
}

// The settings are never taken for mode none when they cannot be read.
func TestConfigFailsClosed(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)
	if c, err := svc.Config(ctx); err != nil || c.Mode != ModeNone || c.SessionHours != DefaultSessionHours {
		t.Fatalf("no setting: %+v %v", c, err)
	}
	st.SetSetting(ctx, "auth", Config{Mode: ModeLocal})
	if c, err := svc.Config(ctx); err != nil || c.Mode != ModeLocal {
		t.Fatalf("local: %+v %v", c, err)
	}
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if c, err := svc.Config(gone); err == nil {
		t.Errorf("cancelled context: mode %q without error", c.Mode)
	}
	if u, err := svc.Resolve(gone, loginFrom("10.0.0.1")); err == nil || errors.Is(err, ErrUnauthenticated) {
		t.Errorf("cancelled context: resolved as %+v, %v", u, err)
	}
	for _, raw := range []string{`{`, `"local"`, `{"mode":"LOCAL"}`, `{"mode":""}`, `null`} {
		if _, err := st.Meta.Exec(`UPDATE setting SET value = ? WHERE key = 'auth'`, raw); err != nil {
			t.Fatal(err)
		}
		if c, err := svc.Config(ctx); err == nil {
			t.Errorf("setting %s: mode %q without error", raw, c.Mode)
		}
		if u, err := svc.Resolve(ctx, loginFrom("10.0.0.1")); err == nil || errors.Is(err, ErrUnauthenticated) {
			t.Errorf("setting %s: resolved as %+v, %v", raw, u, err)
		}
	}
}

// Only the caller of an instance without authentication goes without a
// CSRF token: an empty token is not a pass.
func TestCheckCSRF(t *testing.T) {
	r := httptest.NewRequest("POST", "/", nil)
	if !CheckCSRF(Anonymous(), r) {
		t.Error("mode none refused")
	}
	if CheckCSRF(User{Name: "alice", Role: RoleAdmin}, r) {
		t.Error("a user without token passes")
	}
	r.Header.Set("X-CSRF-Token", "abc")
	if !CheckCSRF(User{Name: "alice", CSRF: "abc"}, r) || CheckCSRF(User{Name: "alice", CSRF: "abd"}, r) {
		t.Error("token comparison")
	}
}

func TestLimiterReserve(t *testing.T) {
	clk := clock.NewManual(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	l := NewLimiter(clk)
	for i := 0; i < 5; i++ {
		if ok, _ := l.Reserve("a", "b"); !ok {
			t.Fatalf("attempt %d refused", i)
		}
	}
	if ok, wait := l.Reserve("a"); ok || wait != 2*time.Second {
		t.Fatalf("6th attempt: ok=%v wait=%v", ok, wait)
	}
	// A refused attempt counts for no key.
	if ok, _ := l.Reserve("a", "c"); ok {
		t.Fatal("blocked key allowed")
	}
	if ok, _ := l.Reserve("c"); !ok {
		t.Error("key counted by a refused attempt")
	}
	// An attempt given back frees its place.
	l.Release("a", "b")
	if ok, _ := l.Reserve("a", "b"); !ok {
		t.Error("released attempt still counted")
	}
	if ok, _ := l.Reserve("a"); ok {
		t.Error("quota exceeded after a release")
	}
	l.Success("a")
	if ok, _ := l.Reserve("a"); !ok {
		t.Error("success did not clear the key")
	}
}

// Parallel wrong logins cannot exceed the free quota: the attempt is
// counted before the password is checked.
func TestParallelLoginsAreThrottled(t *testing.T) {
	ctx := context.Background()
	svc, st := newTestService(t)
	st.SetSetting(ctx, "auth", Config{Mode: ModeLocal})
	h, _ := HashPassword("local-admin-password")
	st.SaveUser(ctx, store.User{Name: "admin", Role: RoleAdmin, Source: "local", Password: h})

	const n = 12
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = svc.Login(ctx, httptest.NewRecorder(), loginFrom("10.0.0.1"), "admin", "wrong-password")
		}()
	}
	wg.Wait()
	invalid, throttled := 0, 0
	for _, err := range errs {
		switch {
		case errors.Is(err, ErrInvalid):
			invalid++
		case errors.Is(err, ErrThrottled):
			throttled++
		default:
			t.Errorf("unexpected result: %v", err)
		}
	}
	if invalid != 5 || throttled != n-5 {
		t.Errorf("%d passwords checked, %d attempts throttled; want 5 and %d", invalid, throttled, n-5)
	}
}

// The hash computations are bounded: a request waits for its turn, and
// gives up with its context.
func TestHashSlots(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < cap(argonSlots); i++ {
		argonSlots <- struct{}{}
	}
	free := func() {
		for len(argonSlots) > 0 {
			<-argonSlots
		}
	}
	defer free()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := VerifyPasswordContext(ctx, h, "correct horse battery"); !errors.Is(err, ErrBusy) {
		t.Errorf("verify without a free slot: %v", err)
	}
	if _, err := HashPasswordContext(ctx, "x"); !errors.Is(err, ErrBusy) {
		t.Errorf("hash without a free slot: %v", err)
	}

	// A login given up while waiting is not a failed attempt.
	svc, st := newTestService(t)
	st.SetSetting(context.Background(), "auth", Config{Mode: ModeLocal})
	for i := 0; i < 8; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := svc.Login(ctx, httptest.NewRecorder(), loginFrom("10.0.0.1"), "admin", "x")
		cancel()
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("login %d without a free slot: %v", i, err)
		}
	}
	free()
	if ok, err := VerifyPasswordContext(context.Background(), h, "correct horse battery"); !ok || err != nil {
		t.Errorf("verify once the slots are free: %v %v", ok, err)
	}
}

// The lock of an account is keyed with the client address; a much higher
// limit per account, whatever the address, stays as a backstop.
func TestUserLimitPerClientAndGlobal(t *testing.T) {
	svc, _ := newTestService(t)
	for i := 0; i < 5; i++ {
		if _, err := svc.reserve(loginFrom("10.6.6.6"), "Alice"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := svc.reserve(loginFrom("10.6.6.6"), "alice"); !errors.Is(err, ErrThrottled) {
		t.Fatalf("6th attempt from the same address: %v", err)
	}
	at, err := svc.reserve(loginFrom("10.0.0.1"), "alice")
	if err != nil {
		t.Fatalf("another address is locked out: %v", err)
	}
	at.release()
	for i := 5; i < 50; i++ {
		if _, err := svc.reserve(loginFrom(fmt.Sprintf("10.1.0.%d", i)), "alice"); err != nil {
			t.Fatalf("attempt %d from a new address: %v", i, err)
		}
	}
	if _, err := svc.reserve(loginFrom("10.2.0.1"), "alice"); !errors.Is(err, ErrThrottled) {
		t.Errorf("51st attempt on the account: %v", err)
	}
	if _, err := svc.reserve(loginFrom("10.2.0.1"), "bob"); err != nil {
		t.Errorf("another account: %v", err)
	}
}

// LDAP mode: a directory login never replaces a local account, whatever
// the case of the name (the local administrator is the way back in).
func TestLDAPLoginKeepsLocalAccount(t *testing.T) {
	ctx := context.Background()
	srv := ldaptest.Start(t)
	svc, st := newTestService(t)
	c := ldapConfig(srv)
	if err := st.SetSetting(ctx, "auth", Config{Mode: ModeLDAP, LDAP: &c}); err != nil {
		t.Fatal(err)
	}
	h, _ := HashPassword("local-admin-password")
	st.SaveUser(ctx, store.User{Name: "bob", Role: RoleAdmin, Source: "local", Password: h})
	login := func(name, pw string) (User, error) {
		return svc.Login(ctx, httptest.NewRecorder(), loginFrom("10.0.0.1"), name, pw)
	}
	// bob is a reader in the directory.
	for _, name := range []string{"Bob", "BOB", "bob"} {
		if u, err := login(name, ldaptest.BobPassword); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s with the directory password: %+v %v", name, u, err)
		}
		if u, _ := st.User(ctx, "bob"); u.Source != "local" || u.Role != RoleAdmin || u.Password != h {
			t.Fatalf("local account replaced by the login of %s: %+v", name, u)
		}
	}
	if u, err := login("Bob", "local-admin-password"); err != nil || u.Name != "bob" || u.Role != RoleAdmin {
		t.Errorf("local account, other case: %+v %v", u, err)
	}
	// Belt and braces: the directory path itself refuses a local account.
	at, _ := svc.reserve(loginFrom("10.0.0.2"), "bob")
	if u, err := svc.ldapLogin(ctx, httptest.NewRecorder(), loginFrom("10.0.0.2"), c, "bob", ldaptest.BobPassword, at); !errors.Is(err, ErrInvalid) {
		t.Errorf("directory login over a local account: %+v %v", u, err)
	}
	if u, _ := st.User(ctx, "bob"); u.Source != "local" || u.Role != RoleAdmin {
		t.Errorf("local account replaced: %+v", u)
	}
	// Directory accounts still work.
	if u, err := login("Alice", ldaptest.AlicePassword); err != nil || u.Name != "alice" {
		t.Errorf("alice: %+v %v", u, err)
	}
}

// A blank name never logs in: for ldapAuthenticate it is a connection test,
// which succeeds on the service bind alone. And a session of an account
// without a known role (such as one left by that flaw) opens nothing.
func TestLDAPLoginBlankName(t *testing.T) {
	ctx := context.Background()
	srv := ldaptest.Start(t)
	svc, st := newTestService(t)
	c := ldapConfig(srv)
	if err := st.SetSetting(ctx, "auth", Config{Mode: ModeLDAP, LDAP: &c}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "   "} {
		for _, pw := range []string{"", "x"} {
			if u, err := svc.Login(ctx, httptest.NewRecorder(), loginFrom("10.0.0.1"), name, pw); !errors.Is(err, ErrInvalid) {
				t.Errorf("name %q, password %q: %+v %v", name, pw, u, err)
			}
		}
	}
	if _, err := st.User(ctx, ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("account recorded for a blank name: %v", err)
	}

	ghost := store.User{Name: "ghost", Source: "ldap"}
	if err := st.SaveUser(ctx, ghost); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if _, err := svc.open(ctx, rec, loginFrom("10.0.0.1"), ghost); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/clusters", nil)
	for _, ck := range rec.Result().Cookies() {
		r.AddCookie(ck)
	}
	if u, err := svc.Resolve(ctx, r); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("session of an account without role: %+v %v", u, err)
	}
}

// ESTOPO_SECRETS_DIR also holds at login time, for a configuration saved
// before the variable was set.
func TestLDAPLoginSecretsDir(t *testing.T) {
	ctx := context.Background()
	srv := ldaptest.Start(t)
	svc, st := newTestService(t)
	svc.SetSecretsDir(t.TempDir())
	c := ldapConfig(srv)
	st.SetSetting(ctx, "auth", Config{Mode: ModeLDAP, LDAP: &c})
	if _, err := svc.Login(ctx, httptest.NewRecorder(), loginFrom("10.0.0.1"), "alice", ldaptest.AlicePassword); !errors.Is(err, ErrUnavailable) {
		t.Errorf("files outside the secrets directory: %v", err)
	}
	if len(srv.Binds()) != 0 {
		t.Errorf("the directory was contacted: %v", srv.Binds())
	}
}

// A successful login only clears the keys of its account: the holder of
// one account cannot reset, by logging in, the count of the failures of
// its address on the other accounts.
func TestSuccessKeepsAddressFailures(t *testing.T) {
	svc, _ := newTestService(t)
	for i := 0; i < 5; i++ {
		if _, err := svc.reserve(loginFrom("10.6.6.6"), fmt.Sprintf("victim%d", i)); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if i == 4 {
			break
		}
		at, err := svc.reserve(loginFrom("10.6.6.6"), "eve")
		if err != nil {
			t.Fatalf("login %d of the account holder: %v", i, err)
		}
		at.success()
	}
	if _, err := svc.reserve(loginFrom("10.6.6.6"), "victim9"); !errors.Is(err, ErrThrottled) {
		t.Errorf("6th failure from the address after successful logins: %v", err)
	}
	// The successes themselves are not counted against the address.
	svc2, _ := newTestService(t)
	for i := 0; i < 20; i++ {
		at, err := svc2.reserve(loginFrom("10.7.7.7"), fmt.Sprintf("user%d", i))
		if err != nil {
			t.Fatalf("successful login %d from one address: %v", i, err)
		}
		at.success()
	}
}

// The names that the lookup of local accounts takes for the same account
// (strings.EqualFold) share their limiter keys: "ſam" is "sam".
func TestLimiterKeysFoldLikeAccounts(t *testing.T) {
	svc, _ := newTestService(t)
	if !strings.EqualFold("ſam", "sam") {
		t.Fatal("EqualFold no longer matches ſam and sam")
	}
	for i := 0; i < 50; i++ {
		if _, err := svc.reserve(loginFrom(fmt.Sprintf("10.1.0.%d", i)), "sam"); err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	for _, n := range []string{"SAM", "ſam", "ſAM"} {
		if _, err := svc.reserve(loginFrom("10.2.0.1"), n); !errors.Is(err, ErrThrottled) {
			t.Errorf("%q escapes the limit of sam: %v", n, err)
		}
	}
	if foldName("µ") != foldName("μ") || foldName("ς") != foldName("Σ") {
		t.Errorf("fold orbits: µ %q μ %q, ς %q Σ %q", foldName("µ"), foldName("μ"), foldName("ς"), foldName("Σ"))
	}
}
