package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"estopo/internal/clock"
	"estopo/internal/store"
)

// Modes (SPEC §10).
const (
	ModeNone  = "none"  // everybody is admin (warning banner)
	ModeLocal = "local" // local accounts
	ModeLDAP  = "ldap"  // OpenLDAP over LDAPS
)

// Roles.
const (
	RoleReader = "reader"
	RoleAdmin  = "admin"
)

// Session limits: the idle timeout is configurable up to MaxSessionHours,
// the absolute lifetime is not.
const (
	DefaultSessionHours = 12
	MaxSessionHours     = 720
	MaxSessionLifetime  = 7 * 24 * time.Hour
)

// MinPasswordLen is the minimal length of a local password.
const MinPasswordLen = 12

// Config is the "auth" setting of meta.db.
type Config struct {
	Mode         string      `json:"mode"`
	SessionHours int         `json:"session_hours"` // idle timeout, default 12
	LDAP         *LDAPConfig `json:"ldap,omitempty"`
}

// CookieName is the session cookie.
const CookieName = "estopo_session"

// User is the authenticated caller.
type User struct {
	Name string `json:"name"`
	Role string `json:"role"`
	CSRF string `json:"-"`
	// NoAuth marks the caller of an instance without authentication (mode
	// none): it has no session, hence no CSRF token.
	NoAuth bool `json:"-"`
}

// Anonymous is the caller of an instance without authentication.
func Anonymous() User { return User{Name: "anonymous", Role: RoleAdmin, NoAuth: true} }

// Service authenticates web requests.
type Service struct {
	st         *store.Store
	clock      clock.Clock
	log        *slog.Logger
	secure     bool     // HTTPS: Secure cookies
	limiter    *Limiter // per user and client address, and per client address
	global     *Limiter // per user, whatever the address
	secretsDir string
}

// NewService returns the authentication service.
func NewService(st *store.Store, clk clock.Clock, log *slog.Logger, secure bool) *Service {
	// The per-user limit is keyed with the client address, otherwise anybody
	// could lock an account out; the global one only stops a distributed
	// attack on one account.
	global := NewLimiter(clk)
	global.free = 50
	return &Service{st: st, clock: clk, log: log, secure: secure, limiter: NewLimiter(clk), global: global}
}

// SetSecretsDir restricts the files of the LDAP configuration to a
// directory (ESTOPO_SECRETS_DIR).
func (s *Service) SetSecretsDir(dir string) { s.secretsDir = dir }

// Config returns the current configuration (defaults: mode none). An
// unreadable or corrupted setting is an error, never mode none: the callers
// must refuse the request.
func (s *Service) Config(ctx context.Context) (Config, error) {
	var c Config
	found, err := s.st.GetSetting(ctx, "auth", &c)
	if err != nil {
		return Config{}, fmt.Errorf("authentication settings: %w", err)
	}
	switch {
	case !found:
		c.Mode = ModeNone
	case c.Mode != ModeNone && c.Mode != ModeLocal && c.Mode != ModeLDAP:
		return Config{}, fmt.Errorf("authentication settings: unknown mode %q", c.Mode)
	}
	if c.SessionHours <= 0 {
		c.SessionHours = DefaultSessionHours
	}
	c.SessionHours = min(c.SessionHours, MaxSessionHours)
	return c, nil
}

// CheckTransport enforces SPEC §10 / B17: local and LDAP authentication are
// refused over plain HTTP unless the listener is loopback-only or explicitly
// allowed.
func CheckTransport(mode string, tls, loopbackOnly, allowInsecure bool) error {
	if mode == ModeNone || tls || loopbackOnly || allowInsecure {
		return nil
	}
	return fmt.Errorf("authentication mode %q over plain HTTP on a non-loopback address is refused: "+
		"configure ESTOPO_TLS_CERT/ESTOPO_TLS_KEY, listen on 127.0.0.1, or set ESTOPO_ALLOW_INSECURE_AUTH=true", mode)
}

