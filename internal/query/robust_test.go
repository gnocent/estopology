package query

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"estopo/internal/esclient"
	"estopo/internal/record"
	"estopo/internal/sim"
	"estopo/internal/store"
)

// oneConn leaves a single read connection to each partition, as on a loaded
// instance: a query issued while the rows of another are open then waits for
// the connection those rows hold.
func (q *qenv) oneConn() {
	q.t.Helper()
	infos, err := q.st.Partitions(q.ctx, "small")
	if err != nil {
		q.t.Fatal(err)
	}
	for _, info := range infos {
		p, err := q.st.OpenExisting(q.ctx, info)
		if err != nil {
			q.t.Fatal(err)
		}
		p.DB().SetMaxOpenConns(1)
	}
}

// A shard held by a node absent from the _nodes answer of the same capture
// (the node has no open info): the state is still reconstructed, without a
// query nested in the shard rows.
func TestStateAtShardOnNodeWithoutInfo(t *testing.T) {
	q := newQEnv(t)
	r := q.c.CaptureAt(q.ms(0), "small", record.Full, "t")
	gone := r.Shards[0].Node
	var nodes []esclient.NodeInfo
	for _, n := range r.Nodes {
		if n.Name != gone {
			nodes = append(nodes, n)
		}
	}
	r.Nodes = nodes
	if _, err := q.in.Ingest(q.ctx, r); err != nil {
		t.Fatal(err)
	}
	q.oneConn()
	ctx, cancel := context.WithTimeout(q.ctx, 10*time.Second)
	defer cancel()
	s, err := q.e.StateAt(ctx, "small", q.ms(time.Minute))
	if err != nil {
		t.Fatalf("StateAt: %v", err)
	}
	on := 0
	for _, sh := range s.Shards {
		if sh.Node >= 0 && s.Nodes[sh.Node].Name == gone {
			on++
		}
	}
	if on == 0 || len(s.Shards) != q.copies() {
		t.Errorf("%d shards on %s, %d shards in all (want %d)", on, gone, len(s.Shards), q.copies())
	}
}

