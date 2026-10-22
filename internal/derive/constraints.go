package derive

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"

	"estopo/internal/model"
)

// Allocation constraints (SPEC §7.5): for each index at T, can every copy of
// every shard be allocated, and would it still be possible after losing one
// node or one machine? A heuristic modelled on the Elasticsearch deciders; it
// does not replace _cluster/allocation/explain.

// Constraint statuses, from worst to best.
const (
	Infeasible = "infeasible"
	Tight      = "tight"
	OK         = "ok"
)

// Reason codes (translated by the UI; Params give the numbers).
const (
	ReasonNoEligibleNode = "no_eligible_node"
	ReasonNotEnoughNodes = "not_enough_nodes"
	ReasonSameHost       = "same_host"
	ReasonTotalShards    = "total_shards_per_node"
	ReasonClusterTotal   = "cluster_total_shards_per_node"
	ReasonAwareness      = "awareness"
	ReasonLoseNode       = "lose_node"
	ReasonLoseMachine    = "lose_machine"
)

// IndexConstraint is the verdict for one index.
type IndexConstraint struct {
	Index    int               `json:"index"` // position in State.Indices
	Status   string            `json:"status"`
	Reason   string            `json:"reason,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
	Eligible int               `json:"eligible"` // eligible nodes
	Required int               `json:"required"` // copies per shard
}

type ctx struct {
	nodes       []int // present data nodes (positions in State.Nodes)
	st          *model.State
	awareness   []string
	forced      map[string][]string
	sameHost    bool
	clusterTSPN int
	filters     []nodeFilter
	highWM      watermark
	tierPresent map[string]bool
	shardsOn    map[int]int // copies held per node
}

type nodeFilter struct {
	op, attr string
	values   []string
}

type watermark struct {
	pct      float64 // used percentage limit, NaN if expressed in bytes
	minFree  int64   // free bytes limit, 0 if expressed in percent
	headroom int64   // max_headroom of a percentage: free bytes above which a node is below it; 0 = none
	disabled bool    // cluster.routing.allocation.disk.threshold_enabled=false
}

// Constraints evaluates every index of the state.
func Constraints(st *model.State) []IndexConstraint {
	c := newCtx(st)
	cache := map[string]IndexConstraint{}
	out := make([]IndexConstraint, len(st.Indices))
	for i := range st.Indices {
		ix := &st.Indices[i]
		sig := c.signature(ix)
		v, ok := cache[sig]
		if !ok {
			v = c.evaluate(ix)
			cache[sig] = v
		}
		v.Index = i
		out[i] = v
	}
	return out
}

func newCtx(st *model.State) *ctx {
	s := st.ClusterSettings
	get := func(k string) string {
		if v, ok := s[k]; ok {
			return v
		}
		return st.Defaults[k]
	}
	c := &ctx{st: st, forced: map[string][]string{}, tierPresent: map[string]bool{}, shardsOn: map[int]int{},
		sameHost: get("cluster.routing.allocation.same_shard.host") == "true", clusterTSPN: -1}
	if v, err := strconv.Atoi(get("cluster.routing.allocation.total_shards_per_node")); err == nil {
		c.clusterTSPN = v
	}
	for _, a := range splitList(get("cluster.routing.allocation.awareness.attributes")) {
		c.awareness = append(c.awareness, a)
		if f := splitList(get("cluster.routing.allocation.awareness.force." + a + ".values")); len(f) > 0 {
			c.forced[a] = f
		}
	}
	c.filters = parseNodeFilters(s, "cluster.routing.allocation.")
	hw := get("cluster.routing.allocation.disk.watermark.high")
	if hw == "" {
		hw = "90%"
	}
	c.highWM = parseWatermark(hw)
	c.highWM.disabled = get("cluster.routing.allocation.disk.threshold_enabled") == "false"
	// From 8.5, a percentage watermark is capped by a max headroom of free
	// space: 150 GB by default when the watermark is left at its default
	// (include_defaults then reports it, "-1" when the watermark is set).
	if !math.IsNaN(c.highWM.pct) {
		mh := get("cluster.routing.allocation.disk.watermark.high.max_headroom")
		if _, set := s["cluster.routing.allocation.disk.watermark.high"]; mh == "" && !set && minVersionAtLeast(st, 8, 5) {
			mh = "150gb"
		}
		if w := parseWatermark(mh); mh != "" && w.minFree > 0 {
			c.highWM.headroom = w.minFree
		}
	}
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if !n.Present || !n.IsData() {
			continue
		}
		c.nodes = append(c.nodes, i)
		for _, r := range n.Roles {
			c.tierPresent[r] = true
		}
	}
	for _, sh := range st.Shards {
		if sh.Node >= 0 {
			c.shardsOn[int(sh.Node)]++
		}
	}
	return c
}

func splitList(s string) []string {
	s = strings.Trim(strings.TrimSpace(s), "[]")
	var out []string
	for _, x := range strings.Split(s, ",") {
		x = strings.Trim(strings.TrimSpace(x), `"`)
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}

