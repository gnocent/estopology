package sim

import (
	"math"
	"path"
	"sort"
	"strings"
	"time"
)

// A simplified Elasticsearch allocator. It follows the deciders estopo reasons
// about in its allocation constraints (SPEC §7.5): tier preference,
// include/exclude/require filters (index and cluster), same node, same host,
// awareness, total_shards_per_node, disk watermarks, allocation enable, and
// recovery throttling. Balancing is by shard count within a tier.
//
// It is incremental so that a 250,000-shard cluster can be simulated for
// weeks in minutes: unassigned copies and indices to re-check are tracked in
// sets, disk usage is accounted per node (final shard sizes), and per-index
// filters are parsed once per reroute.

type allocCtx struct {
	c           *Cluster
	tierCount   map[string]int
	tierNodes   map[string][]*Node // up data nodes per data role, generic "data" nodes included
	awareness   []string
	awareValues map[string]int
	sameHost    bool
	clusterTSPN int
	lowWM       float64
	highWM      float64
	enable      string
	maxRec      int
	filters     []filter // cluster-level
	idx         map[*Index]*idxInfo
	free        map[string]bool // freeSlots by tier, until a recovery starts
}

type idxInfo struct {
	tier    string
	filters []filter
	tspn    int
	enable  string
}

// idxStatic is the part of idxInfo that only depends on the index settings.
type idxStatic struct {
	tiers   []string // tier preference, in order
	filters []filter
	tspn    int
	enable  string
}

type filter struct {
	op     string // require, include, exclude
	attr   string
	values []string
}

var dataRoles = []string{"data_hot", "data_warm", "data_cold", "data_content", "data_frozen"}

func (c *Cluster) newCtx() *allocCtx {
	a := &allocCtx{
		c: c, tierCount: map[string]int{}, tierNodes: map[string][]*Node{}, awareValues: map[string]int{},
		idx:         map[*Index]*idxInfo{},
		sameHost:    c.setting("cluster.routing.allocation.same_shard.host") == "true",
		clusterTSPN: c.settingInt("cluster.routing.allocation.total_shards_per_node"),
		lowWM:       parsePercent(c.setting("cluster.routing.allocation.disk.watermark.low")),
		highWM:      parsePercent(c.setting("cluster.routing.allocation.disk.watermark.high")),
		enable:      c.setting("cluster.routing.allocation.enable"),
		maxRec:      max(1, c.settingInt("cluster.routing.allocation.node_concurrent_recoveries")),
	}
	for _, attr := range strings.Split(c.setting("cluster.routing.allocation.awareness.attributes"), ",") {
		if attr = strings.TrimSpace(attr); attr != "" {
			a.awareness = append(a.awareness, attr)
		}
	}
	values := map[string]map[string]bool{}
	for _, n := range c.Nodes {
		if !n.Up || !n.IsData() {
			continue
		}
		generic := n.HasRole("data")
		for _, r := range dataRoles {
			if generic || n.HasRole(r) {
				a.tierCount[r]++
				a.tierNodes[r] = append(a.tierNodes[r], n)
			}
		}
		for _, attr := range a.awareness {
			if v := n.Attrs[attr]; v != "" {
				if values[attr] == nil {
					values[attr] = map[string]bool{}
				}
				values[attr][v] = true
			}
		}
	}
	for _, attr := range a.awareness {
		a.awareValues[attr] = len(values[attr])
		if forced := c.setting("cluster.routing.allocation.awareness.force." + attr + ".values"); forced != "" {
			a.awareValues[attr] = len(strings.Split(forced, ","))
		}
	}
	a.filters = parseFilters(c.mergedSettings(), "cluster.routing.allocation.")
	return a
}

