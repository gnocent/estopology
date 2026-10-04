package api

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"estopo/internal/derive"
	"estopo/internal/model"
	"estopo/internal/query"
)

// NodeView is a node with its placement counters at T.
type NodeView struct {
	model.Node
	Missing        bool    `json:"missing"` // in the 7-day reference but absent
	Shards         int     `json:"shards"`
	Primaries      int     `json:"primaries"`
	Initializing   int     `json:"initializing"`
	RelocatingOut  int     `json:"relocating_out"`
	RelocatingIn   int     `json:"relocating_in"`
	DiskPercent    float64 `json:"disk_percent"` // -1 unknown
	AboveHighWM    bool    `json:"above_high_watermark"`
	UnassignedHere int     `json:"unassigned_from_here"` // copies unassigned after this node left
}

// Counts summarizes a state.
type Counts struct {
	Nodes               int `json:"nodes"`
	NodesPresent        int `json:"nodes_present"`
	DataNodes           int `json:"data_nodes"`
	Indices             int `json:"indices"`
	Families            int `json:"families"`
	Shards              int `json:"shards"`
	Primaries           int `json:"primaries"`
	Unassigned          int `json:"unassigned"`
	UnassignedPrimaries int `json:"unassigned_primaries"`
	Relocating          int `json:"relocating"`
	Initializing        int `json:"initializing"`
}

// StateView is the "cluster at T" screen (SPEC §8.2.2).
type StateView struct {
	Cluster         string            `json:"cluster"`
	Name            string            `json:"name"`
	AtMs            int64             `json:"at"`
	CaptureMs       int64             `json:"capture_ts"`
	ProbeMs         int64             `json:"probe_ts"`
	Unknown         bool              `json:"unknown"`
	Health          *model.Health     `json:"health,omitempty"`
	Assessment      derive.Assessment `json:"assessment"`
	Missing         []string          `json:"missing_nodes"`
	Counts          Counts            `json:"counts"`
	Versions        []string          `json:"versions"`
	Nodes           []NodeView        `json:"nodes"`
	Hierarchy       *derive.Group     `json:"hierarchy"`
	Remotes         []model.Remote    `json:"remotes"`
	ClusterSettings map[string]string `json:"cluster_settings"`
}

// levelsFor returns the configured hierarchy of a cluster, else the default.
func (s *Server) levelsFor(ctx context.Context, cluster string, st *model.State) []derive.Level {
	if c, err := s.store.Cluster(ctx, cluster); err == nil && len(c.Hierarchy) > 0 {
		var lv []derive.Level
		if json.Unmarshal(c.Hierarchy, &lv) == nil && len(lv) > 0 {
			return lv
		}
	}
	return derive.DefaultLevels(st)
}

func (s *Server) declaredReference(ctx context.Context, cluster string) []string {
	if c, err := s.store.Cluster(ctx, cluster); err == nil {
		return c.NodeReference
	}
	return nil
}

