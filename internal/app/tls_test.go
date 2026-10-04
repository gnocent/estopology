package app

import (
	"crypto/tls"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"estopo/internal/sim"
	"estopo/internal/testpki"
)

// SPEC §5.6: a global CA (file or directory) applies to the clusters that
// have none of their own, for the connection test and the collection.
func TestGlobalCA(t *testing.T) {
	pki, err := testpki.Generate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(pki.ServerCert, pki.ServerKey)
	if err != nil {
		t.Fatal(err)
	}
	c := sim.Build(sim.PresetSmall("8.19.22"), time.Now().UTC())
	es := httptest.NewUnstartedServer(&sim.Server{Cluster: c, Auth: sim.Auth{Disabled: true}})
	es.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	es.StartTLS()
	defer es.Close()
	url := strings.Replace(es.URL, "127.0.0.1", "localhost", 1)

	n := startNode(t, t.TempDir(), "", "")
	cfg := `{"URLs":["` + url + `"],"Auth":"none","Timeout":5000000000}`
	var res struct {
		Error       string `json:"error"`
		ClusterName string `json:"cluster_name"`
	}
	json.Unmarshal(n.do("POST", "/api/v1/admin/clusters/tls/test", cfg).Body.Bytes(), &res)
	if res.Error == "" {
		t.Fatalf("test CA trusted without configuration: %+v", res)
	}
	if rec := n.do("PUT", "/api/v1/admin/tls", `{"ca_path":"/nonexistent"}`); rec.Code != 400 {
		t.Errorf("missing CA path accepted: %d", rec.Code)
	}
	rec := n.do("PUT", "/api/v1/admin/tls", `{"ca_path":"`+pki.Dir+`"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"certificates":`) {
		t.Fatalf("global CA: %d %s", rec.Code, rec.Body.String())
	}
	res.Error, res.ClusterName = "", ""
	json.Unmarshal(n.do("POST", "/api/v1/admin/clusters/tls/test", cfg).Body.Bytes(), &res)
	if res.Error != "" || res.ClusterName != c.Name {
		t.Fatalf("connection test with the global CA: %+v", res)
	}
	body := `{"name":"TLS","enabled":true,"es":` + cfg + `}`
	if rec := n.do("PUT", "/api/v1/admin/clusters/tls", body); rec.Code != 200 {
		t.Fatalf("save cluster: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "collection over TLS with the global CA", func() bool {
		ts, _ := n.a.ingester.LastTs(t.Context(), "tls")
		return ts > 0
	})
}
