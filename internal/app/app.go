// Package app assembles estopo: store, ingestion pipeline, collector, query
// engine, API and HTTP server.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"estopo/internal/api"
	"estopo/internal/auth"
	"estopo/internal/buildinfo"
	"estopo/internal/clock"
	"estopo/internal/collector"
	"estopo/internal/config"
	"estopo/internal/derive"
	"estopo/internal/ha"
	"estopo/internal/query"
	"estopo/internal/record"
	"estopo/internal/store"
)

// SpoolRetention is how long capture records are kept (SPEC §6.1).
const SpoolRetention = 72 * time.Hour

// AuditRetention is how long the audit trail is kept (it is also replicated
// to the standby at each change).
const AuditRetention = 365 * 24 * time.Hour

// App is a running estopo instance.
type App struct {
	log       *slog.Logger
	clock     clock.Clock
	store     *store.Store
	spool     *record.Spool
	ingester  *store.Ingester
	pipeline  *pipeline
	collector *collector.Collector
	engine    *query.Engine
	api       *api.Server

	syncMu  sync.Mutex        // serializes SyncClusters
	running map[string]string // cluster id -> hash of its collector configuration (syncMu)

	mu       sync.Mutex
	machines map[string]string              // cluster id -> machine attribute
	families map[string][]derive.FamilyRule // cluster id -> family rules
	auth     *auth.Service
	ha       *ha.Manager

	// The replication and the retention loop, which Close stops and waits
	// for before closing the store.
	cancel context.CancelFunc
	loops  sync.WaitGroup
}

// Options for New.
type Options struct {
	DataDir string
	Log     *slog.Logger
	Clock   clock.Clock
	Collect bool         // run the collector when primary (false: never, e.g. read-only tools)
	UI      http.Handler // embedded web UI
	// Web security (SPEC §10).
	TLS, LoopbackOnly, AllowInsecureAuth bool
	AdminPassword                        string // ESTOPO_ADMIN_PASSWORD (first start)
	SecretsDir                           string // ESTOPO_SECRETS_DIR
	// High availability (SPEC §11).
	StandbyOf            string        // ESTOPO_STANDBY_OF (first start)
	ReplicationTokenFile string        // ESTOPO_REPLICATION_TOKEN_FILE
	ReplicationCA        string        // ESTOPO_REPLICATION_CA
	HAInterval           time.Duration // replication period (tests)
}

