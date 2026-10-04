package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	"estopo/internal/derive"
	"estopo/internal/store"
)

var errUnknownGroup = fmt.Errorf("unknown group: %w", store.ErrNotFound)

// GroupRef names one level of a path in the hierarchy.
type GroupRef struct {
	Kind  string `json:"kind"`
	Attr  string `json:"attr,omitempty"`
	Label string `json:"label"`
}

// GroupDetail is what the state view does not give about one group of the
// hierarchy (zone, tier, machine): its members and what they hold.
type GroupDetail struct {
	Path     []GroupRef     `json:"path"`
	Nodes    []string       `json:"nodes"`
	Machines []string       `json:"machines"`
	Families []FamilyOnNode `json:"families"`
}

// findGroup walks the hierarchy along the labels (one per level).
func findGroup(root *derive.Group, labels []string) ([]GroupRef, *derive.Group) {
	g := root
	var path []GroupRef
	for _, lb := range labels {
		var next *derive.Group
		for _, c := range g.Children {
			if c.Label == lb {
				next = c
				break
			}
		}
		if next == nil {
			return nil, nil
		}
		path = append(path, GroupRef{Kind: next.Kind, Attr: next.Attr, Label: next.Label})
		g = next
	}
	return path, g
}

// groupMembers resolves a path of labels at a date: the path as found, the
// names of the member nodes (present or missing) and their machines.
func (s *Server) groupMembers(ctx context.Context, cluster string, at int64, labels []string) ([]GroupRef, []string, []string, error) {
	v, _, err := s.stateView(ctx, cluster, at)
	if err != nil {
		return nil, nil, nil, err
	}
	if v.Hierarchy == nil || len(labels) == 0 {
		return nil, nil, nil, nil
	}
	path, g := findGroup(v.Hierarchy, labels)
	if g == nil {
		return nil, nil, nil, nil
	}
	seen := map[string]bool{}
	var nodes, machines []string
	for _, i := range g.Nodes {
		if i < 0 || i >= len(v.Nodes) {
			continue
		}
		n := &v.Nodes[i]
		nodes = append(nodes, n.Name)
		if n.Machine != "" && !seen[n.Machine] {
			seen[n.Machine] = true
			machines = append(machines, n.Machine)
		}
	}
	sort.Strings(nodes)
	sort.Strings(machines)
	return path, nodes, machines, nil
}

// handleFamilyHistory gives the documents and the size of a family over time.
func (s *Server) handleFamilyHistory(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.window(r, "from", "to", week)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	points, err := intParam(r, "points", 200, 2, 2000)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pts, err := s.engine.FamilyHistory(r.Context(), r.PathValue("id"), r.PathValue("family"), from, to, points)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pts)
}

// handleIndexHistory gives the documents and the size of one index over time.
func (s *Server) handleIndexHistory(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.window(r, "from", "to", week)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	points, err := intParam(r, "points", 200, 2, 2000)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pts, err := s.engine.IndexSizeHistory(r.Context(), r.PathValue("id"), r.PathValue("uuid"), from, to, points)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pts)
}

// handleGroupHistory gives the metrics of the nodes of a group over time;
// the members are those of the group at the date `at`.
func (s *Server) handleGroupHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cluster := r.Context(), r.PathValue("id")
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
	points, err := intParam(r, "points", 300, 1, 2000)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	path, nodes, _, err := s.groupMembers(ctx, cluster, at, r.URL.Query()["g"])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if path == nil {
		writeError(w, http.StatusNotFound, "not_found", "unknown group")
		return
	}
	pts, err := s.engine.GroupMetrics(ctx, cluster, nodes, from, to, points)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, pts)
}

func (s *Server) handleGroup(w http.ResponseWriter, r *http.Request) {
	ctx, cluster := r.Context(), r.PathValue("id")
	at, err := s.at(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	path, nodes, machines, err := s.groupMembers(ctx, cluster, at, r.URL.Query()["g"])
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if path == nil {
		writeError(w, http.StatusNotFound, "not_found", "unknown group")
		return
	}
	d := GroupDetail{Path: path, Nodes: nodes, Machines: machines, Families: []FamilyOnNode{}}
	if d.Nodes == nil {
		d.Nodes = []string{}
	}
	if d.Machines == nil {
		d.Machines = []string{}
	}
	if st, err := s.engine.StateAt(ctx, cluster, at); err == nil {
		member := make(map[string]bool, len(nodes))
		for _, n := range nodes {
			member[n] = true
		}
		by := map[string]*FamilyOnNode{}
		for _, sh := range st.Shards {
			if sh.Node < 0 || !member[st.Nodes[sh.Node].Name] {
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
		sort.Slice(d.Families, func(i, j int) bool {
			if d.Families[i].Copies != d.Families[j].Copies {
				return d.Families[i].Copies > d.Families[j].Copies
			}
			return d.Families[i].Family < d.Families[j].Family
		})
	}
	writeJSON(w, http.StatusOK, d)
}
