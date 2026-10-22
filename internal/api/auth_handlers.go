package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"estopo/internal/auth"
	"estopo/internal/store"
)

// SessionInfo answers GET /api/v1/session (the UI starts there).
type SessionInfo struct {
	Mode          string          `json:"mode"`
	Authenticated bool            `json:"authenticated"`
	User          string          `json:"user,omitempty"`
	Role          string          `json:"role,omitempty"`
	CSRF          string          `json:"csrf,omitempty"`
	Instance      string          `json:"instance"`           // primary, standby, fenced (banner)
	Insecure      bool            `json:"insecure,omitempty"` // credentials over plain HTTP (ESTOPO_ALLOW_INSECURE_AUTH): warning banner
	Prefs         json.RawMessage `json:"prefs,omitempty"`    // language and theme of the account (SPEC §8.1)
}

// authMode returns the authentication mode; an error means that it is
// unknown, which is never to be taken for mode none.
func (s *Server) authMode(r *http.Request) (string, error) {
	if s.auth == nil {
		return auth.ModeNone, nil
	}
	c, err := s.auth.Config(r.Context())
	return c.Mode, err
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	mode, err := s.authMode(r)
	if err != nil {
		s.authUnavailable(w, r, err)
		return
	}
	info := SessionInfo{Mode: mode, Instance: s.role()}
	info.Insecure = info.Mode != auth.ModeNone && !s.transport.TLS && !s.transport.LoopbackOnly
	if s.auth == nil {
		info.Authenticated, info.User, info.Role = true, "anonymous", auth.RoleAdmin
	} else if u, err := s.auth.Resolve(r.Context(), r); err == nil {
		info.Authenticated, info.User, info.Role, info.CSRF = true, u.Name, u.Role, u.CSRF
		if rec, err := s.store.User(r.Context(), u.Name); err == nil && rec.Prefs != "" {
			info.Prefs = json.RawMessage(rec.Prefs)
		}
	} else if !errors.Is(err, auth.ErrUnauthenticated) {
		s.authUnavailable(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Not behind guard (no session yet), but under the same rules: a page of
	// another site must not log a browser in.
	if why := crossSite(r); why != "" {
		writeError(w, http.StatusForbidden, "cross_site", why)
		return
	}
	mode, err := s.authMode(r)
	if err != nil {
		s.authUnavailable(w, r, err)
		return
	}
	if s.auth == nil || mode == auth.ModeNone {
		writeError(w, http.StatusBadRequest, "no_auth", "authentication is disabled")
		return
	}
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	u, err := s.auth.Login(r.Context(), w, r, strings.TrimSpace(body.Name), body.Password)
	switch {
	case errors.Is(err, auth.ErrThrottled):
		writeError(w, http.StatusTooManyRequests, "throttled", err.Error())
	case errors.Is(err, auth.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "directory_unavailable", "the directory is unavailable")
	case errors.Is(err, auth.ErrInvalid):
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid credentials")
	case err != nil: // given up while waiting, or the store failed
		s.authUnavailable(w, r, err)
	default:
		writeJSON(w, http.StatusOK, SessionInfo{Mode: mode, Instance: s.role(), Authenticated: true, User: u.Name, Role: u.Role, CSRF: u.CSRF})
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.auth != nil {
		s.auth.Logout(r.Context(), w, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleOwnPassword lets a local user change its password. The current one
// is checked under the login throttling, and the other sessions of the
// account end.
func (s *Server) handleOwnPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	ctx, me := r.Context(), s.user(r)
	u, err := s.store.User(ctx, me.Name)
	if err != nil || u.Source != "local" || s.auth == nil {
		writeError(w, http.StatusBadRequest, "not_local", "only local accounts have a password here")
		return
	}
	if err := validPassword(body.New); err != nil {
		s.fail(w, r, err)
		return
	}
	switch err := s.auth.CheckPassword(ctx, r, u, body.Old); {
	case errors.Is(err, auth.ErrThrottled):
		writeError(w, http.StatusTooManyRequests, "throttled", err.Error())
		return
	case errors.Is(err, auth.ErrInvalid):
		writeError(w, http.StatusForbidden, "invalid_credentials", "wrong current password")
		return
	case err != nil:
		s.authUnavailable(w, r, err)
		return
	}
	h, err := auth.HashPasswordContext(ctx, body.New)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.SetPassword(ctx, me.Name, h); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := auth.RevokeSessions(ctx, s.store, me.Name, auth.SessionHash(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(ctx, me.Name, "user.password", me.Name, "")
	w.WriteHeader(http.StatusNoContent)
}

func validPassword(p string) error {
	if len(p) < auth.MinPasswordLen {
		return badRequest("the password must have at least 12 characters")
	}
	return nil
}

// --- admin: authentication and users --------------------------------------------------

func (s *Server) handleAdminAuthGet(w http.ResponseWriter, r *http.Request) {
	if s.auth == nil {
		writeJSON(w, http.StatusOK, auth.Config{Mode: auth.ModeNone})
		return
	}
	c, err := s.auth.Config(r.Context())
	if err != nil {
		s.authUnavailable(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// handleAdminAuthPut changes the authentication mode. Switching to local or
// LDAP is refused over plain HTTP on a non-loopback listener (B17), and
// requires at least one local admin: in LDAP mode it is the way back in when
// the directory fails.
func (s *Server) handleAdminAuthPut(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var c auth.Config
	if err := decodeJSON(r, &c); err != nil {
		s.fail(w, r, err)
		return
	}
	switch c.Mode {
	case auth.ModeNone, auth.ModeLocal, auth.ModeLDAP:
	default:
		s.fail(w, r, badRequest("mode must be none, local or ldap"))
		return
	}
	if err := auth.CheckTransport(c.Mode, s.transport.TLS, s.transport.LoopbackOnly, s.transport.AllowInsecureAuth); err != nil {
		writeError(w, http.StatusConflict, "insecure_transport", err.Error())
		return
	}
	if c.Mode == auth.ModeLDAP {
		if c.LDAP == nil {
			s.fail(w, r, badRequest("the LDAP configuration is missing"))
			return
		}
		if err := c.LDAP.Validate(); err != nil {
			s.fail(w, r, badRequest(err.Error()))
			return
		}
		if err := c.LDAP.CheckFiles(s.secretsDir); err != nil {
			s.fail(w, r, badRequest(err.Error()))
			return
		}
	}
	if c.SessionHours == 0 {
		c.SessionHours = auth.DefaultSessionHours
	}
	if c.SessionHours < 1 || c.SessionHours > auth.MaxSessionHours {
		s.fail(w, r, badRequest("session_hours must be between 1 and 720"))
		return
	}
	s.usersMu.Lock()
	defer s.usersMu.Unlock()
	oldMode, err := s.authMode(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if c.Mode != auth.ModeNone {
		users, err := s.store.Users(ctx)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		hasAdmin := false
		for _, u := range users {
			if localAdmin(u) {
				hasAdmin = true
			}
		}
		if !hasAdmin {
			writeError(w, http.StatusConflict, "no_admin", "create a local admin account before enabling authentication (the way back in if the directory fails)")
			return
		}
	}
	if err := s.store.SetSetting(ctx, "auth", c); err != nil {
		s.fail(w, r, err)
		return
	}
	if oldMode != c.Mode {
		// The sessions of the previous mode end: otherwise local -> none ->
		// local would bring them back. A local administrator keeps the
		// session it is using.
		keep := ""
		if c.Mode != auth.ModeNone {
			if u, err := s.store.User(ctx, s.user(r).Name); err == nil && u.Source == "local" {
				keep = auth.SessionHash(r)
			}
		}
		if err := auth.RevokeAllSessions(ctx, s.store, keep); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.store.Audit(ctx, s.user(r).Name, "auth.mode", c.Mode, "")
	writeJSON(w, http.StatusOK, c)
}

// localAdmin reports whether an account lets an administrator in without
// the directory.
func localAdmin(u store.User) bool {
	return u.Source == "local" && u.Role == auth.RoleAdmin && !u.Disabled && u.Password != ""
}

// lastAdmin reports whether u is the only local administrator left while
// authentication is enabled: without it nobody could administer the instance
// (local mode), or get back in when the directory fails (LDAP mode).
func (s *Server) lastAdmin(r *http.Request, u store.User) (bool, error) {
	if !localAdmin(u) {
		return false, nil
	}
	if mode, err := s.authMode(r); err != nil || mode == auth.ModeNone {
		return false, err
	}
	users, err := s.store.Users(r.Context())
	if err != nil {
		return false, err
	}
	for _, o := range users {
		if o.Name != u.Name && localAdmin(o) {
			return false, nil
		}
	}
	return true, nil
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.Users(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if users == nil {
		users = []store.User{}
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleAdminSaveUser(w http.ResponseWriter, r *http.Request) {
	ctx, name := r.Context(), r.PathValue("name")
	var body struct {
		Role     string `json:"role"`
		Password string `json:"password"`
		Disabled bool   `json:"disabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	if name == "" || strings.ContainsAny(name, " /\\") {
		s.fail(w, r, badRequest("invalid user name"))
		return
	}
	if body.Role != auth.RoleReader && body.Role != auth.RoleAdmin {
		s.fail(w, r, badRequest("role must be reader or admin"))
		return
	}
	u := store.User{Name: name, Role: body.Role, Source: "local", Disabled: body.Disabled}
	if body.Password != "" {
		// Hashed before taking usersMu: it takes ~100 ms.
		if err := validPassword(body.Password); err != nil {
			s.fail(w, r, err)
			return
		}
		var err error
		if u.Password, err = auth.HashPasswordContext(ctx, body.Password); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.usersMu.Lock()
	defer s.usersMu.Unlock()
	existing, err := s.store.User(ctx, name)
	exists := err == nil
	switch {
	case exists:
		u.Source = existing.Source
	case !errors.Is(err, store.ErrNotFound):
		s.fail(w, r, err)
		return
	case body.Password == "":
		s.fail(w, r, badRequest("a new local account needs a password"))
		return
	}
	if exists && (u.Role != auth.RoleAdmin || u.Disabled) {
		if last, err := s.lastAdmin(r, existing); err != nil {
			s.fail(w, r, err)
			return
		} else if last {
			writeError(w, http.StatusConflict, "last_admin", "the last local admin account cannot be disabled or demoted")
			return
		}
	}
	if err := s.store.SaveUser(ctx, u); err != nil {
		s.fail(w, r, err)
		return
	}
	// A new password, another role or a disabled account end the sessions
	// of the account; an administrator changing its own password keeps the
	// session it is using.
	if demoted := u.Role != existing.Role || (u.Disabled && !existing.Disabled); exists && (demoted || body.Password != "") {
		keep := ""
		if !demoted && name == s.user(r).Name {
			keep = auth.SessionHash(r)
		}
		if err := auth.RevokeSessions(ctx, s.store, name, keep); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	s.store.Audit(ctx, s.user(r).Name, "user.save", name, body.Role)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	ctx, name := r.Context(), r.PathValue("name")
	if name == s.user(r).Name {
		s.fail(w, r, badRequest("you cannot delete your own account"))
		return
	}
	s.usersMu.Lock()
	defer s.usersMu.Unlock()
	if u, err := s.store.User(ctx, name); err == nil {
		if last, err := s.lastAdmin(r, u); err != nil {
			s.fail(w, r, err)
			return
		} else if last {
			writeError(w, http.StatusConflict, "last_admin", "the last local admin account cannot be deleted")
			return
		}
	}
	if err := s.store.DeleteUser(ctx, name); err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(ctx, s.user(r).Name, "user.delete", name, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminLDAPTest tests an LDAP configuration (not necessarily saved)
// step by step; with a user and password, a full authentication.
func (s *Server) handleAdminLDAPTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		LDAP     auth.LDAPConfig `json:"ldap"`
		User     string          `json:"user"`
		Password string          `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := body.LDAP.Validate(); err != nil {
		s.fail(w, r, badRequest(err.Error()))
		return
	}
	if err := body.LDAP.CheckFiles(s.secretsDir); err != nil {
		s.fail(w, r, badRequest(err.Error()))
		return
	}
	steps, res, err := auth.TestLDAP(body.LDAP, strings.ToLower(strings.TrimSpace(body.User)), body.Password)
	out := struct {
		OK     bool             `json:"ok"`
		Error  string           `json:"error,omitempty"`
		Steps  []auth.LDAPStep  `json:"steps"`
		Result *auth.LDAPResult `json:"result,omitempty"`
	}{OK: err == nil, Steps: steps, Result: res}
	if err != nil {
		out.Error = err.Error()
	}
	if out.Steps == nil {
		out.Steps = []auth.LDAPStep{}
	}
	s.store.Audit(r.Context(), s.user(r).Name, "auth.ldap_test", body.User, "")
	writeJSON(w, http.StatusOK, out)
}

// handleOwnPrefs stores the language and theme of the caller's account, so
// that they follow it from one browser to another (SPEC §8.1).
func (s *Server) handleOwnPrefs(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Lang  string `json:"lang"`
		Theme string `json:"theme"`
	}
	if err := decodeJSON(r, &p); err != nil {
		s.fail(w, r, err)
		return
	}
	if (p.Lang != "fr" && p.Lang != "en") || (p.Theme != "light" && p.Theme != "dark" && p.Theme != "auto") {
		s.fail(w, r, badRequest("lang must be fr or en, theme light, dark or auto"))
		return
	}
	me := s.user(r)
	if me.Name == "anonymous" { // authentication disabled: the browser keeps them
		w.WriteHeader(http.StatusNoContent)
		return
	}
	b, _ := json.Marshal(p)
	if err := s.store.SetUserPrefs(r.Context(), me.Name, string(b)); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
