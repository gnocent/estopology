// Package model holds the reconstructed state of a cluster at a date,
// shared by the queries (which build it) and the derived computations
// (which read it).
package model

// State is a cluster as observed at a date (SPEC §6.4): structure from the
// last successful full capture ≤ T, presence, health and metrics from the
// last probe ≤ T.
type State struct {
	Cluster   string `json:"cluster"`
	AtMs      int64  `json:"at"`         // requested date
	CaptureMs int64  `json:"capture_ts"` // full capture giving the structure (0 = none)
	ProbeMs   int64  `json:"probe_ts"`   // probe giving presence and health (0 = none)
	// Unknown is set when the last capture ≤ T failed: the state shown is
	// the last known one, the UI draws an "unknown" band (SPEC §5.5).
	Unknown bool `json:"unknown"`

	Health          *Health           `json:"health,omitempty"`
	Nodes           []Node            `json:"nodes"`
	Indices         []Index           `json:"indices"`
	Shards          []Shard           `json:"-"`                // large: served through dedicated endpoints
	ClusterSettings map[string]string `json:"cluster_settings"` // effective (transient > persistent)
	Persistent      map[string]string `json:"-"`
	Transient       map[string]string `json:"-"`
	Defaults        map[string]string `json:"-"` // cluster.routing.* defaults of the last full capture
	Remotes         []Remote          `json:"remotes"`
}

// Health is the cluster health at the probe.
type Health struct {
	Status            string `json:"status"`
	Nodes             int    `json:"nodes"`
	DataNodes         int    `json:"data_nodes"`
	ActivePrimary     int    `json:"active_primary"`
	Active            int    `json:"active"`
	Relocating        int    `json:"relocating"`
	Initializing      int    `json:"initializing"`
	Unassigned        int    `json:"unassigned"`
	UnassignedPrimary int    `json:"unassigned_primary"` // -1 before 8.x
	DelayedUnassigned int    `json:"delayed_unassigned"`
	PendingTasks      int    `json:"pending_tasks"`
}

// Node is a node generation. Present tells whether it was in the cluster at
// the probe; Info fields come from the last full capture.
type Node struct {
	Name       string            `json:"name"`
	ESID       string            `json:"es_id"`
	Generation int               `json:"generation"`
	Present    bool              `json:"present"`
	Roles      []string          `json:"roles"`
	Tiers      []string          `json:"tiers"` // hot, warm, cold, content, frozen
	IP         string            `json:"ip"`
	Host       string            `json:"host"`
	Machine    string            `json:"machine"`
	Version    string            `json:"version"`
	OS         string            `json:"os"`
	Kernel     string            `json:"kernel"`
	JVM        string            `json:"jvm"`
	StartMs    int64             `json:"start_ms"`
	Attrs      map[string]string `json:"attrs"`
	Metrics    *NodeMetrics      `json:"metrics,omitempty"`
}

// IsData reports whether the node holds data.
func (n *Node) IsData() bool {
	for _, r := range n.Roles {
		if r == "data" || len(r) > 5 && r[:5] == "data_" {
			return true
		}
	}
	return false
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

// NodeMetrics are the last metric values ≤ T (-1 = unknown).
type NodeMetrics struct {
	Ts          int64   `json:"ts"`
	ProcCPU     int     `json:"proc_cpu"`
	OSCPU       int     `json:"os_cpu"`
	Load1       float64 `json:"load1"`
	HeapPct     int     `json:"heap_pct"`
	DiskTotal   int64   `json:"disk_total"`
	DiskUsed    int64   `json:"disk_used"`
	DiskAvail   int64   `json:"disk_avail"`
	DiskIndices int64   `json:"disk_indices"`
	Shards      int64   `json:"shards"`
}

// DiskPercent returns the used disk percentage, or -1.
func (m *NodeMetrics) DiskPercent() float64 {
	if m == nil || m.DiskTotal <= 0 || m.DiskUsed < 0 {
		return -1
	}
	return float64(m.DiskUsed) * 100 / float64(m.DiskTotal)
}

// Index is an index open (existing) at T.
type Index struct {
	UUID          string            `json:"uuid"`
	Name          string            `json:"name"`
	Family        string            `json:"family"`
	Status        string            `json:"status"`
	Pri           int               `json:"pri"`
	Rep           int               `json:"rep"`
	CreationMs    int64             `json:"creation_ms"`
	Hidden        bool              `json:"hidden"`
	System        bool              `json:"system"` // name starts with '.'
	Settings      map[string]string `json:"settings"`
	Docs          int64             `json:"docs"`
	StoreBytes    int64             `json:"store_bytes"`
	PriStoreBytes int64             `json:"pri_store_bytes"`
}

// Shard is one shard copy at T. Index and Node are positions in
// State.Indices / State.Nodes (Node = -1 when unassigned).
type Shard struct {
	Index          int32  `json:"index"`
	Shard          int32  `json:"shard"`
	Primary        bool   `json:"primary"`
	Node           int32  `json:"node"`
	State          string `json:"state"`
	RelocTarget    int32  `json:"reloc_target"` // -1 if none
	Reason         string `json:"reason,omitempty"`
	UnassignedAtMs int64  `json:"unassigned_at,omitempty"`
	SinceMs        int64  `json:"since"` // start of the interval
}

// Remote is a CCS remote at the probe.
type Remote struct {
	Alias          string `json:"alias"`
	Mode           string `json:"mode"`
	Connected      bool   `json:"connected"`
	NodesConnected int    `json:"nodes_connected"`
	SinceMs        int64  `json:"since"`
}

// TiersOf maps ES roles to tier names.
func TiersOf(roles []string) []string {
	var out []string
	for _, r := range roles {
		switch r {
		case "data_hot":
			out = append(out, "hot")
		case "data_warm":
			out = append(out, "warm")
		case "data_cold":
			out = append(out, "cold")
		case "data_frozen":
			out = append(out, "frozen")
		case "data_content":
			out = append(out, "content")
		case "data":
			out = append(out, "data")
		}
	}
	return out
}