// stateView builds the cluster-at-T view: overview, nodes (missing ones
// included where they used to be), hierarchy.
func (s *Server) stateView(ctx context.Context, cluster string, at int64) (*StateView, *query.Overview, error) {
	o, err := s.engine.Overview(ctx, cluster, at, s.declaredReference(ctx, cluster))
	if err != nil {
		return nil, nil, err
	}
	st := o.State
	v := &StateView{Cluster: cluster, Name: s.clusterName(ctx, cluster), AtMs: st.AtMs, CaptureMs: st.CaptureMs,
		ProbeMs: st.ProbeMs, Unknown: st.Unknown, Health: st.Health, Assessment: o.Assessment, Missing: o.Missing,
		Remotes: st.Remotes, ClusterSettings: st.ClusterSettings}
	if v.Missing == nil {
		v.Missing = []string{}
	}
	if v.Remotes == nil {
		v.Remotes = []model.Remote{}
	}

	// Missing nodes are added (with their last known description) so that
	// the physical view shows the hole where they were.
	known, err := s.engine.LastKnownNodes(ctx, cluster, o.Missing, at)
	if err != nil {
		return nil, nil, err
	}
	nodes := append([]model.Node(nil), st.Nodes...)
	missing := map[string]bool{}
	for _, name := range o.Missing {
		n, ok := known[name]
		if !ok {
			n = model.Node{Name: name, Attrs: map[string]string{}}
		}
		n.Present, n.Metrics = false, nil
		nodes = append(nodes, n)
		missing[name] = true
	}
	views := make([]NodeView, len(nodes))
	for i, n := range nodes {
		views[i] = NodeView{Node: n, Missing: missing[n.Name] && !n.Present, DiskPercent: n.Metrics.DiskPercent()}
	}
	versions := map[string]bool{}
	for _, n := range st.Nodes {
		if n.Present && n.Version != "" {
			versions[n.Version] = true
		}
	}
	for ver := range versions {
		v.Versions = append(v.Versions, ver)
	}
	sort.Strings(v.Versions)

	c := &v.Counts
	families := map[string]bool{}
	for _, ix := range st.Indices {
		families[ix.Family] = true
	}
	c.Indices, c.Families = len(st.Indices), len(families)
	for _, sh := range st.Shards {
		c.Shards++
		if sh.Primary {
			c.Primaries++
		}
		switch sh.State {
		case "UNASSIGNED":
			c.Unassigned++
			if sh.Primary {
				c.UnassignedPrimaries++
			}
		case "RELOCATING":
			c.Relocating++
		case "INITIALIZING":
			c.Initializing++
		}
		if sh.Node >= 0 {
			nv := &views[sh.Node]
			nv.Shards++
			if sh.Primary {
				nv.Primaries++
			}
			switch sh.State {
			case "INITIALIZING":
				nv.Initializing++
			case "RELOCATING":
				nv.RelocatingOut++
				if sh.RelocTarget >= 0 {
					views[sh.RelocTarget].RelocatingIn++
				}
			}
		}
	}
	hw := highWatermarkNodes(st)
	for i := range views {
		n := &views[i]
		if n.Present {
			c.NodesPresent++
			if n.IsData() {
				c.DataNodes++
			}
		}
		n.AboveHighWM = hw[n.Name]
	}
	c.Nodes = len(views)
	v.Nodes = views

	// Hierarchy over every node, missing ones included.
	full := *st
	full.Nodes = nodes
	v.Hierarchy = derive.Hierarchy(&full, s.levelsFor(ctx, cluster, st))
	return v, o, nil
}

// highWatermarkNodes lists the nodes above the high watermark, reusing the
// severity computation.
func highWatermarkNodes(st *model.State) map[string]bool {
	out := map[string]bool{}
	for _, n := range st.Nodes {
		one := &model.State{AtMs: st.AtMs, ClusterSettings: st.ClusterSettings, Defaults: st.Defaults, Nodes: []model.Node{n}}
		a := derive.Assess(one, nil, nil)
		for _, c := range a.Concerns {
			if c.Code == derive.ConcernHighWatermark {
				out[n.Name] = true
			}
		}
	}
	return out
}

// ShardView is one shard copy for the tables.
type ShardView struct {
	UUID           string `json:"uuid"`
	Index          string `json:"index"`
	Family         string `json:"family"`
	Shard          int    `json:"shard"`
	Primary        bool   `json:"primary"`
	Node           string `json:"node,omitempty"`
	Machine        string `json:"machine,omitempty"`
	State          string `json:"state"`
	RelocTarget    string `json:"reloc_target,omitempty"`
	Reason         string `json:"reason,omitempty"`
	UnassignedAtMs int64  `json:"unassigned_at,omitempty"`
	SinceMs        int64  `json:"since"`
	Docs           int64  `json:"docs"`
	StoreBytes     int64  `json:"store_bytes"`
}

