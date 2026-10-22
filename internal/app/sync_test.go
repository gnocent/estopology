package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/esclient"
	"estopo/internal/sim"
	"estopo/internal/store"
)

// collectEnv is an instance collecting essim on a manual clock.
type collectEnv struct {
	t   *testing.T
	ctx context.Context
	a   *App
	clk *clock.Manual
	c   *sim.Cluster
	url string
}

func newCollectEnv(t *testing.T) *collectEnv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	clk := clock.NewManual(pt0)
	c := sim.Build(sim.PresetSmall("8.19.22"), pt0)
	es := httptest.NewServer(&sim.Server{Cluster: c, Clock: clk, Auth: sim.Auth{Disabled: true}})
	a, err := New(ctx, Options{DataDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Clock: clk, Collect: true})
	if err != nil {
		cancel()
		es.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		a.Close()
		es.Close()
	})
	return &collectEnv{t: t, ctx: ctx, a: a, clk: clk, c: c, url: es.URL}
}

// save writes the configuration of a cluster collected from essim.
func (e *collectEnv) save(id, name string, enabled bool) {
	e.t.Helper()
	err := e.a.Store().SaveCluster(e.ctx, store.Cluster{ID: id, Name: name, Enabled: enabled,
		ES: esclient.Config{URLs: []string{e.url}, Auth: esclient.AuthNone, Timeout: 2 * time.Second}})
	if err != nil {
		e.t.Fatal(err)
	}
}

func (e *collectEnv) cluster(id string) store.Cluster {
	e.t.Helper()
	c, err := e.a.Store().Cluster(e.ctx, id)
	if err != nil {
		e.t.Fatal(err)
	}
	return c
}

// sync runs SyncClusters, failing the test if it does not return.
func (e *collectEnv) sync() error {
	e.t.Helper()
	done := make(chan error, 1)
	go func() { done <- e.a.SyncClusters(e.ctx) }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		e.t.Fatal("SyncClusters does not return")
		return nil
	}
}

// A worker stopped while it waits for room in the ingestion queue gives up:
// stopping it does not wait for the ingestion, and SyncClusters does not
// hold the lock the ingestion needs (machine attribute) meanwhile.
func TestSyncClustersDoesNotWaitForTheIngestion(t *testing.T) {
	e := newCollectEnv(t)
	for i := 0; i < cap(e.a.pipeline.ch); i++ { // the ingestion is not started: the queue stays full
		e.a.pipeline.ch <- queued{}
	}
	e.save("small", "Small", true)
	if err := e.sync(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the worker waits for the queue", func() bool { return e.a.pipeline.pending.Load() == 1 })

	e.save("small", "Small", false)
	attr := make(chan struct{})
	go func() {
		<-time.After(100 * time.Millisecond)
		e.a.machineAttr("small")
		close(attr)
	}()
	if err := e.sync(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-attr:
	case <-time.After(10 * time.Second):
		t.Fatal("the machine attribute is blocked")
	}
	if st := e.a.collector.Status(); len(st) != 0 {
		t.Errorf("worker still running: %+v", st)
	}
	if n := e.a.pipeline.pending.Load(); n != 0 {
		t.Errorf("%d records pending after the worker gave up", n)
	}
}

// A cluster suspended on an identity change can still be reconfigured,
// resumed and disabled.
func TestSuspendedClusterStaysManageable(t *testing.T) {
	e := newCollectEnv(t)
	e.save("small", "Small", true)
	if err := e.a.Start(e.ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "bound", func() bool { return e.cluster("small").BoundUUID == e.c.UUID })
	e.c.Do(func(c *sim.Cluster) { c.ChangeUUID() })
	waitFor(t, "suspended", func() bool {
		e.clk.Advance(5 * time.Minute)
		return e.cluster("small").Suspended
	})

	e.save("small", "Small renamed", true)
	if err := e.sync(); err != nil {
		t.Fatalf("reconfigure a suspended cluster: %v", err)
	}
	st := e.a.collector.Status()
	if len(st) != 1 || st[0].Name != "Small renamed" || !st[0].Suspended {
		t.Fatalf("after reconfiguration: %+v", st)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/clusters/small/resume?accept="+e.c.UUID, nil)
	req.Header.Set("X-Requested-With", "estopo")
	e.a.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "full capture after the resume", func() bool {
		st := e.a.collector.Status()
		return len(st) == 1 && !st[0].Suspended && st[0].LastFull != nil
	})
	if err := e.sync(); err != nil {
		t.Fatal(err)
	}

	e.save("small", "Small renamed", false)
	if err := e.sync(); err != nil {
		t.Fatal(err)
	}
	if st := e.a.collector.Status(); len(st) != 0 {
		t.Errorf("disabled cluster still collected: %+v", st)
	}
}

// The binding written by the worker itself does not count as a change of
// configuration: an unrelated change does not restart the worker, which
// would capture everything again at once.
func TestBindingDoesNotRestartTheWorker(t *testing.T) {
	e := newCollectEnv(t)
	e.save("small", "Small", true)
	if err := e.a.Start(e.ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first full capture", func() bool {
		st := e.a.collector.Status()
		return len(st) == 1 && st[0].LastFull != nil
	})
	if e.cluster("small").BoundUUID != e.c.UUID {
		t.Fatal("not bound")
	}
	e.clk.Advance(time.Minute)
	e.save("other", "Other", false)
	if err := e.sync(); err != nil {
		t.Fatal(err)
	}
	if st := e.a.collector.Status(); len(st) != 1 || st[0].LastFull == nil || st[0].LastFull.StartMs != pt0.UnixMilli() {
		t.Errorf("worker restarted: %+v", st)
	}
}