// Bootstrap applies ESTOPO_ADMIN_PASSWORD at the first start: when the
// authentication was never configured and no account exists, it creates the
// local account "admin" with that password and switches to local mode (SPEC
// §10). Afterwards the variable is ignored.
func (s *Service) Bootstrap(ctx context.Context, adminPassword string, tls, loopbackOnly, allowInsecure bool) error {
	if adminPassword == "" {
		return nil
	}
	if len(adminPassword) < MinPasswordLen {
		return fmt.Errorf("ESTOPO_ADMIN_PASSWORD must have at least %d characters", MinPasswordLen)
	}
	var raw json.RawMessage
	if configured, err := s.st.GetSetting(ctx, "auth", &raw); err != nil || configured {
		return err
	}
	users, err := s.st.Users(ctx)
	if err != nil || len(users) > 0 {
		return err
	}
	if err := CheckTransport(ModeLocal, tls, loopbackOnly, allowInsecure); err != nil {
		return fmt.Errorf("ESTOPO_ADMIN_PASSWORD: %w", err)
	}
	h, err := HashPassword(adminPassword)
	if err != nil {
		return err
	}
	if err := s.st.SaveUser(ctx, store.User{Name: "admin", Role: RoleAdmin, Source: "local", Password: h}); err != nil {
		return err
	}
	if err := s.st.SetSetting(ctx, "auth", Config{Mode: ModeLocal, SessionHours: DefaultSessionHours}); err != nil {
		return err
	}
	s.log.Info("first start: account 'admin' created with ESTOPO_ADMIN_PASSWORD, local authentication enabled")
	return s.st.Audit(ctx, "system", "user.bootstrap", "admin", "")
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// ErrInvalid is returned for wrong credentials; ErrThrottled when too many
// attempts failed; ErrUnauthenticated by Resolve when the request carries no
// valid session.
var (
	ErrInvalid         = errors.New("invalid credentials")
	ErrThrottled       = errors.New("too many attempts, retry later")
	ErrUnauthenticated = errors.New("authentication required")
)

// maxNameLen bounds what is kept of a submitted login name (limiter keys,
// audit trail): it comes from an unauthenticated request, and may be a
// password typed in the wrong field.
const maxNameLen = 128

func shortName(name string) string {
	if r := []rune(name); len(r) > maxNameLen {
		return string(r[:maxNameLen])
	}
	return name
}

// attempt is a login attempt, counted by the limiters before the password
// is checked.
type attempt struct {
	s            *Service
	keys         []string
	user         string
	name, client string // audit trail
}

// reserve counts an attempt for name from the client of r, or returns
// ErrThrottled.
func (s *Service) reserve(r *http.Request, name string) (attempt, error) {
	ip := clientIP(r)
	user := "user:" + strings.ToLower(shortName(name))
	a := attempt{s: s, keys: []string{user + "|" + ip, "ip:" + ip}, user: user, name: shortName(name), client: ip}
	if ok, _ := s.limiter.Reserve(a.keys...); !ok {
		return a, ErrThrottled
	}
	if ok, _ := s.global.Reserve(a.user); !ok {
		s.limiter.Release(a.keys...)
		return a, ErrThrottled
	}
	return a, nil
}

// release gives the attempt back: no password was checked.
func (a attempt) release() {
	a.s.limiter.Release(a.keys...)
	a.s.global.Release(a.user)
}

func (a attempt) success() {
	a.s.limiter.Success(a.keys...)
	a.s.global.Success(a.user)
}

// failure leaves the attempt counted and records it.
func (a attempt) failure(ctx context.Context, details string) {
	a.s.st.Audit(ctx, a.name, "login.failure", a.client, details)
}

// localAccount returns the local account designated by a login name. The
// match ignores the case, like the directory does: otherwise "Bob" would
// reach the directory while the local account "bob" exists. found without
// an account means that several local accounts match and none exactly.
func (s *Service) localAccount(ctx context.Context, name string) (u store.User, found bool, err error) {
	users, err := s.st.Users(ctx)
	if err != nil {
		return u, false, err
	}
	n := 0
	for _, c := range users {
		if c.Source != "local" || !strings.EqualFold(c.Name, name) {
			continue
		}
		if c.Name == name {
			return c, true, nil
		}
		u = c
		n++
	}
	if n > 1 {
		u = store.User{}
	}
	return u, n > 0, nil
}

// Login checks the credentials and opens a session. In LDAP mode, local
// accounts keep working: they are the way back in when the directory is
// unavailable or misconfigured (estopo admin reset-password).
func (s *Service) Login(ctx context.Context, w http.ResponseWriter, r *http.Request, name, password string) (User, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return User{}, err
	}
	at, err := s.reserve(r, name)
	if err != nil {
		return User{}, err
	}
	u, local, err := s.localAccount(ctx, name)
	if err != nil {
		at.release()
		return User{}, err
	}
	if cfg.Mode == ModeLDAP && cfg.LDAP != nil && !local {
		return s.ldapLogin(ctx, w, r, *cfg.LDAP, name, password, at)
	}
	valid := false
	if local && !u.Disabled && u.Password != "" {
		valid, err = VerifyPasswordContext(ctx, u.Password, password)
	} else {
		// Same cost whether the account exists or not.
		_, err = VerifyPasswordContext(ctx, dummyHash, password)
	}
	if errors.Is(err, ErrBusy) {
		at.release()
		return User{}, err
	}
	if !valid {
		at.failure(ctx, "")
		return User{}, ErrInvalid
	}
	at.success()
	return s.open(ctx, w, r, u)
}

