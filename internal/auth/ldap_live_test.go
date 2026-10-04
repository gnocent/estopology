package auth

import (
	"errors"
	"os"
	"testing"

	"github.com/go-ldap/ldap/v3"

	"estopo/internal/auth/ldaptest"
)

// TestLiveLDAP runs against the real OpenLDAP of testenv/ldap (run.sh sets
// the environment): LDAPS and StartTLS, the three kinds of groups, wrong
// credentials, and the TLS requirement of the server itself.
func TestLiveLDAP(t *testing.T) {
	if os.Getenv("ESTOPO_LDAP_LIVE") == "" {
		t.Skip("ESTOPO_LDAP_LIVE not set (testenv/ldap/run.sh)")
	}
	base := LDAPConfig{
		CAFile:           os.Getenv("ESTOPO_LDAP_CA"),
		BindDN:           ldaptest.ServiceDN,
		BindPasswordFile: os.Getenv("ESTOPO_LDAP_BIND_PW"),
		UserBase:         ldaptest.PeopleBase,
		GroupBase:        ldaptest.GroupBase,
		AdminGroups:      []string{ldaptest.AdminGroupDN},
		ReaderGroups:     []string{ldaptest.ReaderGroupDN, ldaptest.PosixGroupDN},
		TimeoutSeconds:   5,
	}
	ldaps, startTLS := base, base
	ldaps.URLs = []string{"ldaps://localhost:1636"}
	startTLS.URLs, startTLS.StartTLS = []string{"ldap://127.0.0.1:1389"}, true

	for name, c := range map[string]LDAPConfig{"ldaps": ldaps, "starttls": startTLS} {
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct{ user, pw, role string }{
				{"alice", ldaptest.AlicePassword, RoleAdmin},
				{"bob", ldaptest.BobPassword, RoleReader},
				{"dave", ldaptest.DavePassword, RoleReader},
			} {
				res, err := ldapAuthenticate(c, tc.user, tc.pw, nil)
				if err != nil || res.Role != tc.role {
					t.Errorf("%s: %+v %v", tc.user, res, err)
				}
			}
			for _, tc := range []struct{ name, user, pw string }{
				{"wrong password", "alice", "wrong"},
				{"unknown user", "mallory", "whatever"},
				{"in no estopo group", "carol", ldaptest.CarolPassword},
				{"filter injection", "alice)(uid=*", ldaptest.AlicePassword},
				{"wildcard", "*", ldaptest.AlicePassword},
			} {
				if _, err := ldapAuthenticate(c, tc.user, tc.pw, nil); !errors.Is(err, ErrInvalid) {
					t.Errorf("%s: %v", tc.name, err)
				}
			}
			steps, res, err := TestLDAP(c, "", "")
			if err != nil || res == nil || len(steps) != 2 {
				t.Errorf("connection test: %+v %v", steps, err)
			}
		})
	}

	// Untrusted certificate: the system CAs do not know the test CA.
	c := ldaps
	c.CAFile = ""
	if _, err := ldapAuthenticate(c, "alice", ldaptest.AlicePassword, nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("untrusted certificate: %v", err)
	}
	// estopo never sends a simple bind in clear (Validate), and the test
	// directory refuses one anyway (security simple_bind=128).
	c = startTLS
	c.StartTLS = false
	if c.Validate() == nil {
		t.Error("plain ldap:// accepted")
	}
	conn, err := ldap.DialURL("ldap://127.0.0.1:1389")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Bind(ldaptest.ServiceDN, ldaptest.ServicePass); !ldap.IsErrorWithCode(err, ldap.LDAPResultConfidentialityRequired) {
		t.Errorf("clear simple bind on the test directory: %v", err)
	}
}
