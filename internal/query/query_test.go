package query

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/record"
	"estopo/internal/sim"
	"estopo/internal/store"
)

var t0 = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) // Wednesday, 2026-W40

type qenv struct {
	t   *testing.T
	ctx context.Context
	st  *store.Store
	in  *store.Ingester
	e   *Engine
	c   *sim.Cluster
}

func newQEnv(t *testing.T) *qenv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir(), clock.NewManual(t0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &qenv{t: t, ctx: ctx, st: st, in: store.NewIngester(st, store.Options{MachineAttr: func(string) string { return "machine" }}, log),
		e: New(st, log, 16), c: sim.Build(sim.PresetSmall("8.19.22"), t0)}
}

func (q *qenv) ms(d time.Duration) int64 { return t0.Add(d).UnixMilli() }

func (q *qenv) capture(d time.Duration, kind record.Kind) {
	q.t.Helper()
	if _, err := q.in.Ingest(q.ctx, q.c.CaptureAt(q.ms(d), "small", kind, "t")); err != nil {
		q.t.Fatal(err)
	}
}

func (q *qenv) full(d time.Duration)  { q.capture(d, record.Full) }
func (q *qenv) probe(d time.Duration) { q.capture(d, record.Light) }

func (q *qenv) copies() int {
	n := 0
	for _, ix := range q.c.Indices {
		n += ix.Pri * (1 + ix.Rep)
	}
	return n
}

