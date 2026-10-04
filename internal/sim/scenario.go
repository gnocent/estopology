package sim

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Incidents that can be played on a simulated cluster, either live (the
// essim control API) or at fixed dates (a scenario, e.g. during a
// backfill), so that estopo can be explored as during a real incident.

// Action is one incident or operation. Targets are designated by a suffix of
// their name ("hot-dc1-m01-2" for node "clu-b-hot-dc1-m01-2"), so that a
// scenario fits every preset; an index is designated by its name or by its
// family (the most recent index of the family).
type Action struct {
	Do        string `json:"do"`
	Node      string `json:"node,omitempty"`
	Machine   string `json:"machine,omitempty"`
	DC        string `json:"dc,omitempty"`
	Version   string `json:"version,omitempty"`
	Key       string `json:"key,omitempty"`
	Value     string `json:"value,omitempty"`
	Transient bool   `json:"transient,omitempty"`
	Index     string `json:"index,omitempty"`
	Remote    string `json:"remote,omitempty"`
	Connected bool   `json:"connected,omitempty"`
	Path      string `json:"path,omitempty"`   // fault: API path prefix ("/" = every call)
	Status    int    `json:"status,omitempty"` // fault: HTTP status returned
	Every     string `json:"every,omitempty"`  // rolling-upgrade: delay between two nodes (default 10m)
	Down      string `json:"down,omitempty"`   // rolling-upgrade: restart time of a node (default 3m)
	For       string `json:"for,omitempty"`    // undo after this simulated duration (when the action can be undone)
}

// Actions lists the supported actions (help, validation).
var Actions = map[string]string{
	"node-leave":      "node=<suffix>: the node stops (crash); its copies become unassigned",
	"node-join":       "node=<suffix>: the node comes back with its data",
	"node-replace":    "node=<suffix>: the node is rebuilt (new ES id, empty disk)",
	"machine-leave":   "machine=<suffix>: every node of the machine stops",
	"machine-join":    "machine=<suffix>: the nodes of the machine come back",
	"dc-leave":        "dc=<name>: every node of the data centre stops",
	"dc-join":         "dc=<name>: the nodes of the data centre come back",
	"rolling-upgrade": "version=<v> [every=10m down=3m]: nodes restart one by one in the new version, masters last",
	"cluster-setting": "key=<k> value=<v> [transient=true]: changes a cluster setting (empty value: removes it)",
	"index-setting":   "index=<name or family> key=<k> value=<v>: changes a setting of the index (latest of the family)",
	"close-index":     "index=<name or family>: closes the index",
	"delete-index":    "index=<name or family>: deletes the index",
	"remote":          "remote=<alias> [connected=true]: disconnects (or reconnects) a CCS remote",
	"fault":           "path=<prefix> status=<code>: the API answers this HTTP status (e.g. 503 on /_cat/shards)",
	"clear-faults":    "removes every injected API fault",
	"uuid-change":     "the cluster gets a new identity (rebuilt behind the same URL): estopo suspends it",
}

// result describes an applied action and how to undo it.
type result struct {
	detail string
	undo   func()
}

// apply runs an action at the cluster's current time (c.mu held). The server
// part (API faults) is handled by Server.apply.
func (c *Cluster) apply(a Action) (result, error) {
	switch a.Do {
	case "node-leave", "node-join", "node-replace":
		n, err := c.findNode(a.Node)
		if err != nil {
			return result{}, err
		}
		switch a.Do {
		case "node-leave":
			if !n.Up {
				return result{}, fmt.Errorf("node %s is already down", n.Name)
			}
			c.NodeLeave(n.Name)
			return result{detail: n.Name, undo: func() { c.NodeJoin(n.Name) }}, nil
		case "node-join":
			c.NodeJoin(n.Name)
		default:
			c.NodeReplace(n.Name)
		}
		return result{detail: n.Name}, nil

	case "machine-leave", "machine-join", "dc-leave", "dc-join":
		var nodes []*Node
		var err error
		if strings.HasPrefix(a.Do, "machine") {
			nodes, err = c.machineNodes(a.Machine)
		} else {
			nodes, err = c.dcNodes(a.DC)
		}
		if err != nil {
			return result{}, err
		}
		var names []string
		for _, n := range nodes {
			if strings.HasSuffix(a.Do, "-leave") == n.Up {
				names = append(names, n.Name)
			}
		}
		if len(names) == 0 {
			return result{}, fmt.Errorf("%s: no node to change", a.Do)
		}
		if strings.HasSuffix(a.Do, "-leave") {
			for _, name := range names {
				c.NodeLeave(name)
			}
			return result{detail: strings.Join(names, ", "), undo: func() {
				for _, name := range names {
					c.NodeJoin(name)
				}
			}}, nil
		}
		for _, name := range names {
			c.NodeJoin(name)
		}
		return result{detail: strings.Join(names, ", ")}, nil

	case "rolling-upgrade":
		if a.Version == "" {
			return result{}, fmt.Errorf("rolling-upgrade: version is required")
		}
		every, err := durationOr(a.Every, 10*time.Minute)
		if err != nil {
			return result{}, err
		}
		down, err := durationOr(a.Down, 3*time.Minute)
		if err != nil {
			return result{}, err
		}
		var order []*Node
		for _, n := range c.Nodes {
			if n.Version != a.Version {
				order = append(order, n)
			}
		}
		// Data nodes first, master-eligible nodes last (Elastic's advice).
		sort.SliceStable(order, func(i, j int) bool {
			if order[i].IsMaster() != order[j].IsMaster() {
				return !order[i].IsMaster()
			}
			return order[i].Name < order[j].Name
		})
		for i, n := range order {
			name, at := n.Name, c.nowMs+int64(i)*every.Milliseconds()
			c.schedule(at, func() {
				if node := c.nodeBy[name]; node != nil && node.Up {
					c.NodeLeave(name)
				}
				c.dirty = true
			})
			c.schedule(at+down.Milliseconds(), func() {
				c.SetNodeVersion(name, a.Version)
				c.NodeJoin(name)
				c.dirty = true
			})
		}
		last := c.nowMs + int64(len(order))*every.Milliseconds() + down.Milliseconds()
		c.schedule(last, func() { c.Version = a.Version })
		return result{detail: fmt.Sprintf("%d nodes to %s, one every %v", len(order), a.Version, every)}, nil

	case "cluster-setting":
		if a.Key == "" {
			return result{}, fmt.Errorf("cluster-setting: key is required")
		}
		m := c.Persistent
		if a.Transient {
			m = c.Transient
		}
		old := m[a.Key]
		c.SetClusterSetting(a.Transient, a.Key, a.Value)
		return result{detail: a.Key + "=" + a.Value, undo: func() { c.SetClusterSetting(a.Transient, a.Key, old) }}, nil

	case "index-setting", "close-index", "delete-index":
		ix, err := c.findIndex(a.Index)
		if err != nil {
			return result{}, err
		}
		switch a.Do {
		case "index-setting":
			if a.Key == "" {
				return result{}, fmt.Errorf("index-setting: key is required")
			}
			old := ix.Settings[a.Key]
			c.SetIndexSetting(ix.Name, a.Key, a.Value)
			name := ix.Name
			return result{detail: name + " " + a.Key + "=" + a.Value, undo: func() { c.SetIndexSetting(name, a.Key, old) }}, nil
		case "close-index":
			c.CloseIndex(ix.Name)
		default:
			c.DeleteIndex(ix.Name)
		}
		return result{detail: ix.Name}, nil

	case "remote":
		found := false
		for _, r := range c.Remotes {
			found = found || r.Alias == a.Remote
		}
		if !found {
			return result{}, fmt.Errorf("remote %q not found", a.Remote)
		}
		c.SetRemoteConnected(a.Remote, a.Connected)
		return result{detail: a.Remote, undo: func() { c.SetRemoteConnected(a.Remote, !a.Connected) }}, nil

	case "uuid-change":
		c.ChangeUUID()
		return result{detail: c.UUID}, nil
	}
	return result{}, fmt.Errorf("unknown action %q", a.Do)
}

// findNode resolves a node by name or by name suffix.
func (c *Cluster) findNode(sel string) (*Node, error) {
	if sel == "" {
		return nil, fmt.Errorf("node is required")
	}
	if n := c.nodeBy[sel]; n != nil {
		return n, nil
	}
	var found []*Node
	for _, n := range c.Nodes {
		if strings.HasSuffix(n.Name, "-"+sel) {
			found = append(found, n)
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("node %q: %d matching nodes in %s", sel, len(found), c.Name)
	}
	return found[0], nil
}

func (c *Cluster) machineNodes(sel string) ([]*Node, error) {
	if sel == "" {
		return nil, fmt.Errorf("machine is required")
	}
	var out []*Node
	machines := map[string]bool{}
	for _, n := range c.Nodes {
		if n.Machine == sel || strings.HasSuffix(n.Machine, "-"+sel) {
			out = append(out, n)
			machines[n.Machine] = true
		}
	}
	if len(machines) != 1 {
		return nil, fmt.Errorf("machine %q: %d matching machines in %s", sel, len(machines), c.Name)
	}
	return out, nil
}

func (c *Cluster) dcNodes(dc string) ([]*Node, error) {
	var out []*Node
	for _, n := range c.Nodes {
		if dc != "" && n.Attrs["dc"] == dc {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("data centre %q not found in %s", dc, c.Name)
	}
	return out, nil
}

// findIndex resolves an index by name, else the most recent index whose name
// starts with sel (a family).
func (c *Cluster) findIndex(sel string) (*Index, error) {
	if sel == "" {
		return nil, fmt.Errorf("index is required")
	}
	if ix := c.indexBy[sel]; ix != nil {
		return ix, nil
	}
	var best *Index
	for _, ix := range c.Indices {
		if strings.HasPrefix(ix.Name, sel) && (best == nil || ix.CreatedMs > best.CreatedMs ||
			(ix.CreatedMs == best.CreatedMs && ix.Name > best.Name)) {
			best = ix
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no index named or starting with %q in %s", sel, c.Name)
	}
	return best, nil
}

func durationOr(s string, def time.Duration) (time.Duration, error) {
	if s == "" {
		return def, nil
	}
	d, err := ParseOffset(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	return d, nil
}

// ParseOffset reads a duration with an optional day count: "45m", "36h",
// "2d", "3d14:05" (3 days 14 h 05), "-1d09:15", "-40m".
func ParseOffset(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var d time.Duration
	if i := strings.Index(s, "d"); i > 0 {
		n, err := strconv.Atoi(s[:i])
		if err != nil {
			return 0, fmt.Errorf("invalid offset %q", s)
		}
		d, s = time.Duration(n)*24*time.Hour, s[i+1:]
	}
	switch {
	case s == "":
	case strings.Contains(s, ":"):
		hh, mm, _ := strings.Cut(s, ":")
		h, err1 := strconv.Atoi(hh)
		m, err2 := strconv.Atoi(mm)
		if err1 != nil || err2 != nil || h > 23 || m > 59 {
			return 0, fmt.Errorf("invalid offset %q", s)
		}
		d += time.Duration(h)*time.Hour + time.Duration(m)*time.Minute
	default:
		x, err := time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid offset %q", s)
		}
		d += x
	}
	if neg {
		d = -d
	}
	return d, nil
}

// --- scenarios ------------------------------------------------------------------------

// Step is an action at a date of the scenario: "at" counts from the start,
// or back from the end when negative (so that the incidents of a backfill
// stay recent whatever its length). Cluster is a preset id or "*".
type Step struct {
	At      string `json:"at"`
	Cluster string `json:"cluster"`
	Action
}

// Scenario is a list of steps.
type Scenario struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Steps       []Step `json:"steps"`
}

//go:embed scenarios/*.json
var builtin embed.FS

// LoadScenario reads a scenario file, or a built-in scenario by name
// ("demo").
func LoadScenario(nameOrPath string) (*Scenario, error) {
	b, err := builtin.ReadFile("scenarios/" + nameOrPath + ".json")
	if err != nil {
		if b, err = os.ReadFile(nameOrPath); err != nil {
			return nil, fmt.Errorf("scenario %q: neither built in nor a readable file: %w", nameOrPath, err)
		}
	}
	var sc Scenario
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if err := d.Decode(&sc); err != nil {
		return nil, fmt.Errorf("scenario %q: %w", nameOrPath, err)
	}
	for i, st := range sc.Steps {
		if _, err := ParseOffset(st.At); err != nil {
			return nil, fmt.Errorf("scenario %q, step %d: %w", nameOrPath, i+1, err)
		}
		if _, ok := Actions[st.Do]; !ok {
			return nil, fmt.Errorf("scenario %q, step %d: unknown action %q", nameOrPath, i+1, st.Do)
		}
	}
	return &sc, nil
}

// Play schedules the steps of sc meant for cluster id between start and
// end (end zero: steps counted from the end are refused). logf reports what
// happens, including steps that cannot apply to this cluster.
func (s *Server) Play(sc *Scenario, id string, start, end time.Time, logf func(format string, args ...any)) error {
	c := s.Cluster
	for i, st := range sc.Steps {
		if st.Cluster != "*" && st.Cluster != id {
			continue
		}
		off, err := ParseOffset(st.At)
		if err != nil {
			return err
		}
		at := start.Add(off)
		if off < 0 {
			if end.IsZero() {
				return fmt.Errorf("step %d: %q counts from the end, which is not known here", i+1, st.At)
			}
			at = end.Add(off)
		}
		if at.Before(start) {
			logf("scenario %s, step %d (%s %s): before the start, skipped", sc.Name, i+1, st.Do, st.At)
			continue
		}
		a, n := st.Action, i+1
		c.Schedule(at, func(c *Cluster) {
			res, err := s.apply(a)
			if err != nil {
				logf("scenario %s, step %d on %s at %s: %s: %v", sc.Name, n, id, c.nowTime().Format(time.RFC3339), a.Do, err)
				return
			}
			logf("scenario %s, step %d on %s at %s: %s %s", sc.Name, n, id, c.nowTime().Format(time.RFC3339), a.Do, res.detail)
		})
	}
	return nil
}

// apply runs an action now (cluster locked by the caller) and schedules its
// undo when "for" is set.
func (s *Server) apply(a Action) (result, error) {
	c := s.Cluster
	var res result
	var err error
	switch a.Do {
	case "fault":
		if a.Path == "" || a.Status < 400 {
			return result{}, fmt.Errorf("fault: path and status (>= 400) are required")
		}
		s.SetFault(a.Path, Fault{Status: a.Status})
		res = result{detail: fmt.Sprintf("%s -> %d", a.Path, a.Status), undo: func() { s.RemoveFault(a.Path) }}
	case "clear-faults":
		s.ClearFaults()
	default:
		res, err = c.apply(a)
		if err != nil {
			return res, err
		}
	}
	c.dirty = true
	if a.For != "" {
		d, err := durationOr(a.For, 0)
		if err != nil {
			return res, err
		}
		if res.undo == nil {
			return res, fmt.Errorf("%s cannot be undone (for=%s)", a.Do, a.For)
		}
		undo := res.undo
		c.schedule(c.nowMs+d.Milliseconds(), func() { undo(); c.dirty = true })
		res.detail += " for " + d.String()
	}
	return res, nil
}

func (c *Cluster) nowTime() time.Time { return time.UnixMilli(c.nowMs).UTC() }
