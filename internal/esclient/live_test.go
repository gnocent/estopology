package esclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestLive runs every client call against a real cluster started by
// scripts/capture-fixtures.sh (which sets the variables below), once per
// authentication method. Skipped otherwise.
func TestLive(t *testing.T) {
	url := os.Getenv("ESTOPO_LIVE_URL")
	pki := os.Getenv("ESTOPO_LIVE_PKI")
	if url == "" || pki == "" {
		t.Skip("ESTOPO_LIVE_URL / ESTOPO_LIVE_PKI not set")
	}
	dir := t.TempDir()
	apiKeyFile := filepath.Join(dir, "apikey")
	os.WriteFile(apiKeyFile, []byte(os.Getenv("ESTOPO_LIVE_APIKEY")), 0o600)
	pwFile := filepath.Join(dir, "pw")
	os.WriteFile(pwFile, []byte(os.Getenv("ESTOPO_LIVE_PASSWORD")), 0o600)

	base := Config{URLs: []string{url}, CAPath: filepath.Join(pki, "root.crt")}
	auths := map[string]Config{}
	cert := base
	cert.Auth, cert.CertFile, cert.KeyFile = AuthCert, filepath.Join(pki, "client.crt"), filepath.Join(pki, "client.key")
	auths["cert"] = cert
	basic := base
	basic.Auth, basic.Username, basic.PasswordFile = AuthBasic, "estopo_basic", pwFile
	auths["basic"] = basic
	key := base
	key.Auth, key.APIKeyFile = AuthAPIKey, apiKeyFile
	auths["apikey"] = key

	ctx := context.Background()
	for name, cfg := range auths {
		t.Run(name, func(t *testing.T) {
			c, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			check := func(st CallStat, err error) {
				t.Helper()
				if err != nil || st.Status != 200 || st.Bytes == 0 {
					t.Errorf("%s: %+v %v", st.Name, st, err)
				}
			}
			root, st, err := c.Root(ctx)
			check(st, err)
			if root.ClusterUUID == "" {
				t.Error("no cluster uuid")
			}
			_, st, err = c.Health(ctx)
			check(st, err)
			ns, st, err := c.NodesStats(ctx)
			check(st, err)
			n, st, err := c.Nodes(ctx)
			check(st, err)
			if len(ns) == 0 || len(ns) != len(n) {
				t.Errorf("nodes: %d stats, %d infos", len(ns), len(n))
			}
			_, st, err = c.Allocation(ctx)
			check(st, err)
			_, st, err = c.ClusterSettings(ctx, false)
			check(st, err)
			cs, st, err := c.ClusterSettings(ctx, true)
			check(st, err)
			if cs.Defaults["cluster.routing.allocation.disk.watermark.high"] == "" {
				t.Error("no watermark default")
			}
			rem, st, err := c.RemoteInfo(ctx)
			check(st, err)
			if len(rem) != 3 {
				t.Errorf("remotes: %+v", rem)
			}
			var indices, shards, settings int
			check(c.CatIndices(ctx, func(CatIndex) error { indices++; return nil }))
			check(c.CatShards(ctx, func(CatShard) error { shards++; return nil }))
			check(c.IndexSettings(ctx, DefaultSettingsWhitelist, func(string, map[string]string) error { settings++; return nil }))
			if indices == 0 || shards == 0 || settings != indices {
				t.Errorf("indices=%d shards=%d settings=%d", indices, shards, settings)
			}
			p, st, err := c.HasPrivileges(ctx)
			check(st, err)
			if !p.HasAllRequested {
				t.Errorf("missing privileges: %v", p.Missing)
			}
			t.Logf("%s: last call %s %d ms, %d bytes (gzip=%v)", name, st.Name, st.DurationMs, st.WireBytes, st.Gzip)
		})
	}
}
