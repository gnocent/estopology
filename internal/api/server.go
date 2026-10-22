// Package api serves the REST API, the exports and the embedded web UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"estopo/internal/auth"
	"estopo/internal/buildinfo"
	"estopo/internal/clock"
	"estopo/internal/collector"
	"estopo/internal/ha"
	"estopo/internal/query"
	"estopo/internal/store"
)

// Collector is what the API needs from the collector.
type Collector interface {
	Status() []collector.Status
	CaptureNow(id string, force bool) error
	Resume(id, uuid string) error
}

// Options wires the server.
type Options struct {
	Log       *slog.Logger
	Clock     clock.Clock
	Store     *store.Store
	Engine    *query.Engine
	Collector Collector
	// OnConfigChange is called after the cluster configuration changed, so
	// that the collector restarts the affected workers.
	OnConfigChange func(ctx context.Context) error
	// Purge deletes the history of a cluster, spooled records included
	// (nil: the partitions only, Store.Purge).
	Purge func(ctx context.Context, cluster string) (int, error)
	// HA is the high-availability manager (nil: a primary without replication).
	HA HA
	// ExtraMetrics adds application metrics to /metrics (metric(name, type, help, value, label pairs...)).
	ExtraMetrics func(metric func(name, typ, help string, value float64, labels ...string))
	UI           http.Handler // embedded web UI (nil = none)
	// Auth authenticates the callers; nil means everybody is an
	// administrator (tests).
	Auth *auth.Service
	// Transport describes the listener, to enforce SPEC §10 (B17) when the
	// authentication mode changes.
	Transport Transport
	// SecretsDir, when set, is the only directory where the files named in
	// the administration (certificates, keys, passwords) may be
	// (ESTOPO_SECRETS_DIR).
	SecretsDir string
}

// Transport describes the HTTP listener.
type Transport struct {
	TLS, LoopbackOnly, AllowInsecureAuth bool
}

// Server holds the HTTP handlers' dependencies.
type Server struct {
	log          *slog.Logger
	clock        clock.Clock
	store        *store.Store
	engine       *query.Engine
	collector    Collector
	onConfig     func(ctx context.Context) error
	purge        func(ctx context.Context, cluster string) (int, error)
	ha           HA
	extraMetrics func(metric func(name, typ, help string, value float64, labels ...string))
	home         homeCache  // home screen summaries
	usersMu      sync.Mutex // the checks of the last admin and the writes they guard
	auth         *auth.Service
	transport    Transport
	secretsDir   string
	started      time.Time
	mux          *http.ServeMux
}

