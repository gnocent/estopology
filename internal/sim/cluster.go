package sim

import (
	"container/heap"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Cluster is one simulated cluster. All methods are safe for concurrent use.
type Cluster struct {
	mu sync.Mutex

	Name    string
	UUID    string
	Version string // version of the nodes answering HTTP (response format)

	spec  Spec
	rng   *rand.Rand
	nowMs int64

	Nodes   []*Node
	nodeBy  map[string]*Node
	Indices []*Index // creation order
	indexBy map[string]*Index

	Persistent map[string]string
	Transient  map[string]string
	Remotes    []*Remote

	queue     eventHeap
	nextDaily int64
	nextTick  int64
	dirty     bool // a reroute is needed
	seq       int  // tie-breaker for simultaneous events

	// Allocator bookkeeping (see alloc.go).
	unassigned    map[*Copy]struct{}
	unSorted      []*Copy // unassigned copies in allocation order, valid while unSortedOK
	unSortedOK    bool
	reroutes      int   // number of reroute passes (tests: no reroute storm)
	lastReroute   int64 // simulated time of the last reroute
	rerouteQueued bool  // a deferred reroute is scheduled
	checkIdx      map[*Index]struct{}
	checkAll      bool
	relocating    int
	ticks         int
}

// Cluster setting defaults used by the allocator (ES defaults).
var settingDefaults = map[string]string{
	"cluster.routing.allocation.enable":                            "all",
	"cluster.routing.allocation.same_shard.host":                   "false",
	"cluster.routing.allocation.total_shards_per_node":             "-1",
	"cluster.routing.allocation.disk.watermark.low":                "85%",
	"cluster.routing.allocation.disk.watermark.high":               "90%",
	"cluster.routing.allocation.disk.watermark.flood_stage":        "95%",
	"cluster.routing.allocation.node_concurrent_recoveries":        "2",
	"cluster.routing.allocation.cluster_concurrent_rebalance":      "2",
	"cluster.routing.allocation.awareness.attributes":              "",
	"cluster.routing.rebalance.enable":                             "all",
	"indices.recovery.max_bytes_per_sec":                           "40mb",
	"cluster.routing.allocation.node_initial_primaries_recoveries": "4",
}

// setting returns the effective cluster setting (transient > persistent > default).
func (c *Cluster) setting(key string) string {
	if v, ok := c.Transient[key]; ok {
		return v
	}
	if v, ok := c.Persistent[key]; ok {
		return v
	}
	return settingDefaults[key]
}

func (c *Cluster) settingInt(key string) int {
	n, err := strconv.Atoi(c.setting(key))
	if err != nil {
		return -1
	}
	return n
}

// Now returns the simulated time the cluster has reached.
func (c *Cluster) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.UnixMilli(c.nowMs).UTC()
}

// --- events ------------------------------------------------------------------

type event struct {
	at  int64
	seq int
	fn  func()
}

type eventHeap []event

func (h eventHeap) Len() int { return len(h) }
func (h eventHeap) Less(i, j int) bool {
	return h[i].at < h[j].at || (h[i].at == h[j].at && h[i].seq < h[j].seq)
}
func (h eventHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *eventHeap) Push(x any)   { *h = append(*h, x.(event)) }
func (h *eventHeap) Pop() any {
	old := *h
	e := old[len(old)-1]
	*h = old[:len(old)-1]
	return e
}

// schedule runs fn at simulated time at (caller holds the lock).
func (c *Cluster) schedule(at int64, fn func()) {
	c.seq++
	heap.Push(&c.queue, event{at: at, seq: c.seq, fn: fn})
}

// Schedule registers a scenario action at simulated time t. The action runs
// with the cluster locked and may call the unexported mutators.
func (c *Cluster) Schedule(t time.Time, fn func(*Cluster)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.schedule(t.UnixMilli(), func() { fn(c) })
}

// Do runs fn with the cluster locked, at its current simulated time.
func (c *Cluster) Do(fn func(*Cluster)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(c)
	c.reroute(false)
}

const (
	dayMs  = int64(24 * time.Hour / time.Millisecond)
	tickMs = int64(5 * time.Minute / time.Millisecond) // rebalance period
	// rerouteGapMs is the minimum simulated time between two reroutes caused
	// by events (a master batches its cluster state updates).
	rerouteGapMs = int64(5 * time.Second / time.Millisecond)
)

