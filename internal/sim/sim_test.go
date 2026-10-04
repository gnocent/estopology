package sim

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/esclient"
	"estopo/internal/testpki"
)

func counts(c *Cluster) (total, started, unassigned, relocating int) {
	for _, ix := range c.Indices {
		for _, sh := range ix.Shards {
			for _, cp := range sh {
				total++
				switch cp.State {
				case Started:
					started++
				case Unassigned:
					unassigned++
				case Relocating:
					relocating++
				}
			}
		}
	}
	return
}

// checkInvariants verifies placement rules that must always hold.
func checkInvariants(t *testing.T, c *Cluster) {
	t.Helper()
	seen := map[string]bool{}
	for _, ix := range c.Indices {
		if seen[ix.Name] || c.indexBy[ix.Name] != ix {
			t.Fatalf("index list inconsistent at %s", ix.Name)
		}
		seen[ix.Name] = true
	}
	if len(seen) != len(c.indexBy) {
		t.Fatalf("%d indices listed, %d by name", len(seen), len(c.indexBy))
	}
	reserved := map[*Node]int64{}
	relocating, unassigned := 0, 0
	for _, ix := range c.Indices {
		for _, sh := range ix.Shards {
			for _, cp := range sh {
				if cp.Node != nil {
					reserved[cp.Node] += ix.ShardBytes
				}
				if cp.Target != nil {
					reserved[cp.Target] += ix.ShardBytes
					relocating++
				}
				if cp.State == Unassigned {
					unassigned++
					if _, ok := c.unassigned[cp]; !ok {
						t.Fatalf("%s[%d] unassigned but not tracked", ix.Name, cp.Shard)
					}
				}
			}
		}
	}
	if unassigned != len(c.unassigned) || relocating != c.relocating {
		t.Fatalf("bookkeeping: unassigned %d vs %d tracked, relocating %d vs %d counted", unassigned, len(c.unassigned), relocating, c.relocating)
	}
	for _, n := range c.Nodes {
		if n.reserved != reserved[n] {
			t.Fatalf("%s: reserved %d, actual %d", n.Name, n.reserved, reserved[n])
		}
	}
	for _, ix := range c.Indices {
		for s, sh := range ix.Shards {
			primaries, nodes, dcs := 0, map[*Node]bool{}, map[string]int{}
			for _, cp := range sh {
				if cp.Primary {
					primaries++
				}
				if cp.Node == nil {
					continue
				}
				if !cp.Node.Up {
					t.Fatalf("%s[%d] on down node %s", ix.Name, s, cp.Node.Name)
				}
				if nodes[cp.Node] {
					t.Fatalf("%s[%d]: two copies on %s", ix.Name, s, cp.Node.Name)
				}
				nodes[cp.Node] = true
				if _, ok := cp.Node.copies[cp]; !ok {
					t.Fatalf("%s[%d]: node %s does not track its copy", ix.Name, s, cp.Node.Name)
				}
				dcs[cp.Node.Attrs["dc"]]++
			}
			if primaries != 1 {
				t.Fatalf("%s[%d]: %d primaries", ix.Name, s, primaries)
			}
			for dc, n := range dcs {
				if n > 1 && len(sh) <= 2 {
					t.Fatalf("%s[%d]: awareness violated, %d copies in %s", ix.Name, s, n, dc)
				}
			}
		}
	}
}

func TestBuildSmallIsGreenAndTiered(t *testing.T) {
	c := Build(PresetSmall("8.19.22"), t0)
	total, started, unassigned, _ := counts(c)
	if total == 0 || started != total || unassigned != 0 {
		t.Fatalf("total=%d started=%d unassigned=%d", total, started, unassigned)
	}
	checkInvariants(t, c)
	for _, ix := range c.Indices {
		age := c.lifecycleAge(ix)
		want := "data_hot"
		switch {
		case age >= 5*dayMs:
			want = "data_cold"
		case age >= 2*dayMs:
			want = "data_warm"
		}
		for _, sh := range ix.Shards {
			for _, cp := range sh {
				if !cp.Node.HasRole(want) {
					t.Errorf("%s (age %dd) on %s, want %s", ix.Name, age/dayMs, cp.Node.Name, want)
				}
			}
		}
	}
	if h := c.health(); h.status != "green" {
		t.Errorf("health %s", h.status)
	}
}

func TestDeterministic(t *testing.T) {
	render := func() string {
		c := Build(PresetSmall("9.5.4"), t0)
		c.AdvanceTo(t0.Add(26 * time.Hour))
		var b bytes.Buffer
		c.renderCatShards(&b, strings.Split("index,shard,prirep,state,id,node", ","))
		c.renderCatIndices(&b, []string{"uuid", "index"})
		return b.String()
	}
	if a, b := render(), render(); a != b {
		t.Error("two runs with the same seed differ")
	}
}