var dummyHash, _ = HashPassword("not a real password")

// CheckPassword verifies the current password of a local account for the
// client of r (password change), throttled like a login.
func (s *Service) CheckPassword(ctx context.Context, r *http.Request, u store.User, password string) error {
	at, err := s.reserve(r, u.Name)
	if err != nil {
		return err
	}
	ok, err := VerifyPasswordContext(ctx, u.Password, password)
	if errors.Is(err, ErrBusy) {
		at.release()
		return err
	}
	if !ok {
		at.failure(ctx, "password change")
		return ErrInvalid
	}
	at.success()
	return nil
}

// ldapLogin authenticates against the directory and records the account
// (source "ldap", role from the groups at each login). An account disabled
// by an administrator stays disabled, and a local account is never replaced.
func (s *Service) ldapLogin(ctx context.Context, w http.ResponseWriter, r *http.Request, c LDAPConfig, name, password string, at attempt) (User, error) {
	name = strings.ToLower(name) // directory names are case-insensitive
	var res LDAPResult
	err := c.CheckFiles(s.secretsDir)
	if err != nil {
		err = fmt.Errorf("%w: %v", ErrUnavailable, err)
	} else {
		res, err = ldapAuthenticate(c, name, password, nil)
	}
	if errors.Is(err, ErrUnavailable) {
		// Not counted as a failure: no password was checked.
		at.release()
		s.log.Warn("LDAP authentication unavailable", "user", at.name, "err", err)
		s.st.Audit(ctx, at.name, "login.ldap_unavailable", at.client, err.Error())
		return User{}, err
	}
	existing, uerr := s.st.User(ctx, name)
	switch {
	case uerr != nil && !errors.Is(uerr, store.ErrNotFound):
		at.release()
		return User{}, uerr
	case err == nil && uerr == nil && (existing.Disabled || existing.Source == "local"):
		err = ErrInvalid
	}
	if err != nil {
		at.failure(ctx, "ldap")
		return User{}, ErrInvalid
	}
	at.success()
	if uerr == nil && existing.Role != res.Role {
		// The sessions opened with the previous role end.
		if err := RevokeSessions(ctx, s.st, name, ""); err != nil {
			return User{}, err
		}
	}
	u := store.User{Name: name, Role: res.Role, Source: "ldap"}
	if err := s.st.SaveUser(ctx, u); err != nil {
		return User{}, err
	}
	return s.open(ctx, w, r, u)
}

// TestLDAP runs an authentication with c, step by step, for the admin
// screen. An empty name only tests the connection and the service account.
func TestLDAP(c LDAPConfig, name, password string) ([]LDAPStep, *LDAPResult, error) {
	var steps []LDAPStep
	res, err := ldapAuthenticate(c, name, password, func(s LDAPStep) { steps = append(steps, s) })
	if err != nil {
		return steps, nil, err
	}
	return steps, &res, nil
}

// secureCookie reports whether the session cookie gets the Secure
// attribute: always when the listener serves HTTPS, and when the browser
// says that the page is an HTTPS one (TLS ended by a reverse proxy). The
// Origin header proves nothing, but lying only yields a cookie that the
// liar's own browser will not send back; X-Forwarded-Proto is not read.
func (s *Service) secureCookie(r *http.Request) bool {
	if s.secure || r.TLS != nil {
		return true
	}
	u, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && u.Scheme == "https"
}