// New opens the data directory and assembles the components.
func New(ctx context.Context, o Options) (*App, error) {
	if o.Clock == nil {
		o.Clock = clock.Real{}
	}
	st, err := store.Open(ctx, o.DataDir, o.Clock)
	if err != nil {
		return nil, err
	}
	sp, err := record.OpenSpool(filepath.Join(o.DataDir, "spool"))
	if err != nil {
		st.Close()
		return nil, err
	}
	a := &App{log: o.Log, clock: o.Clock, store: st, spool: sp, running: map[string]string{}, machines: map[string]string{}}
	a.auth = auth.NewService(st, o.Clock, o.Log, o.TLS)
	a.auth.SetSecretsDir(o.SecretsDir)
	// A standby gets its accounts and its authentication mode from the primary.
	if o.StandbyOf == "" {
		if err := a.auth.Bootstrap(ctx, o.AdminPassword, o.TLS, o.LoopbackOnly, o.AllowInsecureAuth); err != nil {
			st.Close()
			return nil, err
		}
	}
	// Unreadable settings stop the start: they are never taken for mode none.
	ac, err := a.auth.Config(ctx)
	if err == nil {
		err = auth.CheckTransport(ac.Mode, o.TLS, o.LoopbackOnly, o.AllowInsecureAuth)
	}
	if err != nil {
		st.Close()
		return nil, err
	}
	a.ingester = store.NewIngester(st, store.Options{MachineAttr: a.machineAttr}, o.Log)
	a.pipeline = newPipeline(a.ingester, sp, o.Log)
	cache := store.CacheSettings{Structures: store.DefaultCacheStructures}
	st.GetSetting(ctx, "cache", &cache)
	a.engine = query.New(st, o.Log, cache.Structures)
	a.engine.SetFamilyFunc(a.familyOf)
	if o.ReplicationTokenFile != "" {
		// The replication token must not travel in clear either (SPEC §10, §11).
		if !o.TLS && !o.LoopbackOnly && !o.AllowInsecureAuth {
			st.Close()
			return nil, errors.New("replication over plain HTTP on a non-loopback address is refused: " +
				"configure ESTOPO_TLS_CERT/ESTOPO_TLS_KEY, listen on 127.0.0.1, or set ESTOPO_ALLOW_INSECURE_AUTH=true")
		}
	}
	a.ha, err = ha.New(ctx, ha.Options{Store: st, Spool: sp, Clock: o.Clock, Log: o.Log,
		StandbyOf: o.StandbyOf, TokenFile: o.ReplicationTokenFile, CAFile: o.ReplicationCA, AllowInsecure: o.AllowInsecureAuth,
		Interval: o.HAInterval, TmpDir: st.TmpDir(),
		Hooks: ha.Hooks{Ingest: a.pipeline.Record, Idle: a.Idle, LastTs: a.ingester.LastTs, Forget: a.ingester.Forget,
			Changed: a.SyncClusters}})
	if err != nil {
		st.Close()
		return nil, err
	}
	opts := api.Options{Log: o.Log, Clock: o.Clock, Store: st, Engine: a.engine, UI: o.UI, OnConfigChange: a.SyncClusters, Purge: a.purge,
		Auth: a.auth, Transport: api.Transport{TLS: o.TLS, LoopbackOnly: o.LoopbackOnly, AllowInsecureAuth: o.AllowInsecureAuth},
		SecretsDir: o.SecretsDir,
		HA:         a.ha, ExtraMetrics: a.metrics}
	if o.Collect {
		cs := store.CollectorSettings{Parallelism: 3}
		st.GetSetting(ctx, "collector", &cs)
		if cs.Parallelism < 1 {
			cs.Parallelism = 3
		}
		a.collector = collector.New(o.Clock, o.Log, collectorSink{a.pipeline}, a, cs.Parallelism)
		opts.Collector = a.collector
	}
	a.api = api.New(opts)
	return a, nil
}

// HA exposes the high-availability manager (tests).
func (a *App) HA() *ha.Manager { return a.ha }

// Handler is the HTTP handler (API + UI).
func (a *App) Handler() http.Handler { return a.api }

// Store exposes the store (tests, admin commands).
func (a *App) Store() *store.Store { return a.store }

// Start replays the spool, starts the ingestion, the collector and the
// retention loop. They stop with ctx, or with Close.
func (a *App) Start(ctx context.Context) error {
	ctx, a.cancel = context.WithCancel(ctx)
	// Partition copies a previous run left unfinished (HA).
	if err := a.store.CleanTmp(); err != nil {
		return err
	}
	if err := a.pipeline.recover(ctx); err != nil {
		return fmt.Errorf("replay spool: %w", err)
	}
	go a.pipeline.run(ctx)
	// An old primary coming back checks its peer before collecting again.
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	a.ha.CheckPeer(cctx)
	cancel()
	if err := a.SyncClusters(ctx); err != nil { // also loads machine attributes and family rules
		return err
	}
	a.loops.Add(2)
	go func() {
		defer a.loops.Done()
		a.retentionLoop(ctx)
	}()
	go func() {
		defer a.loops.Done()
		a.ha.Run(ctx)
	}()
	return nil
}