func (a *allocCtx) info(ix *Index) *idxInfo {
	// Kept on the index for the life of this context (no map lookup per copy).
	if ix.infoCtx == a && ix.info != nil {
		return ix.info
	}
	// The parsed settings are kept on the index (markIndex drops them when a
	// setting changes); only the tier depends on the nodes present now.
	st := ix.static
	if st == nil {
		st = &idxStatic{
			filters: parseFilters(ix.Settings, "index.routing.allocation."),
			tspn:    atoiDefault(ix.Setting("index.routing.allocation.total_shards_per_node", "-1"), -1),
			enable:  ix.Setting("index.routing.allocation.enable", ""),
		}
		if pref := ix.Setting(tierPrefKey, ""); pref != "" {
			for _, t := range strings.Split(pref, ",") {
				st.tiers = append(st.tiers, strings.TrimSpace(t))
			}
		}
		ix.static = st
	}
	in := &idxInfo{filters: st.filters, tspn: st.tspn, enable: st.enable, tier: "data_content"}
	if len(st.tiers) > 0 {
		in.tier = st.tiers[0]
		for _, t := range st.tiers {
			if a.tierCount[t] > 0 {
				in.tier = t
				break
			}
		}
	}
	ix.info, ix.infoCtx = in, a
	return in
}

func (c *Cluster) mergedSettings() map[string]string {
	m := make(map[string]string, len(c.Persistent)+len(c.Transient))
	for k, v := range c.Persistent {
		m[k] = v
	}
	for k, v := range c.Transient {
		m[k] = v
	}
	return m
}

func parseFilters(settings map[string]string, prefix string) []filter {
	var out []filter
	for k, v := range settings {
		if !strings.HasPrefix(k, prefix) || v == "" {
			continue
		}
		for _, op := range []string{"require", "include", "exclude"} {
			p := prefix + op + "."
			if strings.HasPrefix(k, p) {
				attr := strings.TrimPrefix(k, p)
				if attr == "_tier_preference" {
					continue
				}
				var vals []string
				for _, x := range strings.Split(v, ",") {
					if x = strings.TrimSpace(x); x != "" {
						vals = append(vals, x)
					}
				}
				out = append(out, filter{op: op, attr: attr, values: vals})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].op+out[i].attr < out[j].op+out[j].attr })
	return out
}

func nodeAttr(n *Node, attr string) string {
	switch attr {
	case "_name":
		return n.Name
	case "_host", "_host_name":
		return n.Machine
	case "_ip", "_host_ip", "_publish_ip":
		return n.IP
	case "_id":
		return n.ID
	}
	return n.Attrs[attr]
}

func matchAny(v string, patterns []string) bool {
	for _, p := range patterns {
		if p == v {
			return true
		}
		if strings.ContainsAny(p, "*?[") {
			if ok, _ := path.Match(p, v); ok {
				return true
			}
		}
	}
	return false
}

// filtersAllow applies require (AND), include (OR) and exclude (OR) filters.
func filtersAllow(n *Node, fs []filter) (bool, string) {
	if len(fs) == 0 {
		return true, ""
	}
	var includes []filter
	for _, f := range fs {
		v := nodeAttr(n, f.attr)
		switch f.op {
		case "require":
			if !matchAny(v, f.values) {
				return false, "require." + f.attr
			}
		case "exclude":
			if v != "" && matchAny(v, f.values) {
				return false, "exclude." + f.attr
			}
		case "include":
			includes = append(includes, f)
		}
	}
	if len(includes) == 0 {
		return true, ""
	}
	for _, f := range includes {
		if v := nodeAttr(n, f.attr); v != "" && matchAny(v, f.values) {
			return true, ""
		}
	}
	return false, "include." + includes[0].attr
}

// diskUsed is the accounted disk usage of a node (final shard sizes plus ~2%
// of non-index data).
func diskUsed(n *Node) int64 { return n.reserved + n.DiskTotal/50 }