// New builds the HTTP handler tree.
func New(o Options) *Server {
	s := &Server{log: o.Log, clock: o.Clock, store: o.Store, engine: o.Engine, collector: o.Collector,
		onConfig: o.OnConfigChange, purge: o.Purge, ha: o.HA, extraMetrics: o.ExtraMetrics, started: o.Clock.Now(), mux: http.NewServeMux(),
		auth: o.Auth, transport: o.Transport, secretsDir: o.SecretsDir}
	m := s.mux
	m.HandleFunc("GET /healthz", s.handleHealthz)
	if o.HA != nil {
		m.Handle("GET /api/v1/replication/", o.HA.Handler())
	}
	if s.store != nil {
		m.HandleFunc("GET /metrics", s.handleMetrics)
		m.HandleFunc("GET /api/v1/meta", s.read(s.handleMeta))
		m.HandleFunc("GET /api/v1/clusters", s.read(s.handleClusters))
		m.HandleFunc("GET /api/v1/search", s.read(s.handleSearch))
		m.HandleFunc("GET /api/v1/clusters/{id}/state", s.read(s.handleState))
		m.HandleFunc("GET /api/v1/clusters/{id}/timeline", s.read(s.handleTimeline))
		m.HandleFunc("GET /api/v1/clusters/{id}/matrix", s.read(s.handleMatrix))
		m.HandleFunc("GET /api/v1/clusters/{id}/shards", s.read(s.handleShards))
		m.HandleFunc("GET /api/v1/clusters/{id}/indices", s.read(s.handleIndices))
		m.HandleFunc("GET /api/v1/clusters/{id}/families", s.read(s.handleFamilies))
		m.HandleFunc("GET /api/v1/clusters/{id}/families/{family}/history", s.read(s.handleFamilyHistory))
		m.HandleFunc("GET /api/v1/clusters/{id}/nodes/{name}", s.read(s.handleNode))
		m.HandleFunc("GET /api/v1/clusters/{id}/group", s.read(s.handleGroup))
		m.HandleFunc("GET /api/v1/clusters/{id}/group/history", s.read(s.handleGroupHistory))
		m.HandleFunc("GET /api/v1/clusters/{id}/indices/{uuid}/history", s.read(s.handleIndexHistory))
		m.HandleFunc("GET /api/v1/clusters/{id}/indices/{uuid}", s.read(s.handleIndex))
		m.HandleFunc("GET /api/v1/clusters/{id}/lives", s.read(s.handleLives))
		m.HandleFunc("GET /api/v1/clusters/{id}/diff", s.read(s.handleDiff))
		m.HandleFunc("GET /api/v1/clusters/{id}/path", s.read(s.handlePath))
		m.HandleFunc("GET /api/v1/clusters/{id}/losses", s.read(s.handleLosses))
		m.HandleFunc("GET /api/v1/clusters/{id}/events", s.read(s.handleEvents))
		m.HandleFunc("GET /api/v1/clusters/{id}/constraints", s.read(s.handleConstraints))
		m.HandleFunc("GET /api/v1/clusters/{id}/flows", s.read(s.handleFlows))
		m.HandleFunc("GET /api/v1/clusters/{id}/export/{what}", s.read(s.handleExport))

		m.HandleFunc("GET /api/v1/admin/clusters", s.admin(s.handleAdminClusters))
		m.HandleFunc("PUT /api/v1/admin/clusters/{id}", s.admin(s.handleAdminSaveCluster))
		m.HandleFunc("DELETE /api/v1/admin/clusters/{id}", s.admin(s.handleAdminRemoveCluster))
		m.HandleFunc("POST /api/v1/admin/clusters/{id}/test", s.admin(s.handleAdminTestCluster))
		m.HandleFunc("POST /api/v1/admin/clusters/{id}/capture", s.admin(s.handleAdminCapture))
		m.HandleFunc("POST /api/v1/admin/clusters/{id}/resume", s.admin(s.handleAdminResume))
		m.HandleFunc("POST /api/v1/admin/clusters/{id}/reattach", s.admin(s.handleAdminReattach))
		m.HandleFunc("POST /api/v1/admin/clusters/{id}/purge", s.admin(s.handleAdminPurge))
		m.HandleFunc("GET /api/v1/admin/collectors", s.admin(s.handleAdminCollectors))
		m.HandleFunc("GET /api/v1/admin/cache", s.admin(s.handleAdminCacheGet))
		m.HandleFunc("PUT /api/v1/admin/cache", s.admin(s.handleAdminCachePut))
		m.HandleFunc("GET /api/v1/admin/collector", s.admin(s.handleAdminCollectorGet))
		m.HandleFunc("PUT /api/v1/admin/collector", s.admin(s.handleAdminCollectorPut))

		m.HandleFunc("GET /api/v1/session", s.handleSession)
		m.HandleFunc("POST /api/v1/login", s.handleLogin)
		m.HandleFunc("POST /api/v1/logout", s.read(s.handleLogout))
		m.HandleFunc("PUT /api/v1/me/password", s.read(s.handleOwnPassword))
		m.HandleFunc("PUT /api/v1/me/prefs", s.read(s.handleOwnPrefs))
		m.HandleFunc("GET /api/v1/admin/auth", s.admin(s.handleAdminAuthGet))
		m.HandleFunc("PUT /api/v1/admin/auth", s.admin(s.handleAdminAuthPut))
		m.HandleFunc("POST /api/v1/admin/auth/ldap-test", s.admin(s.handleAdminLDAPTest))
		m.HandleFunc("GET /api/v1/admin/ha", s.admin(s.handleAdminHA))
		m.HandleFunc("GET /api/v1/admin/tls", s.admin(s.handleAdminTLSGet))
		m.HandleFunc("PUT /api/v1/admin/tls", s.admin(s.handleAdminTLSPut))
		m.HandleFunc("GET /api/v1/admin/metrics", s.admin(s.handleAdminMetricsGet))
		m.HandleFunc("PUT /api/v1/admin/metrics", s.admin(s.handleAdminMetricsPut))
		m.HandleFunc("POST /api/v1/admin/ha/{action}", s.admin(s.handleAdminHAAction))
		m.HandleFunc("GET /api/v1/admin/users", s.admin(s.handleAdminUsers))
		m.HandleFunc("PUT /api/v1/admin/users/{name}", s.admin(s.handleAdminSaveUser))
		m.HandleFunc("DELETE /api/v1/admin/users/{name}", s.admin(s.handleAdminDeleteUser))
		m.HandleFunc("GET /api/v1/admin/retention", s.admin(s.handleAdminRetentionGet))
		m.HandleFunc("PUT /api/v1/admin/retention", s.admin(s.handleAdminRetentionPut))
		m.HandleFunc("GET /api/v1/admin/storage", s.admin(s.handleAdminStorage))
		m.HandleFunc("GET /api/v1/admin/settings-whitelist", s.admin(s.handleAdminWhitelistGet))
		m.HandleFunc("PUT /api/v1/admin/settings-whitelist", s.admin(s.handleAdminWhitelistPut))
		m.HandleFunc("GET /api/v1/admin/clusters/{id}/acks", s.admin(s.handleAdminAcks))
		m.HandleFunc("POST /api/v1/admin/clusters/{id}/acks", s.admin(s.handleAdminAddAck))
		m.HandleFunc("DELETE /api/v1/admin/clusters/{id}/acks/{ack}", s.admin(s.handleAdminRevokeAck))
		m.HandleFunc("GET /api/v1/admin/clusters/{id}/family-rules", s.admin(s.handleAdminFamilyRulesGet))
		m.HandleFunc("PUT /api/v1/admin/clusters/{id}/family-rules", s.admin(s.handleAdminFamilyRulesPut))
		m.HandleFunc("POST /api/v1/admin/clusters/{id}/family-preview", s.admin(s.handleAdminFamilyPreview))
		m.HandleFunc("GET /api/v1/admin/audit", s.admin(s.handleAdminAudit))
		m.HandleFunc("GET /api/v1/admin/alerts", s.admin(s.handleAdminAlerts))
		m.HandleFunc("GET /api/v1/admin/system", s.admin(s.handleAdminSystem))
	}
	if o.UI != nil {
		m.Handle("GET /", o.UI)
	}
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if s.transport.TLS || r.TLS != nil {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	}
	s.mux.ServeHTTP(w, r)
}