// Close stops the collector and closes the store.
func (a *App) Close() error {
	// The replication and the retention first: they use the store, and a
	// partition copy must not be cut by its closing. Waited for before the
	// collector stops, a configuration the replication applies (Hooks.Changed)
	// cannot start a worker after it.
	if a.cancel != nil {
		a.cancel()
	}
	a.loops.Wait()
	// Then the pipeline: a worker handing over its last record must not wait
	// for an ingestion that has stopped (the record is in the spool).
	a.pipeline.close()
	if a.collector != nil {
		a.collector.Stop()
	}
	a.pipeline.wait()
	return a.store.Close()
}

// purge deletes the whole history of a cluster (admin action, SPEC §6.6):
// its partitions and its spooled records. Left in the spool, these would be
// replayed at the next start (the history would come back) and would keep
// announcing the cluster to the standby, which would not purge it (SPEC §11).
// The ingestion of the cluster waits meanwhile. A record being spooled at
// that very moment (pipeline.send) may still survive the purge. A cluster
// still collected starts over with a full capture.
func (a *App) purge(ctx context.Context, cluster string) (int, error) {
	n, err := a.ingester.Purge(ctx, cluster, func() error {
		if !record.ValidCluster(cluster) { // it names a directory; no other has a spool
			return nil
		}
		return os.RemoveAll(filepath.Join(a.spool.Dir(), cluster))
	})
	if err == nil && a.collector != nil {
		a.collector.Reset(cluster) // ErrUnknownCluster: not collected
	}
	return n, err
}

// familyOf applies the family rules of a cluster (SPEC §7.3).
func (a *App) familyOf(cluster, index string) (string, bool) {
	a.mu.Lock()
	rules := a.families[cluster]
	a.mu.Unlock()
	if len(rules) == 0 {
		return "", false
	}
	return derive.Family(index, rules), true
}

func (a *App) machineAttr(cluster string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.machines[cluster]
}

// SyncClusters starts, restarts or stops collector workers to match the
// configuration in meta.db. Calls run one at a time, reading included, so
// that an older configuration is never applied last. a.mu is not held while
// workers stop: a stopping worker may wait for the ingestion, which takes it.
func (a *App) SyncClusters(ctx context.Context) error {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	cs, err := a.store.Clusters(ctx, false)
	if err != nil {
		return err
	}
	var whitelist []string
	a.store.GetSetting(ctx, "settings_whitelist", &whitelist)
	families := map[string][]derive.FamilyRule{}
	for _, c := range cs {
		patterns, err := a.store.FamilyRules(ctx, c.ID)
		if err != nil {
			return err
		}
		for _, p := range patterns {
			if fr, err := derive.NewFamilyRule(p); err == nil {
				families[c.ID] = append(families[c.ID], fr)
			}
		}
	}
	a.mu.Lock()
	for _, c := range cs {
		a.machines[c.ID] = c.MachineAttr
	}
	a.families = families
	a.mu.Unlock()
	a.engine.Invalidate()
	if a.collector == nil {
		return nil
	}
	want := map[string]collector.ClusterConfig{}
	for _, c := range cs {
		if !c.Enabled || !a.ha.Collecting() { // a standby never contacts the clusters (SPEC §11)
			continue
		}
		sched := collector.DefaultSchedule()
		if len(c.Schedule) > 0 {
			if err := json.Unmarshal(c.Schedule, &sched); err != nil {
				a.log.Error("invalid schedule, defaults used", "cluster", c.ID, "err", err)
				sched = collector.DefaultSchedule()
			}
		}
		want[c.ID] = collector.ClusterConfig{ID: c.ID, Name: c.Name, ES: a.store.ApplyTLSDefaults(ctx, c.ES), BoundUUID: c.BoundUUID, Suspended: c.Suspended,
			Schedule: sched, SettingsWhitelist: whitelist}
	}
	for id := range a.running {
		if _, ok := want[id]; !ok {
			a.collector.Remove(id)
			delete(a.running, id)
		}
	}
	var errs []error
	for id, cfg := range want {
		// The binding and the suspension are left out: the worker sets them
		// itself (Bound, Suspended) and Resume applies to the running worker.
		// Restarting it would lose its schedule and capture everything again.
		hc := cfg
		hc.BoundUUID, hc.Suspended = "", false
		b, _ := json.Marshal(hc)
		h := fmt.Sprintf("%x", sha256.Sum256(b))
		if a.running[id] == h {
			continue
		}
		if _, ok := a.running[id]; ok {
			a.collector.Remove(id)
		}
		if err := a.collector.Add(cfg); err != nil {
			errs = append(errs, err)
			delete(a.running, id)
			continue
		}
		a.running[id] = h
	}
	return errors.Join(errs...)
}