// decide reports whether cp may be on n. remain=true checks an existing
// placement (throttling and the low watermark do not apply).
func (a *allocCtx) decide(cp *Copy, n *Node, remain bool) (bool, string) {
	ix := cp.Index
	in := a.info(ix)
	if !n.Up || !n.IsData() {
		return false, "node"
	}
	if !n.HasRole(in.tier) && !n.HasRole("data") {
		return false, "tier"
	}
	if ok, why := filtersAllow(n, in.filters); !ok {
		return false, "index." + why
	}
	if ok, why := filtersAllow(n, a.filters); !ok {
		return false, "cluster." + why
	}
	copies := ix.Shards[cp.Shard]
	for _, o := range copies {
		if o == cp {
			continue
		}
		if o.Node == n || o.Target == n {
			return false, "same_shard"
		}
		if a.sameHost && ((o.Node != nil && o.Node.Machine == n.Machine) || (o.Target != nil && o.Target.Machine == n.Machine)) {
			return false, "same_host"
		}
	}
	for _, attr := range a.awareness {
		nv := a.awareValues[attr]
		v := n.Attrs[attr]
		if nv == 0 || v == "" {
			continue
		}
		limit := (len(copies) + nv - 1) / nv
		count := 1
		for _, o := range copies {
			if o == cp {
				continue
			}
			if (o.Node != nil && o.Node.Up && o.Node.Attrs[attr] == v && o.State != Unassigned) || (o.Target != nil && o.Target.Attrs[attr] == v) {
				count++
			}
		}
		if count > limit {
			return false, "awareness." + attr
		}
	}
	if in.tspn >= 0 {
		on := ix.copiesOn(n)
		if remain {
			on--
		}
		if on >= in.tspn {
			return false, "index.total_shards_per_node"
		}
	}
	if a.clusterTSPN >= 0 {
		held := len(n.copies)
		if remain {
			held--
		}
		if held >= a.clusterTSPN {
			return false, "cluster.total_shards_per_node"
		}
	}
	if n.DiskTotal > 0 {
		pct := float64(diskUsed(n)) * 100 / float64(n.DiskTotal)
		if remain && !math.IsNaN(a.highWM) && pct > a.highWM {
			return false, "disk.high"
		}
		if !remain && !math.IsNaN(a.lowWM) && pct > a.lowWM {
			return false, "disk.low"
		}
	}
	if !remain && n.recoveries >= a.maxRec {
		return false, "throttled"
	}
	return true, ""
}

// best returns the least loaded node of the right tier that may take cp.
func (a *allocCtx) best(cp *Copy) *Node {
	var bestN *Node
	bestOn, bestLoad := 0, 0
	for _, n := range a.tierNodes[a.info(cp.Index).tier] {
		if n.recoveries >= a.maxRec {
			continue
		}
		if ok, _ := a.decide(cp, n, false); !ok {
			continue
		}
		on, load := cp.Index.copiesOn(n), len(n.copies)
		if bestN == nil || on < bestOn || (on == bestOn && (load < bestLoad || (load == bestLoad && n.Name < bestN.Name))) {
			bestN, bestOn, bestLoad = n, on, load
		}
	}
	return bestN
}

// freeSlots reports whether some node of the tier can start a recovery.
func (a *allocCtx) freeSlots(tier string) bool {
	if v, ok := a.free[tier]; ok {
		return v
	}
	free := false
	for _, n := range a.tierNodes[tier] {
		if n.recoveries < a.maxRec {
			free = true
			break
		}
	}
	if a.free == nil {
		a.free = map[string]bool{}
	}
	a.free[tier] = free
	return free
}

// started forgets the cached free slots after a recovery started.
func (a *allocCtx) started() { a.free = nil }

// allocatable reports whether an unassigned copy may be allocated now.
func (a *allocCtx) allocatable(cp *Copy) bool {
	ix := cp.Index
	enable := a.enable
	if in := a.info(ix); in.enable != "" {
		enable = in.enable
	}
	switch enable {
	case "none":
		return false
	case "primaries":
		if !cp.Primary {
			return false
		}
	case "new_primaries":
		if !cp.Primary || cp.UReason != "INDEX_CREATED" {
			return false
		}
	}
	if !cp.Primary {
		active := false
		for _, o := range ix.Shards[cp.Shard] {
			if o.Primary && (o.State == Started || o.State == Relocating) {
				active = true
			}
		}
		if !active {
			return false
		}
		if cp.DelayMs > a.c.nowMs && (cp.LastNode == nil || !cp.LastNode.Up) {
			return false
		}
	}
	return true
}

