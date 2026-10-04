package store

import (
	"sort"
	"strconv"
)

// shardEvents derives the shard events of one capture from the intervals it
// opened and closed (SPEC §6.5, §7.6), shard by shard:
//   - same key closed and reopened: a state change on the same node, no event;
//   - replica closed and primary opened on the same node: promoted;
//   - copy closed on a node still present, reopened elsewhere: relocated;
//   - unassigned copy closed, copy opened on a node: assigned;
//   - assigned copy closed without successor: lost (its node left, or it
//     vanished); a primary lost without promotion is critical;
//   - unassigned copy opened: unassigned (reason, exact date).
//
// Copies of indices created or deleted in the same capture, and copies
// removed by a lower replica count, produce no shard event.
func (w *writer) shardEvents(opened, closed []shardChange) error {
	type gk struct {
		uuid  string
		shard int
	}
	groups := map[gk]*struct{ op, cl []shardChange }{}
	add := func(c shardChange, isOpen bool) {
		k := gk{c.key.uuid, c.key.shard}
		if w.createdIdx[k.uuid] || w.deletedIdx[k.uuid] {
			return
		}
		g := groups[k]
		if g == nil {
			g = &struct{ op, cl []shardChange }{}
			groups[k] = g
		}
		if isOpen {
			g.op = append(g.op, c)
		} else {
			g.cl = append(g.cl, c)
		}
	}
	for _, c := range opened {
		add(c, true)
	}
	for _, c := range closed {
		add(c, false)
	}
	keys := make([]gk, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].uuid != keys[j].uuid {
			return keys[i].uuid < keys[j].uuid
		}
		return keys[i].shard < keys[j].shard
	})

	cs := w.cs
	lo := cs.lastFullTs
	assigned := func(c shardChange) bool { return c.key.node != (nodeKey{}) }
	present := func(n nodeKey) bool { ns := cs.nodes[n]; return ns != nil && ns.present }

	for _, k := range keys {
		g := groups[k]
		is := cs.indices[k.uuid]
		name := ""
		if is != nil {
			name = is.name
		}
		ref := k.uuid + "/" + strconv.Itoa(k.shard)
		payload := func(c shardChange, extra map[string]any) map[string]any {
			p := map[string]any{"index": name, "shard": k.shard, "primary": c.key.primary}
			for kk, v := range extra {
				p[kk] = v
			}
			return p
		}
		sort.Slice(g.cl, func(i, j int) bool { return shardKeyLess(g.cl[i].key, g.cl[j].key) })
		sort.Slice(g.op, func(i, j int) bool { return shardKeyLess(g.op[i].key, g.op[j].key) })
		usedOp := make([]bool, len(g.op))
		usedCl := make([]bool, len(g.cl))

		// Same key: state change on the same copy.
		for i, c := range g.cl {
			for j, o := range g.op {
				if !usedOp[j] && o.key == c.key {
					usedCl[i], usedOp[j] = true, true
					break
				}
			}
		}
		// Promotion: replica closed, primary opened on the same node.
		promoted := false
		for i, c := range g.cl {
			if usedCl[i] || !assigned(c) || c.key.primary {
				continue
			}
			for j, o := range g.op {
				if !usedOp[j] && assigned(o) && o.key.primary && o.key.node == c.key.node {
					usedCl[i], usedOp[j], promoted = true, true, true
					if err := w.event(evShardPromoted, sevMinor, "shard", ref, c.key.node.name, lo, 0,
						payload(o, map[string]any{"node": c.key.node.name})); err != nil {
						return err
					}
					break
				}
			}
		}
		// Relocation: closed on a node still present, reopened elsewhere.
		for i, c := range g.cl {
			if usedCl[i] || !assigned(c) || !present(c.key.node) {
				continue
			}
			for j, o := range g.op {
				if !usedOp[j] && assigned(o) && o.key.primary == c.key.primary && o.key.node != c.key.node {
					usedCl[i], usedOp[j] = true, true
					if err := w.event(evShardRelocated, sevInfo, "shard", ref, c.key.node.name, lo, 0,
						payload(o, map[string]any{"from": c.key.node.name, "to": o.key.node.name})); err != nil {
						return err
					}
					break
				}
			}
		}
		// Assignment: an unassigned copy found a node.
		for i, c := range g.cl {
			if usedCl[i] || assigned(c) {
				continue
			}
			for j, o := range g.op {
				if !usedOp[j] && assigned(o) && o.key.primary == c.key.primary {
					usedCl[i], usedOp[j] = true, true
					if err := w.event(evShardAssigned, sevInfo, "shard", ref, o.key.node.name, lo, 0,
						payload(o, map[string]any{"node": o.key.node.name, "state": o.vals.state, "was": c.vals.reason})); err != nil {
						return err
					}
					break
				}
			}
		}
		// Losses.
		for i, c := range g.cl {
			if usedCl[i] || !assigned(c) {
				continue
			}
			usedCl[i] = true
			if w.repDecreased[k.uuid] && !c.key.primary {
				continue
			}
			sev := sevMajor
			if c.key.primary && !promoted {
				sev = sevCritical
			}
			if err := w.event(evShardLost, sev, "shard", ref, c.key.node.name, lo, 0,
				payload(c, map[string]any{"node": c.key.node.name, "node_left": !present(c.key.node)})); err != nil {
				return err
			}
		}
		// New unassigned copies and remaining new assignments.
		for j, o := range g.op {
			if usedOp[j] {
				continue
			}
			if !assigned(o) {
				sev := sevMinor
				if o.key.primary {
					sev = sevCritical
				}
				group := ""
				if o.vals.reason == "NODE_LEFT" {
					for _, c := range g.cl {
						if assigned(c) && !present(c.key.node) {
							group = c.key.node.name
						}
					}
				}
				if err := w.event(evShardUnassigned, sev, "shard", ref, group, lo, o.vals.at,
					payload(o, map[string]any{"reason": o.vals.reason})); err != nil {
					return err
				}
				continue
			}
			if err := w.event(evShardAssigned, sevInfo, "shard", ref, o.key.node.name, lo, 0,
				payload(o, map[string]any{"node": o.key.node.name, "state": o.vals.state})); err != nil {
				return err
			}
		}
	}
	return nil
}
