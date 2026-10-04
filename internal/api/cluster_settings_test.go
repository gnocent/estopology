package api

import (
	"context"
	"strings"
	"testing"
)

// Saving a cluster from the form (no schedule, no hierarchy) keeps the ones
// already set; an explicit null resets them; invalid values are refused.
func TestClusterScheduleAndHierarchy(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	es := `"es":{"URLs":["https://es:9200"],"Auth":"none"}`
	sched := `"schedule":{"ProbeEvery":120000000000,"MinInterval":600000000000,"MaxInterval":1800000000000,"Jitter":0.2,"ShardsThreshold":5}`
	hier := `"hierarchy":[{"kind":"attr","attr":"dc"},{"kind":"tier"},{"kind":"node"}]`
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/clusters/c1", body: `{` + es + `,` + sched + `,` + hier + `}`}); rec.Code != 200 {
		t.Fatalf("save with schedule: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/clusters/c1", body: `{"name":"renamed",` + es + `}`}); rec.Code != 200 {
		t.Fatalf("save from the form: %d", rec.Code)
	}
	c, _ := e.st.Cluster(context.Background(), "c1")
	if !strings.Contains(string(c.Schedule), `"ShardsThreshold":5`) || !strings.Contains(string(c.Hierarchy), `"dc"`) || c.Name != "renamed" {
		t.Errorf("schedule or hierarchy lost: %s %s %s", c.Schedule, c.Hierarchy, c.Name)
	}
	e.do(call{method: "PUT", path: "/api/v1/admin/clusters/c1", body: `{` + es + `,"schedule":null}`})
	if c, _ := e.st.Cluster(context.Background(), "c1"); len(c.Schedule) != 0 || len(c.Hierarchy) == 0 {
		t.Errorf("explicit null: %s / %s", c.Schedule, c.Hierarchy)
	}
	for name, body := range map[string]string{
		"probe too fast":      `"schedule":{"ProbeEvery":1000000000}`,
		"max below min":       `"schedule":{"MinInterval":3600000000000,"MaxInterval":600000000000}`,
		"unknown field":       `"schedule":{"Probe":1}`,
		"attr without name":   `"hierarchy":[{"kind":"attr"}]`,
		"node not last":       `"hierarchy":[{"kind":"node"},{"kind":"tier"}]`,
		"unknown level":       `"hierarchy":[{"kind":"rack"}]`,
		"duplicate attribute": `"hierarchy":[{"kind":"attr","attr":"dc"},{"kind":"attr","attr":"dc"}]`,
	} {
		if rec := e.do(call{method: "PUT", path: "/api/v1/admin/clusters/c1", body: `{` + es + `,` + body + `}`}); rec.Code != 400 {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/collector", body: `{"parallelism":0}`}); rec.Code != 400 {
		t.Errorf("parallelism 0: %d", rec.Code)
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/collector", body: `{"parallelism":5}`}); rec.Code != 200 {
		t.Errorf("parallelism 5: %d", rec.Code)
	}
	if rec := e.do(call{method: "GET", path: "/api/v1/admin/collector"}); !strings.Contains(rec.Body.String(), `"parallelism":5`) {
		t.Errorf("parallelism: %s", rec.Body.String())
	}
}

// SPEC §5.5: a cluster whose identity changed can be handled as a new
// cluster; the old configuration is disabled and keeps its history.
func TestReattachAsNewCluster(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	ctx := context.Background()
	body := `{"name":"Prod","enabled":true,"machine_attr":"machine","es":{"URLs":["https://es:9200"],"Auth":"apikey","APIKeyFile":"/etc/estopo/k"}}`
	e.do(call{method: "PUT", path: "/api/v1/admin/clusters/prod", body: body})
	e.st.SetBound(ctx, "prod", "uuid-old")
	e.st.SetSuspended(ctx, "prod", true, "cluster_uuid changed from uuid-old to uuid-new")

	if rec := e.do(call{method: "POST", path: "/api/v1/admin/clusters/prod/reattach", body: `{"new_id":"prod"}`}); rec.Code != 400 {
		t.Errorf("existing id accepted: %d", rec.Code)
	}
	rec := e.do(call{method: "POST", path: "/api/v1/admin/clusters/prod/reattach", body: `{"new_id":"prod-2","name":"Prod (rebuilt)"}`})
	if rec.Code != 200 {
		t.Fatalf("reattach: %d %s", rec.Code, rec.Body.String())
	}
	nc, err := e.st.Cluster(ctx, "prod-2")
	if err != nil || !nc.Enabled || nc.Suspended || nc.BoundUUID != "" || nc.ES.APIKeyFile != "/etc/estopo/k" || nc.MachineAttr != "machine" || nc.Name != "Prod (rebuilt)" {
		t.Errorf("new cluster: %+v %v", nc, err)
	}
	old, _ := e.st.Cluster(ctx, "prod")
	if old.Enabled || old.BoundUUID != "uuid-old" {
		t.Errorf("old cluster: %+v", old)
	}
}

func TestCacheSetting(t *testing.T) {
	e := newAuthEnv(t, Transport{LoopbackOnly: true})
	if rec := e.do(call{method: "GET", path: "/api/v1/admin/cache"}); !strings.Contains(rec.Body.String(), `"structures":64`) {
		t.Errorf("default: %s", rec.Body.String())
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/cache", body: `{"structures":1}`}); rec.Code != 400 {
		t.Errorf("1 accepted: %d", rec.Code)
	}
	if rec := e.do(call{method: "PUT", path: "/api/v1/admin/cache", body: `{"structures":256}`}); rec.Code != 200 {
		t.Errorf("256: %d", rec.Code)
	}
	if rec := e.do(call{method: "GET", path: "/api/v1/admin/cache"}); !strings.Contains(rec.Body.String(), `"structures":256`) {
		t.Errorf("saved: %s", rec.Body.String())
	}
}
