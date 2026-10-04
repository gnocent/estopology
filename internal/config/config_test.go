package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	exeDir := t.TempDir()
	c, err := Load(env(nil), exeDir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != DefaultListen || c.LogLevel != "info" || c.LogFormat != "text" {
		t.Errorf("unexpected defaults: %+v", c)
	}
	if want := filepath.Join(exeDir, "data"); c.DataDir != want {
		t.Errorf("DataDir = %q, want %q", c.DataDir, want)
	}
	if c.TLS() || c.AllowInsecureAuth {
		t.Errorf("TLS and insecure auth must be off by default")
	}
}

func TestLoadValues(t *testing.T) {
	c, err := Load(env(map[string]string{
		"ESTOPO_LISTEN":                 "127.0.0.1:9443",
		"ESTOPO_DATA_DIR":               "/var/lib/estopo",
		"ESTOPO_TLS_CERT":               "/etc/cert.pem",
		"ESTOPO_TLS_KEY":                "/etc/key.pem",
		"ESTOPO_ALLOW_INSECURE_AUTH":    "true",
		"ESTOPO_LOG_LEVEL":              "DEBUG",
		"ESTOPO_LOG_FORMAT":             "json",
		"ESTOPO_STANDBY_OF":             "https://primary:8443",
		"ESTOPO_REPLICATION_TOKEN_FILE": "/etc/estopo/token",
	}), "/opt/estopo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Load(env(map[string]string{"ESTOPO_PPROF_LISTEN": "0.0.0.0:6060"}), "/opt"); err == nil || !strings.Contains(err.Error(), "ESTOPO_PPROF_LISTEN") {
		t.Errorf("non-loopback profiler accepted: %v", err)
	}
	if c, err := Load(env(map[string]string{"ESTOPO_PPROF_LISTEN": "127.0.0.1:6060"}), "/opt"); err != nil || c.PprofListen == "" {
		t.Errorf("loopback profiler refused: %v", err)
	}
	if !c.TLS() || !c.AllowInsecureAuth || c.LogLevel != "debug" || c.LogFormat != "json" {
		t.Errorf("unexpected config: %+v", c)
	}
	if !c.LoopbackOnly() {
		t.Errorf("127.0.0.1 must be loopback")
	}
}

func TestLoadErrors(t *testing.T) {
	_, err := Load(env(map[string]string{
		"ESTOPO_LISTEN":              "nope",
		"ESTOPO_TLS_CERT":            "/etc/cert.pem",
		"ESTOPO_ALLOW_INSECURE_AUTH": "maybe",
		"ESTOPO_LOG_LEVEL":           "verbose",
		"ESTOPO_LOG_FORMAT":          "xml",
		"ESTOPO_STANDBY_OF":          "https://primary:8443",
	}), "/opt/estopo")
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"ESTOPO_LISTEN", "ESTOPO_TLS_KEY", "ESTOPO_ALLOW_INSECURE_AUTH",
		"ESTOPO_LOG_LEVEL", "ESTOPO_LOG_FORMAT", "ESTOPO_REPLICATION_TOKEN_FILE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestLoopbackOnly(t *testing.T) {
	for listen, want := range map[string]bool{
		":8080":          false,
		"0.0.0.0:8080":   false,
		"10.1.2.3:8080":  false,
		"127.0.0.1:8080": true,
		"[::1]:8080":     true,
		"localhost:8080": true,
	} {
		if got := (Config{Listen: listen}).LoopbackOnly(); got != want {
			t.Errorf("LoopbackOnly(%q) = %v, want %v", listen, got, want)
		}
	}
}

// Out of the box nobody is authenticated: the default listener must not
// accept connections from other hosts.
func TestDefaultListenIsLoopback(t *testing.T) {
	c, err := Load(env(nil), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8080" || !c.LoopbackOnly() {
		t.Errorf("default listen address %q", c.Listen)
	}
}

func TestSecretsDir(t *testing.T) {
	if c, err := Load(env(nil), t.TempDir()); err != nil || c.SecretsDir != "" {
		t.Errorf("default: %q %v", c.SecretsDir, err)
	}
	dir := t.TempDir()
	c, err := Load(env(map[string]string{"ESTOPO_SECRETS_DIR": dir}), t.TempDir())
	if err != nil || c.SecretsDir != dir {
		t.Errorf("SecretsDir = %q, want %q (%v)", c.SecretsDir, dir, err)
	}
	if _, err := Load(env(map[string]string{"ESTOPO_SECRETS_DIR": filepath.Join(dir, "absent")}), t.TempDir()); err == nil || !strings.Contains(err.Error(), "ESTOPO_SECRETS_DIR") {
		t.Errorf("missing directory accepted: %v", err)
	}
}
