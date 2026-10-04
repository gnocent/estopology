package derive

import (
	"testing"

	"estopo/internal/model"
)

// node builds a present node.
func node(name, machine, dc string, roles ...string) model.Node {
	return model.Node{Name: name, Machine: machine, Host: machine, Present: true, Roles: roles,
		Attrs:   map[string]string{"dc": dc, "machine": machine},
		Metrics: &model.NodeMetrics{DiskTotal: 1000, DiskUsed: 500}}
}

func index(name string, pri, rep int, settings map[string]string) model.Index {
	if settings == nil {
		settings = map[string]string{}
	}
	return model.Index{UUID: name, Name: name, Pri: pri, Rep: rep, Settings: settings, Status: "open"}
}

func hot(extra map[string]string) map[string]string {
	m := map[string]string{"index.routing.allocation.include._tier_preference": "data_hot"}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// Two DCs, two hot machines per DC with one node each, one warm machine in dc1
// with two nodes.
func baseState() *model.State {
	return &model.State{
		Cluster: "c", AtMs: 1000,
		ClusterSettings: map[string]string{"cluster.routing.allocation.awareness.attributes": "dc"},
		Nodes: []model.Node{
			node("h1", "m1", "dc1", "data_hot", "data_content"),
			node("h2", "m2", "dc1", "data_hot", "data_content"),
			node("h3", "m3", "dc2", "data_hot", "data_content"),
			node("h4", "m4", "dc2", "data_hot", "data_content"),
			node("w1", "m5", "dc1", "data_warm"),
			node("w2", "m5", "dc1", "data_warm"),
			node("master", "m6", "dc2", "master"),
		},
	}
}

func verdict(t *testing.T, st *model.State, ix model.Index) IndexConstraint {
	t.Helper()
	st.Indices = []model.Index{ix}
	return Constraints(st)[0]
}

func TestConstraints(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*model.State)
		ix     model.Index
		status string
		reason string
	}{
		{"healthy", nil, index("ok", 2, 1, hot(nil)), OK, ""},
		// B12: total_shards_per_node too low for the eligible nodes.
		{"tspn infeasible", nil, index("tspn", 3, 1, hot(map[string]string{"index.routing.allocation.total_shards_per_node": "1"})), Infeasible, ReasonTotalShards},
		// B12: feasible, but losing one node makes it infeasible.
		{"tspn tight", nil, index("tspn2", 2, 1, hot(map[string]string{"index.routing.allocation.total_shards_per_node": "1"})), Tight, ReasonLoseNode},
		{"no eligible node", nil, index("req", 1, 0, hot(map[string]string{"index.routing.allocation.require._name": "nope"})), Infeasible, ReasonNoEligibleNode},
		{"too many replicas", nil, index("rep", 1, 4, hot(nil)), Infeasible, ReasonNotEnoughNodes},
		// Warm nodes exist only in dc1 while awareness knows dc1 and dc2:
		// one copy per DC at most, the replica cannot be placed.
		{"awareness pitfall", nil, index("warm", 1, 1, map[string]string{"index.routing.allocation.include._tier_preference": "data_warm,data_hot"}), Infeasible, ReasonAwareness},
		{"same host", func(st *model.State) {
			st.ClusterSettings["cluster.routing.allocation.same_shard.host"] = "true"
			st.ClusterSettings["cluster.routing.allocation.awareness.attributes"] = ""
		}, index("warm2", 1, 1, map[string]string{"index.routing.allocation.include._tier_preference": "data_warm"}), Infeasible, ReasonSameHost},
		{"high watermark", func(st *model.State) {
			for i := 0; i < 3; i++ {
				st.Nodes[i].Metrics.DiskUsed = 950
			}
		}, index("full", 1, 1, hot(nil)), Infeasible, ReasonNotEnoughNodes},
		// Forced awareness with two machines per DC: losing one machine still
		// leaves a node in each DC (the one-machine-per-DC case is
		// TestConstraintTightByMachine).
		{"forced awareness ok", func(st *model.State) {
			st.ClusterSettings["cluster.routing.allocation.awareness.force.dc.values"] = "dc1,dc2"
		}, index("forced", 1, 1, hot(nil)), OK, ""},
		{"exclude by cluster filter", func(st *model.State) {
			st.ClusterSettings["cluster.routing.allocation.exclude._name"] = "h1,h2,h3"
		}, index("excl", 1, 1, hot(nil)), Infeasible, ReasonNotEnoughNodes},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := baseState()
			if tc.mutate != nil {
				tc.mutate(st)
			}
			v := verdict(t, st, tc.ix)
			if v.Status != tc.status || v.Reason != tc.reason {
				t.Errorf("got %s/%s %v, want %s/%s", v.Status, v.Reason, v.Params, tc.status, tc.reason)
			}
		})
	}
}

// Losing a machine that hosts both copies' only candidates in one DC.
func TestConstraintTightByMachine(t *testing.T) {
	st := baseState()
	// dc1: h1, h2 on the same machine m1; dc2: h3, h4 on distinct machines.
	st.Nodes[1].Machine = "m1"
	st.ClusterSettings["cluster.routing.allocation.awareness.force.dc.values"] = "dc1,dc2"
	v := verdict(t, st, index("x", 1, 1, hot(nil)))
	if v.Status != Tight || v.Reason != ReasonLoseMachine || v.Params["machine"] != "m1" {
		t.Errorf("got %+v", v)
	}
}

func TestSignatureCache(t *testing.T) {
	st := baseState()
	st.Indices = []model.Index{index("a", 1, 1, hot(nil)), index("b", 1, 1, hot(nil)), index("c", 3, 1, hot(map[string]string{"index.routing.allocation.total_shards_per_node": "1"}))}
	out := Constraints(st)
	if out[0].Index != 0 || out[1].Index != 1 || out[1].Status != OK || out[2].Status != Infeasible {
		t.Errorf("%+v", out)
	}
}

func TestAssess(t *testing.T) {
	st := baseState()
	if a := Assess(st, nil, nil); a.Level != 0 || len(a.Concerns) != 0 {
		t.Errorf("healthy: %+v", a)
	}
	st.Shards = []model.Shard{{Node: -1, State: "UNASSIGNED", Primary: false}, {Node: 0, State: "RELOCATING"}}
	a := Assess(st, []string{"gone"}, nil)
	if a.Level != 2 || len(a.Concerns) != 3 || a.Concerns[0].Code != ConcernMissingNodes {
		t.Errorf("missing node first: %+v", a)
	}
	st.Shards = append(st.Shards, model.Shard{Node: -1, State: "UNASSIGNED", Primary: true})
	if a := Assess(st, nil, nil); a.Level != 1 || a.Concerns[0].Count != 1 {
		t.Errorf("red: %+v", a)
	}
	st.Unknown = true
	if a := Assess(st, nil, nil); a.Level != -1 {
		t.Errorf("unknown: %+v", a)
	}
	// Without structure, health counters are used.
	st2 := baseState()
	st2.Health = &model.Health{Status: "red", Unassigned: 3, UnassignedPrimary: -1}
	if a := Assess(st2, nil, nil); a.Level != 1 {
		t.Errorf("health fallback: %+v", a)
	}
}

// B10: an acknowledged node is no longer missing from the acknowledgement date.
func TestMissingNodes(t *testing.T) {
	st := baseState()
	st.AtMs = 1000
	ref := []string{"h1", "h2", "gone1", "gone2", "gone1"}
	if got := MissingNodes(st, ref, nil); len(got) != 2 || got[0] != "gone1" || got[1] != "gone2" {
		t.Errorf("missing %v", got)
	}
	got := MissingNodes(st, ref, []Ack{{Node: "gone1", FromMs: 900}, {Node: "gone2", FromMs: 2000}})
	if len(got) != 1 || got[0] != "gone2" {
		t.Errorf("with acks %v", got)
	}
}

func TestHierarchy(t *testing.T) {
	st := baseState()
	tree := Hierarchy(st, DefaultLevels(st))
	if len(tree.Children) != 2 || tree.Children[0].Label != "dc1" || tree.Children[0].Kind != LevelAttr {
		t.Fatalf("root children %+v", tree.Children)
	}
	dc1 := tree.Children[0]
	if len(dc1.Children) != 2 || dc1.Children[0].Label != "hot" || dc1.Children[1].Label != "warm" {
		t.Fatalf("dc1 tiers %+v", dc1.Children)
	}
	warm := dc1.Children[1]
	if len(warm.Children) != 1 || warm.Children[0].Label != "m5" || len(warm.Children[0].Nodes) != 2 {
		t.Errorf("warm machines %+v", warm.Children)
	}
	dc2 := tree.Children[1]
	if dc2.Children[0].Label != "hot" || dc2.Children[1].Label != "master" {
		t.Errorf("dc2 tiers %+v", dc2.Children)
	}
}

func TestWatermarkParsing(t *testing.T) {
	for s, want := range map[string]float64{"90%": 90, "0.85": 85, "85.5%": 85.5} {
		if w := parseWatermark(s); w.pct != want {
			t.Errorf("%s: %+v", s, w)
		}
	}
	if w := parseWatermark("100gb"); w.minFree != 100<<30 {
		t.Errorf("bytes: %+v", w)
	}
}
