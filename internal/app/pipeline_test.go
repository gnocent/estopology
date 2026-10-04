package app

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"estopo/internal/clock"
	"estopo/internal/record"
	"estopo/internal/sim"
	"estopo/internal/store"
)

var pt0 = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

type penv struct {
	t   *testing.T
	st  *store.Store
	ing *store.Ingester
	p   *pipeline
	c   *sim.Cluster
}

func newPipelineEnv(t *testing.T) *penv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), dir, clock.NewManual(pt0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sp, err := record.OpenSpool(filepath.Join(dir, "spool"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ing := store.NewIngester(st, store.Options{}, log)
	return &penv{t: t, st: st, ing: ing, p: newPipeline(ing, sp, log), c: sim.Build(sim.PresetSmall("8.19.22"), pt0)}
}

func (e *penv) rec(minutes int, kind record.Kind) *record.Record {
	return e.c.CaptureAt(pt0.Add(time.Duration(minutes)*time.Minute).UnixMilli(), "small", kind, record.TriggerSchedule)
}

// send hands a record to the pipeline and waits until it has dealt with it.
func (e *penv) send(minutes int) {
	e.t.Helper()
	if err := e.p.Record(e.rec(minutes, record.Light)); err != nil {
		e.t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for e.p.pending.Load() != 0 {
		if time.Now().After(deadline) {
			e.t.Fatal("the pipeline did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// captured lists the minutes (after pt0) of the captures stored.
func (e *penv) captured() []int {
	e.t.Helper()
	p, err := e.st.Partition(context.Background(), "small", pt0.UnixMilli())
	if err != nil {
		e.t.Fatal(err)
	}
	rows, err := p.DB().Query(`SELECT ts FROM capture ORDER BY id`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var ts int64
		rows.Scan(&ts)
		out = append(out, int((ts-pt0.UnixMilli())/60000))
	}
	return out
}

// breakIngestion makes every ingestion fail until the returned function is
// called.
func (e *penv) breakIngestion() (repair func()) {
	e.t.Helper()
	p, err := e.st.Partition(context.Background(), "small", pt0.UnixMilli())
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := p.DB().Exec(`ALTER TABLE capture RENAME TO capture_away`); err != nil {
		e.t.Fatal(err)
	}
	return func() {
		if _, err := p.DB().Exec(`ALTER TABLE capture_away RENAME TO capture`); err != nil {
			e.t.Fatal(err)
		}
	}
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Shutdown: once the ingestion has stopped, a worker handing over a record
// is not held (the collector could never stop); the record is spooled.
func TestRecordAfterTheIngestionStopped(t *testing.T) {
	e := newPipelineEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	go e.p.run(ctx)
	e.send(0)
	cancel()
	e.p.wait()
	done := make(chan error, 1)
	go func() {
		for i := 1; i <= 8; i++ { // more than the queue holds
			if err := e.p.Record(e.rec(i, record.Light)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Record blocks once the ingestion has stopped")
	}
	if entries, _ := e.p.spool.List("small", 0); len(entries) != 9 {
		t.Errorf("%d records in the spool, want 9", len(entries))
	}
}

// Idle is false from the moment a record is accepted until it is ingested,
// with no window between the queue and the ingestion.
func TestIdleCountsRecordsInFlight(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, Options{DataDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), LoopbackOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	c := sim.Build(sim.PresetSmall("8.19.22"), pt0)
	for i := 0; i < 200; i++ {
		r := c.CaptureAt(pt0.Add(time.Duration(i)*time.Minute).UnixMilli(), "small", record.Light, record.TriggerSchedule)
		if err := a.pipeline.Record(r); err != nil {
			t.Fatal(err)
		}
		if a.Idle() {
			if last, err := a.ingester.LastTs(ctx, "small"); err != nil || last != r.StartMs {
				t.Fatalf("record %d: idle while the record is not ingested (last %d, %v)", i, last, err)
			}
		}
	}
}

// What a crash during a partition copy left in tmp is removed at the start.
func TestStartCleansTmp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, Options{DataDir: t.TempDir(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), LoopbackOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	left := filepath.Join(a.store.TmpDir(), "partition-1.db")
	os.MkdirAll(a.store.TmpDir(), 0o750)
	os.WriteFile(left, []byte("x"), 0o600)
	if err := a.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Errorf("partition copy left by the previous run: %v", err)
	}
}

// A record whose ingestion fails is tried again, in order, when the next
// record of its cluster arrives; after ingestAttempts failures it is given
// up and counted, and the following ones are ingested.
func TestFailedIngestionIsRetried(t *testing.T) {
	e := newPipelineEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.p.run(ctx)
	defer e.p.wait()
	defer cancel()
	e.send(0)

	repair := e.breakIngestion()
	e.send(1) // fails
	repair()
	if got := e.captured(); !equal(got, []int{0}) {
		t.Fatalf("captures after a failed ingestion: %v", got)
	}
	e.send(2) // 1 is ingested first
	if got := e.captured(); !equal(got, []int{0, 1, 2}) {
		t.Fatalf("captures after the retry: %v, want [0 1 2]", got)
	}

	repair = e.breakIngestion()
	e.send(3) // first failure of 3
	e.send(4) // second
	e.send(5) // third: 3 is given up; 4 fails in its turn
	repair()
	e.send(6) // 4, 5, 6
	if got := e.captured(); !equal(got, []int{0, 1, 2, 4, 5, 6}) {
		t.Errorf("captures: %v, want [0 1 2 4 5 6]", got)
	}
	e.p.statsMu.Lock()
	skipped := e.p.stat("small", record.Light).Skipped
	e.p.statsMu.Unlock()
	if skipped != 1 {
		t.Errorf("%d records given up, want 1", skipped)
	}
	// Nothing is pending: a later record goes straight in.
	e.send(7)
	if got := e.captured(); len(got) != 7 || got[6] != 7 {
		t.Errorf("captures: %v", got)
	}
}
