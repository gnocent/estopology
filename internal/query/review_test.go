package query

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"estopo/internal/record"
	"estopo/internal/sim"
)

// The family filters of Path, Events and Search follow the regex rules
// (familyOf), not the family stored at ingestion (SPEC §7.3).
func TestFamilyRulesInFilters(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	s, err := q.e.StateAt(q.ctx, "small", q.ms(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	auto := s.Indices[0].Family
	q.e.SetFamilyFunc(func(_, index string) (string, bool) {
		if strings.HasPrefix(index, auto) {
			return "custom", true
		}
		return "", false
	})
	q.c.Do(func(c *sim.Cluster) {
		c.CreateIndex(auto+"-2026.09.30-new", 1, 1, 100, map[string]string{"index.routing.allocation.include._tier_preference": "data_hot"})
	})
	q.full(10 * time.Minute)
	a, b := q.ms(0), q.ms(time.Hour)

	path, err := q.e.Path(q.ctx, "small", PathQuery{Family: "custom", Shard: -1, A: a, B: b})
	if err != nil || len(path.Segments) == 0 {
		t.Errorf("path of the family: %+v %v", path, err)
	}
	if path, _ := q.e.Path(q.ctx, "small", PathQuery{Family: auto, Shard: -1, A: a, B: b}); len(path.Segments) != 0 {
		t.Errorf("path of the overridden family: %d segments", len(path.Segments))
	}
	page, err := q.e.Events(q.ctx, "small", EventFilter{From: a, To: b, Family: "custom"})
	if err != nil || len(page.Events) == 0 {
		t.Fatalf("events of the family: %+v %v", page, err)
	}
	created := false
	for _, ev := range page.Events {
		if ev.Type == "index_created" {
			created = true
			if ev.Group != "custom" {
				t.Errorf("group of index_created: %q", ev.Group)
			}
		}
	}
	if !created {
		t.Errorf("no index_created among %+v", page.Events)
	}
	if page, _ := q.e.Events(q.ctx, "small", EventFilter{From: a, To: b, Family: auto}); len(page.Events) != 0 {
		t.Errorf("events of the overridden family: %+v", page.Events)
	}
	hits, err := q.e.Search(q.ctx, []string{"small"}, "custom", 20)
	if err != nil || len(hits) == 0 || hits[0].Kind != "family" || hits[0].Value != "custom" {
		t.Errorf("search of the family: %+v %v", hits, err)
	}
}

// A copy is never "reallocated" by an assignment older than its loss.
func TestLossesReallocationAfterLoss(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave("small-hot-dc1-m01-2") })
	q.full(30 * time.Second) // lost, reallocation delayed
	q.full(10 * time.Minute) // reallocated
	l, err := q.e.Losses(q.ctx, "small", q.ms(0), q.ms(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	target := ""
	for _, c := range l.Groups[0].Copies {
		if c.After == "reallocated" && c.AfterTs == q.ms(10*time.Minute) {
			target = c.AfterNode
			break
		}
	}
	if target == "" {
		t.Fatalf("no reallocated copy: %+v", l.Groups)
	}
	// The node that received the copies leaves in turn; the window starts
	// after the first loss, so the 10 min assignments have no loss of their own.
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave(target) })
	q.full(10*time.Minute + 30*time.Second)
	l, err = q.e.Losses(q.ctx, "small", q.ms(5*time.Minute), q.ms(11*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, g := range l.Groups {
		for _, c := range g.Copies {
			n++
			if c.After == "reallocated" && c.AfterTs < c.TsHi {
				t.Errorf("reallocated at %d before its loss at %d: %+v", c.AfterTs, c.TsHi, c)
			}
		}
	}
	if n == 0 {
		t.Fatal("no copy lost with the second node")
	}
}

// A copy lost then whose index is deleted is classified "deleted", not
// still unassigned (SPEC §7.6, B5).
func TestLossesIndexDeletedAfterLoss(t *testing.T) {
	q := newQEnv(t)
	q.c.Do(func(c *sim.Cluster) { c.SetClusterSetting(true, "cluster.routing.allocation.enable", "primaries") })
	q.full(0)
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave("small-hot-dc1-m01-2") })
	q.full(2 * time.Minute)
	l, err := q.e.Losses(q.ctx, "small", q.ms(0), q.ms(3*time.Minute))
	if err != nil || len(l.Groups) == 0 {
		t.Fatalf("losses %+v %v", l, err)
	}
	var victim LostCopy
	for _, c := range l.Groups[0].Copies {
		if c.After == "unassigned" {
			victim = c
			break
		}
	}
	if victim.Index == "" {
		t.Fatalf("no copy left unassigned: %+v", l.Groups[0].Copies)
	}
	q.c.Do(func(c *sim.Cluster) { c.DeleteIndex(victim.Index) })
	q.full(5 * time.Minute)
	l, err = q.e.Losses(q.ctx, "small", q.ms(0), q.ms(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	deleted, unassigned := 0, 0
	for _, c := range l.Groups[0].Copies {
		if c.UUID == victim.UUID {
			if c.After != "deleted" {
				t.Errorf("copy of the deleted index: %+v", c)
			}
			deleted++
		} else if c.After == "unassigned" {
			unassigned++
		}
	}
	if deleted == 0 || l.Summary.Deleted != deleted || l.Summary.StillUnassigned != unassigned {
		t.Errorf("deleted %d unassigned %d, summary %+v", deleted, unassigned, l.Summary)
	}
}

// The zone of a lost copy comes from the awareness attribute even when it is
// only set in the node configuration (cluster settings defaults).
func TestLossesZoneFromDefaults(t *testing.T) {
	q := newQEnv(t)
	const key = "cluster.routing.allocation.awareness.attributes"
	ingest := func(d time.Duration, kind record.Kind) {
		r := q.c.CaptureAt(q.ms(d), "small", kind, "t")
		cs := r.ClusterSettings
		v := cs.Persistent[key] + cs.Transient[key]
		delete(cs.Persistent, key)
		delete(cs.Transient, key)
		if kind == record.Full {
			if cs.Defaults == nil {
				cs.Defaults = map[string]string{}
			}
			cs.Defaults[key] = `["` + v + `"]` // list settings come as JSON lists
		}
		if _, err := q.in.Ingest(q.ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	ingest(0, record.Full)
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave("small-hot-dc1-m01-2") })
	ingest(time.Minute, record.Light)
	ingest(10*time.Minute, record.Full)
	l, err := q.e.Losses(q.ctx, "small", q.ms(0), q.ms(20*time.Minute))
	if err != nil || len(l.Groups) == 0 {
		t.Fatalf("losses %+v %v", l, err)
	}
	for _, c := range l.Groups[0].Copies {
		if c.Zone != "dc1" {
			t.Fatalf("zone of %+v", c)
		}
	}
}

// Nodes whose metrics stay within the dead band write nothing: a slice
// without a reading keeps their last value while they are present.
func TestGroupMetricsDeadband(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	r := q.c.CaptureAt(q.ms(0), "small", record.Light, "t")
	for i := 1; i <= 6; i++ { // the same readings: nothing new is written
		r.StartMs, r.EndMs = q.ms(time.Duration(i)*10*time.Minute), q.ms(time.Duration(i)*10*time.Minute)
		if _, err := q.in.Ingest(q.ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	s, err := q.e.StateAt(q.ctx, "small", q.ms(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var total int64
	for _, n := range s.Nodes {
		if n.IsData() {
			names = append(names, n.Name)
			total += n.Metrics.DiskTotal
		}
	}
	pts, err := q.e.GroupMetrics(q.ctx, "small", names, q.ms(0), q.ms(time.Hour), 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 6 {
		t.Fatalf("%d points: %+v", len(pts), pts)
	}
	for _, p := range pts {
		if p.Nodes != len(names) || p.DiskTotal != total {
			t.Errorf("point %+v: want %d nodes, disk %d", p, len(names), total)
		}
	}
	// A node gone stops counting.
	gone := names[0]
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave(gone) })
	q.probe(70 * time.Minute)
	pts, _ = q.e.GroupMetrics(q.ctx, "small", []string{gone}, q.ms(80*time.Minute), q.ms(2*time.Hour), 4)
	if len(pts) != 0 {
		t.Errorf("metrics of a node gone: %+v", pts)
	}
}

// A position taken exactly at B is part of the path and of the histories,
// as it is of the state at B.
func TestPathAndHistoriesIncludeB(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	victim := "small-hot-dc1-m01-2"
	name := q.c.Indices[0].Name
	q.c.Do(func(c *sim.Cluster) {
		c.NodeLeave(victim)
		c.NodeReplace("small-hot-dc2-m01-1")
		c.SetIndexSetting(name, "index.routing.allocation.total_shards_per_node", "2")
	})
	b := q.ms(10 * time.Minute)
	q.full(10 * time.Minute)
	l, err := q.e.Losses(q.ctx, "small", q.ms(0), b)
	if err != nil || len(l.Groups) == 0 {
		t.Fatalf("losses %+v %v", l, err)
	}
	lc := l.Groups[0].Copies[0]
	path, err := q.e.Path(q.ctx, "small", PathQuery{UUID: lc.UUID, Shard: lc.Shard, A: q.ms(0), B: b})
	if err != nil {
		t.Fatal(err)
	}
	atB := false
	for _, s := range path.Segments {
		atB = atB || s.FromMs == b
	}
	if !atB {
		t.Errorf("no segment starting at B: %+v", path.Segments)
	}
	h, err := q.e.NodeHistory(q.ctx, "small", "small-hot-dc2-m01-1", q.ms(0), b, 10)
	if err != nil || len(h.Generations) != 2 || len(h.Generations[1].Presence) != 1 || len(h.Infos) != 2 {
		t.Errorf("node history up to B: %+v %v", h, err)
	}
	var uuid string
	s, _ := q.e.StateAt(q.ctx, "small", b)
	for _, ix := range s.Indices {
		if ix.Name == name {
			uuid = ix.UUID
		}
	}
	ih, err := q.e.IndexHistory(q.ctx, "small", uuid, q.ms(0), b)
	if err != nil || len(ih.Periods) != 2 {
		t.Errorf("index history up to B: %+v %v", ih, err)
	}
}

// A before any full capture, on a light probe: the diff starts from the first
// full capture instead of an empty structure.
func TestDiffFromProbeWithoutStructure(t *testing.T) {
	q := newQEnv(t)
	q.probe(0)
	if _, err := q.e.Diff(q.ctx, "small", q.ms(0), q.ms(5*time.Minute)); err != ErrNoData {
		t.Errorf("no full capture: %v", err)
	}
	q.full(10 * time.Minute)
	d, err := q.e.Diff(q.ctx, "small", q.ms(time.Minute), q.ms(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if d.CaptureA != q.ms(10*time.Minute) || d.Summary.IndicesAdded != 0 || d.Summary.NodesChanged != 0 {
		t.Errorf("diff %+v", d.Summary)
	}
}

// A node seen by a probe after the last full capture has no description
// yet: it is not "changed" when the next full capture describes it.
func TestDiffNodeWithoutInfo(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	q.c.Do(func(c *sim.Cluster) { c.NodeReplace("small-hot-dc2-m01-1") })
	q.probe(5 * time.Minute)
	q.full(10 * time.Minute)
	d, err := q.e.Diff(q.ctx, "small", q.ms(5*time.Minute), q.ms(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range d.Nodes {
		if n.Change == "changed" {
			t.Errorf("node changed: %+v", n)
		}
	}
}

// A primary lost and replaced by its promoted replica: the replica is
// "promoted" on its node, the copy to rebuild is a replica.
func TestDiffPromotion(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	victim := "small-hot-dc1-m01-2"
	var index, other string
	shard := -1
	q.c.Do(func(c *sim.Cluster) {
		for _, ix := range c.Indices {
			for s, copies := range ix.Shards {
				if len(copies) != 2 || copies[0].Node == nil || copies[1].Node == nil {
					continue
				}
				p, r := copies[0], copies[1]
				if !p.Primary {
					p, r = r, p
				}
				if p.Node.Name == victim && r.State == sim.Started {
					index, shard, other = ix.Name, s, r.Node.Name
					return
				}
			}
		}
	})
	if index == "" {
		t.Fatal("no primary on the victim")
	}
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave(victim) })
	q.full(30 * time.Second) // before the delayed reallocation
	d, err := q.e.Diff(q.ctx, "small", q.ms(0), q.ms(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range d.Shards {
		if s.Index == index && s.Shard == shard {
			pr := "r"
			if s.Primary {
				pr = "p"
			}
			got = append(got, s.Change+" "+pr+" "+strings.Join(s.From, ",")+">"+strings.Join(s.To, ","))
		}
	}
	sort.Strings(got)
	want := []string{"lost r " + victim + ">", "promoted p " + other + ">" + other}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("shard changes %v\nwant %v", got, want)
	}
	if d.Summary.ShardsPromoted == 0 {
		t.Errorf("summary %+v", d.Summary)
	}
}

// Beyond the limit the path says it is truncated, and keeps the first
// segments in (uuid, shard, p/r, start) order.
func TestPathTruncated(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave("small-hot-dc1-m01-2") })
	q.full(10 * time.Minute)
	s, _ := q.e.StateAt(q.ctx, "small", q.ms(10*time.Minute))
	family := s.Indices[0].Family
	pq := PathQuery{Family: family, Shard: -1, A: q.ms(0), B: q.ms(time.Hour)}
	all, err := q.e.Path(q.ctx, "small", pq)
	if err != nil || all.Truncated || len(all.Segments) < 4 {
		t.Fatalf("path %+v %v", all, err)
	}
	pq.Limit = 3
	cut, err := q.e.Path(q.ctx, "small", pq)
	if err != nil || !cut.Truncated || len(cut.Segments) != 3 {
		t.Fatalf("truncated path %+v %v", cut, err)
	}
	key := func(s Segment) string {
		pr := "r"
		if s.Primary {
			pr = "p"
		}
		return fmt.Sprintf("%s|%04d|%s|%020d", s.UUID, s.Shard, pr, s.FromMs)
	}
	var keys []string
	for _, s := range all.Segments {
		keys = append(keys, key(s))
	}
	sort.Strings(keys)
	var got []string
	for _, s := range cut.Segments {
		got = append(got, key(s))
	}
	sort.Strings(got)
	if strings.Join(got, " ") != strings.Join(keys[:3], " ") {
		t.Errorf("kept %v\nwant %v", got, keys[:3])
	}
}
