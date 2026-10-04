package sim

import (
	"math/rand/v2"
	"os"
	"time"
)

// New returns an empty cluster (no node, no index) at time start. Use the
// Add*/Create* helpers, then Place, to build a hand-made topology (tests,
// fixture-like clusters). Scenario code may call them inside Do or Schedule.
func New(name, version string, seed uint64, start time.Time) *Cluster {
	rng := rand.New(rand.NewPCG(seed, seed^0x5eed))
	ms := start.UTC().UnixMilli()
	return &Cluster{
		Name: name, UUID: newID(rng), Version: version, rng: rng, nowMs: ms,
		spec:   Spec{Name: name, Version: version, Seed: seed},
		nodeBy: map[string]*Node{}, indexBy: map[string]*Index{},
		Persistent: map[string]string{}, Transient: map[string]string{},
		unassigned: map[*Copy]struct{}{}, checkIdx: map[*Index]struct{}{},
		nextDaily: start.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour).UnixMilli(),
		nextTick:  ms + tickMs,
	}
}

// AddNode adds an up node. attrs are node attributes (dc, machine, ...).
func (c *Cluster) AddNode(name, machine, ip string, slot int, roles []string, attrs map[string]string, disk int64) *Node {
	a := map[string]string{"xpack.installed": "true", "machine": machine}
	for k, v := range attrs {
		a[k] = v
	}
	n := &Node{
		ID: newID(c.rng), Name: name, Machine: machine, IP: ip, Transport: 9300 + slot, HTTP: 9200 + slot,
		Roles: sortedRoles(roles), Attrs: a, Version: c.Version, OSPretty: "Rocky Linux 9.6 (Blue Onyx)",
		Kernel: "5.14.0-570.el9.x86_64", JVM: jvmFor(c.Version), Processors: 8, DiskTotal: disk, Up: true,
		StartMs: c.nowMs - 3600_000, Generation: 1, copies: map[*Copy]struct{}{}, cpuBias: c.rng.Float64(),
	}
	c.Nodes = append(c.Nodes, n)
	c.nodeBy[name] = n
	c.markAll()
	return n
}

// CreateIndex creates an unmanaged index of pri shards and rep replicas,
// each primary shard holding shardBytes.
func (c *Cluster) CreateIndex(name string, pri, rep int, shardBytes int64, settings map[string]string) *Index {
	if old, ok := c.indexBy[name]; ok {
		c.dropIndex(old)
	}
	ix := &Index{UUID: newID(c.rng), Name: name, CreatedMs: c.nowMs, Pri: pri, Rep: rep,
		Settings: map[string]string{}, ShardBytes: shardBytes, DocBytes: 500}
	for k, v := range settings {
		if k == "index.hidden" {
			ix.Hidden = v == "true"
			continue
		}
		ix.Settings[k] = v
	}
	c.addIndex(ix)
	return ix
}

// DeleteIndex deletes an index by name.
func (c *Cluster) DeleteIndex(name string) {
	if ix, ok := c.indexBy[name]; ok {
		c.dropIndex(ix)
	}
}

// CloseIndex marks an index closed (its shards stay allocated).
func (c *Cluster) CloseIndex(name string) {
	if ix, ok := c.indexBy[name]; ok {
		ix.Closed = true
	}
}

// SetIndexSetting changes one setting of an index.
func (c *Cluster) SetIndexSetting(name, key, value string) {
	if ix, ok := c.indexBy[name]; ok {
		if value == "" {
			delete(ix.Settings, key)
		} else {
			ix.Settings[key] = value
		}
		c.markIndex(ix)
	}
}

// SetClusterSetting sets (or, with an empty value, removes) a persistent or
// transient cluster setting.
func (c *Cluster) SetClusterSetting(transient bool, key, value string) {
	m := c.Persistent
	if transient {
		m = c.Transient
	}
	if value == "" {
		delete(m, key)
	} else {
		m[key] = value
	}
	c.markAll()
}

// ChangeUUID gives the cluster a new identity (cluster rebuilt behind the same
// URL, SPEC §5.5).
func (c *Cluster) ChangeUUID() { c.UUID = newID(c.rng) }

// SetRemoteConnected changes the connection state of a CCS remote.
func (c *Cluster) SetRemoteConnected(alias string, connected bool) {
	for _, r := range c.Remotes {
		if r.Alias == alias {
			r.Connected = connected
		}
	}
}

// AddRemote adds a CCS remote. mode is "sniff" (addr is a seed) or "proxy".
func (c *Cluster) AddRemote(alias, mode, addr string, connected bool, nodes int) *Remote {
	r := &Remote{Alias: alias, Mode: mode, Connected: connected, Nodes: nodes}
	if mode == "proxy" {
		r.Proxy = addr
		c.Persistent["cluster.remote."+alias+".mode"] = "proxy"
		c.Persistent["cluster.remote."+alias+".proxy_address"] = addr
	} else {
		r.Seeds = []string{addr}
		c.Persistent["cluster.remote."+alias+".seeds"] = `["` + addr + `"]`
	}
	c.Remotes = append(c.Remotes, r)
	return r
}

// Place allocates every unassigned copy instantly (STARTED), bypassing
// recoveries: used to build an initial state.
func (c *Cluster) Place() { c.placeAll() }

// Reroute runs the allocator now (recoveries take simulated time).
func (c *Cluster) Reroute() { c.reroute(false) }

// Relocate starts moving the copy of index/shard held by from to node to.
func (c *Cluster) Relocate(index string, shard int, from, to string) bool {
	ix := c.indexBy[index]
	src, dst := c.nodeBy[from], c.nodeBy[to]
	if ix == nil || src == nil || dst == nil || shard >= len(ix.Shards) {
		return false
	}
	for _, cp := range ix.Shards[shard] {
		if cp.Node == src && cp.State == Started {
			c.relocate(cp, dst)
			return true
		}
	}
	return false
}

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }
