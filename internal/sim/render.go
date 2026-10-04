package sim

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Renderers of the Elasticsearch APIs estopo uses. Shapes follow the
// responses recorded from real clusters in testdata/es (checked by
// conformance_test.go); only the fields estopo requests are produced.

type format struct {
	v7, v9 bool // 7.x, 9.x+ (else 8.x)
}

func formatOf(version string) format {
	return format{v7: strings.HasPrefix(version, "7."), v9: !strings.HasPrefix(version, "7.") && !strings.HasPrefix(version, "8.")}
}

// jw is a minimal JSON writer (ASCII-oriented, escapes what JSON requires).
type jw struct {
	w *bufio.Writer
}

func (j jw) raw(s string) { j.w.WriteString(s) }
func (j jw) str(s string) {
	j.w.WriteByte('"')
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '"' || ch == '\\':
			j.w.WriteByte('\\')
			j.w.WriteByte(ch)
		case ch < 0x20:
			fmt.Fprintf(j.w, `\u%04x`, ch)
		default:
			j.w.WriteByte(ch)
		}
	}
	j.w.WriteByte('"')
}
func (j jw) key(k string) { j.str(k); j.w.WriteByte(':') }
func (j jw) int(n int64)  { j.w.WriteString(strconv.FormatInt(n, 10)) }
func (j jw) float(f float64) {
	j.w.WriteString(strconv.FormatFloat(f, 'f', -1, 64))
}
func (j jw) strOrNull(s *string) {
	if s == nil {
		j.raw("null")
		return
	}
	j.str(*s)
}
func (j jw) strs(xs []string) {
	j.raw("[")
	for i, x := range xs {
		if i > 0 {
			j.raw(",")
		}
		j.str(x)
	}
	j.raw("]")
}

