// Package collector schedules the light probes and full captures of each
// cluster (SPEC §5.3) and turns them into records (SPEC §6.7).
package collector

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"sort"
	"sync"
	"time"

	"estopo/internal/clock"
	"estopo/internal/esclient"
	"estopo/internal/record"
)

// Schedule holds the per-cluster timing parameters (admin, SPEC §9).
type Schedule struct {
	ProbeEvery  time.Duration // light probe period (default 5 min)
	MinInterval time.Duration // minimum delay between two full captures (default 10 min)
	MaxInterval time.Duration // a full capture at least this often (default 60 min)
	Jitter      float64       // fraction of ProbeEvery added at random (default 0.1)
	// A change of relocating, initializing or unassigned shards larger than
	// this, compared with the last full capture, triggers a full capture.
	ShardsThreshold int
}

// DefaultSchedule returns the SPEC defaults.
func DefaultSchedule() Schedule {
	return Schedule{ProbeEvery: 5 * time.Minute, MinInterval: 10 * time.Minute, MaxInterval: 60 * time.Minute, Jitter: 0.1}
}

// ClusterConfig describes one collected cluster.
type ClusterConfig struct {
	ID                string // stable configuration id (spool directory, partitions)
	Name              string
	ES                esclient.Config
	BoundUUID         string // cluster_uuid bound at the first capture; "" = not bound yet
	Suspended         bool
	Schedule          Schedule
	SettingsWhitelist []string
}

// Sink receives every record (spool + ingestion).
type Sink interface {
	Record(r *record.Record) error
}

// Events lets the owner persist bindings and raise admin alerts.
type Events interface {
	// Bound is called when a cluster is bound to its cluster_uuid.
	Bound(clusterID, uuid string)
	// Suspended is called when the cluster behind the URL changed identity
	// (SPEC §5.5, B8): collection stops until an admin decides.
	Suspended(clusterID, bound, seen string)
}

// Collector runs one worker per cluster and bounds concurrent full captures.
type Collector struct {
	clock   clock.Clock
	log     *slog.Logger
	sink    Sink
	events  Events
	fullSem chan struct{}

	mu      sync.Mutex
	workers map[string]*worker
}

// New returns a collector allowing maxFull concurrent full captures
// (default 3, SPEC §5.3).
func New(clk clock.Clock, log *slog.Logger, sink Sink, events Events, maxFull int) *Collector {
	if maxFull <= 0 {
		maxFull = 3
	}
	return &Collector{clock: clk, log: log, sink: sink, events: events,
		fullSem: make(chan struct{}, maxFull), workers: map[string]*worker{}}
}

// Add starts collecting a cluster.
func (c *Collector) Add(cfg ClusterConfig) error {
	if cfg.ID == "" {
		return errors.New("cluster id is empty")
	}
	if cfg.Schedule.ProbeEvery <= 0 {
		cfg.Schedule = DefaultSchedule()
	}
	if len(cfg.SettingsWhitelist) == 0 {
		cfg.SettingsWhitelist = esclient.DefaultSettingsWhitelist
	}
	client, err := esclient.New(cfg.ES)
	if err != nil {
		return fmt.Errorf("cluster %s: %w", cfg.ID, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.workers[cfg.ID]; ok {
		return fmt.Errorf("cluster %s already collected", cfg.ID)
	}
	h := fnv.New64a()
	h.Write([]byte(cfg.ID))
	w := &worker{
		c: c, cfg: cfg, client: client, rng: rand.New(rand.NewPCG(h.Sum64(), 1)),
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}),
	}
	w.status = Status{ID: cfg.ID, Name: cfg.Name, Suspended: cfg.Suspended}
	c.workers[cfg.ID] = w
	go w.run()
	return nil
}

// Remove stops collecting a cluster and waits for its worker.
func (c *Collector) Remove(id string) {
	c.mu.Lock()
	w := c.workers[id]
	delete(c.workers, id)
	c.mu.Unlock()
	if w != nil {
		close(w.stop)
		<-w.done
	}
}

// Stop stops every worker.
func (c *Collector) Stop() {
	c.mu.Lock()
	ids := make([]string, 0, len(c.workers))
	for id := range c.workers {
		ids = append(ids, id)
	}
	c.mu.Unlock()
	for _, id := range ids {
		c.Remove(id)
	}
}

// ErrUnknownCluster is returned for an id that is not collected.
var ErrUnknownCluster = errors.New("unknown cluster")

// CaptureNow asks for a full capture. Unless force is set, the minimum
// interval between full captures still applies (the capture is deferred).
func (c *Collector) CaptureNow(id string, force bool) error {
	w := c.worker(id)
	if w == nil {
		return ErrUnknownCluster
	}
	w.mu.Lock()
	w.manual = true
	w.force = w.force || force
	w.mu.Unlock()
	w.poke()
	return nil
}

// Resume restarts a suspended cluster, bound to uuid (the new identity
// accepted by an admin, or "" to bind at the next capture).
func (c *Collector) Resume(id, uuid string) error {
	w := c.worker(id)
	if w == nil {
		return ErrUnknownCluster
	}
	w.mu.Lock()
	w.cfg.Suspended, w.cfg.BoundUUID = false, uuid
	w.status.Suspended, w.status.SuspendReason = false, ""
	w.reset = true // new identity: start over with a full capture
	w.mu.Unlock()
	w.poke()
	return nil
}

// Status returns the state of every worker, sorted by id.
func (c *Collector) Status() []Status {
	c.mu.Lock()
	ws := make([]*worker, 0, len(c.workers))
	for _, w := range c.workers {
		ws = append(ws, w)
	}
	c.mu.Unlock()
	out := make([]Status, 0, len(ws))
	for _, w := range ws {
		w.mu.Lock()
		s := w.status
		s.RecentErrors = append([]string(nil), s.RecentErrors...)
		w.mu.Unlock()
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (c *Collector) worker(id string) *worker {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.workers[id]
}

// Status is what the admin "Collectors" page shows (SPEC §9).
type Status struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Suspended      bool           `json:"suspended"`
	SuspendReason  string         `json:"suspend_reason,omitempty"`
	LastProbe      *CaptureStatus `json:"last_probe,omitempty"`
	LastFull       *CaptureStatus `json:"last_full,omitempty"`
	NextProbeMs    int64          `json:"next_probe_ms"`
	PendingTrigger string         `json:"pending_trigger,omitempty"`
	RecentErrors   []string       `json:"recent_errors,omitempty"`
}

// CaptureStatus summarizes one capture.
type CaptureStatus struct {
	StartMs    int64               `json:"start_ms"`
	DurationMs int64               `json:"duration_ms"`
	Status     record.Status       `json:"status"`
	Trigger    string              `json:"trigger"`
	Error      string              `json:"error,omitempty"`
	Calls      []esclient.CallStat `json:"calls"`
}

// ctxFor returns a context cancelled when the worker stops.
func (w *worker) ctx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-w.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
