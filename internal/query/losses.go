package query

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Losses over [A, B] (SPEC §7.6, §8.2.7).
type Losses struct {
	A           int64          `json:"a"`
	B           int64          `json:"b"`
	Summary     LossSummary    `json:"summary"`
	Groups      []LossGroup    `json:"groups"`
	Deleted     []DeletedIndex `json:"deleted"` // voluntary deletions (separate filter)
	DeletedMore int            `json:"deleted_more,omitempty"`
	// Truncated: the window holds more than maxLossEvents events; the oldest
	// ones were left out, the result is incomplete (narrow the window).
	Truncated bool `json:"truncated,omitempty"`
}

// LossSummary counts the outcomes of the lost copies.
type LossSummary struct {
	Lost            int `json:"lost"`
	PrimariesLost   int `json:"primaries_lost"`   // primary copies lost
	NotPromoted     int `json:"not_promoted"`     // primaries lost without a promoted replica
	Promoted        int `json:"promoted"`         // replicas promoted
	Reallocated     int `json:"reallocated"`      // re-replicated elsewhere
	StillUnassigned int `json:"still_unassigned"` // at B
	DeletedIndices  int `json:"deleted_indices"`
}

// LossGroup gathers the copies lost with one node, or with a whole machine.
type LossGroup struct {
	Kind   string     `json:"kind"` // node, machine
	Name   string     `json:"name"`
	Nodes  []string   `json:"nodes"`
	LeftLo int64      `json:"left_lo,omitempty"`
	LeftHi int64      `json:"left_hi"`
	Copies []LostCopy `json:"copies"`
	More   int        `json:"more,omitempty"` // copies left out of the response (limit); the export has them all
}

// LostCopy is one copy that disappeared, with what happened next.
type LostCopy struct {
	UUID      string `json:"uuid"`
	Index     string `json:"index"`
	Shard     int    `json:"shard"`
	Primary   bool   `json:"primary"`
	Node      string `json:"node"`
	Zone      string `json:"zone,omitempty"` // first awareness attribute value
	SizeBytes int64  `json:"size_bytes"`     // -1 unknown
	TsLo      int64  `json:"ts_lo,omitempty"`
	TsHi      int64  `json:"ts_hi"`
	// After: promoted (a replica became primary), reallocated (new copy on
	// AfterNode at AfterTs), unassigned (still unassigned at B, with reason).
	After       string `json:"after"`
	AfterNode   string `json:"after_node,omitempty"`
	AfterTs     int64  `json:"after_ts,omitempty"`
	AfterReason string `json:"after_reason,omitempty"`
	Promoted    bool   `json:"promoted"` // for a lost primary: a replica was promoted
}

// DeletedIndex is an index deleted in the window.
type DeletedIndex struct {
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	Family string `json:"family"`
	Ts     int64  `json:"ts"`
}

type evPayload struct {
	Index    string   `json:"index"`
	Shard    int      `json:"shard"`
	Primary  bool     `json:"primary"`
	Node     string   `json:"node"`
	NodeLeft bool     `json:"node_left"`
	Name     string   `json:"name"`
	Nodes    []string `json:"nodes"`
	Reason   string   `json:"reason"`
}

// maxLossEvents bounds the events Losses loads in memory (a few hundred
// bytes each). The loss of a data centre of the reference cluster (250k
// shards), with the promotions and reallocations that follow, is about 250k
// of them. A variable for the tests.
var maxLossEvents = 500000