func shardView(st *model.State, sh model.Shard) ShardView {
	ix := &st.Indices[sh.Index]
	v := ShardView{UUID: ix.UUID, Index: ix.Name, Family: ix.Family, Shard: int(sh.Shard), Primary: sh.Primary,
		State: sh.State, Reason: sh.Reason, UnassignedAtMs: sh.UnassignedAtMs, SinceMs: sh.SinceMs, Docs: -1, StoreBytes: -1}
	if sh.Node >= 0 {
		v.Node, v.Machine = st.Nodes[sh.Node].Name, st.Nodes[sh.Node].Machine
	}
	if sh.RelocTarget >= 0 {
		v.RelocTarget = st.Nodes[sh.RelocTarget].Name
	}
	return v
}

// IndexView is an index with its placement summary at T.
type IndexView struct {
	model.Index
	Health     string `json:"health"`
	Shards     int    `json:"shards"`
	Unassigned int    `json:"unassigned"`
	Tier       string `json:"tier"`
}

func indexViews(st *model.State) []IndexView {
	out := make([]IndexView, len(st.Indices))
	for i, ix := range st.Indices {
		out[i] = IndexView{Index: ix, Health: "green"}
		if pref := ix.Settings["index.routing.allocation.include._tier_preference"]; pref != "" {
			out[i].Tier = strings.TrimPrefix(strings.Split(pref, ",")[0], "data_")
		}
	}
	for _, sh := range st.Shards {
		v := &out[sh.Index]
		v.Shards++
		if sh.State == "UNASSIGNED" || sh.State == "INITIALIZING" {
			if sh.State == "UNASSIGNED" {
				v.Unassigned++
			}
			if sh.Primary {
				v.Health = "red"
			} else if v.Health != "red" {
				v.Health = "yellow"
			}
		}
	}
	return out
}

// FamilyView aggregates the indices of a family at T.
type FamilyView struct {
	Family     string   `json:"family"`
	Indices    int      `json:"indices"`
	Shards     int      `json:"shards"`
	Primaries  int      `json:"primaries"`
	Unassigned int      `json:"unassigned"`
	StoreBytes int64    `json:"store_bytes"`
	Docs       int64    `json:"docs"`
	Tiers      []string `json:"tiers"`
	Newest     string   `json:"newest"`
	System     bool     `json:"system"`
}

func familyViews(st *model.State) []FamilyView {
	idx := indexViews(st)
	by := map[string]*FamilyView{}
	tiers := map[string]map[string]bool{}
	newest := map[string]int64{}
	for _, v := range idx {
		f := by[v.Family]
		if f == nil {
			f = &FamilyView{Family: v.Family, System: v.System}
			by[v.Family] = f
			tiers[v.Family] = map[string]bool{}
		}
		f.Indices++
		f.Shards += v.Shards
		f.Primaries += v.Pri
		f.Unassigned += v.Unassigned
		if v.StoreBytes > 0 {
			f.StoreBytes += v.StoreBytes
		}
		if v.Docs > 0 {
			f.Docs += v.Docs
		}
		if v.Tier != "" {
			tiers[v.Family][v.Tier] = true
		}
		if v.CreationMs >= newest[v.Family] {
			newest[v.Family], f.Newest = v.CreationMs, v.Name
		}
	}
	out := make([]FamilyView, 0, len(by))
	for name, f := range by {
		for t := range tiers[name] {
			f.Tiers = append(f.Tiers, t)
		}
		sort.Strings(f.Tiers)
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StoreBytes != out[j].StoreBytes {
			return out[i].StoreBytes > out[j].StoreBytes
		}
		return out[i].Family < out[j].Family
	})
	return out
}

// Matrix is the logical view: copies per family × node (SPEC §8.2.2).
type Matrix struct {
	Families   []string  `json:"families"`        // top N, then "" for the others
	UUIDs      []string  `json:"uuids,omitempty"` // rows are indices: their UUIDs, in the order of Families
	Nodes      []string  `json:"nodes"`
	Counts     [][]int32 `json:"counts"`     // [family][node]
	Primaries  [][]int32 `json:"primaries"`  // [family][node]
	Unassigned []int32   `json:"unassigned"` // per family
	Total      int       `json:"total"`      // families that match, shown or not
}