// target picks the node for an unassigned copy: its previous node if it came
// back (data still there), otherwise the best node. A primary that lost its
// data (no replica was promoted) can only return to its previous node.
func (a *allocCtx) target(cp *Copy) *Node {
	if cp.LastNode != nil && cp.LastNode.Up {
		ok, why := a.decide(cp, cp.LastNode, false)
		if ok {
			return cp.LastNode
		}
		if why == "throttled" { // wait for the node that still has the data
			return nil
		}
	}
	if cp.Primary && cp.UReason == "NODE_LEFT" {
		return nil
	}
	return a.best(cp)
}

// --- bookkeeping ----------------------------------------------------------------

// assign places cp on n in the given state.
func (c *Cluster) assign(cp *Copy, n *Node, state string) {
	cp.Node, cp.State = n, state
	n.copies[cp] = struct{}{}
	n.reserved += cp.Index.ShardBytes
	delete(c.unassigned, cp)
}

// unassign detaches cp from its node(s) and marks it unassigned.
func (c *Cluster) unassign(cp *Copy) {
	if cp.Node != nil {
		delete(cp.Node.copies, cp)
		cp.Node.reserved -= cp.Index.ShardBytes
		if cp.State != Started {
			cp.Node.recoveries--
		}
	}
	if cp.Target != nil {
		cp.Target.reserved -= cp.Index.ShardBytes
		cp.Target.recoveries--
		c.relocating--
	}
	cp.Node, cp.Target, cp.State, cp.DoneMs = nil, nil, Unassigned, -1
	c.unassigned[cp] = struct{}{}
	c.unSortedOK = false
}

// placeAll allocates every unassigned copy instantly (initial state).
func (c *Cluster) placeAll() {
	a := c.newCtx()
	for _, cp := range c.unassignedCopies() {
		if n := a.target(cp); n != nil {
			c.assign(cp, n, Started)
			cp.UReason, cp.UAtMs, cp.UDetails, cp.LastNode = "", 0, "", nil
		}
	}
	c.dirty = false
}

// unassignedCopies returns the unassigned copies in a deterministic order:
// primaries first, then by index creation, name and shard.
func (c *Cluster) unassignedCopies() []*Copy {
	// The order only changes when a copy becomes unassigned: until then the
	// sorted list is reused, minus the copies allocated or deleted since.
	if c.unSortedOK {
		kept := c.unSorted[:0]
		for _, cp := range c.unSorted {
			if _, ok := c.unassigned[cp]; ok {
				kept = append(kept, cp)
			}
		}
		c.unSorted = kept
		return kept
	}
	out := make([]*Copy, 0, len(c.unassigned))
	for cp := range c.unassigned {
		out = append(out, cp)
	}
	defer func() { c.unSorted, c.unSortedOK = out, true }()
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if x.Primary != y.Primary {
			return x.Primary
		}
		if x.Index.CreatedMs != y.Index.CreatedMs {
			return x.Index.CreatedMs < y.Index.CreatedMs
		}
		if x.Index.Name != y.Index.Name {
			return x.Index.Name < y.Index.Name
		}
		if x.Shard != y.Shard {
			return x.Shard < y.Shard
		}
		return copyRank(x) < copyRank(y)
	})
	return out
}

func copyRank(cp *Copy) int {
	for i, o := range cp.Index.Shards[cp.Shard] {
		if o == cp {
			return i
		}
	}
	return 0
}

// markIndex asks the next reroute to check whether the copies of ix may stay.
func (c *Cluster) markIndex(ix *Index) {
	ix.static = nil
	c.checkIdx[ix] = struct{}{}
	c.dirty = true
}

// markAll asks the next reroute to check every copy (cluster-wide change).
func (c *Cluster) markAll() {
	c.checkAll = true
	c.dirty = true
}

