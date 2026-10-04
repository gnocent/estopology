package sim

import (
	"bytes"
	"io"

	"estopo/internal/esclient"
	"estopo/internal/record"
)

// Capture builds the record the collector would produce right now, by
// rendering the simulated answers and parsing them with the real parsers
// (no HTTP). Used by ingestion tests and benchmarks.
func (c *Cluster) Capture(clusterID string, kind record.Kind, trigger string) *record.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capture(clusterID, kind, trigger)
}

// CaptureAt advances the cluster to ms, then captures.
func (c *Cluster) CaptureAt(ms int64, clusterID string, kind record.Kind, trigger string) *record.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.advanceTo(ms)
	return c.capture(clusterID, kind, trigger)
}

func (c *Cluster) capture(clusterID string, kind record.Kind, trigger string) *record.Record {
	r := &record.Record{Format: record.FormatVersion, Cluster: clusterID, Kind: kind, Trigger: trigger,
		StartMs: c.nowMs, EndMs: c.nowMs, Status: record.OK}
	render := func(fn func(io.Writer)) *bytes.Buffer {
		var b bytes.Buffer
		fn(&b)
		return &b
	}
	must := func(err error) {
		if err != nil {
			panic(err) // renderer/parser mismatch: a bug, caught by tests
		}
	}
	root, err := esclient.ParseRoot(render(c.renderRoot))
	must(err)
	r.Root = &root
	if !c.hasMaster() {
		r.Status, r.Error = record.Failed, "health: HTTP 503: master_not_discovered_exception"
		return r
	}
	h, err := esclient.ParseHealth(render(c.renderHealth))
	must(err)
	r.Health = &h
	r.NodesStats, err = esclient.ParseNodesStats(render(c.renderNodesStats))
	must(err)
	r.Allocation, err = esclient.ParseAllocation(render(func(w io.Writer) { must(c.renderAllocation(w, nil)) }))
	must(err)
	cs, err := esclient.ParseClusterSettings(render(func(w io.Writer) { c.renderClusterSettings(w, kind == record.Full) }))
	must(err)
	r.ClusterSettings = &cs
	r.Remotes, err = esclient.ParseRemoteInfo(render(c.renderRemoteInfo))
	must(err)
	if r.Remotes == nil {
		r.Remotes = []esclient.Remote{}
	}
	if kind == record.Full {
		r.Nodes, err = esclient.ParseNodes(render(c.renderNodes))
		must(err)
		r.Indices = []esclient.CatIndex{}
		must(esclient.DecodeCatIndices(render(func(w io.Writer) {
			must(c.renderCatIndices(w, []string{"uuid", "index", "status", "health", "pri", "rep", "docs.count", "store.size", "pri.store.size", "creation.date"}))
		}), func(ci esclient.CatIndex) error { r.Indices = append(r.Indices, ci); return nil }))
		r.IndexSettings = []record.IndexSettings{}
		must(esclient.DecodeIndexSettings(render(func(w io.Writer) { c.renderIndexSettings(w, esclient.DefaultSettingsWhitelist) }),
			func(ix string, s map[string]string) error {
				r.IndexSettings = append(r.IndexSettings, record.IndexSettings{Index: ix, Settings: s})
				return nil
			}))
		r.Shards = []esclient.CatShard{}
		must(esclient.DecodeCatShards(render(func(w io.Writer) {
			must(c.renderCatShards(w, []string{"index", "shard", "prirep", "state", "docs", "store", "id", "node",
				"unassigned.reason", "unassigned.at", "unassigned.details"}))
		}), func(s esclient.CatShard) error { r.Shards = append(r.Shards, s); return nil }))
	}
	r.Sort()
	return r
}

// NodeReplace replaces a node by a new one with the same name and a new ES
// id (a rebuilt node: new generation, empty disk, SPEC §5.4).
func (c *Cluster) NodeReplace(name string) {
	n := c.nodeBy[name]
	if n == nil {
		return
	}
	c.NodeLeave(name)
	for _, ix := range c.Indices {
		for _, sh := range ix.Shards {
			for _, cp := range sh {
				if cp.LastNode == n {
					cp.LastNode = nil // its data is gone
					if cp.Primary && cp.State == Unassigned {
						cp.UReason = "NODE_LEFT" // stays red until an empty primary is forced: realistic
					}
				}
			}
		}
	}
	n.ID = newID(c.rng)
	n.Generation++
	c.NodeJoin(name)
}

// SetNodeVersion changes the ES version of a node (rolling upgrade: the
// node restarts).
func (c *Cluster) SetNodeVersion(name, version string) {
	if n := c.nodeBy[name]; n != nil {
		n.Version = version
		n.JVM = jvmFor(version)
		n.StartMs = c.nowMs
	}
}
