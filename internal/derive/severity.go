package derive

import (
	"sort"

	"estopo/internal/model"
)

// Severity concerns, most severe first (SPEC §7.1).
const (
	ConcernUnassignedPrimaries = "unassigned_primaries" // 1: cluster red
	ConcernMissingNodes        = "missing_nodes"        // 2
	ConcernUnassignedReplicas  = "unassigned_replicas"  // 3
	ConcernHighWatermark       = "high_watermark"       // 4
	ConcernInfeasible          = "infeasible_constraints"
	ConcernMoving              = "moving_shards" // 6: initializing / relocating
)

var concernOrder = []string{ConcernUnassignedPrimaries, ConcernMissingNodes, ConcernUnassignedReplicas,
	ConcernHighWatermark, ConcernInfeasible, ConcernMoving}

// Concern is one reason for concern with its count.
type Concern struct {
	Code  string `json:"code"`
	Level int    `json:"level"` // 1 = most severe
	Count int    `json:"count"`
}

// Assessment is the severity of a cluster at T: Level is the most severe
// concern (1..6), 0 when healthy, -1 when unknown (failed capture).
type Assessment struct {
	Level    int       `json:"level"`
	Concerns []Concern `json:"concerns"`
}

// Assess computes the severity of a state. missing are the nodes missing
// against the reference (see MissingNodes); constraints may be nil.
func Assess(st *model.State, missing []string, constraints []IndexConstraint) Assessment {
	counts := map[string]int{}
	for _, sh := range st.Shards {
		switch {
		case sh.State == "UNASSIGNED" && sh.Primary:
			counts[ConcernUnassignedPrimaries]++
		case sh.State == "UNASSIGNED":
			counts[ConcernUnassignedReplicas]++
		case sh.State == "INITIALIZING" || sh.State == "RELOCATING":
			counts[ConcernMoving]++
		}
	}
	if len(st.Shards) == 0 && st.Health != nil { // structure unknown: fall back on health counters
		if st.Health.UnassignedPrimary > 0 {
			counts[ConcernUnassignedPrimaries] = st.Health.UnassignedPrimary
		} else if st.Health.Status == "red" {
			counts[ConcernUnassignedPrimaries] = 1
		}
		counts[ConcernUnassignedReplicas] = max(0, st.Health.Unassigned-counts[ConcernUnassignedPrimaries])
		counts[ConcernMoving] = st.Health.Initializing + st.Health.Relocating
	}
	counts[ConcernMissingNodes] = len(missing)
	c := newCtx(st)
	for _, i := range c.nodes {
		if c.aboveHigh(&st.Nodes[i]) {
			counts[ConcernHighWatermark]++
		}
	}
	for _, ic := range constraints {
		if ic.Status == Infeasible {
			counts[ConcernInfeasible]++
		}
	}
	a := Assessment{}
	for i, code := range concernOrder {
		if n := counts[code]; n > 0 {
			a.Concerns = append(a.Concerns, Concern{Code: code, Level: i + 1, Count: n})
			if a.Level == 0 {
				a.Level = i + 1
			}
		}
	}
	if st.Unknown {
		a.Level = -1
	}
	return a
}

// Ack is a node acknowledged as removed (SPEC §7.2).
type Ack struct {
	Node   string
	FromMs int64
}

// MissingNodes returns the nodes of the reference (names seen during the 7
// days before T, plus the declared ones) that are not present at T and not
// acknowledged as removed at T, sorted.
func MissingNodes(st *model.State, reference []string, acks []Ack) []string {
	present := map[string]bool{}
	for _, n := range st.Nodes {
		if n.Present {
			present[n.Name] = true
		}
	}
	acked := map[string]bool{}
	for _, a := range acks {
		if a.FromMs <= st.AtMs {
			acked[a.Node] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, name := range reference {
		if !present[name] && !acked[name] && !seen[name] {
			out = append(out, name)
			seen[name] = true
		}
	}
	sort.Strings(out)
	return out
}