// AdvanceTo evolves the cluster up to t (never backwards).
func (c *Cluster) AdvanceTo(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.advanceTo(t.UnixMilli())
}

func (c *Cluster) advanceTo(target int64) {
	for {
		next := min(c.nextDaily, c.nextTick)
		if len(c.queue) > 0 && c.queue[0].at < next {
			next = c.queue[0].at
		}
		if next > target {
			break
		}
		if next > c.nowMs {
			c.nowMs = next
		}
		switch {
		case len(c.queue) > 0 && c.queue[0].at == next:
			e := heap.Pop(&c.queue).(event)
			e.fn()
			// Events of the same instant (e.g. the delayed allocation of
			// every replica of a lost node) share one reroute, after the
			// last of them: a reroute per event is quadratic.
			if len(c.queue) > 0 && c.queue[0].at == next {
				continue
			}
		case next == c.nextDaily:
			c.nextDaily += dayMs
			c.daily()
		default:
			c.nextTick += tickMs
			c.ticks++
			if c.ticks%12 == 0 { // hourly: re-check every placement (disk watermarks)
				c.checkAll = true
			}
			c.reroute(true)
			continue
		}
		if c.dirty {
			// At most one reroute per rerouteGapMs: thousands of recoveries
			// ending one after the other (a data centre coming back) would
			// otherwise each rescan every unassigned copy.
			if wait := c.lastReroute + rerouteGapMs - c.nowMs; wait > 0 && c.lastReroute > 0 {
				if !c.rerouteQueued {
					c.rerouteQueued = true
					c.schedule(c.nowMs+wait, func() { c.rerouteQueued, c.dirty = false, true })
				}
			} else {
				c.reroute(false)
			}
		}
	}
	if target > c.nowMs {
		c.nowMs = target
	}
}

// --- lifecycle ------------------------------------------------------------------

// daily runs at 00:00 UTC: new daily indices, tier moves, retention.
func (c *Cluster) daily() {
	day := time.UnixMilli(c.nowMs).UTC()
	for _, src := range c.spec.Sources {
		if !src.Rollover {
			c.createIndex(src, fmt.Sprintf("%s-%s", src.Name, day.Format("2006.01.02")), c.nowMs, c.nowMs+dayMs, true)
		}
	}
	c.applyLifecycle()
}

// applyLifecycle moves indices across tiers by age and deletes the expired
// ones (the "home-made ILM" of SPEC §1).
func (c *Cluster) applyLifecycle() {
	var keep []*Index
	for _, ix := range c.Indices {
		if ix.Source == "" { // not managed by the simulation (fixtures, scenarios)
			keep = append(keep, ix)
			continue
		}
		age := c.lifecycleAge(ix)
		if c.spec.RetentionDays > 0 && age >= int64(c.spec.RetentionDays)*dayMs {
			c.discard(ix) // c.Indices is rebuilt below: never remove while iterating
			continue
		}
		pref := "data_hot"
		if c.spec.ColdAfterDays > 0 && age >= int64(c.spec.ColdAfterDays)*dayMs {
			pref = "data_cold,data_warm,data_hot"
		} else if c.spec.WarmAfterDays > 0 && age >= int64(c.spec.WarmAfterDays)*dayMs {
			pref = "data_warm,data_hot"
		}
		if ix.Settings[tierPrefKey] != pref {
			ix.Settings[tierPrefKey] = pref
			c.markIndex(ix)
		}
		keep = append(keep, ix)
	}
	c.Indices = keep
}

// lifecycleAge is the age used for tier moves and retention: daily indices age
// from the start of their day, rollover indices from their rollover (the
// write index does not age).
func (c *Cluster) lifecycleAge(ix *Index) int64 {
	switch {
	case ix.Rollover:
		if c.nowMs < ix.GrowUntilMs {
			return 0
		}
		return c.nowMs - ix.GrowUntilMs
	case ix.GrowFromMs > 0:
		return c.nowMs - ix.GrowFromMs
	}
	return c.nowMs - ix.CreatedMs
}

const tierPrefKey = "index.routing.allocation.include._tier_preference"