// matrixView counts the copies per family and node. filter (lower case)
// keeps the families whose name contains it, the others are left out
// altogether. Index names are searched only when no family name matches:
// they share dates and counters, so a few digits would match every family.
func matrixView(st *model.State, top int, hideSystem bool, filter string) Matrix {
	fam := familyViews(st)
	var match map[string]bool
	if filter != "" {
		match = map[string]bool{}
		for i := range st.Indices {
			if ix := &st.Indices[i]; strings.Contains(strings.ToLower(ix.Family), filter) {
				match[ix.Family] = true
			}
		}
		if len(match) == 0 {
			for i := range st.Indices {
				if ix := &st.Indices[i]; strings.Contains(strings.ToLower(ix.Name), filter) {
					match[ix.Family] = true
				}
			}
		}
	}
	rank := map[string]int{}
	var m Matrix
	for _, f := range fam {
		if hideSystem && f.System {
			continue
		}
		if match != nil && !match[f.Family] {
			continue
		}
		m.Total++
		if len(m.Families) < top {
			rank[f.Family] = len(m.Families)
			m.Families = append(m.Families, f.Family)
		}
	}
	others := len(m.Families)
	m.Families = append(m.Families, "")
	fillMatrix(st, &m, func(i int32) (int, bool) {
		ix := &st.Indices[i]
		if (hideSystem && ix.System) || (match != nil && !match[ix.Family]) {
			return 0, false
		}
		if f, ok := rank[ix.Family]; ok {
			return f, true
		}
		return others, true
	})
	return m
}

// matrixByIndex is matrixView with one row per index instead of per family:
// the indices that hold the most copies first, then by name downwards (the
// newest first when names end with a date). filter keeps
// the indices whose name contains it.
func matrixByIndex(st *model.State, top int, hideSystem bool, filter string) Matrix {
	copies := make([]int, len(st.Indices))
	for _, sh := range st.Shards {
		copies[sh.Index]++
	}
	var keep []int32
	for i := range st.Indices {
		ix := &st.Indices[i]
		if (hideSystem && ix.System) || (filter != "" && !strings.Contains(strings.ToLower(ix.Name), filter)) {
			continue
		}
		keep = append(keep, int32(i))
	}
	sort.Slice(keep, func(a, b int) bool {
		if copies[keep[a]] != copies[keep[b]] {
			return copies[keep[a]] > copies[keep[b]]
		}
		return st.Indices[keep[a]].Name > st.Indices[keep[b]].Name // newest first for dated names
	})
	m := Matrix{Total: len(keep), UUIDs: []string{}}
	row := make(map[int32]int, len(keep))
	for _, i := range keep {
		if len(m.Families) < top {
			row[i] = len(m.Families)
			m.Families = append(m.Families, st.Indices[i].Name)
			m.UUIDs = append(m.UUIDs, st.Indices[i].UUID)
		} else {
			row[i] = top
		}
	}
	m.Families = append(m.Families, "")
	fillMatrix(st, &m, func(i int32) (int, bool) {
		r, ok := row[i]
		return r, ok
	})
	return m
}

// fillMatrix counts the shard copies per row and node; rowOf gives the row
// of an index, or false to leave it out.
func fillMatrix(st *model.State, m *Matrix, rowOf func(index int32) (int, bool)) {
	nodePos := map[int32]int{}
	for i, n := range st.Nodes {
		if n.IsData() || n.Present {
			nodePos[int32(i)] = len(m.Nodes)
			m.Nodes = append(m.Nodes, n.Name)
		}
	}
	m.Counts = make([][]int32, len(m.Families))
	m.Primaries = make([][]int32, len(m.Families))
	for i := range m.Counts {
		m.Counts[i] = make([]int32, len(m.Nodes))
		m.Primaries[i] = make([]int32, len(m.Nodes))
	}
	m.Unassigned = make([]int32, len(m.Families))
	for _, sh := range st.Shards {
		f, ok := rowOf(sh.Index)
		if !ok {
			continue
		}
		if sh.Node < 0 {
			m.Unassigned[f]++
			continue
		}
		if p, ok := nodePos[sh.Node]; ok {
			m.Counts[f][p]++
			if sh.Primary {
				m.Primaries[f][p]++
			}
		}
	}
}