// Bound implements collector.Events.
func (a *App) Bound(clusterID, uuid string) {
	if err := a.store.SetBound(context.Background(), clusterID, uuid); err != nil {
		a.log.Error("bind cluster", "cluster", clusterID, "err", err)
	}
}

// Suspended implements collector.Events.
func (a *App) Suspended(clusterID, bound, seen string) {
	ctx := context.Background()
	reason := fmt.Sprintf("cluster_uuid changed from %s to %s", bound, seen)
	// The worker stays: suspended by itself, it waits for Resume, and a later
	// SyncClusters can still reconfigure or remove it.
	if err := a.store.SetSuspended(ctx, clusterID, true, reason); err != nil {
		a.log.Error("suspend cluster", "cluster", clusterID, "err", err)
	}
	if err := a.ingester.Note(ctx, clusterID, a.clock.Now().UnixMilli(), store.EventClusterUUIDChanged, 1,
		map[string]string{"bound": bound, "seen": seen}); err != nil {
		a.log.Error("record identity change", "cluster", clusterID, "err", err)
	}
}

func (a *App) retentionLoop(ctx context.Context) {
	// Errors of a pass cut short by the shutdown are not reported.
	fail := func(msg string, err error) {
		if ctx.Err() == nil {
			a.log.Error(msg, "err", err)
		}
	}
	run := func() {
		pol, err := a.store.RetentionPolicy(ctx)
		if err != nil {
			fail("retention policy", err)
			return
		}
		rep, err := a.store.ApplyRetention(ctx, pol)
		if err != nil {
			fail("retention", err)
		} else if n := len(rep.DeletedByAge) + len(rep.DeletedBySize); n > 0 {
			a.log.Info("retention", "deleted_by_age", len(rep.DeletedByAge), "deleted_by_size", len(rep.DeletedBySize), "bytes", rep.TotalBytes)
		}
		now := a.clock.Now()
		if err := a.store.DeleteExpiredSessions(ctx, now.UnixMilli()); err != nil {
			fail("expired sessions", err)
		}
		if n, err := a.store.PruneAudit(ctx, now.Add(-AuditRetention).UnixMilli()); err != nil {
			fail("audit prune", err)
		} else if n > 0 {
			a.log.Info("audit pruned", "entries", n)
		}
		if n, err := a.spool.Prune(a.clock.Now(), SpoolRetention); err != nil {
			fail("spool prune", err)
		} else if n > 0 {
			a.log.Debug("spool pruned", "files", n)
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.clock.After(time.Hour):
			run()
		}
	}
}

// --- ingestion pipeline ----------------------------------------------------------

// pipeline writes each record to the spool (durable) then queues it for the
// ingestion, which runs in a single goroutine (records of a cluster stay in
// order).
type pipeline struct {
	ing   *store.Ingester
	spool *record.Spool
	log   *slog.Logger
	ch    chan queued
	once  sync.Once
	done  chan struct{} // closed by close
	// stopped is closed when run returns: nothing reads the queue any more.
	stopped chan struct{}
	started atomic.Bool

	pending atomic.Int64 // records handed to Record and not yet ingested (or given up)
	statsMu sync.Mutex
	stats   map[string]*IngestStat // by "cluster/kind"

	// Owned by the run goroutine: per cluster, the record whose ingestion
	// failed and is retried, and the last spool file ingested by a retry.
	failed   map[string]*failedRecord
	replayed map[string]string
}

