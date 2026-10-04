package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"estopo/internal/auth/ldaptest"
	"estopo/internal/clock"
	"estopo/internal/store"
)

func ldapConfig(s *ldaptest.Server) LDAPConfig {
	return LDAPConfig{
		URLs:             []string{s.URL},
		CAFile:           s.CAFile,
		BindDN:           ldaptest.ServiceDN,
		BindPasswordFile: s.BindPasswordFile,
		UserBase:         ldaptest.PeopleBase,
		GroupBase:        ldaptest.GroupBase,
		AdminGroups:      []string{ldaptest.AdminGroupDN},
		ReaderGroups:     []string{ldaptest.ReaderGroupDN, strings.ToUpper(ldaptest.PosixGroupDN)},
		TimeoutSeconds:   5,
	}
}

func TestLDAPValidate(t *testing.T) {
	ok := LDAPConfig{URLs: []string{"ldaps://ldap.example.org"}, BindDN: "cn=x", BindPasswordFile: "/f",
		UserBase: "ou=people", GroupBase: "ou=groups", AdminGroups: []string{"cn=admins,ou=groups"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(c *LDAPConfig){
		"no URL":                func(c *LDAPConfig) { c.URLs = nil },
		"plain LDAP":            func(c *LDAPConfig) { c.URLs = []string{"ldap://ldap.example.org"} },
		"other scheme":          func(c *LDAPConfig) { c.URLs = []string{"http://ldap.example.org"} },
		"no service account":    func(c *LDAPConfig) { c.BindDN = "" },
		"no group base":         func(c *LDAPConfig) { c.GroupBase = "" },
		"no group":              func(c *LDAPConfig) { c.AdminGroups = nil },
		"filter without {user}": func(c *LDAPConfig) { c.UserFilter = "(uid=alice)" },
		"invalid group DN":      func(c *LDAPConfig) { c.ReaderGroups = []string{"not a dn"} },
	}
	for name, mutate := range cases {
		c := ok
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	st := ok
	st.URLs, st.StartTLS = []string{"ldap://ldap.example.org:389"}, true
	if err := st.Validate(); err != nil {
		t.Errorf("ldap:// with StartTLS: %v", err)
	}
}

func TestLDAPAuthenticate(t *testing.T) {
	srv := ldaptest.Start(t)
	c := ldapConfig(srv)
	for _, tc := range []struct{ user, pw, role string }{
		{"alice", ldaptest.AlicePassword, RoleAdmin},
		{"bob", ldaptest.BobPassword, RoleReader},   // groupOfUniqueNames, DN spelled differently
		{"dave", ldaptest.DavePassword, RoleReader}, // posixGroup, group DN in upper case in the config
	} {
		res, err := ldapAuthenticate(c, tc.user, tc.pw, nil)
		if err != nil || res.Role != tc.role {
			t.Errorf("%s: %+v %v", tc.user, res, err)
		}
	}
	if res, _ := ldapAuthenticate(c, "alice", ldaptest.AlicePassword, nil); res.UserDN != "uid=alice,"+ldaptest.PeopleBase || len(res.Groups) != 1 {
		t.Errorf("alice: %+v", res)
	}
	for _, tc := range []struct{ name, user, pw string }{
		{"wrong password", "alice", "wrong"},
		{"unknown user", "mallory", "whatever"},
		{"in no estopo group", "carol", ldaptest.CarolPassword},
		{"filter injection", "*", "whatever"},
		{"filter injection 2", "alice)(uid=*", ldaptest.AlicePassword},
	} {
		if _, err := ldapAuthenticate(c, tc.user, tc.pw, nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// An empty password never reaches the directory (it would be an
	// unauthenticated bind, accepted by OpenLDAP).
	before := len(srv.Binds())
	if _, err := ldapAuthenticate(c, "alice", "", nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty password: %v", err)
	}
	if len(srv.Binds()) != before {
		t.Errorf("empty password sent: %v", srv.Binds()[before:])
	}
	for _, b := range srv.Binds() {
		if strings.HasSuffix(b, ":anonymous") {
			t.Errorf("anonymous bind: %s", b)
		}
	}
}

func TestLDAPInfrastructureErrors(t *testing.T) {
	srv := ldaptest.Start(t)
	other := ldaptest.Start(t) // different CA

	c := ldapConfig(srv)
	c.CAFile = other.CAFile
	if _, err := ldapAuthenticate(c, "alice", ldaptest.AlicePassword, nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("untrusted certificate: %v", err)
	}
	c = ldapConfig(srv)
	c.ServerName = "ldap.elsewhere.org"
	if _, err := ldapAuthenticate(c, "alice", ldaptest.AlicePassword, nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("wrong server name: %v", err)
	}
	c = ldapConfig(srv)
	wrong := filepath.Join(t.TempDir(), "pw")
	os.WriteFile(wrong, []byte("nope"), 0o600)
	c.BindPasswordFile = wrong
	if _, err := ldapAuthenticate(c, "alice", ldaptest.AlicePassword, nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("wrong service password: %v", err)
	}
	// The first server is down: the second one answers.
	c = ldapConfig(srv)
	c.URLs = []string{"ldaps://127.0.0.1:1", srv.URL}
	if res, err := ldapAuthenticate(c, "alice", ldaptest.AlicePassword, nil); err != nil || res.Server != srv.URL {
		t.Errorf("fallback: %+v %v", res, err)
	}
	c.URLs = []string{"ldaps://127.0.0.1:1"}
	if _, err := ldapAuthenticate(c, "alice", ldaptest.AlicePassword, nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("all servers down: %v", err)
	}
}

func TestLDAPTestSteps(t *testing.T) {
	srv := ldaptest.Start(t)
	steps, res, err := TestLDAP(ldapConfig(srv), "", "")
	if err != nil || res == nil || len(steps) != 2 || !steps[1].OK {
		t.Errorf("connection test: %+v %+v %v", steps, res, err)
	}
	steps, res, err = TestLDAP(ldapConfig(srv), "carol", ldaptest.CarolPassword)
	if err == nil || res != nil {
		t.Fatalf("carol: %+v %v", res, err)
	}
	last := steps[len(steps)-1]
	if last.Step != "role" || last.OK {
		t.Errorf("carol steps: %+v", steps)
	}
	steps, _, _ = TestLDAP(ldapConfig(srv), "alice", "wrong")
	if last := steps[len(steps)-1]; last.Step != "user_bind" || last.OK {
		t.Errorf("wrong password steps: %+v", steps)
	}
}

// Login in LDAP mode: accounts recorded with source "ldap", role refreshed
// at each login, disabled accounts stay disabled, local accounts still work,
// and a directory outage is reported as such.
func TestServiceLDAPLogin(t *testing.T) {
	ctx := context.Background()
	srv := ldaptest.Start(t)
	clk := clock.NewManual(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	st, err := store.Open(ctx, t.TempDir(), clk)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := NewService(st, clk, discardLog(), false)
	c := ldapConfig(srv)
	if err := st.SetSetting(ctx, "auth", Config{Mode: ModeLDAP, LDAP: &c}); err != nil {
		t.Fatal(err)
	}
	h, _ := HashPassword("local-admin-password")
	st.SaveUser(ctx, store.User{Name: "admin", Role: RoleAdmin, Source: "local", Password: h})

	login := func(name, pw string) (User, error) {
		return svc.Login(ctx, httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/login", nil), name, pw)
	}
	u, err := login("Alice", ldaptest.AlicePassword)
	if err != nil || u.Name != "alice" || u.Role != RoleAdmin {
		t.Fatalf("alice: %+v %v", u, err)
	}
	rec, _ := st.User(ctx, "alice")
	if rec.Source != "ldap" || rec.Password != "" {
		t.Errorf("recorded account: %+v", rec)
	}
	if _, err := login("carol", ldaptest.CarolPassword); !errors.Is(err, ErrInvalid) {
		t.Errorf("carol: %v", err)
	}
	if u, err := login("admin", "local-admin-password"); err != nil || u.Role != RoleAdmin {
		t.Errorf("local account in LDAP mode: %+v %v", u, err)
	}
	st.SaveUser(ctx, store.User{Name: "alice", Role: RoleAdmin, Source: "ldap", Disabled: true})
	if _, err := login("alice", ldaptest.AlicePassword); !errors.Is(err, ErrInvalid) {
		t.Errorf("disabled account: %v", err)
	}
	if rec, _ := st.User(ctx, "alice"); !rec.Disabled {
		t.Error("login re-enabled a disabled account")
	}
	c.URLs = []string{"ldaps://127.0.0.1:1"}
	st.SetSetting(ctx, "auth", Config{Mode: ModeLDAP, LDAP: &c})
	if _, err := login("bob", ldaptest.BobPassword); !errors.Is(err, ErrUnavailable) {
		t.Errorf("directory down: %v", err)
	}
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
