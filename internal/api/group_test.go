package api

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"estopo/internal/derive"
	"estopo/internal/query"
)

// pathTo returns the labels leading to the first group that satisfies ok.
func pathTo(g *derive.Group, ok func(*derive.Group) bool) []string {
	for _, c := range g.Children {
		if ok(c) {
			return []string{c.Label}
		}
		if p := pathTo(c, ok); p != nil {
			return append([]string{c.Label}, p...)
		}
	}
	return nil
}

func groupQuery(labels []string) string {
	q := url.Values{}
	for _, l := range labels {
		q.Add("g", l)
	}
	return q.Encode()
}

func TestGroup(t *testing.T) {
	e := newAEnv(t)
	var st StateView
	e.get("/api/v1/clusters/small/state?at="+ms(time.Hour), &st)

	// First level: the members and what they hold.
	top := st.Hierarchy.Children[0]
	var g GroupDetail
	e.get("/api/v1/clusters/small/group?at="+ms(time.Hour)+"&"+groupQuery([]string{top.Label}), &g)
	if len(g.Path) != 1 || g.Path[0].Label != top.Label || g.Path[0].Kind != top.Kind || len(g.Nodes) != len(top.Nodes) {
		t.Fatalf("group %+v, want %d nodes of %q", g, len(top.Nodes), top.Label)
	}
	copies, want := 0, 0
	for _, f := range g.Families {
		copies += f.Copies
	}
	for _, i := range top.Nodes {
		want += st.Nodes[i].Shards
	}
	if copies == 0 || copies != want {
		t.Errorf("copies held by the group: %d, the nodes of the state hold %d", copies, want)
	}

	// The machine of the node that left: its events, and only there.
	machine := pathTo(st.Hierarchy, func(g *derive.Group) bool { return g.Kind == "machine" && g.Label == "small-hot-dc1-m01" })
	if machine == nil {
		t.Fatal("machine small-hot-dc1-m01 not in the hierarchy")
	}
	e.get("/api/v1/clusters/small/group?at="+ms(time.Hour)+"&"+groupQuery(machine), &g)
	if len(g.Machines) != 1 || g.Machines[0] != "small-hot-dc1-m01" || len(g.Path) != len(machine) {
		t.Errorf("machine group %+v", g)
	}
	var ev query.EventPage
	e.get("/api/v1/clusters/small/events?from="+ms(0)+"&types=node_left&"+groupQuery(machine), &ev)
	if len(ev.Events) != 1 || ev.Events[0].Ref != "small-hot-dc1-m01-1" {
		t.Errorf("events of the machine: %+v", ev.Events)
	}
	// Shard events of its nodes are left out.
	e.get("/api/v1/clusters/small/events?from="+ms(0)+"&"+groupQuery(machine), &ev)
	for _, x := range ev.Events {
		if strings.HasPrefix(x.Type, "shard_") {
			t.Errorf("shard event in the events of a group: %+v", x)
			break
		}
	}
	other := pathTo(st.Hierarchy, func(g *derive.Group) bool { return g.Kind == "machine" && g.Label != "small-hot-dc1-m01" })
	e.get("/api/v1/clusters/small/events?from="+ms(0)+"&types=node_left&"+groupQuery(other), &ev)
	if len(ev.Events) != 0 {
		t.Errorf("events of another machine: %+v", ev.Events)
	}

	for _, p := range []string{"/api/v1/clusters/small/group?g=nowhere", "/api/v1/clusters/small/group", "/api/v1/clusters/small/events?g=nowhere"} {
		if rec := e.get(p, nil); rec.Code != 404 {
			t.Errorf("GET %s: %d, want 404", p, rec.Code)
		}
	}
}