func parseNodeFilters(settings map[string]string, prefix string) []nodeFilter {
	var out []nodeFilter
	for k, v := range settings {
		if !strings.HasPrefix(k, prefix) || v == "" {
			continue
		}
		rest := strings.TrimPrefix(k, prefix)
		for _, op := range []string{"require", "include", "exclude"} {
			if strings.HasPrefix(rest, op+".") {
				attr := strings.TrimPrefix(rest, op+".")
				if attr == "_tier_preference" {
					continue
				}
				out = append(out, nodeFilter{op: op, attr: attr, values: splitList(v)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].op+out[i].attr < out[j].op+out[j].attr })
	return out
}

func parseWatermark(s string) watermark {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasSuffix(s, "%") {
		if f, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64); err == nil {
			return watermark{pct: f}
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && f <= 1 { // ratio 0.9
		return watermark{pct: f * 100}
	}
	for _, u := range []struct {
		suffix string
		m      float64
	}{{"tb", 1 << 40}, {"gb", 1 << 30}, {"mb", 1 << 20}, {"kb", 1 << 10}, {"b", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			if f, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64); err == nil {
				return watermark{pct: math.NaN(), minFree: int64(f * u.m)}
			}
		}
	}
	return watermark{pct: 90}
}

// aboveHigh reports whether a node is above the high watermark.
func (c *ctx) aboveHigh(n *model.Node) bool {
	m := n.Metrics
	if c.highWM.disabled || m == nil || m.DiskTotal <= 0 || m.DiskUsed < 0 {
		return false
	}
	if c.highWM.minFree > 0 {
		return m.DiskTotal-m.DiskUsed < c.highWM.minFree
	}
	if c.highWM.headroom > 0 && m.DiskTotal-m.DiskUsed >= c.highWM.headroom {
		return false
	}
	return m.DiskPercent() > c.highWM.pct
}

// minVersionAtLeast reports whether every present node with a known version
// runs at least major.minor (a setting exists in a mixed cluster once all
// its nodes know it).
func minVersionAtLeast(st *model.State, major, minor int) bool {
	seen := false
	for i := range st.Nodes {
		n := &st.Nodes[i]
		if !n.Present || n.Version == "" {
			continue
		}
		seen = true
		parts := strings.SplitN(n.Version, ".", 3)
		ma, err1 := strconv.Atoi(parts[0])
		mi := 0
		var err2 error
		if len(parts) > 1 {
			mi, err2 = strconv.Atoi(parts[1])
		}
		if err1 != nil || err2 != nil || ma < major || ma == major && mi < minor {
			return false
		}
	}
	return seen
}

func nodeAttr(n *model.Node, attr string) string {
	switch attr {
	case "_name":
		return n.Name
	case "_host", "_host_name":
		return n.Host
	case "_ip", "_host_ip", "_publish_ip":
		return n.IP
	case "_id":
		return n.ESID
	}
	return n.Attrs[attr]
}

func match(v string, patterns []string) bool {
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

func passFilters(n *model.Node, fs []nodeFilter) bool {
	var includes []nodeFilter
	for _, f := range fs {
		v := nodeAttr(n, f.attr)
		switch f.op {
		case "require":
			// Every value must match (Elasticsearch DiscoveryNodeFilters,
			// AND), except for IP attributes where one is enough.
			if f.attr == "_ip" || f.attr == "_host_ip" || f.attr == "_publish_ip" {
				if !match(v, f.values) {
					return false
				}
				continue
			}
			// A node without the attribute matches no value, not even "*"
			// (checked on Elasticsearch 9.5).
			if _, ok := n.Attrs[f.attr]; !ok && !strings.HasPrefix(f.attr, "_") {
				return false
			}
			for _, p := range f.values {
				if !match(v, []string{p}) {
					return false
				}
			}
		case "exclude":
			if v != "" && match(v, f.values) {
				return false
			}
		default:
			includes = append(includes, f)
		}
	}
	if len(includes) == 0 {
		return true
	}
	for _, f := range includes {
		if v := nodeAttr(n, f.attr); v != "" && match(v, f.values) {
			return true
		}
	}
	return false
}

// indexRules extracts what matters for allocation from index settings.
type indexRules struct {
	tierPref   []string
	filters    []nodeFilter
	tspn       int
	copies     int
	autoExpand bool
	pri        int
	own        map[int]int // copies of this index per node
}

func (c *ctx) rules(ix *model.Index) indexRules {
	r := indexRules{tierPref: splitList(ix.Settings["index.routing.allocation.include._tier_preference"]),
		filters: parseNodeFilters(ix.Settings, "index.routing.allocation."), tspn: -1, copies: 1 + ix.Rep, pri: ix.Pri}
	if v, err := strconv.Atoi(ix.Settings["index.routing.allocation.total_shards_per_node"]); err == nil {
		r.tspn = v
	}
	if ae := ix.Settings["index.auto_expand_replicas"]; ae != "" && ae != "false" {
		r.autoExpand = true
	}
	return r
}

func (c *ctx) signature(ix *model.Index) string {
	var b strings.Builder
	keys := make([]string, 0, len(ix.Settings))
	for k := range ix.Settings {
		if strings.HasPrefix(k, "index.routing.allocation.") || k == "index.auto_expand_replicas" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k + "=" + ix.Settings[k] + ";")
	}
	fmt.Fprintf(&b, "|%d|%d", ix.Pri, ix.Rep)
	if c.clusterTSPN >= 0 {
		b.WriteString("|" + ix.UUID) // depends on the copies already placed
	}
	return b.String()
}

// eligible returns the nodes (among candidates) that may hold the index.
func (c *ctx) eligible(r indexRules, candidates []int) (nodes []int, tier string) {
	tier = ""
	if len(r.tierPref) > 0 {
		tier = r.tierPref[0]
		for _, t := range r.tierPref {
			if c.tierPresentIn(candidates, t) {
				tier = t
				break
			}
		}
	}
	for _, i := range candidates {
		n := &c.st.Nodes[i]
		if tier != "" && !n.HasRole(tier) && !n.HasRole("data") {
			continue
		}
		if !passFilters(n, r.filters) || !passFilters(n, c.filters) || c.aboveHigh(n) {
			continue
		}
		nodes = append(nodes, i)
	}
	return nodes, tier
}

func (c *ctx) tierPresentIn(nodes []int, tier string) bool {
	for _, i := range nodes {
		n := &c.st.Nodes[i]
		if n.HasRole(tier) || n.HasRole("data") {
			return true
		}
	}
	return false
}

// feasible checks the rules against a set of candidate nodes; it returns ""
// when every copy can be allocated, else a reason code and its parameters.
func (c *ctx) feasible(r indexRules, candidates []int) (string, map[string]string, int) {
	nodes, tier := c.eligible(r, candidates)
	copies := r.copies
	if r.autoExpand {
		copies = min(copies, max(1, len(nodes)))
	}
	p := func(kv ...string) map[string]string {
		m := map[string]string{"required": strconv.Itoa(copies), "eligible": strconv.Itoa(len(nodes))}
		if tier != "" {
			m["tier"] = tier
		}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	if len(nodes) == 0 {
		return ReasonNoEligibleNode, p(), 0
	}
	if copies > len(nodes) {
		return ReasonNotEnoughNodes, p(), len(nodes)
	}
	if c.sameHost {
		machines := map[string]bool{}
		for _, i := range nodes {
			machines[c.st.Nodes[i].Machine] = true
		}
		if copies > len(machines) {
			return ReasonSameHost, p("machines", strconv.Itoa(len(machines))), len(nodes)
		}
	}
	if r.tspn >= 0 && r.pri*copies > len(nodes)*r.tspn {
		return ReasonTotalShards, p("total_shards_per_node", strconv.Itoa(r.tspn), "needed", strconv.Itoa(r.pri*copies)), len(nodes)
	}
	if c.clusterTSPN >= 0 {
		free := 0
		for _, i := range nodes {
			free += max(0, c.clusterTSPN-c.shardsOn[i]+r.own[i])
		}
		if r.pri*copies > free {
			return ReasonClusterTotal, p("total_shards_per_node", strconv.Itoa(c.clusterTSPN), "free", strconv.Itoa(free)), len(nodes)
		}
	}
	for _, attr := range c.awareness {
		values := c.forced[attr]
		if len(values) == 0 {
			seen := map[string]bool{}
			for _, i := range candidates {
				if v := c.st.Nodes[i].Attrs[attr]; v != "" && c.st.Nodes[i].IsData() {
					seen[v] = true
				}
			}
			for v := range seen {
				values = append(values, v)
			}
		}
		if len(values) == 0 {
			continue
		}
		limit := (copies + len(values) - 1) / len(values)
		perValue := map[string]int{}
		for _, i := range nodes {
			perValue[c.st.Nodes[i].Attrs[attr]]++
		}
		capacity := 0
		for _, v := range values {
			capacity += min(limit, perValue[v])
		}
		if capacity < copies {
			return ReasonAwareness, p("attribute", attr, "values", strconv.Itoa(len(values)), "per_value", strconv.Itoa(limit)), len(nodes)
		}
	}
	return "", nil, len(nodes)
}

func (c *ctx) evaluate(ix *model.Index) IndexConstraint {
	r := c.rules(ix)
	if c.clusterTSPN >= 0 {
		r.own = map[int]int{}
		for _, sh := range c.st.Shards {
			if sh.Node >= 0 && &c.st.Indices[sh.Index] == ix {
				r.own[int(sh.Node)]++
			}
		}
	}
	reason, params, eligible := c.feasible(r, c.nodes)
	res := IndexConstraint{Status: OK, Eligible: eligible, Required: r.copies}
	if reason != "" {
		res.Status, res.Reason, res.Params = Infeasible, reason, params
		return res
	}
	// Tight: would losing one node, then one machine, make it infeasible?
	eligibleNodes, _ := c.eligible(r, c.nodes)
	for _, lost := range eligibleNodes {
		if why, pp, _ := c.feasible(r, without(c.nodes, func(i int) bool { return i == lost })); why != "" {
			res.Status, res.Reason = Tight, ReasonLoseNode
			res.Params = map[string]string{"node": c.st.Nodes[lost].Name, "then": why}
			for k, v := range pp {
				res.Params[k] = v
			}
			return res
		}
	}
	machines := map[string]bool{}
	for _, i := range eligibleNodes {
		machines[c.st.Nodes[i].Machine] = true
	}
	names := make([]string, 0, len(machines))
	for m := range machines {
		names = append(names, m)
	}
	sort.Strings(names)
	for _, m := range names {
		if why, pp, _ := c.feasible(r, without(c.nodes, func(i int) bool { return c.st.Nodes[i].Machine == m })); why != "" {
			res.Status, res.Reason = Tight, ReasonLoseMachine
			res.Params = map[string]string{"machine": m, "then": why}
			for k, v := range pp {
				res.Params[k] = v
			}
			return res
		}
	}
	return res
}

func without(nodes []int, drop func(int) bool) []int {
	out := make([]int, 0, len(nodes))
	for _, i := range nodes {
		if !drop(i) {
			out = append(out, i)
		}
	}
	return out
}