func TestStateAt(t *testing.T) {
	q := newQEnv(t)
	if _, err := q.e.StateAt(q.ctx, "small", q.ms(0)); err != ErrNoData {
		t.Fatalf("before any capture: %v", err)
	}
	q.full(0)
	q.probe(5 * time.Minute)
	s, err := q.e.StateAt(q.ctx, "small", q.ms(7*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if s.CaptureMs != q.ms(0) || s.ProbeMs != q.ms(5*time.Minute) || s.Unknown {
		t.Errorf("capture %d probe %d unknown %v", s.CaptureMs, s.ProbeMs, s.Unknown)
	}
	if len(s.Nodes) != len(q.c.Nodes) || len(s.Indices) != len(q.c.Indices) || len(s.Shards) != q.copies() {
		t.Errorf("nodes %d/%d indices %d/%d shards %d/%d", len(s.Nodes), len(q.c.Nodes), len(s.Indices), len(q.c.Indices), len(s.Shards), q.copies())
	}
	if s.Health == nil || s.Health.Status != "green" || s.ClusterSettings["cluster.routing.allocation.awareness.attributes"] != "dc" {
		t.Errorf("health %+v settings %v", s.Health, s.ClusterSettings)
	}
	for _, n := range s.Nodes {
		if !n.Present || n.Machine == "" || n.Metrics == nil || n.Version == "" || n.Attrs["dc"] == "" {
			t.Fatalf("incomplete node %+v", n)
		}
	}
	for _, ix := range s.Indices {
		if ix.Settings["index.routing.allocation.include._tier_preference"] == "" || ix.StoreBytes < 0 {
			t.Fatalf("incomplete index %+v", ix)
		}
	}
	// A failed probe after: unknown, last known state kept.
	fail := &record.Record{Format: record.FormatVersion, Cluster: "small", Kind: record.Light, StartMs: q.ms(10 * time.Minute),
		EndMs: q.ms(10 * time.Minute), Status: record.Failed, Error: "timeout"}
	if _, err := q.in.Ingest(q.ctx, fail); err != nil {
		t.Fatal(err)
	}
	s, _ = q.e.StateAt(q.ctx, "small", q.ms(11*time.Minute))
	if !s.Unknown || s.ProbeMs != q.ms(5*time.Minute) {
		t.Errorf("after failure: unknown=%v probe=%d", s.Unknown, s.ProbeMs)
	}
}

// B6: a copy moved N1 → N2 → N1 between A and B: the diff shows nothing,
// the path shows both moves.
func TestPathShowsRoundTrip(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	// Pick a hot primary and a hot node in the same DC that holds no copy of the shard.
	var index, from, to string
	q.c.Do(func(c *sim.Cluster) {
		for _, ix := range c.Indices {
			// A full-size (yesterday's) hot index with one shard.
			if ix.Setting("index.routing.allocation.include._tier_preference", "") != "data_hot" || ix.Pri != 1 || c.NowMs()-ix.CreatedMs < 20*3600*1000 {
				continue
			}
			src := ix.Shards[0][0].Node
			for _, n := range c.Nodes {
				if n != src && n.HasRole("data_hot") && n.Attrs["dc"] == src.Attrs["dc"] && ix.Shards[0][1].Node != n {
					index, from, to = ix.Name, src.Name, n.Name
					return
				}
			}
		}
	})
	if index == "" {
		t.Fatal("no candidate shard")
	}
	// Slow recoveries so that a capture sees each relocation in progress.
	q.c.Do(func(c *sim.Cluster) { c.SetClusterSetting(true, "indices.recovery.max_bytes_per_sec", "20mb") })
	move := func(a, b string, at time.Duration) time.Duration {
		q.c.AdvanceTo(t0.Add(at))
		var ok bool
		q.c.Do(func(c *sim.Cluster) { ok = c.Relocate(index, 0, a, b) })
		if !ok {
			t.Fatalf("relocate %s %s->%s failed", index, a, b)
		}
		d := at + 2*time.Second
		q.full(d)                  // relocating
		for i := 0; i < 500; i++ { // capture until the move is done
			d += 10 * time.Second
			q.full(d)
			done := false
			q.c.Do(func(c *sim.Cluster) {
				for _, cp := range c.IndexByName(index).Shards[0] {
					if cp.Primary && cp.Node != nil && cp.Node.Name == b && cp.State == sim.Started {
						done = true
					}
				}
			})
			if done {
				return d
			}
		}
		t.Fatal("relocation did not finish")
		return 0
	}
	end := move(from, to, 30*time.Second)
	end = move(to, from, end+10*time.Second)
	q.full(end + time.Minute)
	end += time.Minute

	d, err := q.e.Diff(q.ctx, "small", q.ms(0), q.ms(end))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range d.Shards {
		if s.Index == index && s.Shard == 0 {
			t.Errorf("diff shows %+v", s)
		}
	}
	var uuid string
	st, _ := q.e.StateAt(q.ctx, "small", q.ms(end))
	for _, ix := range st.Indices {
		if ix.Name == index {
			uuid = ix.UUID
		}
	}
	segs, err := q.e.Path(q.ctx, "small", PathQuery{UUID: uuid, Shard: 0, A: q.ms(0), B: q.ms(end)})
	if err != nil {
		t.Fatal(err)
	}
	var primary []string
	for _, s := range segs {
		if s.Primary {
			primary = append(primary, s.Node+":"+s.State)
		}
	}
	want := []string{from + ":STARTED", from + ":RELOCATING", to + ":STARTED", to + ":RELOCATING", from + ":STARTED"}
	if strings.Join(primary, " ") != strings.Join(want, " ") {
		t.Errorf("path %v\nwant %v", primary, want)
	}
}

// B4 and B5 through the Losses screen.
func TestLosses(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	victim := "small-hot-dc1-m01-2"
	var deleted string
	q.c.Do(func(c *sim.Cluster) {
		c.NodeLeave(victim)
		deleted = c.Indices[len(c.Indices)-1].Name
		c.DeleteIndex(deleted)
	})
	q.probe(time.Minute)
	q.full(10 * time.Minute)
	l, err := q.e.Losses(q.ctx, "small", q.ms(0), q.ms(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Groups) != 1 || l.Groups[0].Kind != "node" || l.Groups[0].Name != victim || l.Groups[0].LeftHi != q.ms(time.Minute) {
		t.Fatalf("groups %+v", l.Groups)
	}
	primaries := 0
	for _, c := range l.Groups[0].Copies {
		if c.Primary {
			primaries++
			if !c.Promoted {
				t.Errorf("lost primary without promotion %+v", c)
			}
		}
		if c.After != "reallocated" || c.AfterNode == "" || c.AfterTs == 0 || c.SizeBytes < 0 || c.Zone != "dc1" {
			t.Errorf("copy %+v", c)
		}
	}
	if primaries == 0 || l.Summary.PrimariesLost != primaries || l.Summary.NotPromoted != 0 || l.Summary.Reallocated != l.Summary.Lost {
		t.Errorf("summary %+v", l.Summary)
	}
	if len(l.Deleted) != 1 || l.Deleted[0].Name != deleted {
		t.Errorf("deleted %+v", l.Deleted)
	}
	for _, c := range l.Groups[0].Copies {
		if c.Index == deleted {
			t.Error("a deleted index counted as lost")
		}
	}
}

// B3 in the Losses screen: grouped by machine.
func TestLossesByMachine(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	q.c.Do(func(c *sim.Cluster) {
		c.NodeLeave("small-cold-dc2-m01-1")
		c.NodeLeave("small-cold-dc2-m01-2")
	})
	q.full(5 * time.Minute)
	l, _ := q.e.Losses(q.ctx, "small", q.ms(0), q.ms(10*time.Minute))
	if len(l.Groups) != 1 || l.Groups[0].Kind != "machine" || l.Groups[0].Name != "small-cold-dc2-m01" || len(l.Groups[0].Nodes) != 2 {
		t.Errorf("groups %+v", l.Groups)
	}
}

func TestEventsJournal(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	for i := 1; i <= 6; i++ {
		v := "all"
		if i%2 == 1 {
			v = "primaries"
		}
		q.c.Do(func(c *sim.Cluster) { c.SetClusterSetting(true, "cluster.routing.allocation.enable", v) })
		q.probe(time.Duration(i) * 5 * time.Minute)
	}
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave("small-warm-dc1-m01-1") })
	q.probe(30*time.Minute + 10*time.Second) // before the delayed reallocation: yellow
	all, err := q.e.Events(q.ctx, "small", EventFilter{From: q.ms(0), To: q.ms(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	// 6 cluster settings changes, then node_left and health_changed (green → yellow).
	if len(all.Events) != 8 || all.Events[0].Type != "node_left" || all.Events[1].Type != "health_changed" {
		t.Fatalf("events %+v", all.Events)
	}
	// Pagination: 3 + 3 + 2.
	var got []Event
	f := EventFilter{From: q.ms(0), To: q.ms(time.Hour), Limit: 3}
	for i := 0; i < 5; i++ {
		p, err := q.e.Events(q.ctx, "small", f)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, p.Events...)
		if p.NextTs == 0 {
			break
		}
		f.BeforeTs, f.BeforeKey = p.NextTs, p.NextKey
	}
	if len(got) != 8 {
		t.Errorf("paginated %d events", len(got))
	}
	n, _ := q.e.Events(q.ctx, "small", EventFilter{From: q.ms(0), To: q.ms(time.Hour), Node: "small-warm-dc1-m01-1"})
	if len(n.Events) != 1 {
		t.Errorf("node filter %+v", n.Events)
	}
	m, _ := q.e.Events(q.ctx, "small", EventFilter{From: q.ms(0), To: q.ms(time.Hour), Machine: "small-warm-dc1-m01"})
	if len(m.Events) != 1 {
		t.Errorf("machine filter %+v", m.Events)
	}
	s, _ := q.e.Events(q.ctx, "small", EventFilter{From: q.ms(0), To: q.ms(time.Hour), MaxSeverity: 2})
	if len(s.Events) != 2 {
		t.Errorf("severity filter %+v", s.Events)
	}
	tl, err := q.e.Timeline(q.ctx, "small", q.ms(0), q.ms(time.Hour), 12)
	if err != nil || len(tl) != 12 || tl[0].Status != "green" || tl[6].Status != "yellow" || tl[6].Severe == 0 {
		t.Errorf("timeline %+v %v", tl, err)
	}
}

func TestSearchAndLives(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	name := q.c.Indices[0].Name
	q.c.Do(func(c *sim.Cluster) {
		c.DeleteIndex(name)
		c.CreateIndex(name, 1, 0, 100, map[string]string{"index.routing.allocation.include._tier_preference": "data_hot"})
	})
	q.full(10 * time.Minute)
	hits, err := q.e.Search(q.ctx, []string{"small"}, "SMALL-HOT-DC1", 50)
	if err != nil || len(hits) == 0 || hits[0].Kind != "node" && hits[0].Kind != "machine" {
		t.Fatalf("hits %+v %v", hits, err)
	}
	hits, _ = q.e.Search(q.ctx, []string{"small"}, "logs-00", 50)
	kinds := map[string]bool{}
	for _, h := range hits {
		kinds[h.Kind] = true
	}
	if !kinds["family"] || !kinds["index"] {
		t.Errorf("kinds %v", kinds)
	}
	lives, err := q.e.IndexLives(q.ctx, "small", name)
	if err != nil || len(lives) != 2 {
		t.Errorf("B11: lives %+v %v", lives, err)
	}
	h, err := q.e.IndexHistory(q.ctx, "small", lives[0].Value, q.ms(0), q.ms(time.Hour))
	if err != nil || len(h.Periods) == 0 || h.Periods[0].Settings == nil {
		t.Errorf("index history %+v %v", h, err)
	}
}

// B10 end to end: a node gone for good stays missing (reference: 7 days)
// until acknowledged.
func TestOverviewMissingAndAck(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave("small-warm-dc2-m01-2") })
	q.full(10 * time.Minute)
	o, err := q.e.Overview(q.ctx, "small", q.ms(20*time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Missing) != 1 || o.Missing[0] != "small-warm-dc2-m01-2" || o.Assessment.Level != 2 {
		t.Fatalf("missing %v level %d %+v", o.Missing, o.Assessment.Level, o.Assessment.Concerns)
	}
	if _, err := q.st.Meta.ExecContext(q.ctx, `INSERT INTO node_ack(cluster_id, node_name, from_ms, author, created_ms) VALUES ('small', ?, ?, 'admin', ?)`,
		"small-warm-dc2-m01-2", q.ms(15*time.Minute), q.ms(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	o, _ = q.e.Overview(q.ctx, "small", q.ms(20*time.Minute), nil)
	if len(o.Missing) != 0 {
		t.Errorf("acknowledged node still missing: %v", o.Missing)
	}
	o, _ = q.e.Overview(q.ctx, "small", q.ms(12*time.Minute), nil)
	if len(o.Missing) != 1 {
		t.Errorf("before the acknowledgement date the node is missing: %v", o.Missing)
	}
}

// Queries spanning two partitions (keyframe at the week change).
func TestAcrossPartitions(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	monday := 4*24*time.Hour + 12*time.Hour // 2026-10-05 00:00
	q.full(monday + time.Minute)
	q.c.Do(func(c *sim.Cluster) { c.NodeReplace("small-hot-dc2-m01-1") })
	q.full(monday + 20*time.Minute)
	s, err := q.e.StateAt(q.ctx, "small", q.ms(monday+time.Minute))
	if err != nil || len(s.Shards) == 0 {
		t.Fatalf("state in the new week: %v", err)
	}
	h, err := q.e.NodeHistory(q.ctx, "small", "small-hot-dc2-m01-1", q.ms(0), q.ms(monday+time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Generations) != 2 || len(h.Generations[0].Presence) != 1 || h.Generations[0].Presence[0].ToMs != q.ms(monday+20*time.Minute) {
		t.Errorf("generations %+v", h.Generations)
	}
	if len(h.Metrics) == 0 {
		t.Error("no metrics")
	}
	d, err := q.e.Diff(q.ctx, "small", q.ms(0), q.ms(monday+20*time.Minute))
	if err != nil || d.Summary.NodesAdded != 1 || d.Summary.NodesRemoved != 1 {
		t.Errorf("diff across weeks %+v %v", d.Summary, err)
	}
}

// A diff whose A precedes the history starts from the first full capture.
func TestDiffBeforeHistory(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave("small-warm-dc1-m01-2") })
	q.full(10 * time.Minute)
	d, err := q.e.Diff(q.ctx, "small", q.ms(-24*time.Hour), q.ms(time.Hour))
	if err != nil || d.CaptureA != q.ms(0) || d.Summary.NodesRemoved != 1 {
		t.Errorf("diff %+v %v", d, err)
	}
	if _, err := q.e.Diff(q.ctx, "other", q.ms(0), q.ms(time.Hour)); err != ErrNoData {
		t.Errorf("unknown cluster: %v", err)
	}
}

// The node history lists the node attributes of each period (SPEC §8.2.3),
// across partitions.
func TestNodeHistoryAttributes(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	monday := 4*24*time.Hour + 12*time.Hour
	q.full(monday + time.Minute) // keyframe of the new week
	h, err := q.e.NodeHistory(q.ctx, "small", "small-hot-dc2-m01-1", q.ms(0), q.ms(monday+time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Infos) == 0 {
		t.Fatal("no attribute period")
	}
	for _, p := range h.Infos {
		if p.Attrs["dc"] != "dc2" || p.Attrs["machine"] == "" {
			t.Errorf("attributes of %+v: %v", p.Interval, p.Attrs)
		}
	}
}

// The diff lists the changed index settings one line per index and key
// (SPEC §8.2.5, "settings" tab).
func TestDiffIndexSettings(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	name := q.c.Indices[0].Name
	q.c.Do(func(c *sim.Cluster) { c.SetIndexSetting(name, "index.routing.allocation.total_shards_per_node", "2") })
	q.full(20 * time.Minute)
	d, err := q.e.Diff(q.ctx, "small", q.ms(0), q.ms(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	ch := d.IndexSettingChanges()
	if len(ch) != 1 || ch[0].Index != name || ch[0].Key != "index.routing.allocation.total_shards_per_node" || ch[0].To != "2" {
		t.Errorf("setting changes: %+v", ch)
	}
	if d.Summary.SettingsChanged != 1 {
		t.Errorf("summary: %+v", d.Summary)
	}
}
