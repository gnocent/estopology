package sim

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseOffset(t *testing.T) {
	for s, want := range map[string]time.Duration{
		"45m":      45 * time.Minute,
		"36h":      36 * time.Hour,
		"2d":       48 * time.Hour,
		"3d14:05":  3*24*time.Hour + 14*time.Hour + 5*time.Minute,
		"-1d09:15": -(24*time.Hour + 9*time.Hour + 15*time.Minute),
		"-40m":     -40 * time.Minute,
		"1d2h30m":  24*time.Hour + 150*time.Minute,
	} {
		if got, err := ParseOffset(s); err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", s, got, err, want)
		}
	}
	for _, s := range []string{"", "x", "3d25:00", "1dx", "12:61"} {
		if s == "" {
			continue // empty = zero, accepted
		}
		if _, err := ParseOffset(s); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func newScenarioServer(t *testing.T) (*Server, time.Time) {
	t.Helper()
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	return &Server{Cluster: Build(PresetSmall("8.19.22"), t0), Auth: Auth{Disabled: true}}, t0
}

func up(c *Cluster, name string) bool { return c.Node(name).Up }

func TestActions(t *testing.T) {
	s, t0 := newScenarioServer(t)
	c := s.Cluster
	do := func(a Action) result {
		t.Helper()
		var res result
		var err error
		c.Do(func(*Cluster) { res, err = s.apply(a) })
		if err != nil {
			t.Fatalf("%s: %v", a.Do, err)
		}
		return res
	}

	// A node selected by suffix, back by itself after "for".
	do(Action{Do: "node-leave", Node: "hot-dc1-m01-2", For: "30m"})
	if up(c, "small-hot-dc1-m01-2") {
		t.Fatal("node still up")
	}
	c.AdvanceTo(t0.Add(31 * time.Minute))
	if !up(c, "small-hot-dc1-m01-2") {
		t.Error("node not back after 30 min")
	}

	// A whole machine, then a data centre.
	res := do(Action{Do: "machine-leave", Machine: "warm-dc2-m01"})
	if strings.Count(res.detail, ",") != 1 || up(c, "small-warm-dc2-m01-1") || up(c, "small-warm-dc2-m01-2") {
		t.Errorf("machine-leave: %s", res.detail)
	}
	do(Action{Do: "machine-join", Machine: "warm-dc2-m01"})
	do(Action{Do: "dc-leave", DC: "dc2", For: "10m"})
	for _, n := range c.Nodes {
		if n.Attrs["dc"] == "dc2" && n.Up {
			t.Errorf("%s up during the DC loss", n.Name)
		}
	}
	c.AdvanceTo(t0.Add(45 * time.Minute))
	for _, n := range c.Nodes {
		if !n.Up {
			t.Errorf("%s down after the DC came back", n.Name)
		}
	}

	// Settings, undone after "for".
	do(Action{Do: "cluster-setting", Key: "cluster.routing.allocation.enable", Value: "primaries", For: "5m"})
	if c.Persistent["cluster.routing.allocation.enable"] != "primaries" {
		t.Error("cluster setting not set")
	}
	res = do(Action{Do: "index-setting", Index: "logs-002", Key: "index.priority", Value: "9", For: "5m"})
	name := strings.Fields(res.detail)[0]
	if c.IndexByName(name).Settings["index.priority"] != "9" || !strings.HasPrefix(name, "logs-002-") {
		t.Errorf("index setting on %s", name)
	}
	c.AdvanceTo(t0.Add(51 * time.Minute))
	if c.Persistent["cluster.routing.allocation.enable"] != "" || c.IndexByName(name).Settings["index.priority"] != "" {
		t.Error("settings not restored")
	}

	// API fault, removed after "for".
	do(Action{Do: "fault", Path: "/_cat/shards", Status: 503, For: "5m"})
	if _, ok := s.fault("/_cat/shards"); !ok {
		t.Error("fault not set")
	}
	c.AdvanceTo(t0.Add(57 * time.Minute))
	if _, ok := s.fault("/_cat/shards"); ok {
		t.Error("fault not removed")
	}

	// Rolling upgrade: every node, masters last, then the cluster version.
	do(Action{Do: "rolling-upgrade", Version: "9.5.4", Every: "2m", Down: "1m"})
	c.AdvanceTo(t0.Add(57*time.Minute + time.Duration(len(c.Nodes)+1)*2*time.Minute))
	for _, n := range c.Nodes {
		if n.Version != "9.5.4" || !n.Up {
			t.Errorf("%s: %s up=%v", n.Name, n.Version, n.Up)
		}
	}
	if c.Version != "9.5.4" {
		t.Errorf("cluster version %s", c.Version)
	}

	// Errors: unknown target, ambiguous suffix, action that cannot be undone.
	for _, a := range []Action{
		{Do: "node-leave", Node: "nope"},
		{Do: "node-leave", Node: "1"},
		{Do: "delete-index", Index: "logs-001", For: "1h"},
		{Do: "explode"},
	} {
		var err error
		c.Do(func(*Cluster) { _, err = s.apply(a) })
		if err == nil {
			t.Errorf("%+v accepted", a)
		}
	}
}

// The built-in demo scenario loads, and its steps for "small" happen at
// their dates (counted back from the end).
func TestDemoScenario(t *testing.T) {
	sc, err := LoadScenario("demo")
	if err != nil {
		t.Fatal(err)
	}
	s, t0 := newScenarioServer(t)
	end := t0.Add(14 * 24 * time.Hour)
	var logs []string
	if err := s.Play(sc, "small", t0, end, func(f string, args ...any) {
		logs = append(logs, strings.TrimSpace(fmt.Sprintf(f, args...)))
	}); err != nil {
		t.Fatal(err)
	}
	c := s.Cluster
	c.AdvanceTo(end)
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"node-replace small-hot-dc2-m01-1", "delete-index logs-001-", "node-leave small-warm-dc1-m01-1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	// 4 steps concern small (one of them for every cluster); none may fail.
	if len(logs) != 4 {
		t.Errorf("%d step logs, want 4:\n%s", len(logs), joined)
	}
	for _, l := range logs {
		for _, bad := range []string{"matching", "not found", "required", "already", "cannot"} {
			if strings.Contains(l, bad) {
				t.Errorf("a step failed: %s", l)
			}
		}
	}
	if up(c, "small-warm-dc1-m01-1") {
		t.Error("the incident in progress at the end is over")
	}
	if c.Node("small-hot-dc2-m01-1").Generation != 2 {
		t.Error("node not rebuilt")
	}
	if err := s.Play(sc, "small", t0, time.Time{}, func(string, ...any) {}); err == nil {
		t.Error("steps counted from an unknown end accepted")
	}
}

func TestControlAPI(t *testing.T) {
	s, _ := newScenarioServer(t)
	call := func(method, path, body, remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}
	if rec := call("GET", "/_essim/topology", "", "10.1.2.3:4000"); rec.Code != 403 {
		t.Errorf("remote client: %d", rec.Code)
	}
	rec := call("GET", "/_essim/topology", "", "127.0.0.1:4000")
	var topo Topology
	json.Unmarshal(rec.Body.Bytes(), &topo)
	if rec.Code != 200 || len(topo.Nodes) != len(s.Cluster.Nodes) || len(topo.DCs) != 2 || len(topo.Families) == 0 {
		t.Fatalf("topology: %d %+v", rec.Code, topo)
	}
	rec = call("POST", "/_essim/action", `{"do":"node-leave","node":"cold-dc1-m01-1"}`, "127.0.0.1:4000")
	if rec.Code != 200 || up(s.Cluster, "small-cold-dc1-m01-1") {
		t.Errorf("action: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("POST", "/_essim/action", `{"do":"node-leave","node":"cold-dc1-m01-1"}`, "127.0.0.1:4000"); rec.Code != 400 {
		t.Errorf("node already down: %d", rec.Code)
	}
	if rec := call("POST", "/_essim/action", `{"do":"node-leave","nod":"x"}`, "127.0.0.1:4000"); rec.Code != 400 {
		t.Errorf("unknown field: %d", rec.Code)
	}
	// The ES API stays read-only.
	if rec := call("POST", "/_cluster/settings", `{}`, "127.0.0.1:4000"); rec.Code != 405 {
		t.Errorf("POST on the ES API: %d", rec.Code)
	}
}

// Every target of the demo scenario exists on the presets it concerns.
func TestDemoTargets(t *testing.T) {
	sc, err := LoadScenario("demo")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	presets := map[string]Spec{"a": PresetA("7.17.29"), "b": PresetB("8.19.22"), "c": PresetC("9.5.4"), "small": PresetSmall("8.19.22")}
	for id, spec := range presets {
		c := Build(spec, t0)
		for i, st := range sc.Steps {
			if st.Cluster != "*" && st.Cluster != id {
				continue
			}
			var err error
			switch {
			case st.Node != "":
				_, err = c.findNode(st.Node)
			case st.Machine != "":
				_, err = c.machineNodes(st.Machine)
			case st.DC != "":
				_, err = c.dcNodes(st.DC)
			case st.Index != "":
				_, err = c.findIndex(st.Index)
			case st.Remote != "":
				found := false
				for _, r := range c.Remotes {
					found = found || r.Alias == st.Remote
				}
				if !found {
					err = fmt.Errorf("remote %s not found", st.Remote)
				}
			}
			if err != nil {
				t.Errorf("%s, step %d (%s): %v", id, i+1, st.Do, err)
			}
		}
	}
}

// Losing many nodes at once schedules one delayed allocation per replica,
// all at the same instant: they share one reroute (a reroute per event made
// a data centre loss take minutes of CPU).
func TestNoRerouteStorm(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	c := Build(PresetC("9.5.4"), t0)
	s := &Server{Cluster: c}
	lost := 0
	c.Do(func(*Cluster) {
		if _, err := s.apply(Action{Do: "dc-leave", DC: "dc2", For: "2h"}); err != nil {
			t.Fatal(err)
		}
		lost = len(c.unassigned)
	})
	if lost < 5000 {
		t.Fatalf("only %d copies unassigned", lost)
	}
	before := c.reroutes
	c.AdvanceTo(t0.Add(3 * time.Hour))
	// 36 ticks of 5 min, plus the reroutes after recoveries (distinct instants).
	if n := c.reroutes - before; n > lost/4 {
		t.Errorf("%d reroutes for %d unassigned copies", n, lost)
	}
	for _, n := range c.Nodes {
		if !n.Up {
			t.Errorf("%s still down", n.Name)
		}
	}
}