// reroute moves copies that cannot stay, allocates unassigned copies and,
// when rebalance is set, balances shard counts within each tier.
func (c *Cluster) reroute(rebalance bool) {
	c.dirty = false
	c.reroutes++
	c.lastReroute = c.nowMs
	a := c.newCtx()

	// 1. Copies that can no longer stay where they are.
	if c.checkAll || len(c.checkIdx) > 0 {
		all := c.checkAll
		c.checkAll = false
		for _, ix := range c.Indices {
			if _, marked := c.checkIdx[ix]; !all && !marked {
				continue
			}
			pending := false
			for _, sh := range ix.Shards {
				for _, cp := range sh {
					if cp.State != Started || cp.Node == nil {
						if cp.State == Relocating {
							pending = true
						}
						continue
					}
					if ok, _ := a.decide(cp, cp.Node, true); ok {
						continue
					}
					pending = true
					if cp.Node.recoveries >= a.maxRec || !a.freeSlots(a.info(ix).tier) {
						continue
					}
					if n := a.best(cp); n != nil {
						c.relocate(cp, n)
						a.started()
					}
				}
			}
			if pending {
				c.checkIdx[ix] = struct{}{}
			} else {
				delete(c.checkIdx, ix)
			}
		}
	}

	// 2. Unassigned copies.
	for _, cp := range c.unassignedCopies() {
		if cp.State != Unassigned || !a.allocatable(cp) {
			continue
		}
		// Without a node that still holds the data, the copy needs a free
		// recovery slot in its tier: no need to examine every node otherwise.
		if (cp.LastNode == nil || !cp.LastNode.Up) && !a.freeSlots(a.info(cp.Index).tier) {
			continue
		}
		if n := a.target(cp); n != nil {
			c.initialize(cp, n)
			a.started()
		}
	}

	// 3. Balance.
	if rebalance && c.setting("cluster.routing.rebalance.enable") != "none" {
		c.rebalance(a)
	}
}

func (c *Cluster) recoveryMs(bytes int64) int64 {
	rate := parseByteRate(c.setting("indices.recovery.max_bytes_per_sec"))
	return max(int64(float64(bytes)/rate*1000), 1000)
}

// initialize starts the recovery of an unassigned copy on n.
func (c *Cluster) initialize(cp *Copy, n *Node) {
	c.assign(cp, n, Initializing)
	n.recoveries++
	var dur int64
	switch {
	case cp.LastNode == n: // data still on disk
		dur = 2000
	case cp.Primary:
		dur = 1000 // new empty primary
	default:
		dur = c.recoveryMs(cp.Index.bytesAt(c.nowMs))
	}
	done := c.nowMs + dur
	cp.DoneMs = done
	c.schedule(done, func() {
		if cp.DoneMs != done || cp.State != Initializing {
			return
		}
		cp.State = Started
		cp.UReason, cp.UAtMs, cp.UDetails, cp.LastNode, cp.DelayMs = "", 0, "", nil, 0
		n.recoveries--
		c.dirty = true
	})
}

// relocate starts moving a started copy to n.
func (c *Cluster) relocate(cp *Copy, n *Node) {
	src := cp.Node
	cp.State, cp.Target = Relocating, n
	src.recoveries++
	n.recoveries++
	n.reserved += cp.Index.ShardBytes
	c.relocating++
	done := c.nowMs + c.recoveryMs(cp.Index.bytesAt(c.nowMs))
	cp.DoneMs = done
	c.schedule(done, func() {
		if cp.DoneMs != done || cp.State != Relocating {
			return
		}
		delete(src.copies, cp)
		src.reserved -= cp.Index.ShardBytes
		src.recoveries--
		n.recoveries--
		c.relocating--
		cp.Node, cp.Target, cp.State = n, nil, Started
		n.copies[cp] = struct{}{}
		c.dirty = true
	})
}

// cancelRelocation stops a relocation; the copy stays on its source.
func (c *Cluster) cancelRelocation(cp *Copy) {
	if cp.State != Relocating || cp.Target == nil {
		return
	}
	cp.Target.reserved -= cp.Index.ShardBytes
	cp.Target.recoveries--
	cp.Node.recoveries--
	c.relocating--
	cp.Target, cp.State, cp.DoneMs = nil, Started, -1
}

