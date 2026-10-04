// Package auth handles the web users (SPEC §10): local passwords
// (argon2id), sessions with CSRF tokens, login throttling. LDAP comes with
// plan step 12.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (RFC 9106 "second recommended option": 64 MiB).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// argonSlots bounds the simultaneous argon2 computations (64 MiB each):
// parallel logins, which need no authentication, must not exhaust the
// memory.
var argonSlots = make(chan struct{}, 4)

// ErrBusy is returned when the caller gave up (cancelled context) while
// waiting for its turn to compute a hash.
var ErrBusy = errors.New("auth: busy, retry later")

func argon(ctx context.Context, password string, salt []byte, t, m uint32, p uint8, keyLen uint32) ([]byte, error) {
	select {
	case argonSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, fmt.Errorf("%w (%v)", ErrBusy, ctx.Err())
	}
	defer func() { <-argonSlots }()
	return argon2.IDKey([]byte(password), salt, t, m, p, keyLen), nil
}

// HashPassword returns an encoded argon2id hash:
// $argon2id$v=19$m=65536,t=3,p=2$<salt>$<hash>.
func HashPassword(password string) (string, error) {
	return HashPasswordContext(context.Background(), password)
}

// HashPasswordContext is HashPassword for a request: it waits for its turn
// as long as ctx lives (ErrBusy otherwise).
func HashPasswordContext(ctx context.Context, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := argon(ctx, password, salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	if err != nil {
		return "", err
	}
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// ErrBadHash is returned for a malformed stored hash.
var ErrBadHash = errors.New("auth: malformed password hash")

// VerifyPassword checks password against an encoded hash in constant time.
func VerifyPassword(encoded, password string) (bool, error) {
	return VerifyPasswordContext(context.Background(), encoded, password)
}

// VerifyPasswordContext is VerifyPassword for a request: it waits for its
// turn as long as ctx lives (ErrBusy otherwise).
func VerifyPasswordContext(ctx context.Context, encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrBadHash
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, ErrBadHash
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, ErrBadHash
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, ErrBadHash
	}
	got, err := argon(ctx, password, salt, t, m, p, uint32(len(want)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// RandomPassword returns a random password (initial admin, reset).
func RandomPassword() string {
	b := make([]byte, 15)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// RandomToken returns a random URL-safe token of n bytes of entropy.
func RandomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
