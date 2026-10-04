// Package backfill produces an estopo history quickly: simulated clusters
// and the real collector share an accelerated clock, and the whole pipeline
// (HTTP calls, records, ingestion) runs as in production (SPEC §13.1). It
// serves the benchmark and the demonstration data of essim.
package backfill

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"time"

	"estopo/internal/app"
	"estopo/internal/clock"
	"estopo/internal/esclient"
	"estopo/internal/sim"
	"estopo/internal/store"
)

// Cluster is one simulated cluster to collect.
type Cluster struct {
	ID   string // estopo cluster id (spool and partition directories)
	Spec sim.Spec
}

// Options for Run.
type Options struct {
	Dir      string    // estopo data directory (created)
	Start    time.Time // simulated start
	Days     int
	Clusters []Cluster
	Scenario *sim.Scenario // incidents, optional (steps counted back from Start+Days)
	// DisableAtEnd disables the cluster configurations at the end: their
	// simulators stop with the backfill.
	DisableAtEnd bool
	Retention    store.Retention  // default: 90 days
	Log          *slog.Logger     // estopo logs (and scenario steps when OnStep is nil)
	OnStep       func(msg string) // scenario steps as they happen
	// Progress is called at each simulated day boundary.
	Progress func(day string, dayWall, total time.Duration, heapBytes uint64)
}

// Result summarizes a run.
type Result struct {
	Wall   time.Duration
	Ingest []app.IngestStat
	Heap   uint64 // bytes in use at the end
	MaxSys uint64 // largest memory obtained from the system
	Sims   map[string]*sim.Cluster
}

// Run backfills Days days from Start into Dir.
func Run(ctx context.Context, o Options) (*Result, error) {
	if o.Days <= 0 || len(o.Clusters) == 0 {
		return nil, errors.New("backfill: days and clusters are required")
	}
	if o.Retention.MaxAgeDays == 0 {
		o.Retention.MaxAgeDays = 90
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	clk := clock.NewManual(o.Start)
	// The HA loop (a primary watching no peer) would wake the accelerated
	// clock every 30 s for nothing.
	a, err := app.New(ctx, app.Options{DataDir: o.Dir, Log: o.Log, Clock: clk, Collect: true, HAInterval: 24 * time.Hour})
	if err != nil {
		return nil, err
	}
	defer a.Close()
	st := a.Store()
	if err := st.SetSetting(ctx, "retention", o.Retention); err != nil {
		return nil, err
	}

	end := o.Start.Add(time.Duration(o.Days) * 24 * time.Hour)
	res := &Result{Sims: map[string]*sim.Cluster{}}
	for _, cl := range o.Clusters {
		c := sim.Build(cl.Spec, o.Start)
		res.Sims[cl.ID] = c
		srv := &sim.Server{Cluster: c, Clock: clk, Auth: sim.Auth{Disabled: true}}
		if o.Scenario != nil {
			id := cl.ID
			if err := srv.Play(o.Scenario, id, o.Start, end, func(f string, args ...any) {
				if o.OnStep != nil {
					o.OnStep(fmt.Sprintf(f, args...))
				} else {
					o.Log.Info(fmt.Sprintf(f, args...))
				}
			}); err != nil {
				return nil, err
			}
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
		go hs.Serve(ln)
		defer hs.Close()
		if err := st.SaveCluster(ctx, store.Cluster{ID: cl.ID, Name: cl.Spec.Name, Enabled: true, MachineAttr: "machine",
			ES: esclient.Config{URLs: []string{"http://" + ln.Addr().String()}, Auth: esclient.AuthNone, Timeout: 5 * time.Minute}}); err != nil {
			return nil, err
		}
	}

	wall := time.Now()
	if err := a.Start(ctx); err != nil {
		return nil, err
	}
	waiters := len(o.Clusters) + 2 // one worker per cluster, the retention loop, the HA loop
	dayWall, day := time.Now(), o.Start.Format("2006-01-02")
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		clk.BlockUntil(waiters)
		for idle := 0; idle < 2; { // ingestion caught up (checked twice)
			if a.Idle() {
				idle++
			} else {
				idle = 0
			}
			time.Sleep(2 * time.Millisecond)
		}
		next, ok := clk.NextDeadline()
		if !ok || !next.Before(end) {
			break
		}
		clk.Set(next)
		if d := next.Format("2006-01-02"); d != day {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			res.MaxSys = max(res.MaxSys, m.Sys)
			if o.Progress != nil {
				o.Progress(day, time.Since(dayWall), time.Since(wall), m.HeapAlloc)
			}
			day, dayWall = d, time.Now()
		}
	}
	res.Wall = time.Since(wall)
	res.Ingest = a.IngestStats()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	res.Heap, res.MaxSys = m.HeapAlloc, max(res.MaxSys, m.Sys)

	if o.DisableAtEnd {
		for _, cl := range o.Clusters {
			c, err := st.Cluster(ctx, cl.ID)
			if err != nil {
				return nil, err
			}
			c.Enabled = false
			if err := st.SaveCluster(ctx, c); err != nil {
				return nil, err
			}
		}
	}
	return res, nil
}
