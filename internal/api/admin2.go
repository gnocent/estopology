package api

import (
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"estopo/internal/auth"
	"estopo/internal/buildinfo"
	"estopo/internal/derive"
	"estopo/internal/esclient"
	"estopo/internal/ha"
	"estopo/internal/store"
)

// Administration (SPEC §9): retention and storage, settings whitelist,
// node acknowledgements, family rules, audit, alerts, system.

func (s *Server) handleAdminRetentionGet(w http.ResponseWriter, r *http.Request) {
	pol, err := s.store.RetentionPolicy(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pol)
}

func (s *Server) handleAdminRetentionPut(w http.ResponseWriter, r *http.Request) {
	var pol store.Retention
	if err := decodeJSON(r, &pol); err != nil {
		s.fail(w, r, err)
		return
	}
	if pol.MaxAgeDays < 7 || pol.MaxAgeDays > 3650 || pol.MaxBytes < 0 {
		s.fail(w, r, badRequest("max_age_days must be between 7 and 3650, max_bytes >= 0"))
		return
	}
	if err := s.store.SetSetting(r.Context(), "retention", pol); err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(r.Context(), s.user(r).Name, "retention.save", "", strconv.Itoa(pol.MaxAgeDays)+"d "+strconv.FormatInt(pol.MaxBytes, 10)+"B")
	writeJSON(w, http.StatusOK, pol)
}

// ClusterStorage is the storage of one cluster (SPEC §9: bytes per day and
// projections).
type ClusterStorage struct {
	Cluster      string  `json:"cluster"`
	Partitions   int     `json:"partitions"`
	Bytes        int64   `json:"bytes"`
	Days         float64 `json:"days"`
	BytesPerDay  int64   `json:"bytes_per_day"`
	Projection30 int64   `json:"projection_30d"`
	Projection1Y int64   `json:"projection_1y"`
}

// Storage is the storage page.
type Storage struct {
	Clusters   []ClusterStorage      `json:"clusters"`
	Partitions []store.PartitionInfo `json:"partitions"`
	MetaBytes  int64                 `json:"meta_bytes"`
	Total      int64                 `json:"total_bytes"`
	Retention  store.Retention       `json:"retention"`
	Alerts     []store.Alert         `json:"alerts"`
}

func (s *Server) handleAdminStorage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	parts, err := s.store.Partitions(ctx, "")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	now := s.clock.Now().UnixMilli()
	by := map[string]*ClusterStorage{}
	out := Storage{Partitions: parts, MetaBytes: s.store.MetaSize()}
	for _, p := range parts {
		c := by[p.Cluster]
		if c == nil {
			c = &ClusterStorage{Cluster: p.Cluster}
			by[p.Cluster] = c
		}
		c.Partitions++
		c.Bytes += p.SizeBytes
		c.Days += float64(max(0, min(p.ToMs, now)-p.FromMs)) / float64(24*time.Hour/time.Millisecond)
		out.Total += p.SizeBytes
	}
	for _, c := range by {
		if c.Days > 0.5 {
			c.BytesPerDay = int64(float64(c.Bytes) / c.Days)
			c.Projection30, c.Projection1Y = c.BytesPerDay*30, c.BytesPerDay*365
		}
		out.Clusters = append(out.Clusters, *c)
	}
	sort.Slice(out.Clusters, func(i, j int) bool { return out.Clusters[i].Cluster < out.Clusters[j].Cluster })
	out.Total += out.MetaBytes
	out.Retention, _ = s.store.RetentionPolicy(ctx)
	out.Alerts, _ = s.store.Alerts(ctx)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAdminWhitelistGet(w http.ResponseWriter, r *http.Request) {
	var wl []string
	if ok, err := s.store.GetSetting(r.Context(), "settings_whitelist", &wl); err != nil {
		s.fail(w, r, err)
		return
	} else if !ok || len(wl) == 0 {
		wl = esclient.DefaultSettingsWhitelist
	}
	writeJSON(w, http.StatusOK, wl)
}

// settingName is what a whitelist entry may be made of: the entries end up
// in the path of a request to Elasticsearch.
var settingName = regexp.MustCompile(`^[A-Za-z0-9._*-]+$`)

func (s *Server) handleAdminWhitelistPut(w http.ResponseWriter, r *http.Request) {
	var wl []string
	if err := decodeJSON(r, &wl); err != nil {
		s.fail(w, r, err)
		return
	}
	for _, k := range wl {
		if len(k) > 200 || !settingName.MatchString(k) {
			s.fail(w, r, badRequest("invalid setting name "+strconv.Quote(k)+" (letters, digits, '.', '_', '-' and '*')"))
			return
		}
	}
	if err := s.store.SetSetting(r.Context(), "settings_whitelist", wl); err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(r.Context(), s.user(r).Name, "whitelist.save", "", "")
	s.configChanged(r.Context())
	writeJSON(w, http.StatusOK, wl)
}

