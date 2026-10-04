package auth

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"
)

// LDAPConfig configures OpenLDAP authentication (SPEC §10): a service
// account searches the user, the user's password is checked by binding as
// that user, and the role comes from group membership. Secrets live in files
// (like the Elasticsearch credentials), never in meta.db.
type LDAPConfig struct {
	URLs             []string `json:"urls"`                  // ldaps://host:636, tried in order
	StartTLS         bool     `json:"start_tls,omitempty"`   // required for ldap:// URLs
	CAFile           string   `json:"ca_file,omitempty"`     // enterprise CA (PEM); empty = system CAs
	ServerName       string   `json:"server_name,omitempty"` // TLS name when it differs from the URL host
	BindDN           string   `json:"bind_dn"`
	BindPasswordFile string   `json:"bind_password_file"`
	UserBase         string   `json:"user_base"`
	UserFilter       string   `json:"user_filter,omitempty"` // {user}: escaped login name
	GroupBase        string   `json:"group_base"`
	GroupFilter      string   `json:"group_filter,omitempty"` // {dn}: user DN, {user}: login name (escaped)
	AdminGroups      []string `json:"admin_groups"`
	ReaderGroups     []string `json:"reader_groups"`
	TimeoutSeconds   int      `json:"timeout_s,omitempty"`
}

// Default filters: inetOrgPerson users; groupOfNames, groupOfUniqueNames and
// posixGroup groups.
const (
	DefaultUserFilter  = "(&(objectClass=inetOrgPerson)(uid={user}))"
	DefaultGroupFilter = "(|(&(objectClass=groupOfNames)(member={dn}))(&(objectClass=groupOfUniqueNames)(uniqueMember={dn}))(&(objectClass=posixGroup)(memberUid={user})))"
)

func (c LDAPConfig) userFilter() string  { return cmp(c.UserFilter, DefaultUserFilter) }
func (c LDAPConfig) groupFilter() string { return cmp(c.GroupFilter, DefaultGroupFilter) }

func cmp(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func (c LDAPConfig) timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return 10 * time.Second
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// Validate checks the configuration without contacting the directory.
func (c LDAPConfig) Validate() error {
	if len(c.URLs) == 0 {
		return errors.New("at least one LDAP URL is required")
	}
	for _, raw := range c.URLs {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return fmt.Errorf("invalid LDAP URL %q", raw)
		}
		switch u.Scheme {
		case "ldaps":
		case "ldap":
			if !c.StartTLS {
				return fmt.Errorf("%s: plain LDAP is refused, use ldaps:// or enable StartTLS", raw)
			}
		default:
			return fmt.Errorf("%s: the scheme must be ldaps or ldap", raw)
		}
	}
	if c.BindDN == "" || c.BindPasswordFile == "" {
		return errors.New("the service account (bind DN and password file) is required")
	}
	if c.UserBase == "" || c.GroupBase == "" {
		return errors.New("the user and group search bases are required")
	}
	if !strings.Contains(c.userFilter(), "{user}") {
		return errors.New("the user filter must contain {user}")
	}
	if len(c.AdminGroups) == 0 && len(c.ReaderGroups) == 0 {
		return errors.New("at least one admin or reader group is required")
	}
	for _, g := range append(append([]string{}, c.AdminGroups...), c.ReaderGroups...) {
		if _, err := ldap.ParseDN(g); err != nil {
			return fmt.Errorf("invalid group DN %q: %v", g, err)
		}
	}
	return nil
}

// LDAPResult is the outcome of a successful LDAP authentication.
type LDAPResult struct {
	Server string   `json:"server"`
	UserDN string   `json:"user_dn"`
	Groups []string `json:"groups"`
	Role   string   `json:"role"`
}

