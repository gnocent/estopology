package esclient

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures are real answers of ES 7.17 / 8.x / 9.x recorded by
// scripts/capture-fixtures.sh on a known topology: 4 nodes (es01, es02 hot in
// dc1/dc2; es03 warm and es04 cold on the same machine m3), a red index, a
// relocation in progress, three CCS remotes, then es02 stopped (*_nodeleft).

const fixturesDir = "../../testdata/es"

func fixtureVersions(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	if len(out) < 3 {
		t.Fatalf("expected fixtures for 3 ES versions, found %v", out)
	}
	return out
}

func open(t *testing.T, ver, name string) *os.File {
	t.Helper()
	f, err := os.Open(filepath.Join(fixturesDir, ver, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestFixtures(t *testing.T) {
	for _, ver := range fixtureVersions(t) {
		t.Run(ver, func(t *testing.T) {
			major7 := strings.HasPrefix(ver, "7.")

			root, err := ParseRoot(open(t, ver, "root"))
			if err != nil || root.ClusterName != "estopo-fx" || root.Version != ver || root.ClusterUUID == "" {
				t.Errorf("root: %+v %v", root, err)
			}

			h, err := ParseHealth(open(t, ver, "health"))
			if err != nil || h.Status != "red" || h.Nodes != 4 || h.DataNodes != 4 || h.Relocating != 1 || h.Unassigned < 5 {
				t.Errorf("health: %+v %v", h, err)
			}
			if (h.UnassignedPrimary == Missing) != major7 {
				t.Errorf("unassigned_primary_shards presence: %d", h.UnassignedPrimary)
			}
			hl, err := ParseHealth(open(t, ver, "health_nodeleft"))
			if err != nil || hl.Nodes != 3 || hl.DelayedUnassigned == 0 {
				t.Errorf("health after node left: %+v %v", hl, err)
			}

			stats, err := ParseNodesStats(open(t, ver, "nodes_stats_filtered"))
			if err != nil || len(stats) != 4 {
				t.Fatalf("nodes stats: %d %v", len(stats), err)
			}
			for i, s := range stats {
				if want := []string{"es01", "es02", "es03", "es04"}[i]; s.Name != want {
					t.Errorf("stats[%d] = %s, want %s", i, s.Name, want)
				}
				if s.ID == "" || s.ProcCPU == Missing || s.OSCPU == Missing || s.HeapPct <= 0 || s.UptimeMs <= 0 ||
					s.FSTotal <= 0 || s.Load1 == Missing || s.CgroupCPUUsageNanos <= 0 || s.TimestampMs <= 0 {
					t.Errorf("incomplete stats: %+v", s)
				}
				if s.HasIO { // the test containers expose no block device (io_stats is {})
					t.Errorf("unexpected io stats: %+v", s)
				}
			}
			if full, err := ParseNodesStats(open(t, ver, "nodes_stats")); err != nil || len(full) != 4 {
				t.Errorf("unfiltered nodes stats: %d %v", len(full), err)
			}

			nodes, err := ParseNodes(open(t, ver, "nodes_filtered"))
			if err != nil || len(nodes) != 4 {
				t.Fatalf("nodes: %d %v", len(nodes), err)
			}
			for _, n := range nodes {
				if n.Version != ver || n.HTTPAddress == "" || n.TransportAddress == "" || n.JVMVersion == "" ||
					n.JVMStartMs <= 0 || n.OSKernel == "" || n.OSPrettyName == "" || n.Attributes["dc"] == "" {
					t.Errorf("incomplete node: %+v", n)
				}
			}
			if nodes[2].Attributes["machine"] != "m3" || nodes[3].Attributes["machine"] != "m3" || nodes[3].Roles[0] != "data_cold" {
				t.Errorf("es03/es04: %+v %+v", nodes[2], nodes[3])
			}

			alloc, err := ParseAllocation(open(t, ver, "cat_allocation"))
			if err != nil || len(alloc) != 5 {
				t.Fatalf("allocation: %d %v", len(alloc), err)
			}
			for _, a := range alloc {
				if a.Node == "UNASSIGNED" {
					if a.Shards < 5 || a.DiskTotal != Missing {
						t.Errorf("unassigned row: %+v", a)
					}
				} else if a.DiskTotal <= 0 || a.DiskPercent == Missing || a.Shards <= 0 {
					t.Errorf("allocation row: %+v", a)
				}
			}

			cs, err := ParseClusterSettings(open(t, ver, "cluster_settings"))
			if err != nil || cs.Persistent["cluster.routing.allocation.awareness.attributes"] != "dc" ||
				cs.Persistent["cluster.remote.self.seeds"] != `["es01:9300"]` ||
				cs.Transient["indices.recovery.max_bytes_per_sec"] != "100kb" {
				t.Errorf("cluster settings: %+v %v", cs, err)
			}
			csd, err := ParseClusterSettings(open(t, ver, "cluster_settings_defaults"))
			if err != nil || csd.Defaults["cluster.routing.allocation.disk.watermark.high"] == "" ||
				csd.Defaults["cluster.routing.allocation.same_shard.host"] != "false" {
				t.Errorf("default settings: %v", err)
			}

			remotes, err := ParseRemoteInfo(open(t, ver, "remote_info"))
			if err != nil || len(remotes) != 3 {
				t.Fatalf("remotes: %+v %v", remotes, err)
			}
			if g := remotes[0]; g.Alias != "ghost" || g.Connected || g.Mode != "sniff" {
				t.Errorf("ghost: %+v", g)
			}
			if p := remotes[1]; p.Alias != "prx" || !p.Connected || p.Mode != "proxy" || p.ProxyAddress != "es02:9300" || p.NodesConnected <= 0 {
				t.Errorf("prx: %+v", p)
			}
			if s := remotes[2]; s.Alias != "self" || !s.Connected || s.NodesConnected <= 0 || s.Seeds[0] != "es01:9300" {
				t.Errorf("self: %+v", s)
			}

			indices := map[string]CatIndex{}
			if err := DecodeCatIndices(open(t, ver, "cat_indices"), func(ci CatIndex) error {
				indices[ci.Name] = ci
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if ci := indices["applog-2026.09.28"]; ci.Docs != 2000 || ci.Pri != 2 || ci.Rep != 1 || ci.UUID == "" || ci.CreationDateMs <= 0 || ci.StoreBytes <= 0 {
				t.Errorf("applog: %+v", ci)
			}
			if ci := indices["closed-idx"]; ci.Status != "close" || ci.Docs != Missing {
				t.Errorf("closed index: %+v", ci)
			}
			if ci := indices["red-require"]; ci.Health != "red" {
				t.Errorf("red index: %+v", ci)
			}
			if _, ok := indices[".hidden-idx"]; !ok {
				t.Error("hidden index missing")
			}

			settings := map[string]map[string]string{}
			if err := DecodeIndexSettings(open(t, ver, "index_settings"), func(name string, s map[string]string) error {
				settings[name] = s
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			for name, ci := range indices {
				s, ok := settings[name]
				if !ok {
					t.Errorf("no settings for %s", name)
				} else if s["index.uuid"] != ci.UUID {
					t.Errorf("%s: uuid %q vs %q", name, s["index.uuid"], ci.UUID)
				}
			}
			for name, want := range map[string][2]string{
				"tight-tspn":        {"index.routing.allocation.total_shards_per_node", "1"},
				"applog-2026.09.28": {"index.blocks.write", "true"},
				"red-require":       {"index.routing.allocation.require._name", "nope"},
				"exclude-es01":      {"index.unassigned.node_left.delayed_timeout", "5m"},
				"cold-2026.07":      {"index.routing.allocation.include._tier_preference", "data_cold,data_warm"},
				"archive-2026.08":   {"index.priority", "10"},
			} {
				if got := settings[name][want[0]]; got != want[1] {
					t.Errorf("%s %s = %q, want %q", name, want[0], got, want[1])
				}
			}

			var reloc, redPrimary int
			if err := DecodeCatShards(open(t, ver, "cat_shards"), func(s CatShard) error {
				if _, ok := indices[s.Index]; !ok {
					t.Errorf("shard of unknown index %s", s.Index)
				}
				switch {
				case s.State == "RELOCATING":
					reloc++
					if s.Index != "big-reloc" || s.Node != "es01" || s.RelocTargetNode != "es02" || s.RelocTargetID == "" || s.RelocTargetIP == "" || !s.Primary {
						t.Errorf("relocation: %+v", s)
					}
				case s.Index == "red-require":
					redPrimary++
					if s.State != "UNASSIGNED" || s.UnassignedReason != "INDEX_CREATED" || s.UnassignedAtMs <= 0 || s.Node != "" || s.Docs != Missing {
						t.Errorf("red primary: %+v", s)
					}
				case s.State == "STARTED":
					// docs/store may be null even when STARTED (closed or fresh index, seen on 7.17).
					if s.Node == "" || s.NodeID == "" || s.StoreBytes < Missing {
						t.Errorf("started shard: %+v", s)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if reloc != 1 || redPrimary != 1 {
				t.Errorf("relocating=%d red primaries=%d", reloc, redPrimary)
			}

			var nodeLeft int
			if err := DecodeCatShards(open(t, ver, "cat_shards_nodeleft"), func(s CatShard) error {
				if s.UnassignedReason == "NODE_LEFT" {
					nodeLeft++
					if !strings.Contains(s.UnassignedDetails, "node_left") || s.UnassignedAtMs <= 0 {
						t.Errorf("node left shard: %+v", s)
					}
				}
				if s.Node == "es02" {
					t.Errorf("shard still on stopped node: %+v", s)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if nodeLeft == 0 {
				t.Error("no NODE_LEFT shard after stopping es02")
			}

			p, err := ParsePrivileges(open(t, ver, "has_privileges"))
			if err != nil || !p.HasAllRequested || len(p.Missing) != 0 || p.Username != "estopo" {
				t.Errorf("privileges: %+v %v", p, err)
			}
		})
	}
}

func TestParsePrivilegesMissing(t *testing.T) {
	p, err := ParsePrivileges(strings.NewReader(`{"username":"u","has_all_requested":false,
		"cluster":{"monitor":false},"index":{"*":{"monitor":true,"view_index_metadata":false}}}`))
	if err != nil || p.HasAllRequested || strings.Join(p.Missing, ",") != "cluster:monitor,index:*:view_index_metadata" {
		t.Errorf("%+v %v", p, err)
	}
}

func TestRelocatingNodeWithSpaces(t *testing.T) {
	var got CatShard
	err := DecodeCatShards(strings.NewReader(`[{"index":"i","shard":"0","prirep":"r","state":"RELOCATING",
		"docs":"1","store":"10","id":"A","node":"src node -> 10.0.0.2 B dst node"}]`), func(s CatShard) error { got = s; return nil })
	if err != nil || got.Node != "src node" || got.RelocTargetNode != "dst node" || got.RelocTargetID != "B" || got.Primary {
		t.Errorf("%+v %v", got, err)
	}
}

func TestIndexSettingsPath(t *testing.T) {
	want := "/_all/_settings/index.number_of_shards,index.number_of_replicas,index.auto_expand_replicas,index.routing.allocation.*," +
		"index.routing.rebalance.enable,index.blocks.*,index.unassigned.node_left.delayed_timeout,index.priority,index.refresh_interval," +
		"index.translog.durability,index.lifecycle.name,index.version.created,index.creation_date,index.uuid,index.hidden" +
		"?flat_settings=true&expand_wildcards=all"
	if got := IndexSettingsPath(DefaultSettingsWhitelist); got != want {
		t.Errorf("path mismatch with the captured request:\n got %s\nwant %s", got, want)
	}
}
