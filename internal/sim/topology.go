package sim

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

// Spec describes a simulated cluster.
type Spec struct {
	Name    string
	Version string // e.g. "8.19.22"
	Seed    uint64

	DCs     []string
	Tiers   []TierSpec
	Masters int // dedicated master nodes, spread over the DCs

	Sources       []Source
	RetentionDays int // 0 = keep forever
	WarmAfterDays int // 0 = never
	ColdAfterDays int // 0 = never

	AwarenessAttrs []string // default: ["dc"]
	Remotes        []string // CCS aliases (connected sniff remotes)
	RecoveryRate   string   // indices.recovery.max_bytes_per_sec, default "200mb"
}

// TierSpec describes the machines of one data tier.
type TierSpec struct {
	Tier            string // hot, warm, cold, content
	MachinesPerDC   int
	NodesPerMachine int
	DiskPerNode     int64
	Processors      int
}

// Source is one family of indices (daily or rollover).
type Source struct {
	Name          string
	Shards        int
	Replicas      int
	DailyBytes    int64 // bytes of primaries written per day
	DocBytes      int64
	Rollover      bool  // -00000N indices rolled over at RolloverBytes
	RolloverBytes int64 // total primary size at rollover
	Settings      map[string]string
}

// Build creates the cluster at time start, with the history implied by
// RetentionDays already in place (indices allocated and started), as if the
// cluster had been running for a long time.
func Build(spec Spec, start time.Time) *Cluster {
	if len(spec.AwarenessAttrs) == 0 {
		spec.AwarenessAttrs = []string{"dc"}
	}
	if spec.RecoveryRate == "" {
		spec.RecoveryRate = "200mb"
	}
	rng := rand.New(rand.NewPCG(spec.Seed, spec.Seed^0x5eed))
	startMs := start.UTC().UnixMilli()
	c := &Cluster{
		Name: spec.Name, UUID: newID(rng), Version: spec.Version, spec: spec, rng: rng,
		nodeBy: map[string]*Node{}, indexBy: map[string]*Index{},
		Persistent: map[string]string{}, Transient: map[string]string{},
		unassigned: map[*Copy]struct{}{}, checkIdx: map[*Index]struct{}{},
	}
	if len(spec.AwarenessAttrs) > 0 {
		c.Persistent["cluster.routing.allocation.awareness.attributes"] = joinComma(spec.AwarenessAttrs)
	}
	c.Persistent["indices.recovery.max_bytes_per_sec"] = spec.RecoveryRate
	for _, alias := range spec.Remotes {
		c.Persistent["cluster.remote."+alias+".seeds"] = fmt.Sprintf(`["%s-seed:9300"]`, alias)
		c.Remotes = append(c.Remotes, &Remote{Alias: alias, Mode: "sniff", Seeds: []string{alias + "-seed:9300"}, Connected: true, Nodes: 3})
	}

	// Nodes. Machines get consecutive IPs; nodes of a machine share its IP.
	machine := 0
	addNode := func(name, mach string, roles []string, dc string, disk int64, procs, slot int) {
		ip := fmt.Sprintf("10.%d.%d.%d", 1+int(spec.Seed%200), machine/250, 1+machine%250)
		n := &Node{
			ID: newID(rng), Name: name, Machine: mach, IP: ip, Transport: 9300 + slot, HTTP: 9200 + slot,
			Roles: roles, Attrs: map[string]string{"dc": dc, "machine": mach, "xpack.installed": "true"},
			Version: spec.Version, OSPretty: "Red Hat Enterprise Linux 9.6 (Plow)", Kernel: "5.14.0-570.el9.x86_64",
			JVM: jvmFor(spec.Version), Processors: procs, DiskTotal: disk, Up: true,
			StartMs: startMs - int64(rng.IntN(30*24*3600))*1000, Generation: 1,
			copies: map[*Copy]struct{}{}, cpuBias: rng.Float64(),
		}
		c.Nodes = append(c.Nodes, n)
		c.nodeBy[name] = n
	}
	for i := 0; i < spec.Masters; i++ {
		dc := spec.DCs[i%len(spec.DCs)]
		mach := fmt.Sprintf("%s-master-%s-m%02d", spec.Name, dc, i/len(spec.DCs)+1)
		addNode(mach+"-1", mach, []string{"master"}, dc, 100<<30, 8, 0)
		machine++
	}
	for _, t := range spec.Tiers {
		roles := []string{"data_" + t.Tier}
		if t.Tier == "hot" {
			roles = []string{"data_content", "data_hot", "ingest"}
		}
		if len(spec.Remotes) > 0 {
			roles = append(roles, "remote_cluster_client")
		}
		for _, dc := range spec.DCs {
			for m := 1; m <= t.MachinesPerDC; m++ {
				mach := fmt.Sprintf("%s-%s-%s-m%02d", spec.Name, t.Tier, dc, m)
				for k := 1; k <= t.NodesPerMachine; k++ {
					addNode(fmt.Sprintf("%s-%d", mach, k), mach, sortedRoles(roles), dc, t.DiskPerNode, t.Processors, k-1)
				}
				machine++
			}
		}
	}

	// History: indices of the retention window, oldest first, placed instantly.
	c.nowMs = startMs
	days := spec.RetentionDays
	if days <= 0 {
		days = 1
	}
	today := start.UTC().Truncate(24 * time.Hour)
	for d := days - 1; d >= 0; d-- {
		dayStart := today.AddDate(0, 0, -d)
		c.nowMs = max(dayStart.UnixMilli(), startMs-int64(days)*dayMs)
		for _, src := range spec.Sources {
			if src.Rollover {
				continue
			}
			ix := c.createIndex(src, fmt.Sprintf("%s-%s", src.Name, dayStart.Format("2006.01.02")),
				dayStart.UnixMilli(), dayStart.UnixMilli()+dayMs, true)
			ix.CreatedMs = dayStart.UnixMilli() + int64(rng.IntN(60))*1000
		}
	}
	for _, src := range spec.Sources {
		if !src.Rollover {
			continue
		}
		// Enough rolled-over generations to cover the retention window.
		dur := int64(float64(src.RolloverBytes) / float64(max(src.DailyBytes, 1)) * float64(dayMs))
		dur = max(dur, int64(time.Hour/time.Millisecond))
		// The write index is half full at start; older generations precede it.
		n := 1 + int(math.Ceil(float64(int64(days)*dayMs)/float64(dur)))
		writeFrom := startMs - dur/2
		for g := 1; g <= n; g++ {
			from := writeFrom - int64(n-g)*dur
			c.nowMs = max(from, startMs-int64(days)*dayMs)
			ix := c.createIndex(src, fmt.Sprintf("%s-%06d", src.Name, g), from, from+dur, g == n)
			ix.Source = src.Name
			ix.CreatedMs = from
		}
	}
	c.nowMs = startMs
	c.applyLifecycle()
	c.placeAll()

	c.nextDaily = today.Add(24 * time.Hour).UnixMilli()
	c.nextTick = startMs + tickMs
	return c
}

