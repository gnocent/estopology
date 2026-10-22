package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/record"
	"estopo/internal/sim"
)

// Wednesday of ISO week 2026-W40.
var t0 = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

type tenv struct {
	t   *testing.T
	ctx context.Context
	dir string
	st  *Store
	in  *Ingester
	c   *sim.Cluster
}

func newTEnv(t *testing.T) *tenv {
	t.Helper()
	dir := t.TempDir()
	e := &tenv{t: t, ctx: context.Background(), dir: dir, c: sim.Build(sim.PresetSmall("8.19.22"), t0)}
	e.open()
	return e
}

func (e *tenv) open() {
	st, err := Open(e.ctx, e.dir, clock.NewManual(t0))
	if err != nil {
		e.t.Fatal(err)
	}
	e.st = st
	e.in = NewIngester(st, Options{MachineAttr: func(string) string { return "machine" }}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	e.t.Cleanup(func() { st.Close() })
}

func (e *tenv) ingest(r *record.Record) Result {
	e.t.Helper()
	res, err := e.in.Ingest(e.ctx, r)
	if err != nil {
		e.t.Fatalf("ingest %s at %s: %v", r.Kind, time.UnixMilli(r.StartMs).UTC(), err)
	}
	return res
}

func (e *tenv) at(d time.Duration) int64 { return t0.Add(d).UnixMilli() }

func (e *tenv) full(d time.Duration) Result {
	return e.ingest(e.c.CaptureAt(e.at(d), "small", record.Full, record.TriggerSchedule))
}

func (e *tenv) probe(d time.Duration) Result {
	return e.ingest(e.c.CaptureAt(e.at(d), "small", record.Light, record.TriggerSchedule))
}

// db returns the partition holding time d.
func (e *tenv) db(d time.Duration) *sql.DB {
	p, err := e.st.Partition(e.ctx, "small", e.at(d))
	if err != nil {
		e.t.Fatal(err)
	}
	return p.DB()
}

func (e *tenv) count(d time.Duration, query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db(d).QueryRowContext(e.ctx, query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (e *tenv) events(d time.Duration, typ string) []string {
	e.t.Helper()
	rows, err := e.db(d).QueryContext(e.ctx, `SELECT subject_ref, coalesce(ts_lo, 0), ts_hi, coalesce(payload, '') FROM event WHERE type = ? ORDER BY id`, typ)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ref, payload string
		var lo, hi int64
		rows.Scan(&ref, &lo, &hi, &payload)
		out = append(out, fmt.Sprintf("%s|%d|%d|%s", ref, lo, hi, payload))
	}
	return out
}

func (e *tenv) shardCopies() int {
	n := 0
	for _, ix := range e.c.Indices {
		n += ix.Pri * (1 + ix.Rep)
	}
	return n
}

var tables = []string{"node", "node_presence", "node_info", "idx", "index_iv", "shard_iv", "cluster_settings_iv", "remote_iv"}

func (e *tenv) intervalCounts(d time.Duration) map[string]int {
	m := map[string]int{}
	for _, tbl := range tables {
		m[tbl] = e.count(d, "SELECT count(*) FROM "+tbl)
	}
	return m
}

func TestFirstFullCapture(t *testing.T) {
	e := newTEnv(t)
	res := e.full(0)
	copies := e.shardCopies()
	if res.ShardsOpened != copies {
		t.Errorf("opened %d shard intervals, want %d", res.ShardsOpened, copies)
	}
	if n := e.count(0, `SELECT count(*) FROM shard_iv WHERE valid_to IS NULL`); n != copies {
		t.Errorf("open shard intervals %d", n)
	}
	if n := e.count(0, `SELECT count(*) FROM index_iv WHERE valid_to IS NULL`); n != len(e.c.Indices) {
		t.Errorf("open index intervals %d", n)
	}
	if n := e.count(0, `SELECT count(*) FROM node_presence WHERE valid_to IS NULL`); n != len(e.c.Nodes) {
		t.Errorf("present nodes %d", n)
	}
	if n := e.count(0, `SELECT count(*) FROM event WHERE type = 'index_created'`); n != 0 {
		t.Errorf("initial import produced %d index_created events", n)
	}
	// Families are detected; machines come from the configured attribute.
	if n := e.count(0, `SELECT count(DISTINCT family) FROM idx`); n != 5 {
		t.Errorf("%d families", n)
	}
	if n := e.count(0, `SELECT count(*) FROM node_info WHERE machine NOT LIKE 'small-%'`); n != 0 {
		t.Errorf("%d nodes without machine", n)
	}
}

// B1: two identical full captures open no interval.
func TestIdenticalCapturesOpenNothing(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	before := e.intervalCounts(0)
	caps := e.count(0, `SELECT count(*) FROM capture`)
	res := e.full(time.Second) // nothing moves in one simulated second
	if res.ShardsOpened+res.ShardsClosed != 0 || res.Events != 0 {
		t.Errorf("second capture: %+v", res)
	}
	after := e.intervalCounts(0)
	for _, tbl := range tables {
		if before[tbl] != after[tbl] {
			t.Errorf("%s: %d -> %d rows", tbl, before[tbl], after[tbl])
		}
	}
	if n := e.count(0, `SELECT count(*) FROM capture`); n != caps+1 {
		t.Errorf("captures %d -> %d", caps, n)
	}
	if n := e.count(0, `SELECT count(*) FROM health`); n != 2 {
		t.Errorf("health rows %d", n)
	}
	// Shard sizes did not move: no new shard metric.
	if n := e.count(0, `SELECT count(*) FROM shard_metric WHERE ts = ?`, e.at(time.Second)); n != 0 {
		t.Errorf("%d shard metrics rewritten inside the dead band", n)
	}
}

// B2: a node leaves between two probes.
func TestNodeLeftEvent(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	e.probe(5 * time.Minute)
	e.c.Do(func(c *sim.Cluster) { c.NodeLeave("small-hot-dc1-m01-1") })
	e.probe(10 * time.Minute)
	ev := e.events(0, "node_left")
	want := fmt.Sprintf("small-hot-dc1-m01-1|%d|%d|", e.at(5*time.Minute), e.at(10*time.Minute))
	if len(ev) != 1 || !strings.HasPrefix(ev[0], want) {
		t.Fatalf("node_left events %v, want prefix %s", ev, want)
	}
	if n := e.count(0, `SELECT count(*) FROM node_presence p JOIN node n ON n.id = p.node_id
		WHERE n.name = 'small-hot-dc1-m01-1' AND p.valid_to = ?`, e.at(10*time.Minute)); n != 1 {
		t.Error("presence not closed")
	}
	// Back: node_joined.
	e.c.Do(func(c *sim.Cluster) { c.NodeJoin("small-hot-dc1-m01-1") })
	e.probe(15 * time.Minute)
	if ev := e.events(0, "node_joined"); len(ev) != 1 {
		t.Errorf("node_joined %v", ev)
	}
}

// B3: every node of a machine leaves together.
func TestMachineLost(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	e.c.Do(func(c *sim.Cluster) {
		c.NodeLeave("small-cold-dc1-m01-1")
		c.NodeLeave("small-cold-dc1-m01-2")
	})
	e.probe(5 * time.Minute)
	ev := e.events(0, "machine_lost")
	if len(ev) != 1 || !strings.Contains(ev[0], `small-cold-dc1-m01-1","small-cold-dc1-m01-2`) {
		t.Errorf("machine_lost %v", ev)
	}
	if n := len(e.events(0, "node_left")); n != 2 {
		t.Errorf("node_left %d", n)
	}
}

// B9: same node name, new ES id.
func TestNewGeneration(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	e.c.Do(func(c *sim.Cluster) { c.NodeReplace("small-warm-dc1-m01-1") })
	e.full(10 * time.Minute)
	if ev := e.events(0, "node_new_generation"); len(ev) != 1 || !strings.Contains(ev[0], `"generation":2`) {
		t.Errorf("node_new_generation %v", ev)
	}
	if n := e.count(0, `SELECT count(*) FROM node WHERE name = 'small-warm-dc1-m01-1'`); n != 2 {
		t.Errorf("%d node rows for the name", n)
	}
	if ev := e.events(0, "node_left"); len(ev) != 1 {
		t.Errorf("old generation must leave: %v", ev)
	}
}

// B11: an index recreated under the same name is another entity.
func TestIndexRecreated(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	name := e.c.Indices[len(e.c.Indices)-1].Name
	e.c.Do(func(c *sim.Cluster) {
		c.DeleteIndex(name)
		c.CreateIndex(name, 1, 1, 1000, map[string]string{"index.routing.allocation.include._tier_preference": "data_hot"})
	})
	e.full(10 * time.Minute)
	if n := e.count(0, `SELECT count(DISTINCT uuid) FROM idx WHERE name = ?`, name); n != 2 {
		t.Errorf("%d uuids for %s", n, name)
	}
	if len(e.events(0, "index_deleted")) != 1 || len(e.events(0, "index_created")) != 1 {
		t.Errorf("events: deleted %v created %v", e.events(0, "index_deleted"), e.events(0, "index_created"))
	}
}

// B19: rolling upgrade; B20: cluster setting change; B21: remote down.
func TestUpgradeSettingsRemote(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	e.c.Do(func(c *sim.Cluster) {
		c.SetNodeVersion("small-hot-dc1-m01-1", "9.5.4")
		c.SetClusterSetting(false, "cluster.routing.allocation.enable", "primaries")
		c.SetRemoteConnected("other", false)
	})
	e.full(10 * time.Minute)
	if ev := e.events(0, "node_upgraded"); len(ev) != 1 || !strings.Contains(ev[0], `"es":"9.5.4"`) {
		t.Errorf("node_upgraded %v", ev)
	}
	ev := e.events(0, "cluster_settings_changed")
	if len(ev) != 1 || !strings.Contains(ev[0], `"key":"p:cluster.routing.allocation.enable","to":"primaries"`) {
		t.Errorf("cluster_settings_changed %v", ev)
	}
	if ev := e.events(0, "remote_disconnected"); len(ev) != 1 || !strings.HasPrefix(ev[0], "other|") {
		t.Errorf("remote_disconnected %v", ev)
	}
}

// SPEC §6.4: ts_lo is the last capture where the old state was seen. A failed
// probe in between saw nothing: the window starts before it, also across a
// week change and a restart.
func TestSettingsRemoteWindowSkipsFailedProbes(t *testing.T) {
	for _, restart := range []bool{false, true} {
		e := newTEnv(t)
		e.full(0)
		e.ingest(&record.Record{Format: record.FormatVersion, Cluster: "small", Kind: record.Light, StartMs: e.at(nextWeek),
			EndMs: e.at(nextWeek), Status: record.Failed, Error: "timeout"})
		if restart {
			e.st.Close()
			e.open()
		}
		e.c.Do(func(c *sim.Cluster) {
			c.SetClusterSetting(false, "cluster.routing.allocation.enable", "primaries")
			c.SetRemoteConnected("other", false)
		})
		e.probe(nextWeek + 5*time.Minute)
		want := fmt.Sprintf("|%d|%d|", e.at(0), e.at(nextWeek+5*time.Minute))
		for _, typ := range []string{evClusterSettingsChanged, evRemoteDisconnected} {
			if ev := e.events(nextWeek, typ); len(ev) != 1 || !strings.Contains(ev[0], want) {
				t.Errorf("restart=%v: %s %v, want window %s", restart, typ, ev, want)
			}
		}
	}
}

// _all/_settings failed (the capture stays ok, SPEC §5.5): the settings of the
// indices seen for the first time are unknown, and learning them at the next
// capture is not a change.
func TestUnknownIndexSettingsAreNotChanged(t *testing.T) {
	e := newTEnv(t)
	r := e.c.CaptureAt(e.at(0), "small", record.Full, record.TriggerSchedule)
	r.IndexSettings = nil
	e.ingest(r)
	if n := e.count(0, `SELECT count(*) FROM index_iv WHERE settings_blob IS NULL`); n != len(e.c.Indices) {
		t.Fatalf("test assumption: %d indices without settings", n)
	}
	e.full(10 * time.Minute)
	if ev := e.events(0, evIndexSettingsChanged); len(ev) != 0 {
		t.Errorf("%d index_settings_changed events, first: %s", len(ev), ev[0])
	}
	// An index created while _all/_settings fails: a replica change seen with
	// its first settings is still reported.
	e.c.Do(func(c *sim.Cluster) { c.CreateIndex("late-1", 1, 1, 1000, nil) })
	r = e.c.CaptureAt(e.at(20*time.Minute), "small", record.Full, record.TriggerSchedule)
	r.IndexSettings = nil
	e.ingest(r)
	r = e.c.CaptureAt(e.at(30*time.Minute), "small", record.Full, record.TriggerSchedule)
	for i := range r.Indices {
		if r.Indices[i].Name == "late-1" {
			r.Indices[i].Rep = 2
		}
	}
	e.ingest(r)
	ev := e.events(0, evIndexSettingsChanged)
	if len(ev) != 1 || !strings.Contains(ev[0], `{"from":"1","key":"index.number_of_replicas","to":"2"}`) {
		t.Errorf("index_settings_changed %v", ev)
	}
}

// The same, with a week change between the absence and the return: the
// result does not depend on a restart of estopo (SPEC §6.7), and the node
// copied for the comparison is not in the state of the new week.
func TestUpgradeAcrossAnAbsenceAndAWeekChange(t *testing.T) {
	for _, restart := range []bool{false, true} {
		e := newTEnv(t)
		const node = "small-hot-dc1-m01-2"
		e.full(0)
		e.c.Do(func(c *sim.Cluster) { c.NodeLeave(node) })
		e.full(10 * time.Minute) // the node is absent from this capture
		if res := e.probe(nextWeek); !res.Keyframe {
			t.Fatal("no keyframe")
		}
		if restart {
			e.st.Close()
			e.open()
		}
		if n := e.count(nextWeek, `SELECT count(*) FROM node_info i JOIN node n ON n.id = i.node_id
			WHERE n.name = ? AND (i.valid_to IS NULL OR i.valid_to > ?)`, node, e.at(nextWeek)); n != 0 {
			t.Errorf("restart=%v: the absent node has %d node_info intervals open in the new week", restart, n)
		}
		e.c.Do(func(c *sim.Cluster) {
			c.SetNodeVersion(node, "9.5.4")
			c.NodeJoin(node)
		})
		e.full(nextWeek + 10*time.Minute)
		ev := e.events(nextWeek, evNodeUpgraded)
		if len(ev) != 1 || !strings.HasPrefix(ev[0], node+"|") || !strings.Contains(ev[0], `"es":"8.19.22"`) {
			t.Errorf("restart=%v: node_upgraded events: %v", restart, ev)
		}
	}
}

// B7: a partial capture closes nothing.
func TestPartialCaptureClosesNothing(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	before := e.intervalCounts(0)
	e.c.Do(func(c *sim.Cluster) { c.DeleteIndex(c.Indices[0].Name) })
	r := e.c.CaptureAt(e.at(10*time.Minute), "small", record.Full, record.TriggerSchedule)
	r.Status, r.Error, r.Shards, r.Indices, r.Nodes = record.Partial, "cat_shards: timeout", nil, nil, nil
	e.ingest(r)
	if n := e.count(0, `SELECT count(*) FROM shard_iv WHERE valid_to IS NOT NULL`); n != 0 {
		t.Errorf("%d shard intervals closed by a partial capture", n)
	}
	after := e.intervalCounts(0)
	if before["index_iv"] != after["index_iv"] || before["shard_iv"] != after["shard_iv"] {
		t.Errorf("structure changed: %v -> %v", before, after)
	}
	if ev := e.events(0, "capture_failed"); len(ev) != 1 {
		t.Errorf("capture_failed %v", ev)
	}
	// Failed probe: only the capture row and the event.
	r = &record.Record{Format: record.FormatVersion, Cluster: "small", Kind: record.Light, StartMs: e.at(15 * time.Minute),
		EndMs: e.at(15 * time.Minute), Status: record.Failed, Error: "timeout"}
	e.ingest(r)
	if n := e.count(0, `SELECT count(*) FROM node_presence WHERE valid_to IS NOT NULL`); n != 0 {
		t.Error("a failed probe closed node presences")
	}
}

// A new ISO week starts a new partition that can be read alone (keyframe).
func TestKeyframe(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	// 2026-10-05 is the Monday of W41: 4 days and 12 hours after t0.
	next := 4*24*time.Hour + 12*time.Hour + time.Minute
	res := e.full(next)
	if !res.Keyframe {
		t.Fatal("no keyframe at the week change")
	}
	if WeekOf(e.at(next)).Key != "2026-W41" {
		t.Fatal("test assumption")
	}
	// The new partition alone knows every open interval.
	openShards := e.count(next, `SELECT count(*) FROM shard_iv WHERE valid_to IS NULL`)
	if openShards != e.shardCopies() {
		t.Errorf("new partition: %d open shard intervals, cluster has %d copies", openShards, e.shardCopies())
	}
	if n := e.count(next, `SELECT count(*) FROM capture WHERE kind = 'keyframe'`); n != 1 {
		t.Errorf("%d keyframe captures", n)
	}
	// Intervals copied by the keyframe keep their start date.
	if n := e.count(next, `SELECT count(*) FROM node_presence WHERE valid_from = ?`, e.at(0)); n != len(e.c.Nodes) {
		t.Errorf("%d presences keep their original start", n)
	}
	// Blobs referenced by open intervals are present.
	if n := e.count(next, `SELECT count(*) FROM index_iv v LEFT JOIN blob b ON b.hash = v.settings_blob
		WHERE v.valid_to IS NULL AND v.settings_blob IS NOT NULL AND b.hash IS NULL`); n != 0 {
		t.Errorf("%d settings blobs missing in the new partition", n)
	}
	parts, _ := e.st.Partitions(e.ctx, "small")
	if len(parts) != 2 || !parts[0].Sealed || parts[1].Sealed {
		t.Errorf("catalogue %+v", parts)
	}
}

// After a restart the mirror is rebuilt: the next identical capture writes
// no interval.
func TestRestartReloadsState(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	e.probe(5 * time.Minute)
	e.st.Close()
	e.open()
	before := e.intervalCounts(0)
	res := e.full(5*time.Minute + time.Second)
	if res.ShardsOpened+res.ShardsClosed != 0 {
		t.Errorf("after restart: %+v", res)
	}
	after := e.intervalCounts(0)
	for _, tbl := range tables {
		if before[tbl] != after[tbl] {
			t.Errorf("%s: %d -> %d rows after restart", tbl, before[tbl], after[tbl])
		}
	}
	// A node leaving is still detected with the right window.
	e.c.Do(func(c *sim.Cluster) { c.NodeLeave("small-hot-dc2-m01-2") })
	e.probe(10 * time.Minute)
	if ev := e.events(0, "node_left"); len(ev) != 1 || !strings.Contains(ev[0], fmt.Sprintf("|%d|", e.at(5*time.Minute+time.Second))) {
		t.Errorf("node_left after restart %v", ev)
	}
}

// Replaying the same records into a fresh store gives the same content.
func TestDeterministicReplay(t *testing.T) {
	var recs []*record.Record
	c := sim.Build(sim.PresetSmall("9.5.4"), t0)
	for i := 0; i <= 36; i++ { // 3 hours: probes every 5 min, full every 30 min
		kind := record.Light
		if i%6 == 0 {
			kind = record.Full
		}
		if i == 20 {
			c.Do(func(c *sim.Cluster) { c.NodeLeave("small-warm-dc2-m01-1") })
		}
		recs = append(recs, c.CaptureAt(t0.Add(time.Duration(i)*5*time.Minute).UnixMilli(), "small", kind, "t"))
	}
	dump := func() string {
		e := newTEnv(t)
		for _, r := range recs {
			e.ingest(r)
		}
		var b strings.Builder
		for _, tbl := range append(tables, "event", "shard_metric", "node_metric", "index_metric", "health", "blob") {
			rows, err := e.db(0).QueryContext(e.ctx, "SELECT * FROM "+tbl+" ORDER BY 1, 2")
			if err != nil {
				t.Fatal(err)
			}
			cols, _ := rows.Columns()
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			for rows.Next() {
				rows.Scan(ptrs...)
				fmt.Fprintln(&b, tbl, vals)
			}
			rows.Close()
		}
		return b.String()
	}
	a, b := dump(), dump()
	if a != b {
		t.Error("two replays of the same records differ")
	}
	if !strings.Contains(a, "node_left") {
		t.Error("scenario did not produce node_left")
	}
}

func TestWeekOf(t *testing.T) {
	for ts, want := range map[string]string{
		"2026-09-28T00:00:00Z": "2026-W40",
		"2026-10-04T23:59:59Z": "2026-W40",
		"2026-10-05T00:00:00Z": "2026-W41",
		"2027-01-01T12:00:00Z": "2026-W53",
	} {
		tm, _ := time.Parse(time.RFC3339, ts)
		w := WeekOf(tm.UnixMilli())
		if w.Key != want || w.ToMs-w.FromMs != 7*24*3600*1000 || tm.UnixMilli() < w.FromMs || tm.UnixMilli() >= w.ToMs {
			t.Errorf("%s: %+v, want %s", ts, w, want)
		}
	}
	if filepath.Base(partitionRel("c", "2026-W40")) != "2026-W40.db" {
		t.Error("partition file name")
	}
}

// SPEC §5.4: two nodes carrying the same name at once are signalled, once
// per occurrence, also across a restart.
func TestDuplicateNodeName(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	e.c.Do(func(c *sim.Cluster) {
		old := c.Nodes[0]
		c.AddNode(old.Name, "machine-x", "10.9.9.9", 7, old.Roles, map[string]string{"dc": "dc9"}, old.DiskTotal)
	})
	name := e.c.Nodes[0].Name
	e.probe(5 * time.Minute)
	ev := e.events(0, "node_name_duplicate")
	if len(ev) != 1 || !strings.HasPrefix(ev[0], name+"|") || !strings.Contains(ev[0], `"count":2`) {
		t.Fatalf("duplicate name events: %v", ev)
	}
	e.probe(10 * time.Minute)
	e.st.Close()
	e.open()
	e.probe(15 * time.Minute)
	if ev := e.events(0, "node_name_duplicate"); len(ev) != 1 {
		t.Errorf("repeated: %v", ev)
	}
}

// B19: an upgrade restarts the node. A full capture taken while the node is
// down must not hide the version change when it comes back, even if estopo
// restarted in between.
func TestUpgradeAcrossAnAbsence(t *testing.T) {
	for _, restart := range []bool{false, true} {
		e := newTEnv(t)
		const node = "small-hot-dc1-m01-2"
		e.full(0)
		e.c.Do(func(c *sim.Cluster) { c.NodeLeave(node) })
		e.full(10 * time.Minute) // the node is absent from this capture
		if restart {
			e.st.Close()
			e.open()
		}
		e.c.Do(func(c *sim.Cluster) {
			c.SetNodeVersion(node, "9.5.4")
			c.NodeJoin(node)
		})
		e.full(20 * time.Minute)
		ev := e.events(0, "node_upgraded")
		if len(ev) != 1 || !strings.HasPrefix(ev[0], node+"|") || !strings.Contains(ev[0], `"es":"9.5.4"`) || !strings.Contains(ev[0], `"es":"8.19.22"`) {
			t.Errorf("restart=%v: node_upgraded events: %v", restart, ev)
		}
		// A plain restart of the node (same version) is not an upgrade.
		e.c.Do(func(c *sim.Cluster) { c.NodeLeave(node) })
		e.full(30 * time.Minute)
		e.c.Do(func(c *sim.Cluster) { c.NodeJoin(node) })
		e.full(40 * time.Minute)
		if ev := e.events(0, "node_upgraded"); len(ev) != 1 {
			t.Errorf("restart=%v: upgrade events after a plain restart: %v", restart, ev)
		}
	}
}