// createIndex adds an index whose copies start unassigned (INDEX_CREATED).
func (c *Cluster) createIndex(src Source, name string, growFrom, growUntil int64, managed bool) *Index {
	if old, ok := c.indexBy[name]; ok {
		c.dropIndex(old)
	}
	ix := &Index{
		UUID: newID(c.rng), Name: name, CreatedMs: c.nowMs, Pri: src.Shards, Rep: src.Replicas,
		Settings: map[string]string{tierPrefKey: "data_hot"}, Rollover: src.Rollover,
		GrowFromMs: growFrom, GrowUntilMs: growUntil, DocBytes: src.DocBytes,
	}
	if managed {
		ix.Source = src.Name
	}
	for k, v := range src.Settings {
		ix.Settings[k] = v
	}
	total := src.DailyBytes
	if src.Rollover {
		total = src.RolloverBytes
	}
	jitter := 0.7 + 0.6*c.rng.Float64()
	ix.ShardBytes = int64(float64(total) * jitter / float64(max(1, src.Shards)))
	c.addIndex(ix)
	if src.Rollover && managed {
		c.schedule(growUntil, func() {
			if _, alive := c.indexBy[ix.Name]; alive {
				c.rollover(src, ix)
			}
		})
	}
	return ix
}

// addIndex registers ix and creates its unassigned copies.
func (c *Cluster) addIndex(ix *Index) {
	ix.Shards = make([][]*Copy, ix.Pri)
	for s := 0; s < ix.Pri; s++ {
		for r := 0; r <= ix.Rep; r++ {
			cp := &Copy{Index: ix, Shard: s, Primary: r == 0, State: Unassigned, UReason: "INDEX_CREATED", UAtMs: c.nowMs}
			ix.Shards[s] = append(ix.Shards[s], cp)
			c.unassigned[cp] = struct{}{}
			c.unSortedOK = false
		}
	}
	c.Indices = append(c.Indices, ix)
	c.indexBy[ix.Name] = ix
	c.dirty = true
}

func (c *Cluster) rollover(src Source, cur *Index) {
	n := 1
	if i := strings.LastIndexByte(cur.Name, '-'); i >= 0 {
		n, _ = strconv.Atoi(cur.Name[i+1:])
	}
	dur := int64(float64(src.RolloverBytes) / float64(max(src.DailyBytes, 1)) * float64(dayMs))
	dur = max(dur, int64(time.Hour/time.Millisecond))
	c.createIndex(src, fmt.Sprintf("%s-%06d", src.Name, n+1), c.nowMs, c.nowMs+dur, true)
}

// dropIndex deletes an index and releases its copies.
func (c *Cluster) dropIndex(ix *Index) {
	c.discard(ix)
	for i, x := range c.Indices {
		if x == ix {
			c.Indices = append(c.Indices[:i], c.Indices[i+1:]...)
			break
		}
	}
	c.dirty = true
}

// discard releases the copies of an index and forgets its name, leaving
// c.Indices to the caller.
func (c *Cluster) discard(ix *Index) {
	for _, sh := range ix.Shards {
		for _, cp := range sh {
			c.release(cp)
		}
	}
	if c.indexBy[ix.Name] == ix {
		delete(c.indexBy, ix.Name)
	}
	delete(c.checkIdx, ix)
	c.dirty = true
}

// release detaches a copy of a deleted index from its nodes.
func (c *Cluster) release(cp *Copy) {
	c.unassign(cp) // also cancels pending completion events (DoneMs = -1)
	delete(c.unassigned, cp)
}

// --- helpers used by scenarios and tests ------------------------------------------

// Node returns the node named name.
func (c *Cluster) Node(name string) *Node { return c.nodeBy[name] }

// IndexByName returns the live index named name.
func (c *Cluster) IndexByName(name string) *Index { return c.indexBy[name] }

// NowMs returns the simulated time in ms (caller holds the lock, e.g. in Do).
func (c *Cluster) NowMs() int64 { return c.nowMs }

func parsePercent(s string) float64 {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "%") {
		f, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 64)
		if err == nil {
			return f
		}
	}
	return math.NaN()
}

// parseByteRate parses "40mb", "100kb", "1gb" into bytes per second.
func parseByteRate(s string) float64 {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := 1.0
	for _, u := range []struct {
		suffix string
		m      float64
	}{{"gb", 1 << 30}, {"mb", 1 << 20}, {"kb", 1 << 10}, {"b", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			mult, s = u.m, strings.TrimSuffix(s, u.suffix)
			break
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 40 << 20
	}
	return f * mult
}

func sortIndicesByName(xs []*Index) []*Index {
	out := append([]*Index(nil), xs...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