func TestFamilyHistory(t *testing.T) {
	e := newAEnv(t)
	var fams page[FamilyView]
	e.get("/api/v1/clusters/small/families?limit=500&at="+ms(10*time.Minute), &fams)
	if len(fams.Items) == 0 {
		t.Fatal("no family")
	}
	// One sample every 5 minutes over the hour that was ingested.
	dropped := false
	for _, f := range fams.Items {
		var pts []query.FamilyPoint
		e.get("/api/v1/clusters/small/families/"+url.PathEscape(f.Family)+"/history?points=13&from="+ms(0)+"&to="+ms(time.Hour), &pts)
		if len(pts) != 13 || pts[0].Ts != t0.UnixMilli() || pts[12].Ts != t0.Add(time.Hour).UnixMilli() {
			t.Fatalf("family %q: %d points, %+v", f.Family, len(pts), pts)
		}
		// At 10 minutes: what the state says of the family.
		if pts[2].Indices != f.Indices || pts[2].Docs <= 0 || pts[2].StoreBytes <= 0 {
			t.Errorf("family %q at 10 min: %+v, the state counts %d indices", f.Family, pts[2], f.Indices)
		}
		// The index deleted at 40 minutes no longer counts afterwards.
		if pts[6].Indices-pts[10].Indices == 1 && pts[10].Docs < pts[6].Docs {
			dropped = true
		}
	}
	if !dropped {
		t.Error("no family lost an index and its documents after the deletion")
	}
	var none []query.FamilyPoint
	e.get("/api/v1/clusters/small/families/no-such-family/history?points=3&from="+ms(0)+"&to="+ms(time.Hour), &none)
	if len(none) != 3 || none[2].Indices != 0 || none[2].Docs != 0 {
		t.Errorf("unknown family: %+v", none)
	}
}

func TestIndexAndGroupHistory(t *testing.T) {
	e := newAEnv(t)
	window := "from=" + ms(0) + "&to=" + ms(time.Hour)

	// One index: the same reading as the list of indices at that date.
	var idx page[IndexView]
	e.get("/api/v1/clusters/small/indices?limit=1&at="+ms(10*time.Minute), &idx)
	if len(idx.Items) != 1 {
		t.Fatal("no index")
	}
	var pts []query.FamilyPoint
	e.get("/api/v1/clusters/small/indices/"+idx.Items[0].UUID+"/history?points=13&"+window, &pts)
	if len(pts) != 13 || pts[2].Indices != 1 || pts[2].Docs != idx.Items[0].Docs || pts[2].StoreBytes != idx.Items[0].StoreBytes ||
		pts[2].PriStoreBytes <= 0 || pts[2].PriStoreBytes > pts[2].StoreBytes {
		t.Errorf("index history at 10 min: %+v, the list says %d docs, %d bytes", pts[2], idx.Items[0].Docs, idx.Items[0].StoreBytes)
	}

	// A group: as many nodes measured as it has nodes, plausible means, the disk of its nodes.
	var st StateView
	e.get("/api/v1/clusters/small/state?at="+ms(10*time.Minute), &st)
	top := st.Hierarchy.Children[0]
	used := int64(0)
	for _, i := range top.Nodes {
		if m := st.Nodes[i].Metrics; m != nil {
			used += m.DiskUsed
		}
	}
	var gm []query.GroupMetricPoint
	e.get("/api/v1/clusters/small/group/history?points=12&at="+ms(10*time.Minute)+"&"+window+"&"+groupQuery([]string{top.Label}), &gm)
	var at10 *query.GroupMetricPoint
	for i := range gm {
		if gm[i].Ts > t0.Add(10*time.Minute).UnixMilli() && gm[i].Ts < t0.Add(15*time.Minute).UnixMilli() {
			at10 = &gm[i]
		}
	}
	if at10 == nil || at10.Nodes != len(top.Nodes) || at10.ProcCPU < 0 || at10.ProcCPU > 100 || at10.HeapPct <= 0 || at10.HeapPct > 100 ||
		at10.DiskUsed != used || at10.DiskTotal < at10.DiskUsed {
		t.Errorf("group metrics around 10 min: %+v among %d points (group of %d nodes, %d bytes used)", at10, len(gm), len(top.Nodes), used)
	}
	if rec := e.get("/api/v1/clusters/small/group/history?g=nowhere", nil); rec.Code != 404 {
		t.Errorf("unknown group: %d", rec.Code)
	}
}
