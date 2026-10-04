package query

import (
	"context"
	"sort"
	"strings"

	"estopo/internal/model"
)

// Diff compares a cluster at two dates (SPEC §8.2.5). Lists are complete;
// the API paginates them.
type Diff struct {
	A               int64         `json:"a"`
	B               int64         `json:"b"`
	CaptureA        int64         `json:"capture_a"`
	CaptureB        int64         `json:"capture_b"`
	Summary         DiffSummary   `json:"summary"`
	Nodes           []NodeDiff    `json:"nodes"`
	Indices         []IndexDiff   `json:"indices"`
	Shards          []ShardDiff   `json:"shards"`
	ClusterSettings []SettingDiff `json:"cluster_settings"`
	Remotes         []RemoteDiff  `json:"remotes"`
}

// DiffSummary counts the differences.
type DiffSummary struct {
	NodesAdded      int `json:"nodes_added"`
	NodesRemoved    int `json:"nodes_removed"`
	NodesChanged    int `json:"nodes_changed"`
	IndicesAdded    int `json:"indices_added"`
	IndicesRemoved  int `json:"indices_removed"`
	IndicesChanged  int `json:"indices_changed"`
	ShardsMoved     int `json:"shards_moved"`
	ShardsLost      int `json:"shards_lost"`
	ShardsNew       int `json:"shards_new"`
	SettingsChanged int `json:"settings_changed"` // index settings
	ClusterSettings int `json:"cluster_settings_changed"`
	RemotesChanged  int `json:"remotes_changed"`
}

// SettingDiff is one changed key ("" = absent).
type SettingDiff struct {
	Key  string `json:"key"`
	From string `json:"from"`
	To   string `json:"to"`
}

// NodeDiff is a node added, removed or changed between A and B.
type NodeDiff struct {
	Name    string        `json:"name"`
	ESID    string        `json:"es_id"`
	Change  string        `json:"change"` // added, removed, changed
	Machine string        `json:"machine"`
	Changes []SettingDiff `json:"changes,omitempty"` // version, os, jvm, roles, attributes
}

// IndexDiff is an index added, removed or changed.
type IndexDiff struct {
	UUID    string        `json:"uuid"`
	Name    string        `json:"name"`
	Family  string        `json:"family"`
	Change  string        `json:"change"`
	Changes []SettingDiff `json:"changes,omitempty"`
}

// ShardDiff is a shard copy group whose placement differs.
type ShardDiff struct {
	UUID    string   `json:"uuid"`
	Index   string   `json:"index"`
	Family  string   `json:"family"`
	Shard   int      `json:"shard"`
	Primary bool     `json:"primary"`
	Change  string   `json:"change"` // moved, lost, new
	From    []string `json:"from"`
	To      []string `json:"to"`
}

// RemoteDiff is a CCS remote whose state differs.
type RemoteDiff struct {
	Alias string `json:"alias"`
	From  string `json:"from"` // "", connected, disconnected
	To    string `json:"to"`
}

func nodeKeyOf(n *model.Node) nodeKey { return nodeKey{n.Name, n.ESID} }

// Diff compares the states at a and b.
func (e *Engine) Diff(ctx context.Context, cluster string, a, b int64) (*Diff, error) {
	sa, err := e.StateAt(ctx, cluster, a)
	if err == ErrNoData { // A before the history: start from the first full capture
		first, ferr := e.firstFullCapture(ctx, cluster)
		if ferr != nil {
			return nil, ferr
		}
		if first == 0 || first > b {
			return nil, ErrNoData
		}
		sa, err = e.StateAt(ctx, cluster, first)
	}
	if err != nil {
		return nil, err
	}
	sb, err := e.StateAt(ctx, cluster, b)
	if err != nil {
		return nil, err
	}
	return Compare(sa, sb), nil
}

// Compare computes the differences between two states of a cluster.
func Compare(sa, sb *model.State) *Diff {
	d := &Diff{A: sa.AtMs, B: sb.AtMs, CaptureA: sa.CaptureMs, CaptureB: sb.CaptureMs}

	// Nodes (present ones).
	na, nb := map[nodeKey]*model.Node{}, map[nodeKey]*model.Node{}
	for i := range sa.Nodes {
		if sa.Nodes[i].Present {
			na[nodeKeyOf(&sa.Nodes[i])] = &sa.Nodes[i]
		}
	}
	for i := range sb.Nodes {
		if sb.Nodes[i].Present {
			nb[nodeKeyOf(&sb.Nodes[i])] = &sb.Nodes[i]
		}
	}
	for k, n := range nb {
		if o, ok := na[k]; !ok {
			d.Nodes = append(d.Nodes, NodeDiff{Name: n.Name, ESID: n.ESID, Change: "added", Machine: n.Machine})
		} else if ch := nodeChanges(o, n); len(ch) > 0 {
			d.Nodes = append(d.Nodes, NodeDiff{Name: n.Name, ESID: n.ESID, Change: "changed", Machine: n.Machine, Changes: ch})
		}
	}
	for k, n := range na {
		if _, ok := nb[k]; !ok {
			d.Nodes = append(d.Nodes, NodeDiff{Name: n.Name, ESID: n.ESID, Change: "removed", Machine: n.Machine})
		}
	}
	sort.Slice(d.Nodes, func(i, j int) bool { return d.Nodes[i].Name+d.Nodes[i].ESID < d.Nodes[j].Name+d.Nodes[j].ESID })

	// Indices.
	ia, ib := map[string]*model.Index{}, map[string]*model.Index{}
	for i := range sa.Indices {
		ia[sa.Indices[i].UUID] = &sa.Indices[i]
	}
	for i := range sb.Indices {
		ib[sb.Indices[i].UUID] = &sb.Indices[i]
	}
	for u, x := range ib {
		if o, ok := ia[u]; !ok {
			d.Indices = append(d.Indices, IndexDiff{UUID: u, Name: x.Name, Family: x.Family, Change: "added"})
		} else if ch := indexChanges(o, x); len(ch) > 0 {
			d.Indices = append(d.Indices, IndexDiff{UUID: u, Name: x.Name, Family: x.Family, Change: "changed", Changes: ch})
		}
	}
	for u, x := range ia {
		if _, ok := ib[u]; !ok {
			d.Indices = append(d.Indices, IndexDiff{UUID: u, Name: x.Name, Family: x.Family, Change: "removed"})
		}
	}
	sort.Slice(d.Indices, func(i, j int) bool { return d.Indices[i].Name+d.Indices[i].UUID < d.Indices[j].Name+d.Indices[j].UUID })

	// Shard copies of indices present at both dates: compare node sets.
	type sk struct {
		uuid    string
		shard   int32
		primary bool
	}
	place := func(s *model.State) map[sk][]string {
		m := map[sk][]string{}
		for _, sh := range s.Shards {
			if sh.Node < 0 {
				continue
			}
			k := sk{s.Indices[sh.Index].UUID, sh.Shard, sh.Primary}
			n := &s.Nodes[sh.Node]
			m[k] = append(m[k], n.Name)
		}
		return m
	}
	pa, pb := place(sa), place(sb)
	keys := map[sk]bool{}
	for k := range pa {
		keys[k] = true
	}
	for k := range pb {
		keys[k] = true
	}
	for k := range keys {
		x, inA := ia[k.uuid]
		_, inB := ib[k.uuid]
		if !inA || !inB {
			continue // index added or removed: listed with the indices
		}
		from, to := setDiff(pa[k], pb[k]), setDiff(pb[k], pa[k])
		if len(from) == 0 && len(to) == 0 {
			continue
		}
		base := ShardDiff{UUID: k.uuid, Index: x.Name, Family: x.Family, Shard: int(k.shard), Primary: k.primary}
		moved := min(len(from), len(to))
		if moved > 0 {
			s := base
			s.Change, s.From, s.To = "moved", from[:moved], to[:moved]
			d.Shards = append(d.Shards, s)
		}
		if len(from) > moved {
			s := base
			s.Change, s.From = "lost", from[moved:]
			d.Shards = append(d.Shards, s)
		}
		if len(to) > moved {
			s := base
			s.Change, s.To = "new", to[moved:]
			d.Shards = append(d.Shards, s)
		}
	}
	sort.Slice(d.Shards, func(i, j int) bool {
		a, b := d.Shards[i], d.Shards[j]
		if a.Index != b.Index {
			return a.Index < b.Index
		}
		if a.Shard != b.Shard {
			return a.Shard < b.Shard
		}
		if a.Primary != b.Primary {
			return a.Primary
		}
		return a.Change < b.Change
	})

	d.ClusterSettings = mapDiff(prefixed(sa), prefixed(sb))

	ra, rb := map[string]string{}, map[string]string{}
	for _, r := range sa.Remotes {
		ra[r.Alias] = connState(r.Connected)
	}
	for _, r := range sb.Remotes {
		rb[r.Alias] = connState(r.Connected)
	}
	for _, sd := range mapDiff(ra, rb) {
		d.Remotes = append(d.Remotes, RemoteDiff{Alias: sd.Key, From: sd.From, To: sd.To})
	}

	for _, n := range d.Nodes {
		switch n.Change {
		case "added":
			d.Summary.NodesAdded++
		case "removed":
			d.Summary.NodesRemoved++
		default:
			d.Summary.NodesChanged++
		}
	}
	for _, x := range d.Indices {
		switch x.Change {
		case "added":
			d.Summary.IndicesAdded++
		case "removed":
			d.Summary.IndicesRemoved++
		default:
			d.Summary.IndicesChanged++
			for _, c := range x.Changes {
				if strings.HasPrefix(c.Key, "index.") {
					d.Summary.SettingsChanged++
					break
				}
			}
		}
	}
	for _, s := range d.Shards {
		n := max(len(s.From), len(s.To))
		switch s.Change {
		case "moved":
			d.Summary.ShardsMoved += n
		case "lost":
			d.Summary.ShardsLost += n
		default:
			d.Summary.ShardsNew += n
		}
	}
	d.Summary.ClusterSettings = len(d.ClusterSettings)
	d.Summary.RemotesChanged = len(d.Remotes)
	return d
}