func (s *Server) handleAdminAcks(w http.ResponseWriter, r *http.Request) {
	acks, err := s.store.Acks(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if acks == nil {
		acks = []store.Ack{}
	}
	writeJSON(w, http.StatusOK, acks)
}

func (s *Server) handleAdminAddAck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var a store.Ack
	if err := decodeJSON(r, &a); err != nil {
		s.fail(w, r, err)
		return
	}
	a.Cluster, a.Author = r.PathValue("id"), s.user(r).Name
	if a.Node == "" {
		s.fail(w, r, badRequest("node is required"))
		return
	}
	if a.FromMs == 0 {
		a.FromMs = s.clock.Now().UnixMilli()
	}
	id, err := s.store.AddAck(ctx, a)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(ctx, a.Author, "node.ack", a.Cluster+"/"+a.Node, a.Comment)
	s.engine.Invalidate()
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleAdminRevokeAck(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("ack"), 10, 64)
	if err != nil {
		s.fail(w, r, badRequest("invalid id"))
		return
	}
	if err := s.store.RevokeAck(ctx, id, s.user(r).Name); err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(ctx, s.user(r).Name, "node.ack.revoke", r.PathValue("id"), strconv.FormatInt(id, 10))
	s.engine.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminFamilyRulesGet(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.FamilyRules(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if rules == nil {
		rules = []string{}
	}
	writeJSON(w, http.StatusOK, rules)
}

func compileRules(patterns []string) ([]derive.FamilyRule, error) {
	var out []derive.FamilyRule
	for _, p := range patterns {
		fr, err := derive.NewFamilyRule(p)
		if err != nil {
			return nil, badRequest("invalid regex " + p + ": " + err.Error())
		}
		out = append(out, fr)
	}
	return out, nil
}

func (s *Server) handleAdminFamilyRulesPut(w http.ResponseWriter, r *http.Request) {
	var rules []string
	if err := decodeJSON(r, &rules); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := compileRules(rules); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.SetFamilyRules(r.Context(), r.PathValue("id"), rules); err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(r.Context(), s.user(r).Name, "family_rules.save", r.PathValue("id"), "")
	s.configChanged(r.Context())
	writeJSON(w, http.StatusOK, rules)
}

// FamilyPreview is one family obtained with candidate rules.
type FamilyPreview struct {
	Family   string   `json:"family"`
	Indices  int      `json:"indices"`
	Examples []string `json:"examples"`
}

// handleAdminFamilyPreview groups the current indices with candidate rules
// (SPEC §7.3: preview of the grouping in the admin).
func (s *Server) handleAdminFamilyPreview(w http.ResponseWriter, r *http.Request) {
	var rules []string
	if err := decodeJSON(r, &rules); err != nil {
		s.fail(w, r, err)
		return
	}
	compiled, err := compileRules(rules)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	st, err := s.engine.StateAt(r.Context(), r.PathValue("id"), s.clock.Now().UnixMilli())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	by := map[string]*FamilyPreview{}
	for _, ix := range st.Indices {
		f := derive.Family(ix.Name, compiled)
		p := by[f]
		if p == nil {
			p = &FamilyPreview{Family: f}
			by[f] = p
		}
		p.Indices++
		if len(p.Examples) < 3 {
			p.Examples = append(p.Examples, ix.Name)
		}
	}
	out := make([]FamilyPreview, 0, len(by))
	for _, p := range by {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Indices != out[j].Indices {
			return out[i].Indices > out[j].Indices
		}
		return out[i].Family < out[j].Family
	})
	writePageOf(s, w, r, out)
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r, "limit", 200, 1, 5000)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	log, err := s.store.AuditLog(r.Context(), limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if log == nil {
		log = []store.AuditEntry{}
	}
	writeJSON(w, http.StatusOK, log)
}

func (s *Server) handleAdminAlerts(w http.ResponseWriter, r *http.Request) {
	alerts, err := s.store.Alerts(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if alerts == nil {
		alerts = []store.Alert{}
	}
	writeJSON(w, http.StatusOK, alerts)
}

// SystemInfo is the "system" admin page.
type SystemInfo struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	GoVersion  string `json:"go_version"`
	Role       string `json:"role"`
	UptimeSec  int64  `json:"uptime_s"`
	HeapBytes  uint64 `json:"heap_bytes"`
	SysBytes   uint64 `json:"sys_bytes"`
	Goroutines int    `json:"goroutines"`
	DataDir    string `json:"data_dir"`
	AuthMode   string `json:"auth_mode"`
}

func (s *Server) handleAdminSystem(w http.ResponseWriter, r *http.Request) {
	mode, err := s.authMode(r)
	if err != nil {
		s.authUnavailable(w, r, err)
		return
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	writeJSON(w, http.StatusOK, SystemInfo{Version: buildinfo.Version, Commit: buildinfo.Commit, GoVersion: runtime.Version(),
		Role: s.role(), UptimeSec: int64(s.clock.Now().Sub(s.started) / time.Second), HeapBytes: m.HeapAlloc, SysBytes: m.Sys,
		Goroutines: runtime.NumGoroutine(), DataDir: s.store.Dir(), AuthMode: mode})
}

// --- high availability (SPEC §11) --------------------------------------------------

func (s *Server) handleAdminHA(w http.ResponseWriter, r *http.Request) {
	if s.ha == nil {
		writeJSON(w, http.StatusOK, ha.Status{State: ha.State{Role: ha.RolePrimary}})
		return
	}
	writeJSON(w, http.StatusOK, s.ha.Status())
}

// handleAdminHAAction runs promote, demote, rebuild, resume or peer.
func (s *Server) handleAdminHAAction(w http.ResponseWriter, r *http.Request) {
	if s.ha == nil {
		writeError(w, http.StatusConflict, "no_ha", "high availability is not configured")
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	ctx, actor := r.Context(), s.user(r).Name
	var err error
	switch r.PathValue("action") {
	case "promote":
		err = s.ha.Promote(ctx, actor)
	case "demote":
		err = s.ha.Demote(ctx, actor, body.URL)
	case "rebuild":
		err = s.ha.Rebuild(ctx, actor)
	case "resume":
		err = s.ha.Resume(ctx, actor)
	case "peer":
		err = s.ha.SetPeer(ctx, actor, body.URL)
	default:
		writeError(w, http.StatusNotFound, "not_found", "unknown action")
		return
	}
	if err != nil {
		writeError(w, http.StatusConflict, "ha", err.Error())
		return
	}
	s.engine.Invalidate()
	writeJSON(w, http.StatusOK, s.ha.Status())
}

// --- global TLS settings (SPEC §5.6) -----------------------------------------------

func (s *Server) handleAdminTLSGet(w http.ResponseWriter, r *http.Request) {
	var d store.TLSDefaults
	s.store.GetSetting(r.Context(), "tls", &d)
	writeJSON(w, http.StatusOK, d)
}

// handleAdminTLSPut saves the default CA path after checking that it holds
// certificates.
func (s *Server) handleAdminTLSPut(w http.ResponseWriter, r *http.Request) {
	var d store.TLSDefaults
	if err := decodeJSON(r, &d); err != nil {
		s.fail(w, r, err)
		return
	}
	d.CAPath = strings.TrimSpace(d.CAPath)
	out := struct {
		store.TLSDefaults
		Certificates int `json:"certificates"`
	}{TLSDefaults: d}
	if err := auth.InSecretsDir(s.secretsDir, d.CAPath); err != nil {
		s.fail(w, r, badRequest(err.Error()))
		return
	}
	if d.CAPath != "" {
		n, err := esclient.CheckCAPath(d.CAPath)
		if err != nil {
			s.fail(w, r, badRequest(err.Error()))
			return
		}
		out.Certificates = n
	}
	if err := s.store.SetSetting(r.Context(), "tls", d); err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(r.Context(), s.user(r).Name, "tls.save", "", d.CAPath)
	if s.onConfig != nil {
		if err := s.onConfig(r.Context()); err != nil {
			s.log.Error("apply TLS settings", "err", err)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// --- global collection settings ------------------------------------------------------

func (s *Server) handleAdminCollectorGet(w http.ResponseWriter, r *http.Request) {
	cs := store.CollectorSettings{Parallelism: 3}
	s.store.GetSetting(r.Context(), "collector", &cs)
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) handleAdminCollectorPut(w http.ResponseWriter, r *http.Request) {
	var cs store.CollectorSettings
	if err := decodeJSON(r, &cs); err != nil {
		s.fail(w, r, err)
		return
	}
	if cs.Parallelism < 1 || cs.Parallelism > 32 {
		s.fail(w, r, badRequest("the parallelism must be between 1 and 32"))
		return
	}
	if err := s.store.SetSetting(r.Context(), "collector", cs); err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(r.Context(), s.user(r).Name, "collector.save", "", strconv.Itoa(cs.Parallelism))
	writeJSON(w, http.StatusOK, cs)
}

// --- query cache --------------------------------------------------------------------

func (s *Server) handleAdminCacheGet(w http.ResponseWriter, r *http.Request) {
	c := store.CacheSettings{Structures: store.DefaultCacheStructures}
	s.store.GetSetting(r.Context(), "cache", &c)
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleAdminCachePut(w http.ResponseWriter, r *http.Request) {
	var c store.CacheSettings
	if err := decodeJSON(r, &c); err != nil {
		s.fail(w, r, err)
		return
	}
	if c.Structures < 2 || c.Structures > 4096 {
		s.fail(w, r, badRequest("the number of cached states must be between 2 and 4096"))
		return
	}
	if err := s.store.SetSetting(r.Context(), "cache", c); err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(r.Context(), s.user(r).Name, "cache.save", "", strconv.Itoa(c.Structures))
	writeJSON(w, http.StatusOK, c)
}
