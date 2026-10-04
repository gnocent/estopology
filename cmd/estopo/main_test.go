package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"estopo/internal/auth"
	"estopo/internal/clock"
	"estopo/internal/store"
)

func TestVersion(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"version"}, func(string) string { return "" }, &out, io.Discard); code != 0 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.HasPrefix(out.String(), "estopo dev") {
		t.Errorf("unexpected output %q", out.String())
	}
}

func TestUnknownCommand(t *testing.T) {
	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"frobnicate"}, func(string) string { return "" }, io.Discard, &errOut); code != 2 {
		t.Fatalf("exit code %d", code)
	}
	if !strings.Contains(errOut.String(), "Usage:") {
		t.Errorf("usage not printed: %q", errOut.String())
	}
}

func TestServeRejectsInvalidConfig(t *testing.T) {
	var errOut bytes.Buffer
	env := map[string]string{"ESTOPO_LOG_FORMAT": "xml", "ESTOPO_DATA_DIR": t.TempDir()}
	code := run(context.Background(), []string{"serve"}, func(k string) string { return env[k] }, io.Discard, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), "ESTOPO_LOG_FORMAT") {
		t.Errorf("code %d, stderr %q", code, errOut.String())
	}
}

func TestServeHealthzAndShutdown(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	env := map[string]string{"ESTOPO_LISTEN": addr, "ESTOPO_DATA_DIR": t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, []string{"serve"}, func(k string) string { return env[k] }, io.Discard, io.Discard)
	}()

	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, err = http.Get(fmt.Sprintf("http://%s/healthz", addr))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz status %d", resp.StatusCode)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("serve exit code %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestResetPassword(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"ESTOPO_DATA_DIR": dir}
	getenv := func(k string) string { return env[k] }
	ctx := context.Background()
	var out, errOut bytes.Buffer
	if code := run(ctx, []string{"admin", "reset-password", "nobody"}, getenv, &out, &errOut); code != 1 {
		t.Errorf("unknown user: %d", code)
	}
	st, err := store.Open(ctx, dir, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	st.SaveUser(ctx, store.User{Name: "admin", Role: "admin", Source: "local", Password: "x"})
	st.Close()
	out.Reset()
	if code := run(ctx, []string{"admin", "reset-password", "admin"}, getenv, &out, &errOut); code != 0 {
		t.Fatalf("reset: %d %s", code, errOut.String())
	}
	pw := strings.TrimSpace(strings.TrimPrefix(out.String(), "New password for admin:"))
	st, _ = store.Open(ctx, dir, clock.Real{})
	defer st.Close()
	u, _ := st.User(ctx, "admin")
	if ok, _ := auth.VerifyPassword(u.Password, pw); !ok || len(pw) < 12 {
		t.Errorf("printed password %q does not match", pw)
	}
}

// A password reset closes the sessions of the account: whoever knew the
// old password is out.
func TestResetPasswordClosesSessions(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"ESTOPO_DATA_DIR": dir}
	ctx := context.Background()
	st, err := store.Open(ctx, dir, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	st.SaveUser(ctx, store.User{Name: "admin", Role: "admin", Source: "local", Password: "x"})
	st.SaveUser(ctx, store.User{Name: "other", Role: "reader", Source: "local", Password: "x"})
	for hash, user := range map[string]string{"h1": "admin", "h2": "admin", "h3": "other"} {
		if err := st.CreateSession(ctx, store.Session{TokenHash: hash, User: user, CSRF: "c", ExpiresMs: time.Now().Add(time.Hour).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	var out, errOut bytes.Buffer
	if code := run(ctx, []string{"admin", "reset-password", "admin"}, func(k string) string { return env[k] }, &out, &errOut); code != 0 {
		t.Fatalf("reset: %d %s", code, errOut.String())
	}
	st, _ = store.Open(ctx, dir, clock.Real{})
	defer st.Close()
	for _, hash := range []string{"h1", "h2"} {
		if _, err := st.SessionByHash(ctx, hash); err == nil {
			t.Errorf("session %s of the account still open", hash)
		}
	}
	if _, err := st.SessionByHash(ctx, "h3"); err != nil {
		t.Errorf("session of another account closed: %v", err)
	}
}
