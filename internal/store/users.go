package store

import (
	"context"
	"database/sql"
	"errors"
)

// User is a web account (SPEC §10). Password holds an argon2id hash for
// local accounts; LDAP users are recorded at their first login.
type User struct {
	Name      string `json:"name"`
	Role      string `json:"role"` // reader, admin
	Source    string `json:"source"`
	Password  string `json:"-"`
	CreatedMs int64  `json:"created_ms"`
	Disabled  bool   `json:"disabled"`
	Prefs     string `json:"prefs,omitempty"` // JSON (language, theme)
}

// Users lists the accounts.
func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.Meta.QueryContext(ctx, `SELECT name, role, source, coalesce(password, ''), created_ms, disabled, coalesce(prefs, '') FROM user ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.Name, &u.Role, &u.Source, &u.Password, &u.CreatedMs, &u.Disabled, &u.Prefs); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// User returns one account.
func (s *Store) User(ctx context.Context, name string) (User, error) {
	var u User
	err := s.Meta.QueryRowContext(ctx, `SELECT name, role, source, coalesce(password, ''), created_ms, disabled, coalesce(prefs, '') FROM user WHERE name = ?`, name).
		Scan(&u.Name, &u.Role, &u.Source, &u.Password, &u.CreatedMs, &u.Disabled, &u.Prefs)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// SaveUser creates or updates an account (the password is kept when empty).
func (s *Store) SaveUser(ctx context.Context, u User) error {
	_, err := s.Meta.ExecContext(ctx, `INSERT INTO user(name, role, source, password, created_ms, disabled) VALUES (?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET role = excluded.role, source = excluded.source, disabled = excluded.disabled,
		password = coalesce(excluded.password, user.password)`,
		u.Name, u.Role, u.Source, nullStr(u.Password), s.clock.Now().UnixMilli(), u.Disabled)
	return err
}

// SetPassword replaces the password hash of an account.
func (s *Store) SetPassword(ctx context.Context, name, hash string) error {
	res, err := s.Meta.ExecContext(ctx, `UPDATE user SET password = ? WHERE name = ?`, hash, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUserPrefs stores the UI preferences of an account.
func (s *Store) SetUserPrefs(ctx context.Context, name, prefs string) error {
	_, err := s.Meta.ExecContext(ctx, `UPDATE user SET prefs = ? WHERE name = ?`, prefs, name)
	return err
}

// DeleteUser removes an account and its sessions.
func (s *Store) DeleteUser(ctx context.Context, name string) error {
	if _, err := s.Meta.ExecContext(ctx, `DELETE FROM session WHERE user_name = ?`, name); err != nil {
		return err
	}
	res, err := s.Meta.ExecContext(ctx, `DELETE FROM user WHERE name = ?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Session is a web session (the token itself is never stored, only its hash).
type Session struct {
	TokenHash string
	User      string
	CSRF      string
	CreatedMs int64
	ExpiresMs int64
}

// CreateSession stores a session.
func (s *Store) CreateSession(ctx context.Context, se Session) error {
	_, err := s.Meta.ExecContext(ctx, `INSERT INTO session(token_hash, user_name, csrf, created_ms, expires_ms) VALUES (?,?,?,?,?)`,
		se.TokenHash, se.User, se.CSRF, se.CreatedMs, se.ExpiresMs)
	return err
}

// SessionByHash returns a session (expired ones included).
func (s *Store) SessionByHash(ctx context.Context, hash string) (Session, error) {
	se := Session{TokenHash: hash}
	err := s.Meta.QueryRowContext(ctx, `SELECT user_name, csrf, created_ms, expires_ms FROM session WHERE token_hash = ?`, hash).
		Scan(&se.User, &se.CSRF, &se.CreatedMs, &se.ExpiresMs)
	if errors.Is(err, sql.ErrNoRows) {
		return se, ErrNotFound
	}
	return se, err
}

// ExtendSession moves the expiry of a session.
func (s *Store) ExtendSession(ctx context.Context, hash string, expiresMs int64) error {
	_, err := s.Meta.ExecContext(ctx, `UPDATE session SET expires_ms = ? WHERE token_hash = ?`, expiresMs, hash)
	return err
}

// DeleteSession removes a session (logout).
func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	_, err := s.Meta.ExecContext(ctx, `DELETE FROM session WHERE token_hash = ?`, hash)
	return err
}

// DeleteExpiredSessions removes the sessions expired before nowMs.
func (s *Store) DeleteExpiredSessions(ctx context.Context, nowMs int64) error {
	_, err := s.Meta.ExecContext(ctx, `DELETE FROM session WHERE expires_ms < ?`, nowMs)
	return err
}

// AuditEntry is one admin action.
type AuditEntry struct {
	Ts      int64  `json:"ts"`
	Actor   string `json:"actor"`
	Action  string `json:"action"`
	Target  string `json:"target,omitempty"`
	Details string `json:"details,omitempty"`
}

// AuditLog returns the latest admin actions, newest first.
func (s *Store) AuditLog(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.Meta.QueryContext(ctx, `SELECT ts, actor, action, coalesce(target, ''), coalesce(details, '') FROM audit ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var a AuditEntry
		if err := rows.Scan(&a.Ts, &a.Actor, &a.Action, &a.Target, &a.Details); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// PruneAudit removes the audit entries older than beforeMs.
func (s *Store) PruneAudit(ctx context.Context, beforeMs int64) (int64, error) {
	res, err := s.Meta.ExecContext(ctx, `DELETE FROM audit WHERE ts < ?`, beforeMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
