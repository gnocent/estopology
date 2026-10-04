package sim

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"estopo/internal/clock"
	"estopo/internal/testpki"
)

// Auth configures how a simulated cluster authenticates requests. With
// Disabled, every request is accepted (security off).
type Auth struct {
	Disabled bool
	Users    map[string]string // basic auth user -> password
	APIKeys  map[string]bool   // accepted "ApiKey <encoded>" values (encoded part)
	// A client certificate verified against the server's client CAs is
	// always accepted (PKI realm).
}

// Server exposes one simulated cluster over HTTP(S).
type Server struct {
	Cluster *Cluster
	Clock   clock.Clock
	Auth    Auth

	faultMu sync.Mutex
	faults  map[string]Fault
}

// Fault is an injected failure: a delay (real time, honouring the request
// context) and/or an HTTP error status.
type Fault struct {
	Delay  time.Duration
	Status int
}

// SetFault injects f on requests whose path starts with prefix ("/" = all).
func (s *Server) SetFault(prefix string, f Fault) {
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	if s.faults == nil {
		s.faults = map[string]Fault{}
	}
	s.faults[prefix] = f
}

// RemoveFault removes the fault injected on prefix.
func (s *Server) RemoveFault(prefix string) {
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	delete(s.faults, prefix)
}

// ClearFaults removes every injected fault.
func (s *Server) ClearFaults() {
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	s.faults = nil
}

func (s *Server) fault(path string) (Fault, bool) {
	s.faultMu.Lock()
	defer s.faultMu.Unlock()
	var best string
	found := false
	for p := range s.faults {
		if strings.HasPrefix(path, p) && len(p) >= len(best) {
			best, found = p, true
		}
	}
	return s.faults[best], found
}

// ServeHTTP routes the read APIs estopo uses.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/_essim/") {
		s.control(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		esError(w, http.StatusMethodNotAllowed, "illegal_argument_exception", "essim only serves read APIs")
		return
	}
	if !s.authenticated(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="security" charset="UTF-8"`)
		esError(w, http.StatusUnauthorized, "security_exception", "missing authentication credentials for REST request ["+r.URL.RequestURI()+"]")
		return
	}
	if f, ok := s.fault(r.URL.Path); ok {
		if f.Delay > 0 {
			select {
			case <-time.After(f.Delay):
			case <-r.Context().Done():
				return
			}
		}
		if f.Status >= 400 {
			typ := "illegal_state_exception"
			if f.Status == http.StatusServiceUnavailable {
				typ = "master_not_discovered_exception"
			}
			esError(w, f.Status, typ, "injected fault")
			return
		}
	}
	c := s.Cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.Clock != nil {
		c.advanceTo(s.Clock.Now().UnixMilli())
	}

	p := r.URL.Path
	q := r.URL.Query()
	if p != "/" && !c.hasMaster() {
		esError(w, http.StatusServiceUnavailable, "master_not_discovered_exception", "")
		return
	}
	var cols []string
	if h := q.Get("h"); h != "" {
		cols = strings.Split(h, ",")
	}
	// Render into a buffer so that a rendering error can still become a 400.
	var buf bytes.Buffer
	var err error
	switch {
	case p == "/":
		c.renderRoot(&buf)
	case p == "/_cluster/health":
		c.renderHealth(&buf)
	case strings.HasPrefix(p, "/_nodes/stats"):
		c.renderNodesStats(&buf)
	case p == "/_nodes" || strings.HasPrefix(p, "/_nodes/"):
		c.renderNodes(&buf)
	case p == "/_cat/allocation":
		err = c.renderAllocation(&buf, cols)
	case p == "/_cluster/settings":
		c.renderClusterSettings(&buf, q.Get("include_defaults") == "true")
	case p == "/_remote/info":
		c.renderRemoteInfo(&buf)
	case p == "/_cat/indices":
		err = c.renderCatIndices(&buf, cols)
	case p == "/_cat/shards":
		err = c.renderCatShards(&buf, cols)
	case strings.HasPrefix(p, "/_all/_settings"):
		c.renderIndexSettings(&buf, patternsFromPath(p))
	case p == "/_security/user/_has_privileges":
		io.Copy(io.Discard, r.Body)
		buf.WriteString(`{"username":"estopo","has_all_requested":true,"cluster":{"monitor":true},` +
			`"index":{"*":{"monitor":true,"view_index_metadata":true}},"application":{}}`)
	default:
		esError(w, http.StatusBadRequest, "illegal_argument_exception", "no handler found for uri ["+r.URL.RequestURI()+"] and method [GET]")
		return
	}
	if err != nil {
		esError(w, http.StatusBadRequest, "illegal_argument_exception", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-elastic-product", "Elasticsearch")
	if r.Method == http.MethodGet {
		w.Write(buf.Bytes())
	}
}

func (s *Server) authenticated(r *http.Request) bool {
	if s.Auth.Disabled {
		return true
	}
	if r.TLS != nil && len(r.TLS.VerifiedChains) > 0 {
		return true
	}
	h := r.Header.Get("Authorization")
	switch {
	case strings.HasPrefix(h, "Basic "):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "Basic "))
		if err != nil {
			return false
		}
		user, pw, ok := strings.Cut(string(raw), ":")
		return ok && s.Auth.Users[user] == pw && pw != ""
	case strings.HasPrefix(h, "ApiKey "):
		return s.Auth.APIKeys[strings.TrimPrefix(h, "ApiKey ")]
	}
	return false
}

func esError(w http.ResponseWriter, status int, typ, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	r := "null"
	if reason != "" {
		r = fmt.Sprintf("%q", reason)
	}
	fmt.Fprintf(w, `{"error":{"root_cause":[{"type":%q,"reason":%s}],"type":%q,"reason":%s},"status":%d}`, typ, r, typ, r, status)
}

// ServerTLS returns the TLS configuration of a simulated cluster: server
// certificate from the test PKI, client certificates verified if presented.
func ServerTLS(p *testpki.PKI) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(p.ServerCert, p.ServerKey)
	if err != nil {
		return nil, err
	}
	chain, err := readFile(p.CAChain)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(chain) {
		return nil, fmt.Errorf("no CA in %s", p.CAChain)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.VerifyClientCertIfGiven,
	}, nil
}
