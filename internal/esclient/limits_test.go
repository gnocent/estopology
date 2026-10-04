package esclient

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ES answers 200 to _nodes and _nodes/stats when a node failed or timed
// out, and leaves it out: such an answer, or an empty one from a proxy, must
// not be read as "these nodes are gone".
func TestNodesAnswersMustBeComplete(t *testing.T) {
	for _, ver := range fixtureVersions(t) {
		for _, name := range []string{"nodes_stats_filtered", "nodes_stats", "nodes"} {
			raw, err := os.ReadFile(filepath.Join(fixturesDir, ver, name+".json"))
			if err != nil {
				t.Fatal(err)
			}
			const ok = `"_nodes":{"total":4,"successful":4,"failed":0}`
			if !bytes.Contains(raw, []byte(ok)) {
				t.Fatalf("%s/%s: no %s", ver, name, ok)
			}
			// What ES answers when es02 does not respond: 3 nodes and failed = 1.
			failed := bytes.Replace(raw, []byte(ok), []byte(`"_nodes":{"total":4,"successful":3,"failed":1,"failures":[{"type":"failed_node_exception"}]}`), 1)
			parse := func(b []byte) (int, error) {
				if name == "nodes" {
					v, err := ParseNodes(bytes.NewReader(b))
					return len(v), err
				}
				v, err := ParseNodesStats(bytes.NewReader(b))
				return len(v), err
			}
			if n, err := parse(raw); err != nil || n != 4 {
				t.Errorf("%s/%s: %d nodes, %v", ver, name, n, err)
			}
			if _, err := parse(failed); err == nil || !strings.Contains(err.Error(), "1 of 4 nodes did not answer") {
				t.Errorf("%s/%s with a failed node: %v", ver, name, err)
			}
		}
	}
	for _, empty := range []string{`{}`, `{"cluster_name":"x"}`, `{"_nodes":{"total":0,"successful":0,"failed":0},"nodes":{}}`} {
		if _, err := ParseNodesStats(strings.NewReader(empty)); err == nil {
			t.Errorf("nodes stats %s accepted", empty)
		}
		if _, err := ParseNodes(strings.NewReader(empty)); err == nil {
			t.Errorf("nodes %s accepted", empty)
		}
	}
	// The header must be asked for, or ES would not send it.
	for _, p := range []string{PathNodes, PathNodesStats} {
		if !strings.Contains(p, "filter_path=cluster_name,_nodes,") {
			t.Errorf("%s does not request _nodes", p)
		}
	}
}

// A response is read up to a limit, counted after decompression.
func TestResponseSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := `{"n":` + strings.Repeat(" ", 1<<20) + `1}`
		if r.URL.Path == "/gzip" { // 1 MiB of spaces: about 1 kB on the wire
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			gz.Write([]byte(body))
			gz.Close()
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()
	c, err := New(Config{URLs: []string{srv.URL}, Auth: AuthNone})
	if err != nil {
		t.Fatal(err)
	}
	if c.maxBody != MaxResponseBytes {
		t.Fatalf("default limit %d", c.maxBody)
	}
	readAll := func(r io.Reader) error { _, err := io.ReadAll(r); return err }
	for _, path := range []string{"/gzip", "/plain"} {
		c.maxBody = 2 << 20
		if st, err := c.Get(context.Background(), "x", path, nil, readAll); err != nil {
			t.Errorf("%s under the limit: %v %+v", path, err, st)
		}
		c.maxBody = 64 << 10
		st, err := c.Get(context.Background(), "x", path, nil, readAll)
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Errorf("%s over the limit: %v", path, err)
		}
		if st.Bytes > 1<<19 {
			t.Errorf("%s: %d bytes read past a limit of %d", path, st.Bytes, c.maxBody)
		}
		// Without a decoder too (HEAD, discarded bodies).
		if _, err := c.Get(context.Background(), "x", path, nil, nil); !errors.Is(err, ErrResponseTooLarge) {
			t.Errorf("%s discarded, over the limit: %v", path, err)
		}
	}
}

// A password or an API key is not sent over plain http, except to a
// loopback address.
func TestCredentialsNeedTLS(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	os.WriteFile(secret, []byte("id1:secret\n"), 0o600)
	for _, auth := range []Config{
		{Auth: AuthAPIKey, APIKeyFile: secret},
		{Auth: AuthBasic, Username: "u", PasswordFile: secret},
	} {
		for u, ok := range map[string]bool{
			"https://es.example.org:9200": true,
			"http://127.0.0.1:9200":       true,
			"http://localhost:9200":       true,
			"http://[::1]:9200":           true,
			"http://es.example.org:9200":  false,
			"http://10.0.0.5:9200":        false,
		} {
			cfg := auth
			cfg.URLs = []string{"https://es0.example.org:9200", u}
			_, err := New(cfg)
			if (err == nil) != ok {
				t.Errorf("%s to %s: %v", cfg.Auth, u, err)
			}
			if err != nil && !strings.Contains(err.Error(), "plain http") {
				t.Errorf("%s to %s: unclear error %v", cfg.Auth, u, err)
			}
		}
	}
	// Nothing secret travels without authentication or with a client certificate.
	if _, err := New(Config{URLs: []string{"http://es.example.org:9200"}, Auth: AuthNone}); err != nil {
		t.Errorf("no authentication over http: %v", err)
	}
}
