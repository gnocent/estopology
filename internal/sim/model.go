// Package sim simulates Elasticsearch clusters for tests, benchmarks and
// demos (SPEC §13.1): topology, a simplified allocator, time evolution
// (rollover, tier moves, retention, relocations) and the JSON answers of the
// APIs estopo uses, in the 7.17, 8.x and 9.x formats recorded in testdata/es.
package sim

import (
	"encoding/base64"
	"math/rand/v2"
	"sort"
	"strings"
)

// Shard copy states, as in _cat/shards.
const (
	Started      = "STARTED"
	Initializing = "INITIALIZING"
	Relocating   = "RELOCATING"
	Unassigned   = "UNASSIGNED"
)

// Node is one Elasticsearch node (several may share a machine).
type Node struct {
	ID         string
	Name       string
	Machine    string // machine host name
	IP         string // machine IP
	Transport  int    // transport port
	HTTP       int    // HTTP port
	Roles      []string
	Attrs      map[string]string
	Version    string
	OSPretty   string
	Kernel     string
	JVM        string
	Processors int
	DiskTotal  int64
	Up         bool

	// Cached disk usage of the indices (nodeDiskIndices).
	diskUsed, diskAtMs int64
	diskGen            int
	diskOK             bool
	StartMs            int64
	Generation         int

	copies     map[*Copy]struct{} // copies allocated here (source side for relocations)
	reserved   int64              // final bytes of the copies here or relocating here
	recoveries int                // ongoing recoveries (incoming + outgoing)
	cpuBias    float64
}

// HasRole reports whether the node has role r.
func (n *Node) HasRole(r string) bool {
	for _, x := range n.Roles {
		if x == r {
			return true
		}
	}
	return false
}

// IsData reports whether the node holds data.
func (n *Node) IsData() bool {
	for _, r := range n.Roles {
		if r == "data" || strings.HasPrefix(r, "data_") {
			return true
		}
	}
	return false
}

// IsMaster reports whether the node is master-eligible.
func (n *Node) IsMaster() bool { return n.HasRole("master") }

// Index is one index (identified by UUID; a recreated name is a new Index).
type Index struct {
	UUID      string
	Name      string
	Source    string // family, e.g. "app-042"
	CreatedMs int64
	Pri       int
	Rep       int
	Settings  map[string]string // flat settings beyond the computed ones
	static    *idxStatic        // parsed allocation settings (dropped by markIndex)
	info      *idxInfo          // allocation view of the current reroute context
	infoCtx   *allocCtx
	Closed    bool
	Hidden    bool
	Rollover  bool // named -00000N, rolled over on size

	ShardBytes  int64 // final size of one primary shard
	GrowFromMs  int64 // linear growth window of a write index
	GrowUntilMs int64
	DocBytes    int64 // average document size

	Shards [][]*Copy // [shard number][copy]
}

// Setting returns an index setting, or def.
func (ix *Index) Setting(key, def string) string {
	if v, ok := ix.Settings[key]; ok {
		return v
	}
	return def
}

// bytesAt is the size of one copy of a shard at time t.
func (ix *Index) bytesAt(nowMs int64) int64 {
	if ix.GrowUntilMs <= ix.GrowFromMs || nowMs >= ix.GrowUntilMs {
		return ix.ShardBytes
	}
	if nowMs <= ix.GrowFromMs {
		return 0
	}
	f := float64(nowMs-ix.GrowFromMs) / float64(ix.GrowUntilMs-ix.GrowFromMs)
	return int64(f * float64(ix.ShardBytes))
}

func (ix *Index) docsAt(nowMs int64) int64 {
	if ix.DocBytes <= 0 {
		return 0
	}
	return ix.bytesAt(nowMs) / ix.DocBytes
}

// copiesOn counts the copies of this index held by (or relocating to) n.
func (ix *Index) copiesOn(n *Node) int {
	c := 0
	for _, sh := range ix.Shards {
		for _, cp := range sh {
			if cp.Node == n || cp.Target == n {
				c++
			}
		}
	}
	return c
}

// Copy is one shard copy.
type Copy struct {
	Index   *Index
	Shard   int
	Primary bool
	Node    *Node // nil when unassigned
	State   string
	Target  *Node // relocation target
	DoneMs  int64 // end of the ongoing recovery

	UReason  string
	UAtMs    int64
	UDetails string
	LastNode *Node // node lost with NODE_LEFT, reused if it comes back in time
	DelayMs  int64 // delayed allocation deadline
}

// Remote is a CCS remote cluster.
type Remote struct {
	Alias     string
	Mode      string // sniff or proxy
	Seeds     []string
	Proxy     string
	Connected bool
	Nodes     int
}

// newID returns a 22-character URL-safe id like ES node ids and index UUIDs.
func newID(r *rand.Rand) string {
	var b [16]byte
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func sortedNodes(nodes []*Node) []*Node {
	out := append([]*Node(nil), nodes...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