// Losses classifies the copies lost between a and b.
func (e *Engine) Losses(ctx context.Context, cluster string, a, b int64) (*Losses, error) {
	page, err := e.events(ctx, cluster, EventFilter{From: a, To: b, Limit: maxLossEvents,
		Types: []string{"shard_lost", "shard_promoted", "shard_assigned", "node_left", "machine_lost", "index_deleted"}})
	if err != nil {
		return nil, err
	}
	evs := page.Events
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].TsHi < evs[j].TsHi })
	out := &Losses{A: a, B: b, Truncated: page.NextKey != ""}

	stB, err := e.StateAt(ctx, cluster, b)
	if err != nil && err != ErrNoData {
		return nil, err
	}
	stA, _ := e.StateAt(ctx, cluster, a)
	zoneOf := map[string]string{}
	awareness := ""
	if stA != nil {
		if aw := strings.Split(stA.ClusterSettings["cluster.routing.allocation.awareness.attributes"], ","); len(aw) > 0 {
			awareness = strings.TrimSpace(aw[0])
		}
		for _, n := range stA.Nodes {
			zoneOf[n.Name] = n.Attrs[awareness]
		}
	}

	nodeLeft := map[string]Event{}
	machineOfNode := map[string]string{}
	machineEv := map[string]Event{}
	var lost []*LostCopy
	byShard := map[string][]*LostCopy{}
	// Two passes: the copies lost first, then what happened to them (events of
	// one capture share their date).
	for pass := 0; pass < 2; pass++ {
		for _, ev := range evs {
			if (pass == 0) != (ev.Type == "shard_lost" || ev.Type == "node_left" || ev.Type == "machine_lost" || ev.Type == "index_deleted") {
				continue
			}
			var p evPayload
			if len(ev.Payload) > 0 {
				json.Unmarshal(ev.Payload, &p)
			}
			switch ev.Type {
			case "node_left":
				nodeLeft[ev.Ref] = ev
			case "machine_lost":
				machineEv[ev.Ref] = ev
				for _, n := range p.Nodes {
					machineOfNode[n] = ev.Ref
				}
			case "index_deleted":
				fam := ev.Group
				out.Deleted = append(out.Deleted, DeletedIndex{UUID: ev.Ref, Name: p.Name, Family: fam, Ts: ev.TsHi})
			case "shard_lost":
				uuid := ev.Ref // "uuid/shard"
				if i := strings.LastIndexByte(uuid, '/'); i >= 0 {
					uuid = uuid[:i]
				}
				lc := &LostCopy{UUID: uuid, Index: p.Index, Shard: p.Shard, Primary: p.Primary, Node: p.Node, Zone: zoneOf[p.Node],
					SizeBytes: -1, TsLo: ev.TsLo, TsHi: ev.TsHi, After: "unassigned"}
				lost = append(lost, lc)
				byShard[ev.Ref] = append(byShard[ev.Ref], lc)
			case "shard_promoted":
				for _, lc := range byShard[ev.Ref] {
					if lc.Primary && !lc.Promoted {
						lc.Promoted = true
						break
					}
				}
			case "shard_assigned":
				for _, lc := range byShard[ev.Ref] {
					if lc.After == "unassigned" && lc.AfterNode == "" {
						lc.After, lc.AfterNode, lc.AfterTs = "reallocated", p.Node, ev.TsHi
						break
					}
				}
			}
		}
	}
	// Still unassigned at B: reason from the state.
	if stB != nil {
		reasons := map[string]string{}
		for _, sh := range stB.Shards {
			if sh.Node < 0 {
				reasons[stB.Indices[sh.Index].UUID+"/"+strconv.Itoa(int(sh.Shard))] = sh.Reason
			}
		}
		for _, lc := range lost {
			if lc.After == "unassigned" {
				lc.AfterReason = reasons[lc.UUID+"/"+strconv.Itoa(lc.Shard)]
			}
		}
	}
	// A lost primary replaced by a promoted replica: the copy to rebuild is
	// a replica; the primary itself is "promoted".
	for _, lc := range lost {
		if lc.Primary && lc.Promoted && lc.After == "unassigned" {
			lc.After = "promoted"
		}
	}
	e.fillLostSizes(ctx, cluster, lost)

	// Groups: machine when the whole machine left, else node.
	groups := map[string]*LossGroup{}
	for _, lc := range lost {
		gk, g := "node:"+lc.Node, (*LossGroup)(nil)
		if m, ok := machineOfNode[lc.Node]; ok {
			gk = "machine:" + m
		}
		if g = groups[gk]; g == nil {
			g = &LossGroup{Kind: "node", Name: lc.Node}
			if strings.HasPrefix(gk, "machine:") {
				m := strings.TrimPrefix(gk, "machine:")
				ev := machineEv[m]
				var p evPayload
				json.Unmarshal(ev.Payload, &p)
				g = &LossGroup{Kind: "machine", Name: m, Nodes: p.Nodes, LeftLo: ev.TsLo, LeftHi: ev.TsHi}
			} else if ev, ok := nodeLeft[lc.Node]; ok {
				g.Nodes, g.LeftLo, g.LeftHi = []string{lc.Node}, ev.TsLo, ev.TsHi
			} else {
				g.Nodes, g.LeftHi = []string{lc.Node}, lc.TsHi
			}
			groups[gk] = g
		}
		g.Copies = append(g.Copies, *lc)
	}
	for _, g := range groups {
		sort.Slice(g.Copies, func(i, j int) bool {
			a, b := g.Copies[i], g.Copies[j]
			if a.Node != b.Node {
				return a.Node < b.Node
			}
			if a.Index != b.Index {
				return a.Index < b.Index
			}
			return a.Shard < b.Shard
		})
		out.Groups = append(out.Groups, *g)
	}
	sort.Slice(out.Groups, func(i, j int) bool {
		if len(out.Groups[i].Copies) != len(out.Groups[j].Copies) {
			return len(out.Groups[i].Copies) > len(out.Groups[j].Copies)
		}
		return out.Groups[i].Name < out.Groups[j].Name
	})
	for _, lc := range lost {
		out.Summary.Lost++
		if lc.Primary {
			out.Summary.PrimariesLost++
			if !lc.Promoted {
				out.Summary.NotPromoted++
			}
		}
		switch lc.After {
		case "promoted":
			out.Summary.Promoted++
		case "reallocated":
			out.Summary.Reallocated++
		default:
			out.Summary.StillUnassigned++
		}
	}
	out.Summary.DeletedIndices = len(out.Deleted)
	return out, nil
}

// fillLostSizes sets the last known size of each lost copy.
func (e *Engine) fillLostSizes(ctx context.Context, cluster string, lost []*LostCopy) {
	if len(lost) == 0 || len(lost) > 20000 {
		return
	}
	for _, lc := range lost {
		p, _, err := e.lastCapture(ctx, cluster, lc.TsHi, "1=1")
		if err != nil || p.p == nil {
			continue
		}
		prirep := "r"
		if lc.Primary {
			prirep = "p"
		}
		var size int64
		err = p.db().QueryRowContext(ctx, `SELECT m.store_bytes FROM shard_metric m JOIN idx i ON i.id = m.index_id
			JOIN node n ON n.id = m.node_id WHERE i.uuid = ? AND m.shard = ? AND m.prirep = ? AND n.name = ? AND m.ts <= ?
			ORDER BY m.ts DESC LIMIT 1`, lc.UUID, lc.Shard, prirep, lc.Node, lc.TsHi).Scan(&size)
		if err == nil {
			lc.SizeBytes = size
		}
	}
}
