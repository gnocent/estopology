package esclient

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Parsers of the raw responses. Large responses (_cat/shards, _cat/indices,
// index settings) are decoded as streams and handed row by row to a callback.

// ParseRoot parses GET /.
func ParseRoot(r io.Reader) (Root, error) {
	var raw struct {
		Name        string `json:"name"`
		ClusterName string `json:"cluster_name"`
		ClusterUUID string `json:"cluster_uuid"`
		Version     struct {
			Number string `json:"number"`
		} `json:"version"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return Root{}, err
	}
	return Root{NodeName: raw.Name, ClusterName: raw.ClusterName, ClusterUUID: raw.ClusterUUID, Version: raw.Version.Number}, nil
}

// ParseHealth parses GET _cluster/health.
func ParseHealth(r io.Reader) (Health, error) {
	var raw struct {
		ClusterName       string `json:"cluster_name"`
		Status            string `json:"status"`
		TimedOut          bool   `json:"timed_out"`
		Nodes             int    `json:"number_of_nodes"`
		DataNodes         int    `json:"number_of_data_nodes"`
		ActivePrimary     int    `json:"active_primary_shards"`
		Active            int    `json:"active_shards"`
		Relocating        int    `json:"relocating_shards"`
		Initializing      int    `json:"initializing_shards"`
		Unassigned        int    `json:"unassigned_shards"`
		UnassignedPrimary *int   `json:"unassigned_primary_shards"`
		DelayedUnassigned int    `json:"delayed_unassigned_shards"`
		PendingTasks      int    `json:"number_of_pending_tasks"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return Health{}, err
	}
	h := Health{
		ClusterName: raw.ClusterName, Status: raw.Status, TimedOut: raw.TimedOut,
		Nodes: raw.Nodes, DataNodes: raw.DataNodes, ActivePrimary: raw.ActivePrimary, Active: raw.Active,
		Relocating: raw.Relocating, Initializing: raw.Initializing, Unassigned: raw.Unassigned,
		UnassignedPrimary: Missing, DelayedUnassigned: raw.DelayedUnassigned, PendingTasks: raw.PendingTasks,
	}
	if raw.UnassignedPrimary != nil {
		h.UnassignedPrimary = *raw.UnassignedPrimary
	}
	if h.Status == "" {
		return h, fmt.Errorf("health: no status")
	}
	return h, nil
}

// nodesHeader is the "_nodes" object of the _nodes APIs: how many nodes were
// asked and how many answered.
type nodesHeader struct {
	Total      int `json:"total"`
	Successful int `json:"successful"`
	Failed     int `json:"failed"`
}

// check refuses an answer that does not describe every node: ES answers 200
// when some nodes failed or timed out and simply leaves them out, and a
// proxy can answer 200 with an empty object. Taken as complete, such an
// answer would make the missing nodes look gone.
func (h *nodesHeader) check(nodes int) error {
	if h != nil && h.Failed > 0 {
		return fmt.Errorf("%d of %d nodes did not answer", h.Failed, h.Total)
	}
	if nodes == 0 {
		return fmt.Errorf("no node in the answer")
	}
	return nil
}

// ParseNodesStats parses GET _nodes/stats/os,process,jvm,fs (filtered or not).
// Nodes are returned sorted by name. An answer where a node failed, or with
// no node at all, is an error.
func ParseNodesStats(r io.Reader) ([]NodeStats, error) {
	type ioTotal struct {
		ReadOps  *int64 `json:"read_operations"`
		WriteOps *int64 `json:"write_operations"`
		ReadKB   *int64 `json:"read_kilobytes"`
		WriteKB  *int64 `json:"write_kilobytes"`
	}
	var raw struct {
		Header *nodesHeader `json:"_nodes"`
		Nodes  map[string]struct {
			Timestamp int64    `json:"timestamp"`
			Name      string   `json:"name"`
			Host      string   `json:"host"`
			IP        string   `json:"ip"`
			Roles     []string `json:"roles"`
			OS        struct {
				CPU struct {
					Percent *int                `json:"percent"`
					Load    map[string]*float64 `json:"load_average"`
				} `json:"cpu"`
				Cgroup *struct {
					CPUAcct struct {
						UsageNanos *int64 `json:"usage_nanos"`
					} `json:"cpuacct"`
					CPU struct {
						Period *int64 `json:"cfs_period_micros"`
						Quota  *int64 `json:"cfs_quota_micros"`
						Stat   struct {
							Throttled *int64 `json:"time_throttled_nanos"`
						} `json:"stat"`
					} `json:"cpu"`
				} `json:"cgroup"`
			} `json:"os"`
			Process struct {
				CPU struct {
					Percent *int `json:"percent"`
				} `json:"cpu"`
			} `json:"process"`
			JVM struct {
				Uptime *int64 `json:"uptime_in_millis"`
				Mem    struct {
					HeapPct *int `json:"heap_used_percent"`
				} `json:"mem"`
			} `json:"jvm"`
			FS struct {
				Total struct {
					Total *int64 `json:"total_in_bytes"`
					Free  *int64 `json:"free_in_bytes"`
					Avail *int64 `json:"available_in_bytes"`
				} `json:"total"`
				IOStats struct {
					Total *ioTotal `json:"total"`
				} `json:"io_stats"`
			} `json:"fs"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, err
	}
	if err := raw.Header.check(len(raw.Nodes)); err != nil {
		return nil, err
	}
	out := make([]NodeStats, 0, len(raw.Nodes))
	for id, n := range raw.Nodes {
		s := NodeStats{
			ID: id, Name: n.Name, Host: n.Host, IP: n.IP, Roles: n.Roles, TimestampMs: n.Timestamp,
			ProcCPU: intOr(n.Process.CPU.Percent), OSCPU: intOr(n.OS.CPU.Percent), Load1: Missing,
			HeapPct: intOr(n.JVM.Mem.HeapPct), UptimeMs: int64Or(n.JVM.Uptime),
			CgroupCPUUsageNanos: Missing, CgroupQuotaMicros: Missing, CgroupPeriodMicros: Missing, CgroupThrottledNs: Missing,
			FSTotal: int64Or(n.FS.Total.Total), FSFree: int64Or(n.FS.Total.Free), FSAvail: int64Or(n.FS.Total.Avail),
			IOReadOps: Missing, IOWriteOps: Missing, IOReadKB: Missing, IOWriteKB: Missing,
		}
		if l := n.OS.CPU.Load["1m"]; l != nil {
			s.Load1 = *l
		}
		if cg := n.OS.Cgroup; cg != nil {
			s.CgroupCPUUsageNanos = int64Or(cg.CPUAcct.UsageNanos)
			s.CgroupQuotaMicros = int64Or(cg.CPU.Quota)
			s.CgroupPeriodMicros = int64Or(cg.CPU.Period)
			s.CgroupThrottledNs = int64Or(cg.CPU.Stat.Throttled)
		}
		if t := n.FS.IOStats.Total; t != nil && t.ReadOps != nil {
			s.HasIO = true
			s.IOReadOps, s.IOWriteOps = int64Or(t.ReadOps), int64Or(t.WriteOps)
			s.IOReadKB, s.IOWriteKB = int64Or(t.ReadKB), int64Or(t.WriteKB)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name || (out[i].Name == out[j].Name && out[i].ID < out[j].ID)
	})
	return out, nil
}

// ParseAllocation parses GET _cat/allocation?format=json&bytes=b.
func ParseAllocation(r io.Reader) ([]Allocation, error) {
	var out []Allocation
	err := decodeArray(r, func(row *struct {
		Shards      *string `json:"shards"`
		DiskIndices *string `json:"disk.indices"`
		DiskUsed    *string `json:"disk.used"`
		DiskAvail   *string `json:"disk.avail"`
		DiskTotal   *string `json:"disk.total"`
		DiskPercent *string `json:"disk.percent"`
		Host        *string `json:"host"`
		IP          *string `json:"ip"`
		Node        *string `json:"node"`
	}) error {
		out = append(out, Allocation{
			Node: str(row.Node), Host: str(row.Host), IP: str(row.IP),
			Shards: int(num(row.Shards)), DiskIndices: num(row.DiskIndices), DiskUsed: num(row.DiskUsed),
			DiskAvail: num(row.DiskAvail), DiskTotal: num(row.DiskTotal), DiskPercent: int(num(row.DiskPercent)),
		})
		return nil
	})
	return out, err
}

// ParseClusterSettings parses GET _cluster/settings?flat_settings=true
// (optionally with include_defaults=true).
func ParseClusterSettings(r io.Reader) (ClusterSettings, error) {
	var raw struct {
		Persistent map[string]json.RawMessage `json:"persistent"`
		Transient  map[string]json.RawMessage `json:"transient"`
		Defaults   map[string]json.RawMessage `json:"defaults"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return ClusterSettings{}, err
	}
	cs := ClusterSettings{Persistent: flatValues(raw.Persistent), Transient: flatValues(raw.Transient)}
	if raw.Defaults != nil {
		cs.Defaults = flatValues(raw.Defaults)
	}
	return cs, nil
}

// ParseRemoteInfo parses GET _remote/info, sorted by alias.
func ParseRemoteInfo(r io.Reader) ([]Remote, error) {
	var raw map[string]struct {
		Connected       bool     `json:"connected"`
		Mode            string   `json:"mode"`
		Seeds           []string `json:"seeds"`
		NumNodes        *int     `json:"num_nodes_connected"`
		NumProxy        *int     `json:"num_proxy_sockets_connected"`
		ProxyAddress    string   `json:"proxy_address"`
		SkipUnavailable bool     `json:"skip_unavailable"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]Remote, 0, len(raw))
	for alias, v := range raw {
		n := v.NumNodes
		if n == nil {
			n = v.NumProxy
		}
		out = append(out, Remote{Alias: alias, Mode: v.Mode, Connected: v.Connected, NodesConnected: intOr(n),
			Seeds: v.Seeds, ProxyAddress: v.ProxyAddress, SkipUnavailable: v.SkipUnavailable})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, nil
}

// ParseNodes parses GET _nodes/os,jvm,http,transport, sorted by name, with
// the same completeness rule as ParseNodesStats.
func ParseNodes(r io.Reader) ([]NodeInfo, error) {
	var raw struct {
		Header *nodesHeader `json:"_nodes"`
		Nodes  map[string]struct {
			Name             string            `json:"name"`
			TransportAddress string            `json:"transport_address"`
			Host             string            `json:"host"`
			IP               string            `json:"ip"`
			Version          string            `json:"version"`
			Roles            []string          `json:"roles"`
			Attributes       map[string]string `json:"attributes"`
			OS               struct {
				Name       string `json:"name"`
				PrettyName string `json:"pretty_name"`
				Arch       string `json:"arch"`
				Version    string `json:"version"`
				Processors int    `json:"available_processors"`
			} `json:"os"`
			JVM struct {
				Version   string `json:"version"`
				StartTime int64  `json:"start_time_in_millis"`
			} `json:"jvm"`
			HTTP struct {
				PublishAddress string `json:"publish_address"`
			} `json:"http"`
		} `json:"nodes"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, err
	}
	if err := raw.Header.check(len(raw.Nodes)); err != nil {
		return nil, err
	}
	out := make([]NodeInfo, 0, len(raw.Nodes))
	for id, n := range raw.Nodes {
		out = append(out, NodeInfo{
			ID: id, Name: n.Name, Host: n.Host, IP: n.IP, TransportAddress: n.TransportAddress,
			HTTPAddress: n.HTTP.PublishAddress, Version: n.Version, Roles: n.Roles, Attributes: n.Attributes,
			OSName: n.OS.Name, OSPrettyName: n.OS.PrettyName, OSKernel: n.OS.Version, OSArch: n.OS.Arch,
			Processors: n.OS.Processors, JVMVersion: n.JVM.Version, JVMStartMs: n.JVM.StartTime,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name || (out[i].Name == out[j].Name && out[i].ID < out[j].ID)
	})
	return out, nil
}

// DecodeCatIndices streams GET _cat/indices?format=json&bytes=b&h=... rows.
func DecodeCatIndices(r io.Reader, fn func(CatIndex) error) error {
	return decodeArray(r, func(row *struct {
		UUID         *string `json:"uuid"`
		Index        *string `json:"index"`
		Status       *string `json:"status"`
		Health       *string `json:"health"`
		Pri          *string `json:"pri"`
		Rep          *string `json:"rep"`
		Docs         *string `json:"docs.count"`
		Store        *string `json:"store.size"`
		PriStore     *string `json:"pri.store.size"`
		CreationDate *string `json:"creation.date"`
	}) error {
		return fn(CatIndex{
			UUID: str(row.UUID), Name: str(row.Index), Status: str(row.Status), Health: str(row.Health),
			Pri: int(num(row.Pri)), Rep: int(num(row.Rep)), Docs: num(row.Docs),
			StoreBytes: num(row.Store), PriStoreBytes: num(row.PriStore), CreationDateMs: num(row.CreationDate),
		})
	})
}

// DecodeCatShards streams GET _cat/shards?format=json&bytes=b&h=... rows.
func DecodeCatShards(r io.Reader, fn func(CatShard) error) error {
	return decodeArray(r, func(row *struct {
		Index   *string `json:"index"`
		Shard   *string `json:"shard"`
		PriRep  *string `json:"prirep"`
		State   *string `json:"state"`
		Docs    *string `json:"docs"`
		Store   *string `json:"store"`
		ID      *string `json:"id"`
		Node    *string `json:"node"`
		UReason *string `json:"unassigned.reason"`
		UAt     *string `json:"unassigned.at"`
		UDet    *string `json:"unassigned.details"`
	}) error {
		s := CatShard{
			Index: str(row.Index), Shard: int(num(row.Shard)), Primary: str(row.PriRep) == "p",
			State: str(row.State), Docs: num(row.Docs), StoreBytes: num(row.Store),
			NodeID: str(row.ID), Node: str(row.Node),
			UnassignedReason: str(row.UReason), UnassignedDetails: str(row.UDet),
		}
		if at := str(row.UAt); at != "" {
			s.UnassignedAtMs = parseESTime(at)
		}
		if src, dst, ok := strings.Cut(s.Node, " -> "); ok {
			s.Node = src
			// dst is "<ip> <node id> <node name>"; the name may contain spaces.
			f := strings.SplitN(dst, " ", 3)
			if len(f) == 3 {
				s.RelocTargetIP, s.RelocTargetID, s.RelocTargetNode = f[0], f[1], f[2]
			} else {
				s.RelocTargetNode = dst
			}
		}
		return fn(s)
	})
}

// DecodeIndexSettings streams GET _all/_settings/<names>?flat_settings=true:
// fn receives each index name with its flat settings.
func DecodeIndexSettings(r io.Reader, fn func(index string, settings map[string]string) error) error {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		name, _ := tok.(string)
		var v struct {
			Settings map[string]json.RawMessage `json:"settings"`
		}
		if err := dec.Decode(&v); err != nil {
			return fmt.Errorf("settings of %s: %w", name, err)
		}
		if err := fn(name, flatValues(v.Settings)); err != nil {
			return err
		}
	}
	return expectDelim(dec, '}')
}

// ParsePrivileges parses GET _security/user/_has_privileges.
func ParsePrivileges(r io.Reader) (Privileges, error) {
	var raw struct {
		Username string                     `json:"username"`
		All      bool                       `json:"has_all_requested"`
		Cluster  map[string]bool            `json:"cluster"`
		Index    map[string]map[string]bool `json:"index"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return Privileges{}, err
	}
	p := Privileges{Username: raw.Username, HasAllRequested: raw.All}
	for k, ok := range raw.Cluster {
		if !ok {
			p.Missing = append(p.Missing, "cluster:"+k)
		}
	}
	for idx, privs := range raw.Index {
		for k, ok := range privs {
			if !ok {
				p.Missing = append(p.Missing, "index:"+idx+":"+k)
			}
		}
	}
	sort.Strings(p.Missing)
	return p, nil
}

// --- helpers -------------------------------------------------------------

func decodeArray[T any](r io.Reader, fn func(*T) error) error {
	dec := json.NewDecoder(r)
	if err := expectDelim(dec, '['); err != nil {
		return err
	}
	for dec.More() {
		var v T
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if err := fn(&v); err != nil {
			return err
		}
	}
	return expectDelim(dec, ']')
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("expected %q, got %v", want, tok)
	}
	return nil
}

// flatValues turns flat settings into strings; non-string values (lists,
// numbers, booleans) keep their compact JSON text.
func flatValues(m map[string]json.RawMessage) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
			continue
		}
		var buf bytes.Buffer
		if json.Compact(&buf, v) == nil {
			out[k] = buf.String()
		} else {
			out[k] = string(v)
		}
	}
	return out
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// num parses a _cat numeric cell; null, empty or non-numeric gives Missing.
func num(p *string) int64 {
	if p == nil || *p == "" {
		return Missing
	}
	n, err := strconv.ParseInt(*p, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(*p, 64)
		if ferr != nil {
			return Missing
		}
		return int64(f)
	}
	return n
}

// parseESTime parses an ES timestamp (ISO-8601, UTC) into epoch ms, 0 if invalid.
func parseESTime(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func intOr(p *int) int {
	if p == nil {
		return Missing
	}
	return *p
}

func int64Or(p *int64) int64 {
	if p == nil {
		return Missing
	}
	return *p
}