// Every query of the engine runs with one connection per partition: none
// issues a query from inside the rows of another.
func TestQueriesWithOneConnection(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	victim := "small-hot-dc1-m01-2"
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave(victim) })
	q.probe(time.Minute)
	q.full(10 * time.Minute)
	q.full(8 * 24 * time.Hour) // a second partition
	q.oneConn()

	ctx, cancel := context.WithTimeout(q.ctx, 30*time.Second)
	defer cancel()
	a, b := q.ms(0), q.ms(9*24*time.Hour)
	s, err := q.e.StateAt(ctx, "small", q.ms(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	uuid, family, node := s.Indices[0].UUID, s.Indices[0].Family, s.Nodes[0]
	calls := map[string]func() error{
		"Diff":   func() error { _, err := q.e.Diff(ctx, "small", a, b); return err },
		"Losses": func() error { _, err := q.e.Losses(ctx, "small", a, b); return err },
		"Events": func() error { _, err := q.e.Events(ctx, "small", EventFilter{From: a, To: b}); return err },
		"Events node": func() error {
			_, err := q.e.Events(ctx, "small", EventFilter{From: a, To: b, Node: victim})
			return err
		},
		"Events machine": func() error {
			_, err := q.e.Events(ctx, "small", EventFilter{From: a, To: b, Machine: node.Machine})
			return err
		},
		"Events family": func() error {
			_, err := q.e.Events(ctx, "small", EventFilter{From: a, To: b, Family: family})
			return err
		},
		"Timeline":         func() error { _, err := q.e.Timeline(ctx, "small", a, b, 50); return err },
		"NodeHistory":      func() error { _, err := q.e.NodeHistory(ctx, "small", victim, a, b, 100); return err },
		"IndexHistory":     func() error { _, err := q.e.IndexHistory(ctx, "small", uuid, a, b); return err },
		"IndexLives":       func() error { _, err := q.e.IndexLives(ctx, "small", s.Indices[0].Name); return err },
		"IndexSizeHistory": func() error { _, err := q.e.IndexSizeHistory(ctx, "small", uuid, a, b, 20); return err },
		"FamilyHistory":    func() error { _, err := q.e.FamilyHistory(ctx, "small", family, a, b, 20); return err },
		"GroupMetrics":     func() error { _, err := q.e.GroupMetrics(ctx, "small", []string{node.Name}, a, b, 20); return err },
		"Path": func() error {
			_, err := q.e.Path(ctx, "small", PathQuery{UUID: uuid, Shard: -1, A: a, B: b})
			return err
		},
		"Path family": func() error {
			_, err := q.e.Path(ctx, "small", PathQuery{Family: family, Shard: -1, A: a, B: b})
			return err
		},
		"LastKnownNodes": func() error { _, err := q.e.LastKnownNodes(ctx, "small", []string{victim}, b); return err },
		"ShardSizes":     func() error { _, err := q.e.ShardSizes(ctx, "small", uuid, b); return err },
		"ReferenceNodes": func() error { _, err := q.e.ReferenceNodes(ctx, "small", b, nil); return err },
		"Overview":       func() error { _, err := q.e.Overview(ctx, "small", b, nil); return err },
		"Search":         func() error { _, err := q.e.Search(ctx, []string{"small"}, "small", 20); return err },
		"LastCaptureTs":  func() error { _, err := q.e.LastCaptureTs(ctx, "small"); return err },
	}
	for name, call := range calls {
		if err := call(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The journal filtered by a family of 2500 indices: the filter is one SQL
// term, not one per index (SQLite limits the depth of an expression).
func TestEventsOfLargeFamily(t *testing.T) {
	q := newQEnv(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	in := store.NewIngester(q.st, store.Options{Family: func(_, index string) string {
		if strings.HasPrefix(index, "bulk-") {
			return "bulk"
		}
		return "other"
	}}, log)
	// Shard 0 of bulk-0000 moves from one node to another between the captures.
	capture := func(d time.Duration, on int) {
		r := q.c.CaptureAt(q.ms(d), "small", record.Full, "t")
		for i := 0; i < 2500; i++ {
			r.Indices = append(r.Indices, esclient.CatIndex{UUID: fmt.Sprintf("uuid%04d", i), Name: fmt.Sprintf("bulk-%04d", i),
				Status: "open", Health: "green", Pri: 1, Docs: 1, StoreBytes: 1, PriStoreBytes: 1})
		}
		n := r.Nodes[on]
		r.Shards = append(r.Shards, esclient.CatShard{Index: "bulk-0000", Primary: true, State: "STARTED", Docs: 1, StoreBytes: 1,
			Node: n.Name, NodeID: n.ID})
		r.Sort()
		if _, err := in.Ingest(q.ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	capture(0, 0)
	capture(10*time.Minute, 1)
	f := EventFilter{From: q.ms(-time.Hour), To: q.ms(time.Hour), Family: "bulk"}
	page, err := q.e.Events(q.ctx, "small", f)
	if err != nil {
		t.Fatalf("events of a family of 2500 indices: %v", err)
	}
	if len(page.Events) != 1 || page.Events[0].Type != "shard_relocated" || page.Events[0].Ref != "uuid0000/0" {
		t.Errorf("events of the family: %+v", page.Events)
	}
	f.Family = "nothing"
	if page, err := q.e.Events(q.ctx, "small", f); err != nil || len(page.Events) != 0 {
		t.Errorf("events of an unknown family: %+v %v", page, err)
	}
}

// % and _ in the node filter are literal characters, not LIKE wildcards.
func TestEventsNodeFilterIsLiteral(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	victim := "small-hot-dc1-m01-2"
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave(victim) })
	q.full(10 * time.Minute)
	w := EventFilter{From: q.ms(0), To: q.ms(time.Hour), Types: []string{"shard_lost"}}
	w.Node = victim
	if page, err := q.e.Events(q.ctx, "small", w); err != nil || len(page.Events) == 0 {
		t.Fatalf("shards lost with %s: %+v %v", victim, page, err)
	}
	for _, pattern := range []string{"%", "small_hot_dc1_m01_2", `\`} {
		w.Node = pattern
		if page, err := q.e.Events(q.ctx, "small", w); err != nil || len(page.Events) != 0 {
			t.Errorf("node filter %q: %d events, %v", pattern, len(page.Events), err)
		}
	}
}

// Losses loads a bounded number of events and says so; a shard event whose
// subject has no slash is read as it is.
func TestLossesBoundedAndMalformedSubject(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	q.c.Do(func(c *sim.Cluster) { c.NodeLeave("small-hot-dc1-m01-2") })
	q.full(10 * time.Minute)
	l, err := q.e.Losses(q.ctx, "small", q.ms(0), q.ms(time.Hour))
	if err != nil || l.Truncated || l.Summary.Lost < 3 {
		t.Fatalf("losses %+v %v", l, err)
	}

	infos, _ := q.st.Partitions(q.ctx, "small")
	p, err := q.st.OpenExisting(q.ctx, infos[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DB().ExecContext(q.ctx, `INSERT INTO event(ts_hi, type, severity, subject_kind, subject_ref, payload)
		VALUES (?, 'shard_lost', 2, 'shard', 'noslash', '{"index":"x","node":"n"}')`, q.ms(11*time.Minute)); err != nil {
		t.Fatal(err)
	}
	all, err := q.e.Losses(q.ctx, "small", q.ms(0), q.ms(time.Hour))
	if err != nil || all.Summary.Lost != l.Summary.Lost+1 {
		t.Fatalf("losses with a subject without slash: %+v %v", all, err)
	}

	defer func(n int) { maxLossEvents = n }(maxLossEvents)
	maxLossEvents = 3
	cut, err := q.e.Losses(q.ctx, "small", q.ms(0), q.ms(time.Hour))
	if err != nil || !cut.Truncated || cut.Summary.Lost > 3 {
		t.Errorf("losses over the limit: truncated %v, %+v, %v", cut != nil && cut.Truncated, cut, err)
	}
}

// A window wider than an int64 difference (dates given by a caller) neither
// overflows nor indexes out of range.
func TestExtremeWindows(t *testing.T) {
	q := newQEnv(t)
	q.full(0)
	q.probe(5 * time.Minute)
	const far = 5e18
	s, err := q.e.StateAt(q.ctx, "small", q.ms(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	pts, err := q.e.GroupMetrics(q.ctx, "small", []string{s.Nodes[0].Name}, -far, far, 100)
	if err != nil || len(pts) == 0 {
		t.Errorf("group metrics: %d points, %v", len(pts), err)
	}
	if pts, err := q.e.GroupMetrics(q.ctx, "small", []string{s.Nodes[0].Name}, q.ms(0), q.ms(time.Hour), 1<<40); err != nil || len(pts) == 0 {
		t.Errorf("group metrics with too many points: %d points, %v", len(pts), err)
	}
	tl, err := q.e.Timeline(q.ctx, "small", -far, far, 200)
	if err != nil || len(tl) != 200 {
		t.Fatalf("timeline: %d buckets, %v", len(tl), err)
	}
	seen := false
	for i, b := range tl {
		if b.ToMs < b.FromMs || (i > 0 && b.FromMs < tl[i-1].FromMs) {
			t.Fatalf("bucket %d: %+v", i, b)
		}
		seen = seen || b.Status == "green"
	}
	if !seen {
		t.Error("timeline: the health of the captures is in no bucket")
	}
	if _, err := q.e.FamilyHistory(q.ctx, "small", s.Indices[0].Family, -far, far, 50); err != nil {
		t.Errorf("family history: %v", err)
	}
}