func connState(c bool) string {
	if c {
		return "connected"
	}
	return "disconnected"
}

func prefixed(s *model.State) map[string]string {
	m := map[string]string{}
	for k, v := range s.Persistent {
		m["persistent."+k] = v
	}
	for k, v := range s.Transient {
		m["transient."+k] = v
	}
	return m
}

func setDiff(a, b []string) []string {
	in := map[string]int{}
	for _, x := range b {
		in[x]++
	}
	var out []string
	for _, x := range a {
		if in[x] > 0 {
			in[x]--
			continue
		}
		out = append(out, x)
	}
	sort.Strings(out)
	return out
}

func mapDiff(a, b map[string]string) []SettingDiff {
	var out []SettingDiff
	for k, v := range b {
		if o, ok := a[k]; !ok || o != v {
			out = append(out, SettingDiff{Key: k, From: a[k], To: v})
		}
	}
	for k, v := range a {
		if _, ok := b[k]; !ok {
			out = append(out, SettingDiff{Key: k, From: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func nodeChanges(a, b *model.Node) []SettingDiff {
	m := func(n *model.Node) map[string]string {
		x := map[string]string{"version": n.Version, "os": n.OS, "kernel": n.Kernel, "jvm": n.JVM,
			"roles": strings.Join(n.Roles, ","), "machine": n.Machine, "ip": n.IP}
		for k, v := range n.Attrs {
			x["attr."+k] = v
		}
		return x
	}
	return mapDiff(m(a), m(b))
}

func indexChanges(a, b *model.Index) []SettingDiff {
	m := func(x *model.Index) map[string]string {
		out := map[string]string{"status": x.Status}
		for k, v := range x.Settings {
			out[k] = v
		}
		return out
	}
	return mapDiff(m(a), m(b))
}

// IndexSettingChange is one changed index setting (diff "settings" tab,
// SPEC §8.2.5).
type IndexSettingChange struct {
	UUID   string `json:"uuid"`
	Index  string `json:"index"`
	Family string `json:"family"`
	Key    string `json:"key"`
	From   string `json:"from"`
	To     string `json:"to"`
}

// IndexSettingChanges lists the index settings that changed, one line per
// index and key.
func (d *Diff) IndexSettingChanges() []IndexSettingChange {
	out := []IndexSettingChange{}
	for _, x := range d.Indices {
		for _, c := range x.Changes {
			if strings.HasPrefix(c.Key, "index.") {
				out = append(out, IndexSettingChange{UUID: x.UUID, Index: x.Name, Family: x.Family, Key: c.Key, From: c.From, To: c.To})
			}
		}
	}
	return out
}