func (s *Service) open(ctx context.Context, w http.ResponseWriter, r *http.Request, u store.User) (User, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return User{}, err
	}
	token, csrf := RandomToken(32), RandomToken(24)
	now := s.clock.Now()
	ttl := time.Duration(cfg.SessionHours) * time.Hour
	if err := s.st.CreateSession(ctx, store.Session{TokenHash: hashToken(token), User: u.Name, CSRF: csrf,
		CreatedMs: now.UnixMilli(), ExpiresMs: now.Add(ttl).UnixMilli()}); err != nil {
		return User{}, err
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: token, Path: "/", HttpOnly: true, Secure: s.secureCookie(r),
		SameSite: http.SameSiteStrictMode})
	s.st.Audit(ctx, u.Name, "login", "", "")
	return User{Name: u.Name, Role: u.Role, CSRF: csrf}, nil
}

// Logout closes the session of the request.
func (s *Service) Logout(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if h := SessionHash(r); h != "" {
		s.st.DeleteSession(ctx, h)
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookie(r),
		SameSite: http.SameSiteStrictMode})
}

// SessionHash identifies the session of a request in the store ("" when it
// carries none).
func SessionHash(r *http.Request) string {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	return hashToken(c.Value)
}

// RevokeSessions closes the sessions of an account, except the one
// identified by keep (SessionHash), if any: after a password or role
// change, or when the account is disabled.
func RevokeSessions(ctx context.Context, st *store.Store, name, keep string) error {
	_, err := st.Meta.ExecContext(ctx, `DELETE FROM session WHERE user_name = ? AND token_hash <> ?`, name, keep)
	return err
}

// Resolve returns the caller of a request. In mode none everybody is an
// administrator. The error is ErrUnauthenticated when authentication is
// required and missing; any other error means that the caller could not be
// determined (unreadable settings or sessions): the request must be refused,
// never served as in mode none.
func (s *Service) Resolve(ctx context.Context, r *http.Request) (User, error) {
	cfg, err := s.Config(ctx)
	if err != nil {
		return User{}, err
	}
	if cfg.Mode == ModeNone {
		return Anonymous(), nil
	}
	h := SessionHash(r)
	if h == "" {
		return User{}, ErrUnauthenticated
	}
	se, err := s.st.SessionByHash(ctx, h)
	if errors.Is(err, store.ErrNotFound) {
		return User{}, ErrUnauthenticated
	} else if err != nil {
		return User{}, err
	}
	// Idle timeout, and absolute lifetime whatever the activity.
	now := s.clock.Now()
	end := time.UnixMilli(se.CreatedMs).Add(MaxSessionLifetime)
	if se.ExpiresMs < now.UnixMilli() || !now.Before(end) || se.CSRF == "" {
		return User{}, ErrUnauthenticated
	}
	u, err := s.st.User(ctx, se.User)
	if errors.Is(err, store.ErrNotFound) {
		return User{}, ErrUnauthenticated
	} else if err != nil {
		return User{}, err
	}
	if u.Disabled || (u.Source == "ldap" && cfg.Mode != ModeLDAP) {
		return User{}, ErrUnauthenticated
	}
	// Sliding expiry: extend when less than half of the idle timeout is left.
	ttl := time.Duration(cfg.SessionHours) * time.Hour
	if time.UnixMilli(se.ExpiresMs).Sub(now) < ttl/2 {
		s.st.ExtendSession(ctx, h, min(now.Add(ttl).UnixMilli(), end.UnixMilli()))
	}
	return User{Name: u.Name, Role: u.Role, CSRF: se.CSRF}, nil
}

// CheckCSRF verifies the X-CSRF-Token header of a state-changing request.
// Only the caller of an instance without authentication has no token.
func CheckCSRF(u User, r *http.Request) bool {
	if u.NoAuth {
		return true
	}
	got := r.Header.Get("X-CSRF-Token")
	return u.CSRF != "" && got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(u.CSRF)) == 1
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ResetPassword sets a new random password for a local account, closes its
// sessions and returns the password (estopo admin reset-password).
func ResetPassword(ctx context.Context, st *store.Store, name string) (string, error) {
	u, err := st.User(ctx, name)
	if err != nil {
		return "", err
	}
	if u.Source != "local" {
		return "", fmt.Errorf("account %s is not a local account", name)
	}
	pw := RandomPassword()
	h, err := HashPassword(pw)
	if err != nil {
		return "", err
	}
	if err := st.SetPassword(ctx, name, h); err != nil {
		return "", err
	}
	if err := RevokeSessions(ctx, st, name, ""); err != nil {
		return "", err
	}
	return pw, st.Audit(ctx, "cli", "user.reset_password", name, "")
}