// queued is a record with its spool file.
type queued struct {
	r *record.Record
	e record.Entry
}

// failedRecord is a spooled record whose ingestion failed.
type failedRecord struct {
	entry    record.Entry
	attempts int
}

// ingestAttempts is how many times the ingestion of a record is tried
// before the record is given up (a gap in the history, logged).
const ingestAttempts = 3

func newPipeline(ing *store.Ingester, sp *record.Spool, log *slog.Logger) *pipeline {
	return &pipeline{ing: ing, spool: sp, log: log, ch: make(chan queued, 4), done: make(chan struct{}), stopped: make(chan struct{}),
		stats: map[string]*IngestStat{}, failed: map[string]*failedRecord{}, replayed: map[string]string{}}
}

// Record spools r and queues it for the ingestion.
func (p *pipeline) Record(r *record.Record) error { return p.send(r, nil) }

// send spools r and queues it, unless stop is closed first (nil: never).
func (p *pipeline) send(r *record.Record, stop <-chan struct{}) error {
	e, err := p.spool.Write(r)
	if err != nil {
		return err
	}
	p.pending.Add(1)
	select {
	case p.ch <- queued{r, e}:
	case <-p.done: // shutting down: the record is replayed from the spool at the next start
		p.pending.Add(-1)
	case <-p.stopped:
		p.pending.Add(-1)
	case <-stop:
		// Worker stopped (cluster removed or reconfigured): the record stays
		// in the spool, replayed at the next start unless a newer record of
		// its cluster is ingested first.
		p.pending.Add(-1)
	}
	return nil
}

// collectorSink hands the collector's records to the pipeline.
type collectorSink struct{ p *pipeline }

// Record implements collector.Sink: a worker being stopped stops waiting for
// room in the queue.
func (s collectorSink) Record(r *record.Record, stop <-chan struct{}) error { return s.p.send(r, stop) }

func (p *pipeline) run(ctx context.Context) {
	p.started.Store(true)
	defer close(p.stopped)
	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case q := <-p.ch:
			p.ingest(ctx, q)
			p.pending.Add(-1)
		}
	}
}

// ingest ingests one queued record. A record whose ingestion fails stays in
// the spool and is tried again, before anything newer of its cluster, when
// the next record of that cluster arrives (and at the next start): records
// of a cluster are ingested in order or not at all. After ingestAttempts
// failures it is given up, which leaves a gap in the history.
func (p *pipeline) ingest(ctx context.Context, q queued) {
	c, name := q.r.Cluster, filepath.Base(q.e.Path)
	if f := p.failed[c]; f != nil {
		p.retry(ctx, c, f) // reads q back from the spool, in its turn
		return
	}
	if name <= p.replayed[c] {
		return // ingested by a retry while it waited in the queue
	}
	if err := p.ingestOne(ctx, q.r); err != nil {
		p.failed[c] = &failedRecord{entry: q.e, attempts: 1}
		p.log.Error("ingest: the record will be tried again with the next one of its cluster", "cluster", c, "kind", q.r.Kind,
			"ts", q.r.StartMs, "err", err)
	}
}

func (p *pipeline) ingestOne(ctx context.Context, r *record.Record) error {
	start := time.Now()
	res, err := p.ing.Ingest(ctx, r)
	p.record(r, time.Since(start), err)
	if err == nil {
		p.log.Debug("ingested", "cluster", r.Cluster, "kind", r.Kind, "status", r.Status, "ms", time.Since(start).Milliseconds(),
			"opened", res.ShardsOpened, "closed", res.ShardsClosed, "events", res.Events, "keyframe", res.Keyframe)
	}
	return err
}

