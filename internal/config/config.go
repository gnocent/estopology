// Package config reads the bootstrap settings that must be known before the
// database is opened (SPEC §12.1). Everything else lives in meta.db and is
// edited from the admin UI.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config holds the environment-provided settings.
type Config struct {
	Listen            string // ESTOPO_LISTEN
	DataDir           string // ESTOPO_DATA_DIR (absolute)
	TLSCert           string // ESTOPO_TLS_CERT
	TLSKey            string // ESTOPO_TLS_KEY
	AdminPassword     string // ESTOPO_ADMIN_PASSWORD
	AllowInsecureAuth bool   // ESTOPO_ALLOW_INSECURE_AUTH
	LogLevel          string // ESTOPO_LOG_LEVEL: debug, info, warn, error
	LogFormat         string // ESTOPO_LOG_FORMAT: text, json
	StandbyOf         string // ESTOPO_STANDBY_OF: primary base URL
	ReplicationToken  string // ESTOPO_REPLICATION_TOKEN_FILE (path): enables the replication API, required on a standby
	ReplicationCA     string // ESTOPO_REPLICATION_CA: CA of the primary certificate (standby; default system CAs)
	PprofListen       string // ESTOPO_PPROF_LISTEN: loopback address of the Go profiler (diagnostics), off by default
	SecretsDir        string // ESTOPO_SECRETS_DIR (absolute): when set, the only place of the files named in the administration
}

// Defaults (SPEC §12.1). Out of the box there is no authentication and every
// visitor is an administrator: the default listener is local only.
const (
	DefaultListen    = "127.0.0.1:8080"
	DefaultLogLevel  = "info"
	DefaultLogFormat = "text"
)

// Load builds the configuration from getenv (os.Getenv in production).
// exeDir is the directory of the running binary, used for the default data
// directory ("./data next to the binary").
func Load(getenv func(string) string, exeDir string) (Config, error) {
	c := Config{
		Listen:           orDefault(getenv("ESTOPO_LISTEN"), DefaultListen),
		DataDir:          getenv("ESTOPO_DATA_DIR"),
		TLSCert:          getenv("ESTOPO_TLS_CERT"),
		TLSKey:           getenv("ESTOPO_TLS_KEY"),
		AdminPassword:    getenv("ESTOPO_ADMIN_PASSWORD"),
		LogLevel:         strings.ToLower(orDefault(getenv("ESTOPO_LOG_LEVEL"), DefaultLogLevel)),
		LogFormat:        strings.ToLower(orDefault(getenv("ESTOPO_LOG_FORMAT"), DefaultLogFormat)),
		StandbyOf:        getenv("ESTOPO_STANDBY_OF"),
		ReplicationToken: getenv("ESTOPO_REPLICATION_TOKEN_FILE"),
		ReplicationCA:    getenv("ESTOPO_REPLICATION_CA"),
		PprofListen:      getenv("ESTOPO_PPROF_LISTEN"),
		SecretsDir:       getenv("ESTOPO_SECRETS_DIR"),
	}

	var errs []error

	if v := getenv("ESTOPO_ALLOW_INSECURE_AUTH"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("ESTOPO_ALLOW_INSECURE_AUTH: %q is not a boolean", v))
		}
		c.AllowInsecureAuth = b
	}

	if c.DataDir == "" {
		c.DataDir = filepath.Join(exeDir, "data")
	}
	if abs, err := filepath.Abs(c.DataDir); err == nil {
		c.DataDir = abs
	}

	if c.SecretsDir != "" {
		if abs, err := filepath.Abs(c.SecretsDir); err == nil {
			c.SecretsDir = abs
		}
		if fi, err := os.Stat(c.SecretsDir); err != nil || !fi.IsDir() {
			errs = append(errs, fmt.Errorf("ESTOPO_SECRETS_DIR: %q is not a directory", c.SecretsDir))
		}
	}

	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		errs = append(errs, fmt.Errorf("ESTOPO_LISTEN: %q: %v", c.Listen, err))
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		errs = append(errs, errors.New("ESTOPO_TLS_CERT and ESTOPO_TLS_KEY must be set together"))
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("ESTOPO_LOG_LEVEL: %q (expected debug, info, warn or error)", c.LogLevel))
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		errs = append(errs, fmt.Errorf("ESTOPO_LOG_FORMAT: %q (expected text or json)", c.LogFormat))
	}
	if c.PprofListen != "" {
		if !(Config{Listen: c.PprofListen}).LoopbackOnly() {
			errs = append(errs, fmt.Errorf("ESTOPO_PPROF_LISTEN: %q must be a loopback address (e.g. 127.0.0.1:6060)", c.PprofListen))
		}
	}
	if c.StandbyOf != "" && c.ReplicationToken == "" {
		errs = append(errs, errors.New("ESTOPO_STANDBY_OF requires ESTOPO_REPLICATION_TOKEN_FILE"))
	}

	return c, errors.Join(errs...)
}

// TLS reports whether the web UI is served over HTTPS.
func (c Config) TLS() bool { return c.TLSCert != "" }

// LoopbackOnly reports whether the listen address only accepts local
// connections (used to allow authentication over plain HTTP, SPEC §10).
func (c Config) LoopbackOnly() bool {
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ExeDir returns the directory of the running executable, or the working
// directory if it cannot be determined.
func ExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		wd, _ := os.Getwd()
		return wd
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