// rebalance moves shards from the most to the least loaded node of each tier.
func (c *Cluster) rebalance(a *allocCtx) {
	limit := max(1, c.settingInt("cluster.routing.allocation.cluster_concurrent_rebalance"))
	for _, tier := range []string{"data_hot", "data_warm", "data_cold", "data_content"} {
		if c.relocating >= limit {
			return
		}
		nodes := append([]*Node(nil), a.tierNodes[tier]...)
		if len(nodes) < 2 {
			continue
		}
		sort.Slice(nodes, func(i, j int) bool {
			if len(nodes[i].copies) != len(nodes[j].copies) {
				return len(nodes[i].copies) > len(nodes[j].copies)
			}
			return nodes[i].Name < nodes[j].Name
		})
		from, to := nodes[0], nodes[len(nodes)-1]
		if len(from.copies)-len(to.copies) <= 1 || from.recoveries >= a.maxRec || to.recoveries >= a.maxRec {
			continue
		}
		cands := make([]*Copy, 0, len(from.copies))
		for cp := range from.copies {
			if cp.State == Started && a.info(cp.Index).tier == tier {
				cands = append(cands, cp)
			}
		}
		sort.Slice(cands, func(i, j int) bool {
			x, y := cands[i], cands[j]
			if x.Index.CreatedMs != y.Index.CreatedMs {
				return x.Index.CreatedMs < y.Index.CreatedMs
			}
			if x.Index.Name != y.Index.Name {
				return x.Index.Name < y.Index.Name
			}
			return x.Shard < y.Shard
		})
		for _, cp := range cands {
			if ok, _ := a.decide(cp, to, false); ok {
				c.relocate(cp, to)
				break
			}
		}
	}
}

// --- node membership ---------------------------------------------------------------

// NodeLeave makes a node leave the cluster (crash, stop): its copies become
// unassigned (NODE_LEFT), primaries are replaced by promoted replicas when
// possible, and replica reallocation is delayed per index.
func (c *Cluster) NodeLeave(name string) {
	n := c.nodeBy[name]
	if n == nil || !n.Up {
		return
	}
	n.Up = false
	// Relocations towards n are cancelled; the source copy stays.
	for _, ix := range c.Indices {
		for _, sh := range ix.Shards {
			for _, cp := range sh {
				if cp.Target == n {
					c.cancelRelocation(cp)
				}
			}
		}
	}
	lost := make([]*Copy, 0, len(n.copies))
	for cp := range n.copies {
		lost = append(lost, cp)
	}
	sort.Slice(lost, func(i, j int) bool {
		if lost[i].Index.Name != lost[j].Index.Name {
			return lost[i].Index.Name < lost[j].Index.Name
		}
		return lost[i].Shard < lost[j].Shard
	})
	for _, cp := range lost {
		wasPrimary := cp.Primary
		c.unassign(cp)
		cp.UReason, cp.UAtMs, cp.UDetails, cp.LastNode = "NODE_LEFT", c.nowMs, "node_left ["+n.ID+"]", n
		if wasPrimary {
			for _, o := range cp.Index.Shards[cp.Shard] {
				if o != cp && o.State == Started {
					o.Primary, cp.Primary = true, false
					break
				}
			}
		}
		if !cp.Primary {
			d := parseDuration(cp.Index.Setting("index.unassigned.node_left.delayed_timeout", "1m"))
			cp.DelayMs = c.nowMs + d
			c.schedule(cp.DelayMs, func() { c.dirty = true })
		}
	}
	n.recoveries = 0
	c.markAll()
}

// NodeJoin brings a node back; copies it held are recovered locally.
func (c *Cluster) NodeJoin(name string) {
	n := c.nodeBy[name]
	if n == nil || n.Up {
		return
	}
	n.Up = true
	n.StartMs = c.nowMs
	c.markAll()
}

func parseDuration(s string) int64 {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 60_000
	}
	return d.Milliseconds()
}

func atoiDefault(s string, def int) int {
	n := 0
	neg := false
	if strings.HasPrefix(s, "-") {
		neg, s = true, s[1:]
	}
	if s == "" {
		return def
	}
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return def
		}
		n = n*10 + int(ch-'0')
	}
	if neg {
		n = -n
	}
	return n
}
