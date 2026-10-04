package sim

import (
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Control API of essim (not an Elasticsearch API; estopo never calls it):
//
//	GET  /_essim/topology  nodes, machines, data centres, remotes, families
//	POST /_essim/action    applies an Action now (JSON body)
//
// It only answers loopback clients: it changes the simulated cluster.

// TopologyNode describes a node for the control API.
type TopologyNode struct {
	Name    string `json:"name"`
	Short   string `json:"short"` // name without the cluster prefix, usable as a target
	Machine string `json:"machine"`
	DC      string `json:"dc"`
	Roles   string `json:"roles"`
	Version string `json:"version"`
	Up      bool   `json:"up"`
}

// Topology answers GET /_essim/topology.
type Topology struct {
	Cluster  string            `json:"cluster"`
	Version  string            `json:"version"`
	Now      time.Time         `json:"now"`
	Nodes    []TopologyNode    `json:"nodes"`
	DCs      []string          `json:"dcs"`
	Remotes  []string          `json:"remotes"`
	Families []string          `json:"families"`
	Actions  map[string]string `json:"actions"`
}

func (s *Server) control(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		http.Error(w, "the essim control API only answers loopback clients", http.StatusForbidden)
		return
	}
	c := s.Cluster
	c.mu.Lock()
	defer c.mu.Unlock()
	if s.Clock != nil {
		c.advanceTo(s.Clock.Now().UnixMilli())
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/_essim/topology":
		json.NewEncoder(w).Encode(c.topology())
	case r.Method == http.MethodPost && r.URL.Path == "/_essim/action":
		var a Action
		d := json.NewDecoder(r.Body)
		d.DisallowUnknownFields()
		if err := d.Decode(&a); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		res, err := s.apply(a)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		c.reroute(false)
		json.NewEncoder(w).Encode(map[string]string{"applied": a.Do, "detail": res.detail, "at": c.nowTime().Format(time.RFC3339)})
	default:
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"error": "GET /_essim/topology or POST /_essim/action"})
	}
}

func (c *Cluster) topology() Topology {
	t := Topology{Cluster: c.Name, Version: c.Version, Now: c.nowTime(), Actions: Actions, Nodes: []TopologyNode{},
		DCs: []string{}, Remotes: []string{}, Families: []string{}}
	dcs := map[string]bool{}
	for _, n := range c.Nodes {
		t.Nodes = append(t.Nodes, TopologyNode{Name: n.Name, Short: strings.TrimPrefix(n.Name, c.Name+"-"), Machine: n.Machine,
			DC: n.Attrs["dc"], Roles: strings.Join(n.Roles, ","), Version: n.Version, Up: n.Up})
		if dc := n.Attrs["dc"]; dc != "" && !dcs[dc] {
			dcs[dc] = true
			t.DCs = append(t.DCs, dc)
		}
	}
	sort.Slice(t.Nodes, func(i, j int) bool { return t.Nodes[i].Name < t.Nodes[j].Name })
	sort.Strings(t.DCs)
	for _, r := range c.Remotes {
		t.Remotes = append(t.Remotes, r.Alias)
	}
	fams := map[string]bool{}
	for _, ix := range c.Indices {
		f := ix.Name
		if i := strings.LastIndexAny(f, "-"); i > 0 {
			f = f[:i]
		}
		if !fams[f] {
			fams[f] = true
			t.Families = append(t.Families, f)
		}
	}
	sort.Strings(t.Families)
	return t
}