// retry ingests, from the spool and in order, the record that failed and
// those spooled since.
func (p *pipeline) retry(ctx context.Context, cluster string, f *failedRecord) {
	entries, err := p.spool.List(cluster, f.entry.StartMs)
	if err != nil {
		p.log.Error("ingest retry: spool", "cluster", cluster, "err", err)
		return
	}
	first := filepath.Base(f.entry.Path)
	for _, e := range entries {
		name := filepath.Base(e.Path)
		if name < first {
			continue
		}
		last, err := p.ing.LastTs(ctx, cluster)
		if err != nil {
			p.log.Error("ingest retry", "cluster", cluster, "err", err)
			return
		}
		var r *record.Record
		if e.StartMs >= last { // else the history was replaced meanwhile (rebuild) and already covers it
			if r, err = p.spool.Read(e); err == nil {
				err = p.ingestOne(ctx, r)
			}
		}
		if err != nil {
			if name != first {
				f.entry, f.attempts, first = e, 0, name
			}
			if f.attempts++; f.attempts < ingestAttempts && ctx.Err() == nil {
				p.log.Error("ingest: the record will be tried again", "cluster", cluster, "path", e.Path, "attempt", f.attempts, "err", err)
				return
			}
			if ctx.Err() != nil {
				return
			}
			p.skipped(cluster, e)
			p.log.Error("ingest: record given up, the history has a gap", "cluster", cluster, "path", e.Path,
				"attempts", f.attempts, "err", err)
		}
		p.replayed[cluster] = name
	}
	delete(p.failed, cluster)
}

func (p *pipeline) close() { p.once.Do(func() { close(p.done) }) }

// wait returns when the ingestion goroutine, if it was started, has ended.
func (p *pipeline) wait() {
	if p.started.Load() {
		<-p.stopped
	}
}

// recover ingests the spooled records newer than what the store holds
// (crash between spooling and ingestion).
func (p *pipeline) recover(ctx context.Context) error {
	clusters, err := p.spool.Clusters()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, c := range clusters {
		last, err := p.ing.LastTs(ctx, c)
		if err != nil {
			return err
		}
		entries, err := p.spool.List(c, last+1)
		if err != nil {
			return err
		}
		for _, e := range entries {
			r, err := p.spool.Read(e)
			if err != nil {
				p.log.Warn("unreadable spooled record", "path", e.Path, "err", err)
				continue
			}
			if _, err := p.ing.Ingest(ctx, r); err != nil {
				p.log.Error("replay", "path", e.Path, "err", err)
			}
		}
		if len(entries) > 0 {
			p.log.Info("spool replayed", "cluster", c, "records", len(entries))
		}
	}
	return nil
}

// --- HTTP server -------------------------------------------------------------------

