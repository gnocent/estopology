package esclient

import (
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"estopo/internal/testpki"
)

// B16: any non-GET/HEAD call is refused before reaching the network.
func TestWriteMethodsForbidden(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	c, err := New(Config{URLs: []string{srv.URL}, Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, "OPTIONS"} {
		if _, err := c.do(context.Background(), m, "x", "/idx/_doc/1", nil, nil); !errors.Is(err, ErrWriteForbidden) {
			t.Errorf("%s: err = %v, want ErrWriteForbidden", m, err)
		}
		// The transport refuses too, even if Client.do were bypassed.
		req, _ := http.NewRequest(m, srv.URL+"/idx", nil)
		if _, err := c.hc.Do(req); !errors.Is(err, ErrWriteForbidden) {
			t.Errorf("transport %s: err = %v", m, err)
		}
	}
	if hits != 0 {
		t.Errorf("server received %d write requests", hits)
	}
	if _, err := c.do(context.Background(), http.MethodHead, "head", "/", nil, nil); err != nil {
		t.Errorf("HEAD refused: %v", err)
	}
}

func TestMutualTLSWithIntermediateCA(t *testing.T) {
	pki, err := testpki.Generate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != "estopo" {
			http.Error(w, `{"error":{"type":"security_exception","reason":"no client cert"},"status":401}`, 401)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	cert, err := tls.LoadX509KeyPair(pki.ServerCert, pki.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if _, err := loadCAs(pool, pki.CAChain); err != nil {
		t.Fatal(err)
	}
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, ClientCAs: pool, ClientAuth: tls.VerifyClientCertIfGiven}
	srv.StartTLS()
	defer srv.Close()

	// CA given as a directory holding only the root: the server sends the intermediate.
	caDir := t.TempDir()
	copyFile(t, pki.RootCert, filepath.Join(caDir, "corp-root.pem"))

	c, err := New(Config{URLs: []string{srv.URL}, Auth: AuthCert, CertFile: pki.ClientCert, KeyFile: pki.ClientKey, CAPath: caDir})
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ OK bool }
	st, err := c.Get(context.Background(), "root", "/", nil, func(r io.Reader) error { return json.NewDecoder(r).Decode(&got) })
	if err != nil || !got.OK || st.Status != 200 {
		t.Fatalf("mTLS call: st=%+v err=%v", st, err)
	}

	// Without a client certificate the server answers 401, surfaced as HTTPError.
	c2, err := New(Config{URLs: []string{srv.URL}, Auth: AuthNone, CAPath: caDir})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c2.Get(context.Background(), "root", "/", nil, nil)
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != 401 || he.Type != "security_exception" {
		t.Errorf("err = %v", err)
	}

	// Unknown CA: handshake failure.
	c3, err := New(Config{URLs: []string{srv.URL}, Auth: AuthNone, CAPath: filepath.Join("testdata", "other-ca.pem")})
	if err == nil {
		if _, err = c3.Get(context.Background(), "root", "/", nil, nil); err == nil {
			t.Error("expected a certificate verification error")
		}
	}
}

func TestEncryptedKey(t *testing.T) {
	// testdata/encrypted.key: openssl pkcs8 -topk8 -v2 aes256 (PBKDF2-HMAC-SHA256), passphrase "estopo-test".
	dir := t.TempDir()
	pass := filepath.Join(dir, "pass")
	os.WriteFile(pass, []byte("estopo-test\n"), 0o600)
	if _, err := loadKeyPair("testdata/encrypted.crt", "testdata/encrypted.key", pass); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	os.WriteFile(pass, []byte("wrong"), 0o600)
	if _, err := loadKeyPair("testdata/encrypted.crt", "testdata/encrypted.key", pass); err == nil {
		t.Error("wrong passphrase accepted")
	}
	if _, err := loadKeyPair("testdata/encrypted.crt", "testdata/encrypted.key", ""); err == nil {
		t.Error("missing passphrase accepted")
	}
}

func TestAuthHeadersGzipAndFailover(t *testing.T) {
	var gotAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Errorf("gzip not requested")
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		gz.Write([]byte(`{"n":` + strings.Repeat(" ", 1000) + `1}`))
		gz.Close()
	}))
	defer srv.Close()
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "apikey")
	os.WriteFile(keyFile, []byte("id1:secret\n"), 0o600)
	pwFile := filepath.Join(dir, "pw")
	os.WriteFile(pwFile, []byte("pw\n"), 0o600)

	// First URL refuses connections: the client falls back to the second one.
	dead := "http://127.0.0.1:1"
	c, err := New(Config{URLs: []string{dead, srv.URL}, Auth: AuthAPIKey, APIKeyFile: keyFile})
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ N int }
	st, err := c.Get(context.Background(), "x", "/", nil, func(r io.Reader) error { return json.NewDecoder(r).Decode(&v) })
	if err != nil || v.N != 1 {
		t.Fatalf("st=%+v err=%v", st, err)
	}
	if !st.Gzip || st.Bytes <= st.WireBytes || st.URL != srv.URL {
		t.Errorf("unexpected stats %+v", st)
	}
	if c.active != 1 {
		t.Errorf("active URL not remembered")
	}

	cb, err := New(Config{URLs: []string{srv.URL}, Auth: AuthBasic, Username: "u", PasswordFile: pwFile})
	if err != nil {
		t.Fatal(err)
	}
	cb.Get(context.Background(), "x", "/", nil, nil)
	if gotAuth[0] != "ApiKey aWQxOnNlY3JldA==" || gotAuth[1] != "Basic dTpwdw==" {
		t.Errorf("auth headers %q", gotAuth)
	}
}

func TestConfigErrors(t *testing.T) {
	for name, cfg := range map[string]Config{
		"no url":         {Auth: AuthNone},
		"bad scheme":     {URLs: []string{"ftp://x"}, Auth: AuthNone},
		"no key file":    {URLs: []string{"https://x"}, Auth: AuthAPIKey},
		"no user":        {URLs: []string{"https://x"}, Auth: AuthBasic},
		"unknown auth":   {URLs: []string{"https://x"}, Auth: "kerberos"},
		"missing cert":   {URLs: []string{"https://x"}, Auth: AuthCert, CertFile: "nope.crt", KeyFile: "nope.key"},
		"missing CA":     {URLs: []string{"https://x"}, Auth: AuthNone, CAPath: "nope-dir"},
		"CA without pem": {URLs: []string{"https://x"}, Auth: AuthNone, CAPath: "testdata/empty-ca"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
