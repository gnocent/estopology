// Package esclient is the read-only HTTP client used to query Elasticsearch
// 7.17 to 9.x (SPEC §5). It only speaks the stable _cat/_cluster/_nodes APIs,
// decodes large responses as streams and records per-call statistics.
package esclient

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"estopo/internal/buildinfo"
)

// AuthKind selects how estopo authenticates to a cluster.
type AuthKind string

const (
	AuthNone   AuthKind = "none"
	AuthCert   AuthKind = "cert" // client certificate (mTLS), the default in SPEC §5.6
	AuthAPIKey AuthKind = "apikey"
	AuthBasic  AuthKind = "basic"
)

// Config describes one cluster connection. Secrets are never stored in the
// configuration itself: only paths to files readable by estopo.
type Config struct {
	URLs              []string // tried in order; the last one that worked is tried first
	Auth              AuthKind
	CertFile          string // AuthCert: certificate, full chain accepted
	KeyFile           string // AuthCert
	KeyPassphraseFile string // AuthCert, optional
	APIKeyFile        string // AuthAPIKey: encoded key ("id:key" base64) or raw "id:key"
	Username          string // AuthBasic
	PasswordFile      string // AuthBasic
	CAPath            string // CA file or directory; empty = system pool only
	UseSystemCAs      bool   // add the system pool to CAPath
	Timeout           time.Duration
	ConnectTimeout    time.Duration
}

// Default timeouts.
const (
	DefaultTimeout        = 60 * time.Second
	DefaultConnectTimeout = 10 * time.Second
)

// MaxResponseBytes bounds the decoded body of one response. The largest
// one, _cat/shards of 250k shards, is about 60 MB: far below, but a cluster
// (or whatever answers at its URL) must not be able to fill the memory.
const MaxResponseBytes = 2 << 30

// ErrResponseTooLarge is returned when a response exceeds MaxResponseBytes.
var ErrResponseTooLarge = errors.New("esclient: response larger than the limit")

// ErrWriteForbidden is returned for any method other than GET or HEAD: the
// tool must never modify a cluster (SPEC §5.6, B16).
var ErrWriteForbidden = errors.New("esclient: only GET and HEAD requests are allowed")

// Client queries one cluster.
type Client struct {
	urls    []string
	hc      *http.Client
	auth    string // Authorization header value, if any
	cfgTO   time.Duration
	maxBody int64 // bound on the decoded body of one response
	mu      sync.Mutex
	active  int // index of the URL that answered last
}

// New validates the configuration, loads certificates and secrets from disk
// and returns a ready client.
func New(cfg Config) (*Client, error) {
	if len(cfg.URLs) == 0 {
		return nil, errors.New("no URL configured")
	}
	urls := make([]string, len(cfg.URLs))
	for i, u := range cfg.URLs {
		u = strings.TrimRight(strings.TrimSpace(u), "/")
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return nil, fmt.Errorf("URL %q: scheme must be http or https", u)
		}
		urls[i] = u
	}
	if cfg.Auth == "" {
		cfg.Auth = AuthCert
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = DefaultConnectTimeout
	}

	c := &Client{urls: urls, cfgTO: cfg.Timeout, maxBody: MaxResponseBytes}
	switch cfg.Auth {
	case AuthNone, AuthCert:
	case AuthAPIKey:
		key, err := readSecret(cfg.APIKeyFile, "API key")
		if err != nil {
			return nil, err
		}
		if strings.Contains(key, ":") { // raw "id:key"
			key = base64.StdEncoding.EncodeToString([]byte(key))
		}
		c.auth = "ApiKey " + key
	case AuthBasic:
		if cfg.Username == "" {
			return nil, errors.New("basic auth: username is empty")
		}
		pw, err := readSecret(cfg.PasswordFile, "password")
		if err != nil {
			return nil, err
		}
		c.auth = "Basic " + base64.StdEncoding.EncodeToString([]byte(cfg.Username+":"+pw))
	default:
		return nil, fmt.Errorf("unknown auth type %q", cfg.Auth)
	}

	if c.auth != "" {
		// A password or an API key must not travel in clear (SPEC §5.6).
		for _, u := range urls {
			if !strings.HasPrefix(u, "http://") {
				continue
			}
			pu, err := url.Parse(u)
			if err != nil {
				return nil, fmt.Errorf("URL %q: %w", u, err)
			}
			if ip := net.ParseIP(pu.Hostname()); pu.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback()) {
				return nil, fmt.Errorf("URL %q: %s authentication over plain http is refused (the secret would travel in clear), use https", u, cfg.Auth)
			}
		}
	}

	tc, err := buildTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	dialer := &net.Dialer{Timeout: cfg.ConnectTimeout, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:               nil, // clusters are reached directly
		DialContext:         dialer.DialContext,
		TLSClientConfig:     tc,
		TLSHandshakeTimeout: cfg.ConnectTimeout,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true, // gzip is requested and decoded explicitly to measure wire size
		ForceAttemptHTTP2:   false,
	}
	c.hc = &http.Client{
		Transport: readOnlyTransport{tr},
		// Redirects would be surprising from ES; never follow them.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c, nil
}

