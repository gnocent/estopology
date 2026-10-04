package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"estopo/internal/buildinfo"
	"estopo/internal/derive"
	"estopo/internal/model"
	"estopo/internal/query"
	"estopo/internal/store"
)

const week = 7 * 24 * time.Hour

func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	mode, err := s.authMode(r)
	if err != nil {
		s.authUnavailable(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":     buildinfo.Version,
		"role":        s.role(),
		"now":         s.clock.Now().UnixMilli(),
		"user":        s.user(r),
		"auth_mode":   mode,
		"event_types": store.EventTypes,
	})
}

// ClusterSummary is one line of the home screen (SPEC §8.2.1).
type ClusterSummary struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Enabled       bool               `json:"enabled"`
	Suspended     bool               `json:"suspended"`
	SuspendReason string             `json:"suspend_reason,omitempty"`
	Removed       bool               `json:"removed"`
	LastCaptureMs int64              `json:"last_capture_ts"`
	Health        *model.Health      `json:"health,omitempty"`
	Assessment    *derive.Assessment `json:"assessment,omitempty"`
	Counts        *Counts            `json:"counts,omitempty"`
	Versions      []string           `json:"versions"`
	Missing       []string           `json:"missing_nodes"`
	Timeline      []query.Bucket     `json:"timeline"`
	Recent        []query.Event      `json:"recent_events"`
	Error         string             `json:"error,omitempty"`
}

