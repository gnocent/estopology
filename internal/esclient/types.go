package esclient

// Normalized views of the Elasticsearch responses used by estopo. Numeric
// fields that an ES version or a node may not report are set to Missing.

// Missing marks a numeric value absent from the response.
const Missing = -1

// Root is GET / (cluster identity, SPEC §5.1).
type Root struct {
	NodeName    string
	ClusterName string
	ClusterUUID string
	Version     string
}

// Health is GET _cluster/health.
type Health struct {
	ClusterName       string
	Status            string // green, yellow, red
	TimedOut          bool
	Nodes             int
	DataNodes         int
	ActivePrimary     int
	Active            int
	Relocating        int
	Initializing      int
	Unassigned        int
	UnassignedPrimary int // 8.x+, Missing on 7.17
	DelayedUnassigned int
	PendingTasks      int
}

// NodeStats is one node of GET _nodes/stats/os,process,jvm,fs.
type NodeStats struct {
	ID          string
	Name        string
	Host        string
	IP          string // transport address as reported (ip:port)
	Roles       []string
	TimestampMs int64

	ProcCPU  int     // process.cpu.percent: the node's own CPU (meaningful with cpusets)
	OSCPU    int     // os.cpu.percent: whole machine
	Load1    float64 // os.cpu.load_average.1m, Missing if absent
	HeapPct  int
	UptimeMs int64

	CgroupCPUUsageNanos int64 // Missing without cgroup data
	CgroupQuotaMicros   int64
	CgroupPeriodMicros  int64
	CgroupThrottledNs   int64

	FSTotal int64
	FSFree  int64
	FSAvail int64

	// fs.io_stats.total, cumulative counters (rates are computed by delta).
	// HasIO is false when ES reports no device (e.g. some containers).
	HasIO      bool
	IOReadOps  int64
	IOWriteOps int64
	IOReadKB   int64
	IOWriteKB  int64
}

// Allocation is one row of GET _cat/allocation. The pseudo-node
// "UNASSIGNED" carries the count of unassigned shards.
type Allocation struct {
	Node        string
	Host        string
	IP          string
	Shards      int
	DiskIndices int64
	DiskUsed    int64
	DiskAvail   int64
	DiskTotal   int64
	DiskPercent int
}

// ClusterSettings is GET _cluster/settings?flat_settings=true. List values
// are kept as their JSON text (e.g. `["es01:9300"]`).
type ClusterSettings struct {
	Persistent map[string]string
	Transient  map[string]string
	Defaults   map[string]string // only with include_defaults=true
}

// Remote is one entry of GET _remote/info (CCS).
type Remote struct {
	Alias           string
	Mode            string // sniff or proxy
	Connected       bool
	NodesConnected  int // num_nodes_connected (sniff) or num_proxy_sockets_connected (proxy)
	Seeds           []string
	ProxyAddress    string
	SkipUnavailable bool
}

// NodeInfo is one node of GET _nodes/os,jvm,http,transport.
type NodeInfo struct {
	ID               string
	Name             string
	Host             string
	IP               string
	TransportAddress string
	HTTPAddress      string
	Version          string
	Roles            []string
	Attributes       map[string]string
	OSName           string
	OSPrettyName     string
	OSKernel         string // os.version
	OSArch           string
	Processors       int
	JVMVersion       string
	JVMStartMs       int64 // exact node start time
}

// CatIndex is one row of GET _cat/indices.
type CatIndex struct {
	UUID           string
	Name           string
	Status         string // open, close
	Health         string
	Pri            int
	Rep            int
	Docs           int64 // Missing for closed indices
	StoreBytes     int64
	PriStoreBytes  int64
	CreationDateMs int64
}

// CatShard is one row of GET _cat/shards.
type CatShard struct {
	Index      string
	Shard      int
	Primary    bool
	State      string // STARTED, RELOCATING, INITIALIZING, UNASSIGNED
	Docs       int64  // Missing when unknown
	StoreBytes int64
	NodeID     string // source node for RELOCATING
	Node       string
	// RELOCATING only: target parsed from "es01 -> 10.0.0.2 <id> es02".
	RelocTargetNode string
	RelocTargetID   string
	RelocTargetIP   string

	UnassignedReason  string
	UnassignedAtMs    int64 // 0 if absent
	UnassignedDetails string
}

// Privileges is the answer of GET _security/user/_has_privileges.
type Privileges struct {
	Username        string
	HasAllRequested bool
	Missing         []string // "cluster:monitor", "index:*:view_index_metadata", ...
}