type userKey struct{}

// user returns the authenticated caller of a wrapped handler.
func (s *Server) user(r *http.Request) auth.User {
	if u, ok := r.Context().Value(userKey{}).(auth.User); ok {
		return u
	}
	return auth.Anonymous()
}

// RequestedWith is the value of the X-Requested-With header that the UI
// sends with every state-changing request.
const RequestedWith = "estopo"

// crossSite tells why a state-changing request is refused as possibly
// forged by a page of another site ("" when it is not). It holds in every
// authentication mode, mode none included, where there is no CSRF token: the
// browser must not announce another site (Sec-Fetch-Site, Origin), and the
// request must be one that a cross-site form or a simple fetch cannot
// produce: a JSON body, or the X-Requested-With header when it has no body.
func crossSite(r *http.Request) string {
	if v := r.Header.Get("Sec-Fetch-Site"); v != "" && v != "same-origin" && v != "none" {
		return "cross-site request refused"
	}
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			return "cross-site request refused: the origin is not this host"
		}
	}
	if r.ContentLength != 0 {
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			return "Content-Type: application/json is required"
		}
	} else if r.Header.Get("X-Requested-With") != RequestedWith {
		return "the header X-Requested-With: " + RequestedWith + " is required"
	}
	return ""
}

// authUnavailable answers a request whose caller could not be determined
// (unreadable authentication settings or sessions, request given up): it is
// refused, never served as if authentication were disabled.
func (s *Server) authUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() != nil {
		s.log.Debug("authentication unavailable", "path", r.URL.Path, "err", err)
	} else {
		s.log.Error("authentication unavailable", "path", r.URL.Path, "err", err)
	}
	writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "authentication is unavailable, retry later")
}

