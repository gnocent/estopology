package auth

import (
	"strings"
	"testing"
	"time"

	"estopo/internal/clock"
)

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("hash %s", h)
	}
	if ok, err := VerifyPassword(h, "correct horse battery"); !ok || err != nil {
		t.Errorf("valid password refused: %v", err)
	}
	if ok, _ := VerifyPassword(h, "wrong"); ok {
		t.Error("wrong password accepted")
	}
	h2, _ := HashPassword("correct horse battery")
	if h2 == h {
		t.Error("same salt twice")
	}
	if _, err := VerifyPassword("$bcrypt$x", "x"); err != ErrBadHash {
		t.Errorf("malformed hash: %v", err)
	}
}

func TestLimiter(t *testing.T) {
	clk := clock.NewManual(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	l := NewLimiter(clk)
	for i := 0; i < 5; i++ {
		if ok, _ := l.Allowed("user:a"); !ok {
			t.Fatalf("attempt %d refused", i)
		}
		l.Failure("user:a")
	}
	ok, wait := l.Allowed("user:a")
	if ok || wait != 2*time.Second {
		t.Fatalf("after 5 failures: ok=%v wait=%v", ok, wait)
	}
	clk.Advance(2 * time.Second)
	if ok, _ := l.Allowed("user:a"); !ok {
		t.Error("still blocked after the delay")
	}
	l.Failure("user:a") // 7th: 4 s
	if _, wait := l.Allowed("user:a"); wait != 4*time.Second {
		t.Errorf("exponential delay: %v", wait)
	}
	if ok, _ := l.Allowed("user:b"); !ok {
		t.Error("another key blocked")
	}
	l.Success("user:a")
	if ok, _ := l.Allowed("user:a"); !ok {
		t.Error("success did not clear the key")
	}
	for i := 0; i < 40; i++ {
		l.Failure("ip:x")
	}
	if _, wait := l.Allowed("ip:x"); wait > 15*time.Minute {
		t.Errorf("delay not capped: %v", wait)
	}
}

// B17: local and LDAP authentication are refused over plain HTTP on a
// non-loopback listener unless explicitly allowed.
func TestCheckTransport(t *testing.T) {
	for _, tc := range []struct {
		mode                           string
		tls, loopback, insecure, allow bool
	}{
		{ModeNone, false, false, false, true},
		{ModeLocal, false, false, false, false},
		{ModeLDAP, false, false, false, false},
		{ModeLocal, true, false, false, true},
		{ModeLocal, false, true, false, true},
		{ModeLocal, false, false, true, true},
	} {
		err := CheckTransport(tc.mode, tc.tls, tc.loopback, tc.insecure)
		if (err == nil) != tc.allow {
			t.Errorf("%+v: %v", tc, err)
		}
		if err != nil && !strings.Contains(err.Error(), "ESTOPO_ALLOW_INSECURE_AUTH") {
			t.Errorf("message does not explain the way out: %v", err)
		}
	}
}