func TestDaysOfEvolution(t *testing.T) {
	c := Build(PresetSmall("8.19.22"), t0)
	n0 := len(c.Indices)
	sawRelocation := false
	for h := 1; h <= 72; h++ {
		c.AdvanceTo(t0.Add(time.Duration(h) * time.Hour))
		if _, _, _, r := counts(c); r > 0 {
			sawRelocation = true
		}
		checkInvariants(t, c)
	}
	if !sawRelocation {
		t.Error("no relocation in 3 days (tier moves expected)")
	}
	rollovers := 0
	for _, ix := range c.Indices {
		if ix.Rollover {
			rollovers++
		}
	}
	if rollovers == 0 {
		t.Error("no rollover index")
	}
	// Retention keeps the index count stable (±1 day of rollover indices).
	if n := len(c.Indices); n < n0-2 || n > n0+2 {
		t.Errorf("indices %d -> %d", n0, n)
	}
	c.AdvanceTo(t0.Add(80 * time.Hour))
	if h := c.health(); h.status != "green" {
		_, _, un, rel := counts(c)
		t.Errorf("health %s (unassigned %d, relocating %d)", h.status, un, rel)
	}
	for _, ix := range c.Indices {
		if ix.Source != "" && c.lifecycleAge(ix) >= 10*dayMs {
			t.Errorf("%s past retention", ix.Name)
		}
	}
}

func TestNodeLeaveAndReturn(t *testing.T) {
	c := Build(PresetSmall("8.19.22"), t0)
	var victim *Node
	for _, n := range c.Nodes {
		if n.HasRole("data_hot") && len(n.copies) > 0 {
			victim = n
			break
		}
	}
	held := len(victim.copies)
	var primaries int
	for cp := range victim.copies {
		if cp.Primary {
			primaries++
		}
	}
	c.Do(func(c *Cluster) { c.NodeLeave(victim.Name) })
	h := c.health()
	if h.unassigned != held || h.delayed != held || h.status != "yellow" {
		t.Fatalf("after leave: %+v (held %d)", h, held)
	}
	// Every lost primary was replaced by a promoted replica.
	if h.unassignedPrimary != 0 {
		t.Errorf("unassigned primaries %d (lost %d primaries with replicas)", h.unassignedPrimary, primaries)
	}
	for _, ix := range c.Indices {
		for _, sh := range ix.Shards {
			for _, cp := range sh {
				if cp.State == Unassigned && (cp.UReason != "NODE_LEFT" || !strings.Contains(cp.UDetails, victim.ID)) {
					t.Fatalf("unassigned copy %+v", cp)
				}
			}
		}
	}
	// Back within the delayed timeout (1m): copies recover on the same node.
	c.AdvanceTo(t0.Add(30 * time.Second))
	c.Do(func(c *Cluster) { c.NodeJoin(victim.Name) })
	// Checked before the first rebalancing tick (t0+5m), which may move copies.
	c.AdvanceTo(t0.Add(time.Minute))
	if h := c.health(); h.status != "green" || len(victim.copies) != held {
		t.Errorf("after return: %+v, %d copies back of %d", h, len(victim.copies), held)
	}
	checkInvariants(t, c)
}

func TestNodeLossReallocatesAfterDelay(t *testing.T) {
	c := Build(PresetSmall("8.19.22"), t0)
	var victim *Node
	for _, n := range c.Nodes {
		if n.HasRole("data_hot") && len(n.copies) > 0 {
			victim = n
			break
		}
	}
	c.Do(func(c *Cluster) { c.NodeLeave(victim.Name) })
	c.AdvanceTo(t0.Add(2 * time.Hour))
	if h := c.health(); h.status != "green" {
		t.Errorf("replicas not reallocated after the delay: %+v", h)
	}
	checkInvariants(t, c)
}

func TestNoMasterAnswers503(t *testing.T) {
	c := Build(PresetSmall("8.19.22"), t0)
	c.Do(func(c *Cluster) {
		for _, n := range c.Nodes {
			if n.IsMaster() {
				c.NodeLeave(n.Name)
			}
		}
	})
	srv := &Server{Cluster: c, Auth: Auth{Disabled: true}}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_cluster/health", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "master_not_discovered_exception") {
		t.Errorf("status %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"_na_"`) {
		t.Errorf("root without master: %d %s", rec.Code, rec.Body.String())
	}
}