// guard authenticates the caller, checks its role and, for state-changing
// requests, that they come from the UI (or a deliberate client) and carry
// the CSRF token (SPEC §10).
func (s *Server) guard(role string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if why := crossSite(r); why != "" {
				writeError(w, http.StatusForbidden, "cross_site", why)
				return
			}
		}
		u := auth.Anonymous()
		if s.auth != nil {
			var err error
			if u, err = s.auth.Resolve(r.Context(), r); errors.Is(err, auth.ErrUnauthenticated) {
				writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required")
				return
			} else if err != nil {
				s.authUnavailable(w, r, err)
				return
			}
		}
		if role == auth.RoleAdmin && u.Role != auth.RoleAdmin {
			writeError(w, http.StatusForbidden, "forbidden", "admin role required")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !auth.CheckCSRF(u, r) {
			writeError(w, http.StatusForbidden, "csrf", "missing or invalid CSRF token")
			return
		}
		// A standby (or fenced old primary) is read-only: its configuration
		// comes from the primary and it never contacts the clusters.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && s.role() != ha.RolePrimary &&
			!strings.HasPrefix(r.URL.Path, "/api/v1/admin/ha/") && r.URL.Path != "/api/v1/logout" {
			writeError(w, http.StatusConflict, "read_only_instance", "this instance is "+s.role()+": changes are made on the primary")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	}
}

// read wraps a read endpoint (readers and admins).
func (s *Server) read(h http.HandlerFunc) http.HandlerFunc { return s.guard(auth.RoleReader, h) }

// admin wraps an admin endpoint.
func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc { return s.guard(auth.RoleAdmin, h) }

type healthz struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	Role      string `json:"role"`
	Uptime    string `json:"uptime"`
	Clusters  int    `json:"clusters,omitempty"`
	Suspended int    `json:"suspended,omitempty"`
	Stale     int    `json:"stale,omitempty"` // no probe for 3 probe periods
}

// handleHealthz reports liveness and the collection state (SPEC §12.3).
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	h := healthz{Status: "ok", Version: buildinfo.Version, Role: s.role(),
		Uptime: s.clock.Now().Sub(s.started).Truncate(time.Second).String()}
	if s.collector != nil {
		now := s.clock.Now().UnixMilli()
		for _, st := range s.collector.Status() {
			h.Clusters++
			if st.Suspended {
				h.Suspended++
			}
			if st.LastProbe == nil || now-st.LastProbe.StartMs > (15*time.Minute).Milliseconds() {
				h.Stale++
			}
		}
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) clusterName(ctx context.Context, id string) string {
	if c, err := s.store.Cluster(ctx, id); err == nil {
		return c.Name
	}
	return id
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body: " + err.Error())
	}
	return nil
}

// HA is what the API needs from the high-availability manager.
type HA interface {
	Role() string
	Status() ha.Status
	Promote(ctx context.Context, actor string) error
	Demote(ctx context.Context, actor, primaryURL string) error
	Rebuild(ctx context.Context, actor string) error
	Resume(ctx context.Context, actor string) error
	SetPeer(ctx context.Context, actor, peer string) error
	Handler() http.Handler // replication API
}

func (s *Server) role() string {
	if s.ha == nil {
		return ha.RolePrimary
	}
	return s.ha.Role()
}