func readSecret(path, what string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%s file is not configured", what)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s file: %w", what, err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("%s file %s is empty", what, path)
	}
	return s, nil
}

// readOnlyTransport refuses every request that is not a GET or a HEAD, as a
// second line of defence behind Client.Get.
type readOnlyTransport struct{ next http.RoundTripper }

func (t readOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil, ErrWriteForbidden
	}
	return t.next.RoundTrip(r)
}

// CallStat records the cost of one API call (shown in the admin, SPEC §5.2).
type CallStat struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	URL        string `json:"url"` // base URL that answered
	Status     int    `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	WireBytes  int64  `json:"wire_bytes"` // as received (compressed if gzip)
	Bytes      int64  `json:"bytes"`      // decoded body bytes
	Gzip       bool   `json:"gzip"`
	Error      string `json:"error,omitempty"`
}

// HTTPError is a non-2xx answer from Elasticsearch.
type HTTPError struct {
	Status int
	Type   string // ES error type, e.g. master_not_discovered_exception
	Reason string
}

func (e *HTTPError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("HTTP %d: %s: %s", e.Status, e.Type, e.Reason)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Reason)
}

// Get performs GET path (optionally with a JSON body, as ES accepts for
// read APIs) and hands the decoded body stream to decode. URLs are tried in
// turn on connection errors only.
func (c *Client) Get(ctx context.Context, name, path string, body []byte, decode func(io.Reader) error) (CallStat, error) {
	return c.do(ctx, http.MethodGet, name, path, body, decode)
}

// do is the single entry point to the network.
func (c *Client) do(ctx context.Context, method, name, path string, body []byte, decode func(io.Reader) error) (st CallStat, err error) {
	st = CallStat{Name: name, Path: path}
	start := time.Now()
	defer func() {
		st.DurationMs = time.Since(start).Milliseconds()
		if err != nil {
			st.Error = err.Error()
		}
	}()
	if method != http.MethodGet && method != http.MethodHead {
		return st, ErrWriteForbidden
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfgTO)
	defer cancel()

	c.mu.Lock()
	first := c.active
	c.mu.Unlock()

	var resp *http.Response
	for i := range c.urls {
		idx := (first + i) % len(c.urls)
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, rerr := http.NewRequestWithContext(ctx, method, c.urls[idx]+path, rd)
		if rerr != nil {
			return st, rerr
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("User-Agent", "estopo/"+buildinfo.Version)
		req.Header.Set("X-Opaque-Id", "estopo")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		resp, err = c.hc.Do(req)
		if err == nil {
			st.URL = c.urls[idx]
			c.mu.Lock()
			c.active = idx
			c.mu.Unlock()
			break
		}
		if ctx.Err() != nil {
			return st, err
		}
	}
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	st.Status = resp.StatusCode

	wire := &countingReader{r: resp.Body}
	var r io.Reader = wire
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		st.Gzip = true
		gz, gerr := gzip.NewReader(wire)
		if gerr != nil {
			return st, fmt.Errorf("gzip: %w", gerr)
		}
		defer gz.Close()
		r = gz
	}
	// The limit is on the decoded stream: a small gzip answer can expand
	// without end.
	decoded := &countingReader{r: r, max: c.maxBody}
	defer func() {
		st.WireBytes = wire.n
		st.Bytes = decoded.n
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return st, readHTTPError(resp.StatusCode, decoded)
	}
	if decode == nil || method == http.MethodHead {
		_, err = io.Copy(io.Discard, decoded)
		return st, err
	}
	if err = decode(decoded); err != nil {
		return st, fmt.Errorf("decode %s: %w", name, err)
	}
	_, _ = io.Copy(io.Discard, decoded) // drain for connection reuse and exact sizes
	return st, nil
}

func readHTTPError(status int, r io.Reader) error {
	raw, _ := io.ReadAll(io.LimitReader(r, 64<<10))
	var e struct {
		Error json.RawMessage `json:"error"`
	}
	he := &HTTPError{Status: status}
	if json.Unmarshal(raw, &e) == nil && len(e.Error) > 0 {
		var obj struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal(e.Error, &obj) == nil && obj.Type != "" {
			he.Type, he.Reason = obj.Type, obj.Reason
			return he
		}
		var s string
		if json.Unmarshal(e.Error, &s) == nil {
			he.Reason = s
			return he
		}
	}
	he.Reason = strings.TrimSpace(string(raw))
	if len(he.Reason) > 500 {
		he.Reason = he.Reason[:500] + "…"
	}
	return he
}

// countingReader counts the bytes read and, when max is set, fails once
// more than max were read.
type countingReader struct {
	r   io.Reader
	n   int64
	max int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.max > 0 && c.n > c.max {
		return n, fmt.Errorf("%w (%d bytes)", ErrResponseTooLarge, c.max)
	}
	return n, err
}
