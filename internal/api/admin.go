package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"estopo/internal/auth"
	"estopo/internal/collector"
	"estopo/internal/derive"
	"estopo/internal/esclient"
	"estopo/internal/store"
)

// Admin endpoints needed to run the collection (the full administration,
// with authentication, comes with plan step 11).

func (s *Server) handleAdminClusters(w http.ResponseWriter, r *http.Request) {
	cs, err := s.store.Clusters(r.Context(), boolParam(r, "all", false))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if cs == nil {
		cs = []store.Cluster{}
	}
	writeJSON(w, http.StatusOK, cs)
}

func (s *Server) handleAdminSaveCluster(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	var c store.Cluster
	if err := decodeJSON(r, &c); err != nil {
		s.fail(w, r, err)
		return
	}
	c.ID = id
	if c.Name == "" {
		c.Name = id
	}
	if len(c.ES.URLs) == 0 {
		s.fail(w, r, badRequest("at least one URL is required"))
		return
	}
	// Fields absent from the request keep their value (an explicit null
	// resets them to the defaults).
	if old, err := s.store.Cluster(ctx, id); err == nil {
		if c.Schedule == nil {
			c.Schedule = old.Schedule
		}
		if c.Hierarchy == nil {
			c.Hierarchy = old.Hierarchy
		}
	}
	if err := validSchedule(c.Schedule); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := validHierarchy(c.Hierarchy); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.checkFiles(c.ES); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.SaveCluster(ctx, c); err != nil {
		s.fail(w, r, badRequest(err.Error()))
		return
	}
	s.store.Audit(ctx, s.user(r).Name, "cluster.save", id, "")
	s.configChanged(ctx)
	saved, err := s.store.Cluster(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleAdminRemoveCluster(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	if err := s.store.RemoveCluster(ctx, id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(ctx, s.user(r).Name, "cluster.remove", id, "")
	s.configChanged(ctx)
	w.WriteHeader(http.StatusNoContent)
}

// checkFiles refuses the files of a connection (certificate, key, secrets,
// CA) that are outside ESTOPO_SECRETS_DIR, when it is set: an administrator
// of the application must not make it read any file of the host.
func (s *Server) checkFiles(es esclient.Config) error {
	for _, p := range []string{es.CertFile, es.KeyFile, es.KeyPassphraseFile, es.APIKeyFile, es.PasswordFile, es.CAPath} {
		if err := auth.InSecretsDir(s.secretsDir, p); err != nil {
			return badRequest(err.Error())
		}
	}
	return nil
}

func (s *Server) configChanged(ctx context.Context) {
	if s.onConfig != nil {
		if err := s.onConfig(ctx); err != nil {
			s.log.Error("apply configuration", "err", err)
		}
	}
}

// ConnectionTest is the result of "test the connection" (SPEC §9).
type ConnectionTest struct {
	OK          bool                `json:"ok"`
	Error       string              `json:"error,omitempty"`
	ClusterName string              `json:"cluster_name,omitempty"`
	ClusterUUID string              `json:"cluster_uuid,omitempty"`
	Version     string              `json:"version,omitempty"`
	Health      string              `json:"health,omitempty"`
	Nodes       int                 `json:"nodes,omitempty"`
	Missing     []string            `json:"missing_privileges,omitempty"`
	BoundUUID   string              `json:"bound_uuid,omitempty"`
	UUIDChanged bool                `json:"uuid_changed"`
	Calls       []esclient.CallStat `json:"calls"`
}

// handleAdminTestCluster tests the saved configuration of a cluster, or
// the one given in the body.
func (s *Server) handleAdminTestCluster(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	var cfg esclient.Config
	bound := ""
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &cfg); err != nil {
			s.fail(w, r, err)
			return
		}
	} else {
		c, err := s.store.Cluster(ctx, id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		cfg, bound = c.ES, c.BoundUUID
	}
	cfg = s.store.ApplyTLSDefaults(ctx, cfg)
	if err := s.checkFiles(cfg); err != nil {
		s.fail(w, r, err)
		return
	}
	res := ConnectionTest{BoundUUID: bound, Calls: []esclient.CallStat{}}
	cl, err := esclient.New(cfg)
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	root, st, err := cl.Root(cctx)
	res.Calls = append(res.Calls, st)
	if err != nil {
		res.Error = err.Error()
		writeJSON(w, http.StatusOK, res)
		return
	}
	res.ClusterName, res.ClusterUUID, res.Version = root.ClusterName, root.ClusterUUID, root.Version
	res.UUIDChanged = bound != "" && bound != root.ClusterUUID
	if h, st, err := cl.Health(cctx); err == nil {
		res.Health, res.Nodes = h.Status, h.Nodes
		res.Calls = append(res.Calls, st)
	} else {
		res.Calls = append(res.Calls, st)
		res.Error = err.Error()
	}
	p, st, err := cl.HasPrivileges(cctx)
	res.Calls = append(res.Calls, st)
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Missing = p.Missing
	}
	res.OK = res.Error == "" && len(res.Missing) == 0 && !res.UUIDChanged
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleAdminCapture(w http.ResponseWriter, r *http.Request) {
	if s.collector == nil {
		writeError(w, http.StatusServiceUnavailable, "no_collector", "collection is not running on this instance")
		return
	}
	id := r.PathValue("id")
	if err := s.collector.CaptureNow(id, boolParam(r, "force", false)); err != nil {
		writeError(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	s.store.Audit(r.Context(), s.user(r).Name, "cluster.capture", id, r.URL.RawQuery)
	w.WriteHeader(http.StatusAccepted)
}

// handleAdminResume resumes a suspended cluster: ?accept=<uuid> binds it to
// the new identity (or "" to bind at the next capture).
func (s *Server) handleAdminResume(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	uuid := r.URL.Query().Get("accept")
	if err := s.store.SetBound(ctx, id, uuid); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.SetSuspended(ctx, id, false, ""); err != nil {
		s.fail(w, r, err)
		return
	}
	if s.collector != nil {
		s.collector.Resume(id, uuid)
	}
	s.store.Audit(ctx, s.user(r).Name, "cluster.resume", id, uuid)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminPurge(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	purge := s.store.Purge
	if s.purge != nil {
		purge = s.purge
	}
	n, err := purge(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(ctx, s.user(r).Name, "cluster.purge", id, "")
	writeJSON(w, http.StatusOK, map[string]int{"partitions_deleted": n})
}

func (s *Server) handleAdminCollectors(w http.ResponseWriter, r *http.Request) {
	if s.collector == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	sts := s.collector.Status()
	out := make([]collectorStatus, len(sts))
	for i, st := range sts {
		out[i].Status = st
		if last, d, err := s.store.HistoryAhead(r.Context(), st.ID); err == nil {
			out[i].HistoryUntilMs, out[i].HistoryAheadMs = last, d.Milliseconds()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// collectorStatus adds to the state of a collector the date of the last
// ingested record when it is ahead of the clock (SPEC §6.7): the newer
// records are refused until the clock catches up.
type collectorStatus struct {
	collector.Status
	HistoryUntilMs int64 `json:"history_until_ms,omitempty"`
	HistoryAheadMs int64 `json:"history_ahead_ms,omitempty"`
}

// validSchedule checks a cluster schedule (SPEC §5.3, §9 "Planification").
func validSchedule(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	sc := collector.DefaultSchedule()
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&sc); err != nil {
		return badRequest("invalid schedule: " + err.Error())
	}
	switch {
	case sc.ProbeEvery < 30*time.Second:
		return badRequest("the probe period must be at least 30 s")
	case sc.MinInterval < sc.ProbeEvery:
		return badRequest("min_interval must be at least the probe period")
	case sc.MaxInterval < sc.MinInterval:
		return badRequest("max_interval must be at least min_interval")
	case sc.Jitter < 0 || sc.Jitter > 0.5:
		return badRequest("the jitter must be between 0 and 0.5")
	case sc.ShardsThreshold < 0:
		return badRequest("the shard threshold cannot be negative")
	}
	return nil
}

// validHierarchy checks the navigation levels of a cluster (SPEC §7.4).
func validHierarchy(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var lv []derive.Level
	if err := json.Unmarshal(raw, &lv); err != nil {
		return badRequest("invalid hierarchy: " + err.Error())
	}
	seen := map[string]bool{}
	for i, l := range lv {
		key := l.Kind + ":" + l.Attr
		switch {
		case l.Kind == derive.LevelAttr && l.Attr == "":
			return badRequest("an attribute level needs the attribute name")
		case l.Kind != derive.LevelAttr && l.Attr != "":
			return badRequest("only attribute levels have an attribute name")
		case l.Kind != derive.LevelAttr && l.Kind != derive.LevelTier && l.Kind != derive.LevelMachine && l.Kind != derive.LevelNode:
			return badRequest("unknown hierarchy level " + l.Kind)
		case l.Kind == derive.LevelNode && i != len(lv)-1:
			return badRequest("the node level comes last")
		case seen[key]:
			return badRequest("duplicate hierarchy level " + key)
		}
		seen[key] = true
	}
	return nil
}

// handleAdminReattach handles a cluster whose identity changed (SPEC §5.5)
// as a new cluster: a new configuration with the same connection settings,
// bound at its first capture, while the old one is disabled and keeps its
// history.
func (s *Server) handleAdminReattach(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), r.PathValue("id")
	var body struct {
		NewID string `json:"new_id"`
		Name  string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.fail(w, r, err)
		return
	}
	old, err := s.store.Cluster(ctx, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !store.ValidClusterID(body.NewID) {
		s.fail(w, r, badRequest("invalid new cluster id"))
		return
	}
	if _, err := s.store.Cluster(ctx, body.NewID); err == nil {
		s.fail(w, r, badRequest("cluster id "+body.NewID+" already exists"))
		return
	}
	nc := old
	nc.ID, nc.Name, nc.Enabled = body.NewID, body.Name, true
	if nc.Name == "" {
		nc.Name = body.NewID
	}
	if err := s.store.SaveCluster(ctx, nc); err != nil {
		s.fail(w, r, err)
		return
	}
	old.Enabled = false
	if err := s.store.SaveCluster(ctx, old); err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(ctx, s.user(r).Name, "cluster.reattach", id, body.NewID)
	s.configChanged(ctx)
	saved, err := s.store.Cluster(ctx, body.NewID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}