// LDAPStep traces one step of an authentication for the admin test.
type LDAPStep struct {
	Step   string `json:"step"` // connect, service_bind, user_search, user_bind, group_search, role
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// ErrUnavailable is returned when the directory cannot be used (network,
// TLS, service account): the credentials were not checked.
var ErrUnavailable = errors.New("directory unavailable")

// ldapAuthenticate checks name/password against the directory. Wrong
// credentials, unknown users and users outside the configured groups give
// ErrInvalid; infrastructure problems wrap ErrUnavailable. An empty name only
// tests the connection and the service account. trace may be nil.
func ldapAuthenticate(c LDAPConfig, name, password string, trace func(LDAPStep)) (LDAPResult, error) {
	var res LDAPResult
	step := func(s string, err error, detail string) {
		if trace == nil {
			return
		}
		st := LDAPStep{Step: s, OK: err == nil, Detail: detail}
		if err != nil {
			st.Detail = err.Error()
		}
		trace(st)
	}
	if name != "" && password == "" {
		// An empty password would be an unauthenticated bind, which OpenLDAP
		// accepts: never let it through.
		return res, ErrInvalid
	}
	if err := c.Validate(); err != nil {
		return res, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	conn, server, err := c.dial()
	step("connect", err, server)
	if err != nil {
		return res, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer conn.Close()
	res.Server = server

	servicePw, err := readSecret(c.BindPasswordFile)
	if err == nil {
		err = conn.Bind(c.BindDN, servicePw)
	}
	step("service_bind", err, c.BindDN)
	if err != nil {
		return res, fmt.Errorf("%w: service account: %v", ErrUnavailable, err)
	}
	if name == "" {
		return res, nil
	}

	filter := strings.ReplaceAll(c.userFilter(), "{user}", ldap.EscapeFilter(name))
	sr, err := conn.Search(ldap.NewSearchRequest(c.UserBase, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, int(c.timeout().Seconds()), false, filter, []string{"1.1"}, nil))
	switch {
	case err != nil && !ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded):
		step("user_search", err, "")
		return res, fmt.Errorf("%w: user search: %v", ErrUnavailable, err)
	case sr == nil || len(sr.Entries) != 1:
		// Unknown user, or an ambiguous filter: refused either way.
		step("user_search", errors.New("no single matching entry"), "")
		return res, ErrInvalid
	}
	res.UserDN = sr.Entries[0].DN
	step("user_search", nil, res.UserDN)

	err = conn.Bind(res.UserDN, password)
	step("user_bind", err, "")
	if err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return res, ErrInvalid
		}
		return res, fmt.Errorf("%w: user bind: %v", ErrUnavailable, err)
	}

	// Groups are searched with the service account again.
	if err := conn.Bind(c.BindDN, servicePw); err != nil {
		step("group_search", err, "")
		return res, fmt.Errorf("%w: service account: %v", ErrUnavailable, err)
	}
	gf := strings.NewReplacer("{dn}", ldap.EscapeFilter(res.UserDN), "{user}", ldap.EscapeFilter(name)).Replace(c.groupFilter())
	gr, err := conn.Search(ldap.NewSearchRequest(c.GroupBase, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		1000, int(c.timeout().Seconds()), false, gf, []string{"1.1"}, nil))
	if err != nil && !ldap.IsErrorWithCode(err, ldap.LDAPResultSizeLimitExceeded) {
		step("group_search", err, "")
		return res, fmt.Errorf("%w: group search: %v", ErrUnavailable, err)
	}
	res.Groups = []string{}
	if gr != nil {
		for _, e := range gr.Entries {
			res.Groups = append(res.Groups, e.DN)
		}
	}
	step("group_search", nil, strings.Join(res.Groups, "; "))

	switch {
	case memberOfAny(res.Groups, c.AdminGroups):
		res.Role = RoleAdmin
	case memberOfAny(res.Groups, c.ReaderGroups):
		res.Role = RoleReader
	default:
		step("role", errors.New("member of no configured group"), "")
		return res, ErrInvalid
	}
	step("role", nil, res.Role)
	return res, nil
}

// memberOfAny compares DNs as LDAP does (case and spacing insensitive).
func memberOfAny(groups, wanted []string) bool {
	for _, g := range groups {
		gd, err := ldap.ParseDN(g)
		for _, w := range wanted {
			wd, werr := ldap.ParseDN(w)
			if err == nil && werr == nil {
				if gd.EqualFold(wd) {
					return true
				}
			} else if strings.EqualFold(g, w) {
				return true
			}
		}
	}
	return false
}

// dial connects to the first reachable server.
func (c LDAPConfig) dial() (*ldap.Conn, string, error) {
	var errs []string
	for _, raw := range c.URLs {
		u, _ := url.Parse(raw)
		tc, err := c.tlsConfig(u.Hostname())
		if err != nil {
			return nil, "", err
		}
		conn, err := ldap.DialURL(raw, ldap.DialWithTLSConfig(tc), ldap.DialWithDialer(&net.Dialer{Timeout: c.timeout()}))
		if err == nil && u.Scheme == "ldap" {
			if err = conn.StartTLS(tc); err != nil {
				conn.Close()
			}
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", raw, err))
			continue
		}
		conn.SetTimeout(c.timeout())
		return conn, raw, nil
	}
	return nil, "", errors.New(strings.Join(errs, "; "))
}

func (c LDAPConfig) tlsConfig(host string) (*tls.Config, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: cmp(c.ServerName, host)}
	if c.CAFile != "" {
		pem, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("LDAP CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("LDAP CA %s: no PEM certificate", c.CAFile)
		}
		tc.RootCAs = pool
	}
	return tc, nil
}

func readSecret(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimRight(string(b), "\r\n")
	if s == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return s, nil
}