// newHTTPServer bounds what a client can hold: slow requests, idle
// connections, large headers. The write timeout stays long enough for an
// export, or a partition snapshot sent to a standby.
func newHTTPServer(addr string, h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// Serve runs estopo until ctx is cancelled.
func Serve(ctx context.Context, cfg config.Config, log *slog.Logger, ui http.Handler) error {
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	log.Info("starting", "version", buildinfo.Version, "listen", cfg.Listen, "data_dir", cfg.DataDir, "tls", cfg.TLS(),
		"secrets_dir", cfg.SecretsDir)
	if cfg.PprofListen != "" {
		startPprof(cfg.PprofListen, log)
	}
	a, err := New(ctx, Options{DataDir: cfg.DataDir, Log: log, Collect: true, UI: ui,
		TLS: cfg.TLS(), LoopbackOnly: cfg.LoopbackOnly(), AllowInsecureAuth: cfg.AllowInsecureAuth, AdminPassword: cfg.AdminPassword,
		SecretsDir: cfg.SecretsDir,
		StandbyOf:  cfg.StandbyOf, ReplicationTokenFile: cfg.ReplicationToken, ReplicationCA: cfg.ReplicationCA})
	if err != nil {
		return err
	}
	defer a.Close()
	if err := a.Start(ctx); err != nil {
		return err
	}
	srv := newHTTPServer(cfg.Listen, a.Handler(), log)
	errc := make(chan error, 1)
	go func() {
		if cfg.TLS() {
			errc <- srv.ListenAndServeTLS(cfg.TLSCert, cfg.TLSKey)
		} else {
			errc <- srv.ListenAndServe()
		}
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// IngestStat accumulates ingestion timings of one cluster and capture kind.
type IngestStat struct {
	Cluster string        `json:"cluster"`
	Kind    string        `json:"kind"`
	Count   int           `json:"count"`
	Errors  int           `json:"errors"`  // failed attempts
	Skipped int           `json:"skipped"` // records given up after ingestAttempts failures
	Total   time.Duration `json:"total_ns"`
	Max     time.Duration `json:"max_ns"`
}

// stat returns the counters of a cluster and kind (p.statsMu held).
func (p *pipeline) stat(cluster string, kind record.Kind) *IngestStat {
	k := cluster + "/" + string(kind)
	s := p.stats[k]
	if s == nil {
		s = &IngestStat{Cluster: cluster, Kind: string(kind)}
		p.stats[k] = s
	}
	return s
}

func (p *pipeline) skipped(cluster string, e record.Entry) {
	p.statsMu.Lock()
	defer p.statsMu.Unlock()
	p.stat(cluster, e.Kind).Skipped++
}

func (p *pipeline) record(r *record.Record, d time.Duration, err error) {
	p.statsMu.Lock()
	defer p.statsMu.Unlock()
	s := p.stat(r.Cluster, r.Kind)
	s.Count++
	if err != nil {
		s.Errors++
	}
	s.Total += d
	s.Max = max(s.Max, d)
}

// IngestStats returns the ingestion timings since start.
func (a *App) IngestStats() []IngestStat {
	a.pipeline.statsMu.Lock()
	defer a.pipeline.statsMu.Unlock()
	out := make([]IngestStat, 0, len(a.pipeline.stats))
	for _, s := range a.pipeline.stats {
		out = append(out, *s)
	}
	return out
}

// Pending reports how many records wait for the ingestion.
func (a *App) Pending() int { return len(a.pipeline.ch) }

// Idle reports whether every record received so far is ingested: counted
// from the moment Record accepts it, so that a record between the queue and
// the ingestion is not missed.
func (a *App) Idle() bool { return a.pipeline.pending.Load() == 0 }

// metrics adds the ingestion counters to /metrics.
func (a *App) metrics(metric func(name, typ, help string, value float64, labels ...string)) {
	stats := a.IngestStats()
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].Cluster+"/"+stats[i].Kind < stats[j].Cluster+"/"+stats[j].Kind
	})
	for _, s := range stats {
		metric("estopo_ingest_records_total", "counter", "Records ingested since the start.", float64(s.Count), "cluster", s.Cluster, "kind", s.Kind)
		metric("estopo_ingest_errors_total", "counter", "Records whose ingestion failed since the start.", float64(s.Errors), "cluster", s.Cluster, "kind", s.Kind)
		metric("estopo_ingest_skipped_total", "counter", "Records given up after repeated ingestion failures (gaps in the history).", float64(s.Skipped), "cluster", s.Cluster, "kind", s.Kind)
		metric("estopo_ingest_seconds_total", "counter", "Time spent ingesting since the start.", s.Total.Seconds(), "cluster", s.Cluster, "kind", s.Kind)
		metric("estopo_ingest_max_seconds", "gauge", "Longest ingestion of a record since the start.", s.Max.Seconds(), "cluster", s.Cluster, "kind", s.Kind)
	}
	metric("estopo_ingest_queue", "gauge", "Records waiting for the ingestion.", float64(a.Pending()))
}