func jvmFor(version string) string {
	switch version[0] {
	case '7':
		return "22.0.1"
	case '8':
		return "24.0.2"
	default:
		return "25"
	}
}

func joinComma(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}

func sortedRoles(r []string) []string {
	out := append([]string(nil), r...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// --- presets (SPEC §13.1) ------------------------------------------------------

const (
	gb = int64(1) << 30
	tb = int64(1) << 40
)

// sources builds n daily sources with log-normal volumes around median.
func sources(prefix string, n int, seed uint64, median int64, shards [2]int, rollover int) []Source {
	r := rand.New(rand.NewPCG(seed, 7))
	out := make([]Source, 0, n)
	for i := 0; i < n; i++ {
		vol := int64(float64(median) * math.Exp(r.NormFloat64()*0.8))
		s := Source{
			Name: fmt.Sprintf("%s-%03d", prefix, i), Shards: shards[0] + r.IntN(shards[1]-shards[0]+1),
			Replicas: 1, DailyBytes: vol, DocBytes: 400 + int64(r.IntN(800)),
		}
		if i < rollover {
			s.Rollover, s.RolloverBytes = true, 50*gb
		}
		out = append(out, s)
	}
	return out
}

// PresetA is the reference large cluster: ~200 nodes over 3 DCs, 4 hot nodes
// per machine and 8 warm/cold nodes per machine, daily indices kept 90 days
// (~250,000 shard copies).
func PresetA(version string) Spec {
	return Spec{
		Name: "clu-a", Version: version, Seed: 1, DCs: []string{"dc1", "dc2", "dc3"}, Masters: 3,
		Tiers: []TierSpec{
			{Tier: "hot", MachinesPerDC: 5, NodesPerMachine: 4, DiskPerNode: 3 * tb, Processors: 8},
			{Tier: "warm", MachinesPerDC: 3, NodesPerMachine: 8, DiskPerNode: 8 * tb, Processors: 4},
			{Tier: "cold", MachinesPerDC: 3, NodesPerMachine: 8, DiskPerNode: 12 * tb, Processors: 4},
		},
		Sources:       sources("logs", 470, 11, 6*gb, [2]int{1, 5}, 20),
		RetentionDays: 90, WarmAfterDays: 7, ColdAfterDays: 30,
		Remotes: []string{"clu-b", "clu-c"},
	}
}

// PresetB is the second large cluster (~100,000 shard copies).
func PresetB(version string) Spec {
	return Spec{
		Name: "clu-b", Version: version, Seed: 2, DCs: []string{"dc1", "dc2"}, Masters: 3,
		Tiers: []TierSpec{
			{Tier: "hot", MachinesPerDC: 5, NodesPerMachine: 4, DiskPerNode: 3 * tb, Processors: 8},
			{Tier: "warm", MachinesPerDC: 3, NodesPerMachine: 8, DiskPerNode: 8 * tb, Processors: 4},
		},
		Sources:       sources("app", 190, 22, 4*gb, [2]int{1, 5}, 10),
		RetentionDays: 90, WarmAfterDays: 10,
		Remotes: []string{"clu-a"},
	}
}

// PresetC is a small, mostly cold cluster with little activity.
func PresetC(version string) Spec {
	return Spec{
		Name: "clu-c", Version: version, Seed: 3, DCs: []string{"dc1", "dc2"}, Masters: 3,
		Tiers: []TierSpec{
			{Tier: "hot", MachinesPerDC: 1, NodesPerMachine: 2, DiskPerNode: 2 * tb, Processors: 8},
			{Tier: "cold", MachinesPerDC: 2, NodesPerMachine: 8, DiskPerNode: 16 * tb, Processors: 4},
		},
		Sources:       sources("arch", 20, 33, 20*gb, [2]int{1, 3}, 0),
		RetentionDays: 365, ColdAfterDays: 2,
	}
}

// PresetSmall is a small cluster for unit tests: 2 DCs, hot/warm/cold,
// two cold nodes per machine, 5 sources over 10 days.
func PresetSmall(version string) Spec {
	return Spec{
		Name: "small", Version: version, Seed: 42, DCs: []string{"dc1", "dc2"}, Masters: 3,
		Tiers: []TierSpec{
			{Tier: "hot", MachinesPerDC: 2, NodesPerMachine: 2, DiskPerNode: 2 * tb, Processors: 8},
			{Tier: "warm", MachinesPerDC: 1, NodesPerMachine: 2, DiskPerNode: 4 * tb, Processors: 4},
			{Tier: "cold", MachinesPerDC: 1, NodesPerMachine: 2, DiskPerNode: 8 * tb, Processors: 4},
		},
		Sources:       sources("logs", 5, 44, 2*gb, [2]int{1, 3}, 1),
		RetentionDays: 10, WarmAfterDays: 2, ColdAfterDays: 5,
		Remotes: []string{"other"},
	}
}
