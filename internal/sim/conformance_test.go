package sim

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"estopo/internal/esclient"
)

const fixturesDir = "../../testdata/es"

var t0 = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

// fixtureLike rebuilds, in the simulator, the topology of the real test
// cluster of scripts/capture-fixtures.sh (same shapes: tiers, red index,
// relocation, closed and hidden indices, three remotes).
func fixtureLike(version string) *Cluster {
	c := New("estopo-fx", version, 7, t0)
	hotRoles := []string{"data_content", "data_hot", "ingest", "master", "remote_cluster_client"}
	c.AddNode("es01", "m1", "172.20.0.2", 0, hotRoles, map[string]string{"dc": "dc1"}, tb)
	c.AddNode("es02", "m2", "172.20.0.3", 0, hotRoles, map[string]string{"dc": "dc2"}, tb)
	c.AddNode("es03", "m3", "172.20.0.4", 0, []string{"master", "data_warm"}, map[string]string{"dc": "dc1"}, tb)
	c.AddNode("es04", "m3", "172.20.0.4", 1, []string{"data_cold"}, map[string]string{"dc": "dc1"}, tb)
	c.SetClusterSetting(false, "cluster.routing.allocation.awareness.attributes", "dc")
	c.SetClusterSetting(true, "cluster.routing.allocation.node_concurrent_recoveries", "2")
	c.AddRemote("ghost", "sniff", "ghost.invalid:9300", false, 0)
	c.AddRemote("prx", "proxy", "es02:9300", true, 18)
	c.AddRemote("self", "sniff", "es01:9300", true, 3)
	hot := map[string]string{tierPrefKey: "data_hot"}
	for _, d := range []string{"28", "29", "30"} {
		c.CreateIndex("applog-2026.09."+d, 2, 1, 200_000, map[string]string{tierPrefKey: "data_hot", "index.refresh_interval": "30s"})
	}
	c.CreateIndex("archive-2026.08", 1, 0, 1000, map[string]string{tierPrefKey: "data_warm", "index.priority": "10"})
	c.CreateIndex("cold-2026.07", 1, 0, 1000, map[string]string{tierPrefKey: "data_cold,data_warm"})
	c.CreateIndex("tight-tspn", 2, 1, 1000, map[string]string{tierPrefKey: "data_hot", "index.routing.allocation.total_shards_per_node": "1"})
	c.CreateIndex("overrep", 1, 2, 1000, hot)
	c.CreateIndex("red-require", 1, 0, 1000, map[string]string{tierPrefKey: "data_hot", "index.routing.allocation.require._name": "nope"})
	c.CreateIndex("closed-idx", 1, 0, 1000, hot)
	c.CreateIndex(".hidden-idx", 1, 0, 1000, map[string]string{tierPrefKey: "data_hot", "index.hidden": "true"})
	c.CreateIndex("big-reloc", 1, 0, 24<<20, map[string]string{tierPrefKey: "data_hot", "index.routing.allocation.require._name": "es01"})
	c.Place()
	c.CloseIndex("closed-idx")
	c.SetClusterSetting(true, "indices.recovery.max_bytes_per_sec", "100kb")
	c.SetIndexSetting("big-reloc", "index.routing.allocation.require._name", "es02")
	c.Reroute()
	return c
}

// schema mirrors scripts/fixture-schema.sh: the set of "path : type" of the
// scalar leaves, with ids, names and array positions collapsed.
func schema(t *testing.T, name string, data []byte) map[string]bool {
	t.Helper()
	var v any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	collapseTop := strings.HasPrefix(name, "index_settings") || strings.HasPrefix(name, "remote_info")
	out := map[string]bool{}
	var walk func(v any, p []string)
	walk = func(v any, p []string) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				seg := k
				last := ""
				if len(p) > 0 {
					last = p[len(p)-1]
				}
				switch {
				case len(p) == 0 && collapseTop:
					seg = "*"
				case last == "nodes" || last == "attributes" || last == "settings":
					seg = "*"
				case len(p) == 1 && (last == "persistent" || last == "transient" || last == "defaults"):
					seg = "*"
				}
				walk(child, append(append([]string(nil), p...), seg))
			}
		case []any:
			for _, child := range x {
				walk(child, append(append([]string(nil), p...), "[]"))
			}
		default:
			typ := "null"
			switch x.(type) {
			case string:
				typ = "string"
			case json.Number:
				typ = "number"
			case bool:
				typ = "boolean"
			}
			out[strings.Join(p, ".")+" : "+typ] = true
		}
	}
	walk(v, nil)
	return out
}

// extras are paths essim renders although the fixtures lack them.
var extras = []string{
	"nodes.*.fs.io_stats.", // empty on the test clusters, documented shape
}

func isExtra(p string) bool {
	for _, e := range extras {
		if strings.HasPrefix(p, e) {
			return true
		}
	}
	return false
}

func TestConformanceWithRealFixtures(t *testing.T) {
	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		ver := e.Name()
		t.Run(ver, func(t *testing.T) {
			c := fixtureLike(ver)
			srv := &Server{Cluster: c, Auth: Auth{Disabled: true}}
			for name, path := range map[string]string{
				"root":                      esclient.PathRoot,
				"health":                    esclient.PathHealth,
				"nodes_stats_filtered":      esclient.PathNodesStats,
				"nodes_filtered":            esclient.PathNodes,
				"cat_allocation":            esclient.PathAllocation,
				"cluster_settings":          esclient.PathClusterSettings,
				"cluster_settings_defaults": esclient.PathClusterSettingsDefaults,
				"remote_info":               esclient.PathRemoteInfo,
				"cat_indices":               esclient.PathCatIndices,
				"index_settings":            esclient.IndexSettingsPath(esclient.DefaultSettingsWhitelist),
				"cat_shards":                esclient.PathCatShards,
				"has_privileges":            esclient.PathHasPrivileges,
			} {
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				if rec.Code != 200 {
					t.Errorf("%s: status %d: %s", name, rec.Code, rec.Body.String())
					continue
				}
				real, err := os.ReadFile(filepath.Join(fixturesDir, ver, name+".json"))
				if err != nil {
					t.Fatal(err)
				}
				want, got := schema(t, name, real), schema(t, name, rec.Body.Bytes())
				t.Logf("%s: %d paths in the fixture, %d rendered", name, len(want), len(got))
				if name == "cluster_settings_defaults" {
					// Real clusters list hundreds of defaults; only the ones
					// estopo reads must be present.
					for _, k := range []string{"defaults.* : string", "persistent.* : string", "transient.* : string"} {
						if !got[k] {
							t.Errorf("%s: missing %s", name, k)
						}
					}
					continue
				}
				var missing, unexpected []string
				for p := range want {
					if !got[p] {
						missing = append(missing, p)
					}
				}
				for p := range got {
					if !want[p] && !isExtra(p) {
						unexpected = append(unexpected, p)
					}
				}
				sort.Strings(missing)
				sort.Strings(unexpected)
				if len(missing)+len(unexpected) > 0 {
					t.Errorf("%s: schema differs from ES %s\n  missing:    %v\n  unexpected: %v", name, ver, missing, unexpected)
				}
			}
		})
	}
}
