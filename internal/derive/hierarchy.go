package derive

import (
	"sort"
	"strings"

	"estopo/internal/model"
)

// Hierarchy levels (SPEC §7.4).
const (
	LevelAttr    = "attr"
	LevelTier    = "tier"
	LevelMachine = "machine"
	LevelNode    = "node"
)

// Level is one level of the navigation hierarchy.
type Level struct {
	Kind string `json:"kind"`
	Attr string `json:"attr,omitempty"` // for LevelAttr
}

// DefaultLevels: the awareness attributes in order, then tier, machine, node.
func DefaultLevels(st *model.State) []Level {
	var out []Level
	for _, a := range splitList(st.ClusterSettings["cluster.routing.allocation.awareness.attributes"]) {
		out = append(out, Level{Kind: LevelAttr, Attr: a})
	}
	return append(out, Level{Kind: LevelTier}, Level{Kind: LevelMachine}, Level{Kind: LevelNode})
}

// Group is a node of the hierarchy tree. Leaves (node level) list the node
// positions in State.Nodes.
type Group struct {
	Kind     string   `json:"kind"`
	Attr     string   `json:"attr,omitempty"`
	Label    string   `json:"label"`
	Children []*Group `json:"children,omitempty"`
	Nodes    []int    `json:"nodes"`
}

// Hierarchy groups the nodes of the state (present or missing) along the
// levels.
func Hierarchy(st *model.State, levels []Level) *Group {
	root := &Group{Kind: "cluster", Label: st.Cluster}
	all := make([]int, len(st.Nodes))
	for i := range all {
		all[i] = i
	}
	build(st, root, all, levels)
	return root
}

func label(st *model.State, n int, l Level) string {
	node := &st.Nodes[n]
	switch l.Kind {
	case LevelAttr:
		if v := node.Attrs[l.Attr]; v != "" {
			return v
		}
		return "-"
	case LevelTier:
		t := model.TiersOf(node.Roles)
		if len(t) == 0 {
			if node.HasRole("master") {
				return "master"
			}
			return "coordinating"
		}
		// "content" rarely matters when the node has a real tier.
		if len(t) > 1 {
			var real []string
			for _, x := range t {
				if x != "content" {
					real = append(real, x)
				}
			}
			if len(real) > 0 {
				t = real
			}
		}
		return strings.Join(t, "+")
	case LevelMachine:
		return node.Machine
	default:
		return node.Name
	}
}

func build(st *model.State, g *Group, nodes []int, levels []Level) {
	g.Nodes = nodes
	if len(levels) == 0 {
		return
	}
	l := levels[0]
	if l.Kind == LevelNode {
		return // leaves are the nodes themselves
	}
	byLabel := map[string][]int{}
	for _, n := range nodes {
		lb := label(st, n, l)
		byLabel[lb] = append(byLabel[lb], n)
	}
	labels := make([]string, 0, len(byLabel))
	for lb := range byLabel {
		labels = append(labels, lb)
	}
	sort.Strings(labels)
	for _, lb := range labels {
		child := &Group{Kind: l.Kind, Attr: l.Attr, Label: lb}
		build(st, child, byLabel[lb], levels[1:])
		g.Children = append(g.Children, child)
	}
}