func sp(s string) *string   { return &s }
func si(n int64) *string    { return sp(strconv.FormatInt(n, 10)) }
func isoMs(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z") }
func (c *Cluster) upNodes() []*Node {
	var out []*Node
	for _, n := range c.Nodes {
		if n.Up {
			out = append(out, n)
		}
	}
	return out
}

// hasMaster reports whether a majority of master-eligible nodes is up.
func (c *Cluster) hasMaster() bool {
	total, up := 0, 0
	for _, n := range c.Nodes {
		if n.IsMaster() {
			total++
			if n.Up {
				up++
			}
		}
	}
	return total > 0 && up*2 > total
}

// coordinator is the node answering HTTP requests (first up node by name
// among data nodes, as behind a VIP).
func (c *Cluster) coordinator() *Node {
	var best *Node
	for _, n := range c.Nodes {
		if n.Up && n.IsData() && (best == nil || n.Name < best.Name) {
			best = n
		}
	}
	if best == nil {
		for _, n := range c.Nodes {
			if n.Up {
				return n
			}
		}
	}
	return best
}

func (c *Cluster) renderRoot(w io.Writer) {
	j := jw{bufio.NewWriter(w)}
	defer j.w.Flush()
	coord := c.coordinator()
	name := ""
	if coord != nil {
		name = coord.Name
	}
	uuid := c.UUID
	if !c.hasMaster() {
		uuid = "_na_"
	}
	f := formatOf(c.Version)
	lucene, wire, idx := "9.12.2", "7.17.0", "7.0.0"
	if f.v7 {
		lucene, wire, idx = "8.11.3", "6.8.0", "6.0.0-beta1"
	} else if f.v9 {
		lucene, wire, idx = "10.5.1", "8.19.0", "8.0.0"
	}
	fmt.Fprintf(j.w, `{
  "name" : %q,
  "cluster_name" : %q,
  "cluster_uuid" : %q,
  "version" : {
    "number" : %q,
    "build_flavor" : "default",
    "build_type" : "tar",
    "build_hash" : "0000000000000000000000000000000000000000",
    "build_date" : "2026-01-01T00:00:00.000000000Z",
    "build_snapshot" : false,
    "lucene_version" : %q,
    "minimum_wire_compatibility_version" : %q,
    "minimum_index_compatibility_version" : %q
  },
  "tagline" : "You Know, for Search"
}
`, name, c.Name, uuid, c.Version, lucene, wire, idx)
}

type healthCounts struct {
	activePrimary, active, relocating, initializing, unassigned, unassignedPrimary, delayed int
	status                                                                                  string
}

func (c *Cluster) health() healthCounts {
	var h healthCounts
	red, yellow := false, false
	for _, ix := range c.Indices {
		for _, sh := range ix.Shards {
			for _, cp := range sh {
				switch cp.State {
				case Started, Relocating:
					h.active++
					if cp.Primary {
						h.activePrimary++
					}
					if cp.State == Relocating {
						h.relocating++
					}
				case Initializing:
					h.initializing++
					if cp.Primary {
						red = true
					} else {
						yellow = true
					}
				case Unassigned:
					h.unassigned++
					if cp.Primary {
						h.unassignedPrimary++
						red = true
					} else {
						yellow = true
						if cp.DelayMs > c.nowMs {
							h.delayed++
						}
					}
				}
			}
		}
	}
	h.status = "green"
	if yellow {
		h.status = "yellow"
	}
	if red {
		h.status = "red"
	}
	return h
}

func (c *Cluster) renderHealth(w io.Writer) {
	j := jw{bufio.NewWriter(w)}
	defer j.w.Flush()
	h := c.health()
	nodes, data := 0, 0
	for _, n := range c.Nodes {
		if n.Up {
			nodes++
			if n.IsData() {
				data++
			}
		}
	}
	total := h.active + h.initializing + h.unassigned
	pct := 100.0
	if total > 0 {
		pct = math.Round(float64(h.active)*100/float64(total)*1e13) / 1e13
	}
	j.raw("{")
	j.key("cluster_name")
	j.str(c.Name)
	j.raw(`,"status":`)
	j.str(h.status)
	fmt.Fprintf(j.w, `,"timed_out":false,"number_of_nodes":%d,"number_of_data_nodes":%d,"active_primary_shards":%d,"active_shards":%d,"relocating_shards":%d,"initializing_shards":%d,"unassigned_shards":%d`,
		nodes, data, h.activePrimary, h.active, h.relocating, h.initializing, h.unassigned)
	if !formatOf(c.Version).v7 {
		fmt.Fprintf(j.w, `,"unassigned_primary_shards":%d`, h.unassignedPrimary)
	}
	fmt.Fprintf(j.w, `,"delayed_unassigned_shards":%d,"number_of_pending_tasks":0,"number_of_in_flight_fetch":0,"task_max_waiting_in_queue_millis":0,"active_shards_percent_as_number":`,
		h.delayed)
	if pct == math.Trunc(pct) {
		fmt.Fprintf(j.w, "%.1f", pct)
	} else {
		j.float(pct)
	}
	j.raw("}")
}

// --- node metrics (deterministic pseudo-random) ------------------------------------

func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func hashStr(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// noise returns a value in [0,1) stable for (key, minute).
func noise(key string, ms int64) float64 {
	return float64(mix(hashStr(key)^uint64(ms/60000))>>11) / (1 << 53)
}

func (c *Cluster) procCPU(n *Node) int {
	base := 5.0
	switch {
	case n.HasRole("data_hot"):
		base = 25
	case n.HasRole("data_warm"):
		base = 10
	case n.HasRole("master"):
		base = 3
	}
	return int(math.Min(100, base*(0.5+n.cpuBias)+20*noise(n.ID, c.nowMs)))
}

func (c *Cluster) nodeDiskIndices(n *Node) int64 {
	// A probe asks for it twice at the same instant (_nodes/stats and
	// _cat/allocation): kept until the time or the placement changes.
	if n.diskOK && n.diskAtMs == c.nowMs && n.diskGen == c.reroutes {
		return n.diskUsed
	}
	var used int64
	for cp := range n.copies {
		used += cp.Index.bytesAt(c.nowMs)
	}
	n.diskUsed, n.diskAtMs, n.diskGen, n.diskOK = used, c.nowMs, c.reroutes, true
	return used
}

func (c *Cluster) renderNodesStats(w io.Writer) {
	j := jw{bufio.NewWriter(w)}
	defer j.w.Flush()
	up := c.upNodes()
	byMachine := map[string][]*Node{}
	for _, n := range up {
		byMachine[n.Machine] = append(byMachine[n.Machine], n)
	}
	f := formatOf(c.Version)
	fmt.Fprintf(j.w, `{"_nodes":{"total":%d,"successful":%d,"failed":0},"cluster_name":`, len(up), len(up))
	j.str(c.Name)
	j.raw(`,"nodes":{`)
	for i, n := range sortedNodes(up) {
		if i > 0 {
			j.raw(",")
		}
		proc := c.procCPU(n)
		machineCPU := 0
		for _, o := range byMachine[n.Machine] {
			machineCPU += c.procCPU(o)
		}
		osCPU := min(100, machineCPU/max(1, len(byMachine[n.Machine]))+3)
		load := float64(osCPU) / 100 * float64(n.Processors*len(byMachine[n.Machine]))
		heap := 30 + int(45*math.Mod(float64(c.nowMs)/420000+noise(n.ID, 0), 1))
		uptime := max(c.nowMs-n.StartMs, 0)
		used := c.nodeDiskIndices(n) + n.DiskTotal/50
		free := max(n.DiskTotal-used, 0)
		avail := max(free-n.DiskTotal/20, 0)
		cgUsage := uptime * 1_000_000 / 100 * int64(proc) * int64(n.Processors) / 100
		j.key(n.ID)
		fmt.Fprintf(j.w, `{"timestamp":%d,"name":%q,"host":%q,"ip":"%s:%d","roles":`, c.nowMs, n.Name, n.Machine, n.IP, n.Transport)
		j.strs(n.Roles)
		fmt.Fprintf(j.w, `,"os":{"cpu":{"percent":%d,"load_average":{"1m":%.2f,"5m":%.2f,"15m":%.2f}`, osCPU, load, load*0.9, load*0.8)
		if f.v9 {
			fmt.Fprintf(j.w, `,"available_processors":%d`, n.Processors)
		}
		fmt.Fprintf(j.w, `},"cgroup":{"cpuacct":{"control_group":"/","usage_nanos":%d},"cpu":{"control_group":"/","cfs_period_micros":100000,"cfs_quota_micros":-1,"stat":{"number_of_elapsed_periods":0,"number_of_times_throttled":0,"time_throttled_nanos":0}},"memory":{"control_group":"/","limit_in_bytes":"max","usage_in_bytes":"%d"}}}`,
			cgUsage, 32*gb/2)
		fmt.Fprintf(j.w, `,"process":{"cpu":{"percent":%d,"total_in_millis":%d}}`, proc, uptime*int64(proc)/100)
		fmt.Fprintf(j.w, `,"jvm":{"uptime_in_millis":%d,"mem":{"heap_used_percent":%d}}`, uptime, heap)
		// io_stats.total follows the documented ES shape; its real-world
		// behaviour could not be checked on the test clusters (empty).
		ops := uptime / 1000 * int64(1+proc)
		fmt.Fprintf(j.w, `,"fs":{"total":{"total_in_bytes":%d,"free_in_bytes":%d,"available_in_bytes":%d},"io_stats":{"total":{"operations":%d,"read_operations":%d,"write_operations":%d,"read_kilobytes":%d,"write_kilobytes":%d,"io_time_in_millis":%d}}}}`,
			n.DiskTotal, free, avail, ops*2, ops, ops, ops*16, ops*32, ops/10)
	}
	j.raw("}}")
}

func (c *Cluster) renderNodes(w io.Writer) {
	j := jw{bufio.NewWriter(w)}
	defer j.w.Flush()
	j.raw(`{"cluster_name":`)
	j.str(c.Name)
	j.raw(`,"nodes":{`)
	for i, n := range sortedNodes(c.upNodes()) {
		if i > 0 {
			j.raw(",")
		}
		j.key(n.ID)
		fmt.Fprintf(j.w, `{"name":%q,"transport_address":"%s:%d","host":%q,"ip":%q,"version":%q,"roles":`,
			n.Name, n.IP, n.Transport, n.Machine, n.IP, n.Version)
		j.strs(n.Roles)
		j.raw(`,"attributes":{`)
		keys := make([]string, 0, len(n.Attrs))
		for k := range n.Attrs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for k, key := range keys {
			if k > 0 {
				j.raw(",")
			}
			j.key(key)
			j.str(n.Attrs[key])
		}
		fmt.Fprintf(j.w, `},"os":{"name":"Linux","pretty_name":%q,"arch":"amd64","version":%q,"available_processors":%d},"jvm":{"version":%q,"start_time_in_millis":%d},"http":{"publish_address":"%s:%d"}}`,
			n.OSPretty, n.Kernel, n.Processors, n.JVM, n.StartMs, n.IP, n.HTTP)
	}
	j.raw("}}")
}

// roleLetters mimics the node.role column of _cat (8.x+).
func roleLetters(roles []string) string {
	m := map[string]string{"data_cold": "c", "data": "d", "data_frozen": "f", "data_hot": "h", "ingest": "i",
		"ml": "l", "master": "m", "remote_cluster_client": "r", "data_content": "s", "transform": "t",
		"voting_only": "v", "data_warm": "w"}
	var ls []string
	for _, r := range roles {
		if l, ok := m[r]; ok {
			ls = append(ls, l)
		}
	}
	sort.Strings(ls)
	return strings.Join(ls, "")
}

// catTable writes _cat rows as JSON with the requested columns.
func catTable(w io.Writer, cols []string, rows func(emit func(cell func(col string) (*string, bool))) error) error {
	j := jw{bufio.NewWriterSize(w, 64<<10)}
	defer j.w.Flush()
	j.raw("[")
	first := true
	var err error
	rowsErr := rows(func(cell func(string) (*string, bool)) {
		if err != nil {
			return
		}
		if !first {
			j.raw(",")
		}
		first = false
		j.raw("{")
		for i, col := range cols {
			v, ok := cell(col)
			if !ok {
				err = fmt.Errorf("unknown column [%s]", col)
				return
			}
			if i > 0 {
				j.raw(",")
			}
			j.key(col)
			j.strOrNull(v)
		}
		j.raw("}")
	})
	j.raw("]")
	if rowsErr != nil {
		return rowsErr
	}
	return err
}

func (c *Cluster) allocationColumns(h []string) []string {
	if len(h) > 0 {
		return h
	}
	if formatOf(c.Version).v7 {
		return []string{"shards", "disk.indices", "disk.used", "disk.avail", "disk.total", "disk.percent", "host", "ip", "node"}
	}
	return []string{"shards", "shards.undesired", "write_load.forecast", "disk.indices.forecast", "disk.indices",
		"disk.used", "disk.avail", "disk.total", "disk.percent", "host", "ip", "node", "node.role"}
}

func (c *Cluster) renderAllocation(w io.Writer, h []string) error {
	health := c.health()
	incoming := map[*Node]int64{} // relocation targets, one pass
	for _, ix := range c.Indices {
		for _, sh := range ix.Shards {
			for _, cp := range sh {
				if cp.Target != nil {
					incoming[cp.Target]++
				}
			}
		}
	}
	return catTable(w, c.allocationColumns(h), func(emit func(func(string) (*string, bool))) error {
		for _, n := range sortedNodes(c.upNodes()) {
			if !n.IsData() {
				continue
			}
			shards := int64(len(n.copies)) + incoming[n]
			indices := c.nodeDiskIndices(n)
			used := indices + n.DiskTotal/50
			avail := max(n.DiskTotal-used, 0)
			emit(func(col string) (*string, bool) {
				switch col {
				case "shards":
					return si(shards), true
				case "shards.undesired":
					return sp("0"), true
				case "write_load.forecast":
					return sp("0.0"), true
				case "disk.indices.forecast", "disk.indices":
					return si(indices), true
				case "disk.used":
					return si(used), true
				case "disk.avail":
					return si(avail), true
				case "disk.total":
					return si(n.DiskTotal), true
				case "disk.percent":
					return si(used * 100 / max(n.DiskTotal, 1)), true
				case "host", "ip":
					return sp(n.IP), true
				case "node":
					return sp(n.Name), true
				case "node.role":
					return sp(roleLetters(n.Roles)), true
				}
				return nil, false
			})
		}
		if health.unassigned > 0 {
			emit(func(col string) (*string, bool) {
				switch col {
				case "shards":
					return si(int64(health.unassigned)), true
				case "node":
					return sp("UNASSIGNED"), true
				case "shards.undesired", "write_load.forecast", "disk.indices.forecast", "disk.indices", "disk.used",
					"disk.avail", "disk.total", "disk.percent", "host", "ip", "node.role":
					return nil, true
				}
				return nil, false
			})
		}
		return nil
	})
}

func writeFlat(j jw, m map[string]string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	j.raw("{")
	for i, k := range keys {
		if i > 0 {
			j.raw(",")
		}
		j.key(k)
		if v := m[k]; strings.HasPrefix(v, "[") {
			j.raw(v)
		} else {
			j.str(v)
		}
	}
	j.raw("}")
}

func (c *Cluster) renderClusterSettings(w io.Writer, defaults bool) {
	j := jw{bufio.NewWriter(w)}
	defer j.w.Flush()
	j.raw(`{"persistent":`)
	writeFlat(j, c.Persistent)
	j.raw(`,"transient":`)
	writeFlat(j, c.Transient)
	if defaults {
		d := map[string]string{}
		for k, v := range settingDefaults {
			if _, set := c.Persistent[k]; set {
				continue
			}
			if _, set := c.Transient[k]; set {
				continue
			}
			if v != "" {
				d[k] = v
			}
		}
		j.raw(`,"defaults":`)
		writeFlat(j, d)
	}
	j.raw("}")
}

func (c *Cluster) renderRemoteInfo(w io.Writer) {
	j := jw{bufio.NewWriter(w)}
	defer j.w.Flush()
	skip := formatOf(c.Version).v9
	j.raw("{")
	rs := append([]*Remote(nil), c.Remotes...)
	sort.Slice(rs, func(a, b int) bool { return rs[a].Alias < rs[b].Alias })
	for i, r := range rs {
		if i > 0 {
			j.raw(",")
		}
		j.key(r.Alias)
		n := r.Nodes
		if !r.Connected {
			n = 0
		}
		if r.Mode == "proxy" {
			fmt.Fprintf(j.w, `{"connected":%t,"mode":"proxy","proxy_address":%q,"server_name":"","num_proxy_sockets_connected":%d,"max_proxy_socket_connections":18,"initial_connect_timeout":"30s","skip_unavailable":%t}`,
				r.Connected, r.Proxy, n, skip)
		} else {
			fmt.Fprintf(j.w, `{"connected":%t,"mode":"sniff","seeds":`, r.Connected)
			j.strs(r.Seeds)
			fmt.Fprintf(j.w, `,"num_nodes_connected":%d,"max_connections_per_cluster":3,"initial_connect_timeout":"30s","skip_unavailable":%t}`, n, skip)
		}
	}
	j.raw("}")
}

func (ix *Index) health() string {
	status := "green"
	for _, sh := range ix.Shards {
		for _, cp := range sh {
			if cp.State == Unassigned || cp.State == Initializing {
				if cp.Primary {
					return "red"
				}
				status = "yellow"
			}
		}
	}
	return status
}

func (c *Cluster) renderCatIndices(w io.Writer, h []string) error {
	if len(h) == 0 {
		h = []string{"health", "status", "index", "uuid", "pri", "rep", "docs.count", "docs.deleted", "store.size", "pri.store.size"}
	}
	return catTable(w, h, func(emit func(func(string) (*string, bool))) error {
		for _, ix := range c.Indices {
			bytes := ix.bytesAt(c.nowMs)
			var priStore, store int64
			for _, sh := range ix.Shards {
				for _, cp := range sh {
					if cp.State == Started || cp.State == Relocating {
						store += bytes
						if cp.Primary {
							priStore += bytes
						}
					}
				}
			}
			emit(func(col string) (*string, bool) {
				switch col {
				case "uuid":
					return sp(ix.UUID), true
				case "index":
					return sp(ix.Name), true
				case "status":
					if ix.Closed {
						return sp("close"), true
					}
					return sp("open"), true
				case "health":
					return sp(ix.health()), true
				case "pri":
					return si(int64(ix.Pri)), true
				case "rep":
					return si(int64(ix.Rep)), true
				case "docs.count":
					if ix.Closed {
						return nil, true
					}
					return si(ix.docsAt(c.nowMs) * int64(ix.Pri)), true
				case "docs.deleted":
					if ix.Closed {
						return nil, true
					}
					return sp("0"), true
				case "store.size":
					if ix.Closed {
						return nil, true
					}
					return si(store), true
				case "pri.store.size":
					if ix.Closed {
						return nil, true
					}
					return si(priStore), true
				case "creation.date":
					return si(ix.CreatedMs), true
				}
				return nil, false
			})
		}
		return nil
	})
}

// indexSettings returns the flat settings of an index (computed + custom).
func (c *Cluster) indexSettings(ix *Index) map[string]string {
	vc := "8537000"
	switch f := formatOf(c.Version); {
	case f.v7:
		vc = "7172999"
	case f.v9:
		vc = "9111000"
	}
	m := map[string]string{
		"index.creation_date":      strconv.FormatInt(ix.CreatedMs, 10),
		"index.number_of_replicas": strconv.Itoa(ix.Rep),
		"index.number_of_shards":   strconv.Itoa(ix.Pri),
		"index.uuid":               ix.UUID,
		"index.version.created":    vc,
	}
	if ix.Hidden {
		m["index.hidden"] = "true"
	}
	for k, v := range ix.Settings {
		m[k] = v
	}
	return m
}

func (c *Cluster) renderIndexSettings(w io.Writer, patterns []string) {
	j := jw{bufio.NewWriterSize(w, 64<<10)}
	defer j.w.Flush()
	j.raw("{")
	for i, ix := range c.Indices {
		if i > 0 {
			j.raw(",")
		}
		all := c.indexSettings(ix)
		sel := map[string]string{}
		for k, v := range all {
			if len(patterns) == 0 || matchAny(k, patterns) {
				sel[k] = v
			}
		}
		j.key(ix.Name)
		j.raw(`{"settings":`)
		writeFlat(j, sel)
		j.raw("}")
	}
	j.raw("}")
}

func (c *Cluster) renderCatShards(w io.Writer, h []string) error {
	if len(h) == 0 {
		h = []string{"index", "shard", "prirep", "state", "docs", "store", "ip", "node"}
	}
	v7 := formatOf(c.Version).v7
	return catTable(w, h, func(emit func(func(string) (*string, bool))) error {
		for _, ix := range c.Indices {
			bytes, docs := ix.bytesAt(c.nowMs), ix.docsAt(c.nowMs)
			for _, sh := range ix.Shards {
				for _, cp := range sh {
					cp := cp
					assigned := cp.Node != nil
					emit(func(col string) (*string, bool) {
						switch col {
						case "index":
							return sp(ix.Name), true
						case "shard":
							return si(int64(cp.Shard)), true
						case "prirep":
							if cp.Primary {
								return sp("p"), true
							}
							return sp("r"), true
						case "state":
							return sp(cp.State), true
						case "docs", "store":
							if !assigned || (ix.Closed && v7) {
								return nil, true
							}
							v := bytes
							if col == "docs" {
								v = docs
							}
							if cp.State == Initializing {
								v /= 2
							}
							return si(v), true
						case "id":
							if !assigned {
								return nil, true
							}
							return sp(cp.Node.ID), true
						case "ip":
							if !assigned {
								return nil, true
							}
							return sp(cp.Node.IP), true
						case "node":
							if !assigned {
								return nil, true
							}
							if cp.State == Relocating && cp.Target != nil {
								return sp(fmt.Sprintf("%s -> %s %s %s", cp.Node.Name, cp.Target.IP, cp.Target.ID, cp.Target.Name)), true
							}
							return sp(cp.Node.Name), true
						case "unassigned.reason":
							if cp.State != Unassigned {
								return nil, true
							}
							return sp(cp.UReason), true
						case "unassigned.at":
							if cp.State != Unassigned {
								return nil, true
							}
							return sp(isoMs(cp.UAtMs)), true
						case "unassigned.details":
							if cp.State != Unassigned || cp.UDetails == "" {
								return nil, true
							}
							return sp(cp.UDetails), true
						}
						return nil, false
					})
				}
			}
		}
		return nil
	})
}

// patternsFromPath extracts the setting name patterns of
// /_all/_settings/<a>,<b>.
func patternsFromPath(p string) []string {
	rest := strings.TrimPrefix(p, "/_all/_settings")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(rest, ",") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