func (s *Server) handleClusters(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := s.clock.Now().UnixMilli()
	cfgs, err := s.store.Clusters(ctx, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	withHistory, err := s.engine.Clusters(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	known := map[string]store.Cluster{}
	for _, c := range cfgs {
		known[c.ID] = c
	}
	for _, id := range withHistory {
		if _, ok := known[id]; !ok {
			known[id] = store.Cluster{ID: id, Name: id}
		}
	}
	buckets, err := intParam(r, "buckets", 84, 1, 2000)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := make([]ClusterSummary, 0, len(known))
	for _, c := range known {
		cs := ClusterSummary{ID: c.ID, Name: c.Name, Enabled: c.Enabled, Suspended: c.Suspended, SuspendReason: c.SuspendReason,
			Removed: c.RemovedMs > 0, Versions: []string{}, Missing: []string{}, Timeline: []query.Bucket{}, Recent: []query.Event{}}
		cs.LastCaptureMs, _ = s.engine.LastCaptureTs(ctx, c.ID)
		if cs.LastCaptureMs == 0 {
			cs.Error = "no_data"
			out = append(out, cs)
			continue
		}
		// The computed part only changes with a new capture or a configuration
		// change; it is kept at most a minute (the 7-day frieze slides).
		key := fmt.Sprintf("%s|%d|%d|%d", c.ID, cs.LastCaptureMs, buckets, s.engine.Generation())
		if h, ok := s.home.get(key, now); ok {
			cs.Health, cs.Assessment, cs.Counts, cs.Versions, cs.Missing, cs.Timeline, cs.Recent =
				h.Health, h.Assessment, h.Counts, h.Versions, h.Missing, h.Timeline, h.Recent
			out = append(out, cs)
			continue
		}
		v, _, err := s.stateView(ctx, c.ID, now)
		if err != nil {
			// The detail stays in the logs: readers get a code.
			cs.Error = "no_data"
			if !errors.Is(err, query.ErrNoData) {
				s.log.Error("cluster summary", "cluster", c.ID, "err", err)
				cs.Error = "internal"
			}
			out = append(out, cs)
			continue
		}
		cs.Health, cs.Assessment, cs.Counts, cs.Versions, cs.Missing = v.Health, &v.Assessment, &v.Counts, v.Versions, v.Missing
		if tl, err := s.engine.Timeline(ctx, c.ID, now-week.Milliseconds(), now, buckets); err == nil {
			cs.Timeline = tl
		}
		if ev, err := s.engine.Events(ctx, c.ID, query.EventFilter{From: now - week.Milliseconds(), To: now, MaxSeverity: 2, Limit: 5}); err == nil {
			cs.Recent = append(cs.Recent, ev.Events...)
		}
		s.home.put(c.ID, key, now, cs)
		out = append(out, cs)
	}
	// Most worrying first: severity 1..6, unknown (-1), then healthy (0), then without data.
	rank := func(c ClusterSummary) int {
		switch {
		case c.Assessment == nil:
			return 100
		case c.Assessment.Level == -1:
			return 7
		case c.Assessment.Level == 0:
			return 50
		}
		return c.Assessment.Level
	}
	sort.SliceStable(out, func(i, j int) bool {
		if rank(out[i]) != rank(out[j]) {
			return rank(out[i]) < rank(out[j])
		}
		return out[i].Name < out[j].Name
	})
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) at(r *http.Request) (int64, error) {
	return timeParam(r, "at", s.clock.Now().UnixMilli())
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	at, err := s.at(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v, _, err := s.stateView(r.Context(), r.PathValue("id"), at)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.window(r, "from", "to", week)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	n, err := intParam(r, "buckets", 200, 1, 2000)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tl, err := s.engine.Timeline(r.Context(), r.PathValue("id"), from, to, n)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tl)
}

func (s *Server) state(r *http.Request) (*model.State, error) {
	at, err := s.at(r)
	if err != nil {
		return nil, err
	}
	return s.engine.StateAt(r.Context(), r.PathValue("id"), at)
}

func (s *Server) handleMatrix(w http.ResponseWriter, r *http.Request) {
	st, err := s.state(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	top, err := intParam(r, "top", 30, 1, 500)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	filter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	if r.URL.Query().Get("rows") == "index" {
		writeJSON(w, http.StatusOK, matrixByIndex(st, top, boolParam(r, "hide_system", true), filter))
		return
	}
	writeJSON(w, http.StatusOK, matrixView(st, top, boolParam(r, "hide_system", true), filter))
}

func (s *Server) handleShards(w http.ResponseWriter, r *http.Request) {
	st, err := s.state(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	uuid, family, node, state := q.Get("index"), q.Get("family"), q.Get("node"), strings.ToUpper(q.Get("state"))
	hideSystem := boolParam(r, "hide_system", true)
	var out []ShardView
	for _, sh := range st.Shards {
		ix := &st.Indices[sh.Index]
		if (uuid != "" && ix.UUID != uuid) || (family != "" && ix.Family != family) || (state != "" && sh.State != state) ||
			(hideSystem && ix.System) {
			continue
		}
		if node != "" && (sh.Node < 0 || st.Nodes[sh.Node].Name != node) && (sh.RelocTarget < 0 || st.Nodes[sh.RelocTarget].Name != node) {
			continue
		}
		out = append(out, shardView(st, sh))
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Index != b.Index {
			return a.Index < b.Index
		}
		if a.Shard != b.Shard {
			return a.Shard < b.Shard
		}
		if a.Primary != b.Primary {
			return a.Primary
		}
		return a.Node < b.Node
	})
	if uuid != "" { // sizes for one index
		if sizes, err := s.engine.ShardSizes(r.Context(), st.Cluster, uuid, st.AtMs); err == nil {
			for i := range out {
				if sz, ok := sizes[query.CopyKey(out[i].Shard, out[i].Primary, out[i].Node)]; ok {
					out[i].Docs, out[i].StoreBytes = sz.Docs, sz.StoreBytes
				}
			}
		}
	}
	p, err := paginate(r, out)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleIndices(w http.ResponseWriter, r *http.Request) {
	st, err := s.state(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	family, text := q.Get("family"), strings.ToLower(q.Get("q"))
	hideSystem := boolParam(r, "hide_system", true)
	var out []IndexView
	for _, v := range indexViews(st) {
		if (family != "" && v.Family != family) || (text != "" && !strings.Contains(strings.ToLower(v.Name), text)) ||
			(hideSystem && v.System && family == "") {
			continue
		}
		out = append(out, v)
	}
	sortIndices(out, q.Get("sort"))
	p, err := paginate(r, out)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func sortIndices(v []IndexView, by string) {
	sort.SliceStable(v, func(i, j int) bool {
		switch by {
		case "size":
			return v[i].StoreBytes > v[j].StoreBytes
		case "creation":
			return v[i].CreationMs > v[j].CreationMs
		case "health":
			rank := map[string]int{"red": 0, "yellow": 1, "green": 2}
			return rank[v[i].Health] < rank[v[j].Health]
		}
		return v[i].Name < v[j].Name
	})
}

func (s *Server) handleFamilies(w http.ResponseWriter, r *http.Request) {
	st, err := s.state(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	hide := boolParam(r, "hide_system", true)
	var out []FamilyView
	for _, f := range familyViews(st) {
		if !hide || !f.System {
			out = append(out, f)
		}
	}
	p, err := paginate(r, out)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// NodeDetail is the node screen (SPEC §8.2.3).
type NodeDetail struct {
	History  *query.NodeHistory `json:"history"`
	Node     *NodeView          `json:"node,omitempty"` // at T
	Families []FamilyOnNode     `json:"families"`
}

// FamilyOnNode counts the copies of a family held by a node at T.
type FamilyOnNode struct {
	Family    string `json:"family"`
	Copies    int    `json:"copies"`
	Primaries int    `json:"primaries"`
}

func (s *Server) handleNode(w http.ResponseWriter, r *http.Request) {
	ctx, cluster, name := r.Context(), r.PathValue("id"), r.PathValue("name")
	at, err := s.at(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	from, to, err := s.window(r, "from", "to", week)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	points, _ := intParam(r, "points", 500, 10, 5000)
	h, err := s.engine.NodeHistory(ctx, cluster, name, from, to, points)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := NodeDetail{History: h, Families: []FamilyOnNode{}}
	if v, _, err := s.stateView(ctx, cluster, at); err == nil {
		for i := range v.Nodes {
			if v.Nodes[i].Name == name && (v.Nodes[i].Present || d.Node == nil) {
				d.Node = &v.Nodes[i]
			}
		}
		if st, err := s.engine.StateAt(ctx, cluster, at); err == nil {
			by := map[string]*FamilyOnNode{}
			for _, sh := range st.Shards {
				if sh.Node < 0 || st.Nodes[sh.Node].Name != name {
					continue
				}
				f := st.Indices[sh.Index].Family
				if by[f] == nil {
					by[f] = &FamilyOnNode{Family: f}
				}
				by[f].Copies++
				if sh.Primary {
					by[f].Primaries++
				}
			}
			for _, f := range by {
				d.Families = append(d.Families, *f)
			}
			sort.Slice(d.Families, func(i, j int) bool { return d.Families[i].Copies > d.Families[j].Copies })
		}
	}
	if len(h.Generations) == 0 && d.Node == nil {
		writeError(w, http.StatusNotFound, "not_found", "unknown node")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// IndexDetail is the index screen (SPEC §8.2.4).
type IndexDetail struct {
	History *query.IndexHistory `json:"history"`
	Index   *IndexView          `json:"index,omitempty"` // at T (nil if it did not exist)
	Shards  []ShardView         `json:"shards"`
	Nodes   []string            `json:"nodes"` // columns of the shards × nodes matrix
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	ctx, cluster, uuid := r.Context(), r.PathValue("id"), r.PathValue("uuid")
	at, err := s.at(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	from, to, err := s.window(r, "from", "to", week)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h, err := s.engine.IndexHistory(ctx, cluster, uuid, from, to)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := IndexDetail{History: h, Shards: []ShardView{}, Nodes: []string{}}
	if st, err := s.engine.StateAt(ctx, cluster, at); err == nil {
		for i, v := range indexViews(st) {
			if v.UUID == uuid {
				vv := v
				d.Index = &vv
				nodes := map[string]bool{}
				sizes, _ := s.engine.ShardSizes(ctx, cluster, uuid, at)
				for _, sh := range st.Shards {
					if int(sh.Index) != i {
						continue
					}
					sv := shardView(st, sh)
					if sz, ok := sizes[query.CopyKey(sv.Shard, sv.Primary, sv.Node)]; ok {
						sv.Docs, sv.StoreBytes = sz.Docs, sz.StoreBytes
					}
					d.Shards = append(d.Shards, sv)
					if sv.Node != "" {
						nodes[sv.Node] = true
					}
				}
				for n := range nodes {
					d.Nodes = append(d.Nodes, n)
				}
				sort.Strings(d.Nodes)
			}
		}
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleLives(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		s.fail(w, r, badRequest("name is required"))
		return
	}
	lives, err := s.engine.IndexLives(r.Context(), r.PathValue("id"), name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, lives)
}

// diffWindow reads a and b (default: the last 24 hours).
func (s *Server) diffWindow(r *http.Request) (int64, int64, error) {
	return s.window(r, "a", "b", 24*time.Hour)
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	a, b, err := s.diffWindow(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d, err := s.engine.Diff(r.Context(), r.PathValue("id"), a, b)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Sections are truncated to ?limit (default 200); ?section=x&offset=
	// pages through one of them.
	limit, err := intParam(r, "limit", 200, 1, 10000)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	switch sec := r.URL.Query().Get("section"); sec {
	case "":
		type totals struct {
			Nodes, Indices, Shards, Settings, ClusterSettings, Remotes int
		}
		t := totals{len(d.Nodes), len(d.Indices), len(d.Shards), len(d.IndexSettingChanges()), len(d.ClusterSettings), len(d.Remotes)}
		d.Nodes, d.Indices, d.Shards = d.Nodes[:min(limit, len(d.Nodes))], d.Indices[:min(limit, len(d.Indices))], d.Shards[:min(limit, len(d.Shards))]
		writeJSON(w, http.StatusOK, map[string]any{"diff": d, "totals": t})
	case "nodes":
		s.writePage(w, r, d.Nodes)
	case "indices":
		s.writePage(w, r, d.Indices)
	case "shards":
		s.writePage(w, r, d.Shards)
	case "settings":
		s.writePage(w, r, d.IndexSettingChanges())
	case "cluster_settings":
		s.writePage(w, r, d.ClusterSettings)
	case "remotes":
		s.writePage(w, r, d.Remotes)
	default:
		s.fail(w, r, badRequest("unknown section "+sec))
	}
}

func writePageOf[T any](s *Server, w http.ResponseWriter, r *http.Request, items []T) {
	p, err := paginate(r, items)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) writePage(w http.ResponseWriter, r *http.Request, items any) {
	switch v := items.(type) {
	case []query.NodeDiff:
		writePageOf(s, w, r, v)
	case []query.IndexSettingChange:
		writePageOf(s, w, r, v)
	case []query.IndexDiff:
		writePageOf(s, w, r, v)
	case []query.ShardDiff:
		writePageOf(s, w, r, v)
	case []query.SettingDiff:
		writePageOf(s, w, r, v)
	case []query.RemoteDiff:
		writePageOf(s, w, r, v)
	}
}

func (s *Server) handlePath(w http.ResponseWriter, r *http.Request) {
	a, b, err := s.diffWindow(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	pq := query.PathQuery{UUID: q.Get("uuid"), Family: q.Get("family"), Shard: -1, A: a, B: b}
	if pq.UUID == "" && pq.Family == "" {
		s.fail(w, r, badRequest("uuid or family is required"))
		return
	}
	if pq.Shard, err = intParam(r, "shard", -1, -1, 1<<20); err != nil {
		s.fail(w, r, err)
		return
	}
	segs, err := s.engine.Path(r.Context(), r.PathValue("id"), pq)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, segs)
}

func (s *Server) handleLosses(w http.ResponseWriter, r *http.Request) {
	a, b, err := s.diffWindow(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	limit, err := intParam(r, "limit", 1000, 1, 20000)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	l, err := s.engine.Losses(r.Context(), r.PathValue("id"), a, b)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// The summary counts everything; the lists are capped per group (the
	// loss of a data centre can involve tens of thousands of copies).
	for i := range l.Groups {
		if g := &l.Groups[i]; len(g.Copies) > limit {
			g.More, g.Copies = len(g.Copies)-limit, g.Copies[:limit]
		}
	}
	if len(l.Deleted) > limit {
		l.DeletedMore, l.Deleted = len(l.Deleted)-limit, l.Deleted[:limit]
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) eventFilter(r *http.Request) (query.EventFilter, error) {
	from, to, err := s.window(r, "from", "to", 24*time.Hour)
	if err != nil {
		return query.EventFilter{}, err
	}
	q := r.URL.Query()
	f := query.EventFilter{From: from, To: to, Types: listParam(r, "types"), Node: q.Get("node"), Machine: q.Get("machine"),
		Family: q.Get("family"), Subject: q.Get("subject"), BeforeKey: q.Get("before_key")}
	if f.MaxSeverity, err = intParam(r, "severity", 0, 0, 4); err != nil {
		return f, err
	}
	if f.Limit, err = intParam(r, "limit", 200, 1, 1000); err != nil {
		return f, err
	}
	if f.BeforeTs, err = timeParam(r, "before_ts", 0); err != nil {
		return f, err
	}
	// g=<label>&g=<label>: the events of the nodes and machines of one group
	// of the hierarchy, as it is at the end of the window.
	if labels := q["g"]; len(labels) > 0 {
		path, nodes, machines, err := s.groupMembers(r.Context(), r.PathValue("id"), to, labels)
		if err != nil {
			return f, err
		}
		if path == nil {
			return f, errUnknownGroup
		}
		f.Group, f.Nodes, f.Machines = true, nodes, machines
	}
	return f, nil
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	f, err := s.eventFilter(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	p, err := s.engine.Events(r.Context(), r.PathValue("id"), f)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if p.Events == nil {
		p.Events = []query.Event{}
	}
	writeJSON(w, http.StatusOK, p)
}

// ConstraintView is one line of the constraints screen (SPEC §8.2.10).
type ConstraintView struct {
	derive.IndexConstraint
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Family string `json:"family"`
}

func (s *Server) constraints(r *http.Request) ([]ConstraintView, error) {
	at, err := s.at(r)
	if err != nil {
		return nil, err
	}
	o, err := s.engine.Overview(r.Context(), r.PathValue("id"), at, s.declaredReference(r.Context(), r.PathValue("id")))
	if err != nil {
		return nil, err
	}
	status, family := listParam(r, "status"), r.URL.Query().Get("family")
	hide := boolParam(r, "hide_system", true)
	keep := map[string]bool{}
	for _, st := range status {
		keep[st] = true
	}
	var out []ConstraintView
	for _, c := range o.Constraints {
		ix := o.State.Indices[c.Index]
		if (len(keep) > 0 && !keep[c.Status]) || (family != "" && ix.Family != family) || (hide && ix.System) {
			continue
		}
		out = append(out, ConstraintView{IndexConstraint: c, UUID: ix.UUID, Name: ix.Name, Family: ix.Family})
	}
	rank := map[string]int{derive.Infeasible: 0, derive.Tight: 1, derive.OK: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Status] != rank[out[j].Status] {
			return rank[out[i].Status] < rank[out[j].Status]
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Server) handleConstraints(w http.ResponseWriter, r *http.Request) {
	out, err := s.constraints(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	counts := map[string]int{}
	for _, c := range out {
		counts[c.Status]++
	}
	p, err := paginate(r, out)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts, "page": p})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	clusters := listParam(r, "cluster")
	if len(clusters) == 0 {
		var err error
		if clusters, err = s.engine.Clusters(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	limit, err := intParam(r, "limit", 20, 1, 200)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	hits, err := s.engine.Search(r.Context(), clusters, q, limit)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if hits == nil {
		hits = []query.Hit{}
	}
	writeJSON(w, http.StatusOK, hits)
}

// Flows are the relocations between groups of nodes over a period (SPEC
// §8.2.6): Sankey links and a histogram.
type Flows struct {
	Level     string        `json:"level"`
	Links     []FlowLink    `json:"links"`
	Histogram []FlowBucket  `json:"histogram"`
	Events    []query.Event `json:"events,omitempty"` // with ?from_group&to_group
}

// FlowLink counts the relocations from a group to another.
type FlowLink struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Count int    `json:"count"`
}

// FlowBucket counts relocations over time.
type FlowBucket struct {
	FromMs int64 `json:"from"`
	ToMs   int64 `json:"to"`
	Count  int   `json:"count"`
}

func (s *Server) handleFlows(w http.ResponseWriter, r *http.Request) {
	ctx, cluster := r.Context(), r.PathValue("id")
	a, b, err := s.diffWindow(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	level := r.URL.Query().Get("level")
	if level == "" {
		level = "tier"
	}
	n, err := intParam(r, "buckets", 48, 1, 1000)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.engine.Events(ctx, cluster, query.EventFilter{From: a, To: b, Types: []string{"shard_relocated"}, Limit: 1000})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	evs := page.Events
	for page.NextTs > 0 && len(evs) < 200000 { // whole window
		page, err = s.engine.Events(ctx, cluster, query.EventFilter{From: a, To: b, Types: []string{"shard_relocated"}, Limit: 1000,
			BeforeTs: page.NextTs, BeforeKey: page.NextKey})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		evs = append(evs, page.Events...)
	}
	// Node -> group from the states at both ends.
	group := map[string]string{}
	for _, t := range []int64{a, b} {
		st, err := s.engine.StateAt(ctx, cluster, t)
		if err != nil {
			continue
		}
		for _, nd := range st.Nodes {
			group[nd.Name] = groupOf(&nd, level, st)
		}
	}
	links := map[[2]string]int{}
	width := max((b-a+int64(n)-1)/int64(n), 1)
	hist := make([]FlowBucket, n)
	for i := range hist {
		hist[i].FromMs = a + int64(i)*width
		hist[i].ToMs = min(hist[i].FromMs+width, b)
	}
	fromG, toG := r.URL.Query().Get("from_group"), r.URL.Query().Get("to_group")
	out := Flows{Level: level, Links: []FlowLink{}}
	for _, ev := range evs {
		var p struct{ From, To string }
		json.Unmarshal(ev.Payload, &p)
		gf, gt := groupOr(group, p.From), groupOr(group, p.To)
		links[[2]string{gf, gt}]++
		if i := int((ev.TsHi - a) / width); i >= 0 && i < n {
			hist[i].Count++
		}
		if fromG != "" && gf == fromG && gt == toG && len(out.Events) < 5000 {
			out.Events = append(out.Events, ev)
		}
	}
	for k, c := range links {
		out.Links = append(out.Links, FlowLink{From: k[0], To: k[1], Count: c})
	}
	sort.Slice(out.Links, func(i, j int) bool {
		if out.Links[i].Count != out.Links[j].Count {
			return out.Links[i].Count > out.Links[j].Count
		}
		return out.Links[i].From+out.Links[i].To < out.Links[j].From+out.Links[j].To
	})
	out.Histogram = hist
	writeJSON(w, http.StatusOK, out)
}

func groupOr(g map[string]string, node string) string {
	if v, ok := g[node]; ok && v != "" {
		return v
	}
	return node
}

// groupOf labels a node for a flow level: tier, machine, node, attr:<name>
// or "dc" (the first awareness attribute).
func groupOf(n *model.Node, level string, st *model.State) string {
	switch {
	case level == "node":
		return n.Name
	case level == "machine":
		return n.Machine
	case level == "tier":
		t := n.Tiers
		var real []string
		for _, x := range t {
			if x != "content" {
				real = append(real, x)
			}
		}
		if len(real) == 0 {
			real = t
		}
		return strings.Join(real, "+")
	case strings.HasPrefix(level, "attr:"):
		return n.Attrs[strings.TrimPrefix(level, "attr:")]
	default: // dc: first awareness attribute
		aw := strings.Split(st.ClusterSettings["cluster.routing.allocation.awareness.attributes"], ",")
		return n.Attrs[strings.TrimSpace(aw[0])]
	}
}