// The real client talks to essim over mTLS, basic auth and API key, and
// everything it reads is consistent.
func TestClientAgainstSimulator(t *testing.T) {
	pki, err := testpki.Generate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tc, err := ServerTLS(pki)
	if err != nil {
		t.Fatal(err)
	}
	for _, ver := range []string{"7.17.29", "8.19.22", "9.5.4"} {
		t.Run(ver, func(t *testing.T) {
			clk := clock.NewManual(t0)
			c := Build(PresetSmall(ver), t0)
			s := httptest.NewUnstartedServer(&Server{Cluster: c, Clock: clk,
				Auth: Auth{Users: map[string]string{"estopo": "pw"}, APIKeys: map[string]bool{"a2V5OnNlY3JldA==": true}}})
			s.TLS = tc
			s.StartTLS()
			defer s.Close()

			dir := t.TempDir()
			pw := filepath.Join(dir, "pw")
			os.WriteFile(pw, []byte("pw"), 0o600)
			key := filepath.Join(dir, "key")
			os.WriteFile(key, []byte("key:secret"), 0o600)
			base := esclient.Config{URLs: []string{s.URL}, CAPath: pki.RootCert}
			cfgs := map[string]esclient.Config{}
			cert := base
			cert.Auth, cert.CertFile, cert.KeyFile = esclient.AuthCert, pki.ClientCert, pki.ClientKey
			cfgs["cert"] = cert
			basic := base
			basic.Auth, basic.Username, basic.PasswordFile = esclient.AuthBasic, "estopo", pw
			cfgs["basic"] = basic
			ak := base
			ak.Auth, ak.APIKeyFile = esclient.AuthAPIKey, key
			cfgs["apikey"] = ak
			none := base
			none.Auth = esclient.AuthNone
			if cl, err := esclient.New(none); err != nil {
				t.Fatal(err)
			} else if _, _, err := cl.Root(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
				t.Errorf("unauthenticated call: %v", err)
			}

			for name, cfg := range cfgs {
				cl, err := esclient.New(cfg)
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.Background()
				root, _, err := cl.Root(ctx)
				if err != nil || root.Version != ver || root.ClusterUUID != c.UUID {
					t.Fatalf("%s root: %+v %v", name, root, err)
				}
				h, _, err := cl.Health(ctx)
				if err != nil || h.Status != "green" || h.Nodes != len(c.Nodes) {
					t.Errorf("%s health: %+v %v", name, h, err)
				}
				ns, _, err := cl.NodesStats(ctx)
				if err != nil || len(ns) != len(c.Nodes) || !ns[0].HasIO {
					t.Errorf("%s nodes stats: %d %v", name, len(ns), err)
				}
				ni, _, err := cl.Nodes(ctx)
				if err != nil || len(ni) != len(c.Nodes) {
					t.Errorf("%s nodes: %v", name, err)
				}
				alloc, _, err := cl.Allocation(ctx)
				if err != nil || len(alloc) == 0 {
					t.Errorf("%s allocation: %v", name, err)
				}
				cs, _, err := cl.ClusterSettings(ctx, true)
				if err != nil || cs.Persistent["cluster.routing.allocation.awareness.attributes"] != "dc" ||
					cs.Defaults["cluster.routing.allocation.disk.watermark.high"] != "90%" {
					t.Errorf("%s settings: %+v %v", name, cs, err)
				}
				rem, _, err := cl.RemoteInfo(ctx)
				if err != nil || len(rem) != 1 || !rem[0].Connected {
					t.Errorf("%s remotes: %+v %v", name, rem, err)
				}
				uuids := map[string]bool{}
				if _, err := cl.CatIndices(ctx, func(ci esclient.CatIndex) error { uuids[ci.UUID] = true; return nil }); err != nil || len(uuids) != len(c.Indices) {
					t.Errorf("%s indices: %d %v", name, len(uuids), err)
				}
				total, _, _, _ := counts(c)
				shards := 0
				if _, err := cl.CatShards(ctx, func(s esclient.CatShard) error { shards++; return nil }); err != nil || shards != total {
					t.Errorf("%s shards: %d/%d %v", name, shards, total, err)
				}
				settings := 0
				if _, err := cl.IndexSettings(ctx, esclient.DefaultSettingsWhitelist, func(ix string, s map[string]string) error {
					settings++
					if s[tierPrefKey] == "" || s["index.number_of_shards"] == "" {
						t.Errorf("%s: settings of %s: %v", name, ix, s)
					}
					return nil
				}); err != nil || settings != len(c.Indices) {
					t.Errorf("%s settings: %d %v", name, settings, err)
				}
				p, _, err := cl.HasPrivileges(ctx)
				if err != nil || !p.HasAllRequested {
					t.Errorf("%s privileges: %v", name, err)
				}
			}

			// Time flows with the injected clock: after a day, new indices exist.
			before := len(c.Indices)
			clk.Advance(24 * time.Hour)
			cl, _ := esclient.New(cfgs["cert"])
			n := 0
			cl.CatIndices(context.Background(), func(esclient.CatIndex) error { n++; return nil })
			if n == before && c.Now().Sub(t0) < 24*time.Hour {
				t.Errorf("cluster did not follow the clock")
			}
		})
	}
}

func TestUnknownColumnAndMethod(t *testing.T) {
	c := fixtureLike("8.19.22")
	srv := &Server{Cluster: c, Auth: Auth{Disabled: true}}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_cat/shards?format=json&h=index,nope", nil))
	if rec.Code != 400 {
		t.Errorf("unknown column: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/idx", nil))
	if rec.Code != 405 {
		t.Errorf("PUT: %d", rec.Code)
	}
	io.Copy(io.Discard, rec.Body)
}
