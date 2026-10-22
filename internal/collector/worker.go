package collector

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"estopo/internal/esclient"
	"estopo/internal/record"
)

// snapshot is what a probe is compared with to decide on a full capture.
type snapshot struct {
	valid        bool
	health       string
	nodes        string // sorted name:id list
	relocating   int
	initializing int
	unassigned   int
	settings     string // hash of the cluster settings
}

type worker struct {
	c      *Collector
	client *esclient.Client
	rng    *rand.Rand
	wake   chan struct{}
	stop   chan struct{}
	done   chan struct{}

	mu            sync.Mutex
	cfg           ClusterConfig
	manual, force bool
	reset         bool // forget the last full capture (Resume, Reset)
	status        Status

	// Owned by the run goroutine. Schedule times are monotonic ms (mono).
	started     int64 // start of the worker
	nextProbe   int64
	attempted   bool     // a full capture was attempted
	lastAttempt int64    // last full capture attempt (for min/max interval)
	lastFull    snapshot // state seen by the last successful full capture
	lastProbe   snapshot
	pending     string // deferred full capture trigger
}

func (w *worker) poke() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// now is the wall time of the records, in ms.
func (w *worker) now() int64 { return w.c.clock.Now().UnixMilli() }

// mono is the schedule time, in ms: it does not step back with the wall
// clock, which would hold the probes back as long.
func (w *worker) mono() int64 { return w.c.mono().Milliseconds() }

// maxFrom is when the maximum interval runs from: the last full capture
// attempt, or the start of the worker while no probe could trigger one.
func (w *worker) maxFrom() int64 {
	if w.attempted {
		return w.lastAttempt
	}
	return w.started
}

func (w *worker) run() {
	defer close(w.done)
	w.started = w.mono()
	w.nextProbe = w.started // probe immediately at start
	for {
		w.mu.Lock()
		suspended := w.cfg.Suspended
		sched := w.cfg.Schedule
		w.mu.Unlock()

		now := w.mono()
		if !suspended {
			w.step(now, sched)
		}

		// Next wake-up: next probe, a deferred capture becoming possible, or
		// the max interval.
		next := w.nextProbe
		if w.pending != "" && w.attempted {
			next = min(next, w.lastAttempt+sched.MinInterval.Milliseconds())
		}
		next = min(next, w.maxFrom()+sched.MaxInterval.Milliseconds())
		w.mu.Lock()
		w.status.NextProbeMs = w.now() + w.nextProbe - w.mono()
		w.status.PendingTrigger = w.pending
		w.mu.Unlock()

		wait := time.Duration(max(next-w.mono(), 0)) * time.Millisecond
		if suspended {
			wait = sched.ProbeEvery
		}
		if wait == 0 {
			// Deadline already reached (slow capture): loop without waiting,
			// but let a stop through.
			select {
			case <-w.stop:
				return
			default:
				continue
			}
		}
		select {
		case <-w.stop:
			return
		case <-w.wake:
		case <-w.c.clock.After(wait):
		}
	}
}

// step runs whatever is due at now.
func (w *worker) step(now int64, sched Schedule) {
	w.mu.Lock()
	manual, force, reset := w.manual, w.force, w.reset
	w.manual, w.force, w.reset = false, false, false
	w.mu.Unlock()
	if reset {
		w.lastFull = snapshot{}
	}

	var probe *record.Record
	if now >= w.nextProbe {
		probe = w.probe()
		jitter := int64(0)
		if sched.Jitter > 0 {
			jitter = int64(w.rng.Float64() * sched.Jitter * float64(sched.ProbeEvery.Milliseconds()))
		}
		w.nextProbe = now + sched.ProbeEvery.Milliseconds() + jitter
		if probe == nil { // suspended on identity change
			return
		}
		if trig := w.trigger(probe, sched); trig != "" && w.pending == "" {
			w.pending = trig
		}
	}
	if manual && w.pending == "" {
		w.pending = record.TriggerManual
	}
	if now-w.maxFrom() >= sched.MaxInterval.Milliseconds() && w.pending == "" {
		w.pending = record.TriggerMaxInterval
	}
	if w.pending == "" {
		return
	}
	if !force && w.attempted && now-w.lastAttempt < sched.MinInterval.Milliseconds() {
		return // deferred, not lost (SPEC §5.3)
	}
	trig := w.pending
	w.pending = ""
	w.full(trig)
}

// trigger compares a probe with the last full capture (SPEC §5.3).
func (w *worker) trigger(probe *record.Record, sched Schedule) string {
	s := snapshotOf(probe)
	w.lastProbe = s
	if !s.valid {
		return ""
	}
	if !w.lastFull.valid {
		return record.TriggerFirst
	}
	switch {
	case s.health != w.lastFull.health:
		return record.TriggerHealth
	case s.nodes != w.lastFull.nodes:
		return record.TriggerNodes
	case abs(s.relocating-w.lastFull.relocating) > sched.ShardsThreshold,
		abs(s.initializing-w.lastFull.initializing) > sched.ShardsThreshold,
		abs(s.unassigned-w.lastFull.unassigned) > sched.ShardsThreshold:
		return record.TriggerShards
	case s.settings != "" && w.lastFull.settings != "" && s.settings != w.lastFull.settings:
		return record.TriggerSettings
	}
	return ""
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func snapshotOf(r *record.Record) snapshot {
	if r.Status == record.Failed || r.Health == nil {
		return snapshot{}
	}
	var names []string
	switch {
	case r.NodesStats != nil:
		for _, n := range r.NodesStats {
			names = append(names, n.Name+":"+n.ID)
		}
	case r.Nodes != nil:
		// Full capture whose _nodes/stats call failed: _nodes lists the
		// same nodes.
		for _, n := range r.Nodes {
			names = append(names, n.Name+":"+n.ID)
		}
	default:
		return snapshot{}
	}
	sort.Strings(names)
	s := snapshot{
		valid: true, health: r.Health.Status, nodes: strings.Join(names, ","),
		relocating: r.Health.Relocating, initializing: r.Health.Initializing, unassigned: r.Health.Unassigned,
	}
	if r.ClusterSettings != nil {
		s.settings = settingsHash(r.ClusterSettings)
	}
	return s
}

func settingsHash(cs *esclient.ClusterSettings) string {
	h := sha256.New()
	for _, m := range []map[string]string{cs.Persistent, cs.Transient} {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(h, "%s=%s\n", k, m[k])
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// --- captures --------------------------------------------------------------------

func (w *worker) newRecord(kind record.Kind, trigger string) *record.Record {
	return &record.Record{Format: record.FormatVersion, Cluster: w.cfg.ID, Kind: kind, Trigger: trigger,
		StartMs: w.now(), Status: record.OK}
}

// probeCalls runs the light probe calls into r. It returns false when the
// capture must be dropped (identity change).
func (w *worker) probeCalls(r *record.Record, withDefaults bool) bool {
	ctx, cancel := w.ctx()
	defer cancel()
	client := w.client
	call := func(st esclient.CallStat, err error) error {
		r.Calls = append(r.Calls, st)
		return err
	}

	root, st, err := client.Root(ctx)
	if call(st, err) != nil {
		r.Status, r.Error = record.Failed, "root: "+err.Error()
		return true
	}
	r.Root = &root
	if !w.checkIdentity(root) {
		return false
	}

	h, st, err := client.Health(ctx)
	if call(st, err) != nil {
		// No master, 5xx, timeout: the capture failed (SPEC §5.5).
		r.Status, r.Error = record.Failed, "health: "+err.Error()
		return true
	}
	r.Health = &h

	// A failed side call leaves its section out (nil: unknown, not empty).
	// It makes a probe partial; a full capture stays ok unless a structural
	// call fails too (see structure).
	partial := func(what string, err error) {
		if r.Kind == record.Light {
			r.Status = record.Partial
		}
		if r.Error != "" {
			r.Error += "; "
		}
		r.Error += what + ": " + err.Error()
	}
	if ns, st, err := client.NodesStats(ctx); call(st, err) != nil {
		partial("nodes_stats", err)
	} else {
		r.NodesStats = ns
	}
	if a, st, err := client.Allocation(ctx); call(st, err) != nil {
		partial("cat_allocation", err)
	} else {
		r.Allocation = a
	}
	if cs, st, err := client.ClusterSettings(ctx, withDefaults); call(st, err) != nil {
		partial("cluster_settings", err)
	} else {
		r.ClusterSettings = &cs
	}
	rem, st, err := client.RemoteInfo(ctx)
	r.Calls = append(r.Calls, st)
	var he *esclient.HTTPError
	switch {
	case err == nil:
		r.Remotes = rem
		if r.Remotes == nil {
			r.Remotes = []esclient.Remote{}
		}
	case errors.As(err, &he) && he.Status == 400:
		r.RemotesUnavailable = true
	default:
		partial("remote_info", err)
	}
	return true
}

// checkIdentity binds the cluster at the first capture and suspends it if
// the uuid changes behind the URL (SPEC §5.5).
func (w *worker) checkIdentity(root esclient.Root) bool {
	if root.ClusterUUID == "" || root.ClusterUUID == "_na_" {
		return true // no master: identity unknown, not a change
	}
	w.mu.Lock()
	bound := w.cfg.BoundUUID
	if bound == "" {
		w.cfg.BoundUUID = root.ClusterUUID
		w.mu.Unlock()
		if w.c.events != nil {
			w.c.events.Bound(w.cfg.ID, root.ClusterUUID)
		}
		return true
	}
	if bound == root.ClusterUUID {
		w.mu.Unlock()
		return true
	}
	w.cfg.Suspended = true
	w.status.Suspended = true
	w.status.SuspendReason = fmt.Sprintf("cluster_uuid changed from %s to %s", bound, root.ClusterUUID)
	w.mu.Unlock()
	w.c.log.Warn("cluster identity changed, collection suspended", "cluster", w.cfg.ID, "bound", bound, "seen", root.ClusterUUID)
	if w.c.events != nil {
		w.c.events.Suspended(w.cfg.ID, bound, root.ClusterUUID)
	}
	return false
}

func (w *worker) probe() *record.Record {
	r := w.newRecord(record.Light, record.TriggerSchedule)
	if !w.probeCalls(r, false) {
		return nil
	}
	w.finish(r, &w.status.LastProbe)
	return r
}

func (w *worker) full(trigger string) {
	select { // bound concurrent full captures (memory, cluster load)
	case w.c.fullSem <- struct{}{}:
	case <-w.stop:
		return
	}
	defer func() { <-w.c.fullSem }()

	w.lastAttempt, w.attempted = w.mono(), true
	r := w.newRecord(record.Full, trigger)
	if !w.probeCalls(r, true) {
		return
	}
	if r.Status != record.Failed {
		w.structure(r)
	}
	w.finish(r, &w.status.LastFull)
	// An invalid snapshot would make the next probe trigger a "first"
	// capture: keep the previous reference instead.
	if s := snapshotOf(r); r.HasStructure() && s.valid {
		w.lastFull = s
	}
}

// structure runs the full-capture calls. Indices are read before shards so
// that shards of an index created in between are the exception, resolved by
// the ingestion at the next capture (SPEC §5.5).
func (w *worker) structure(r *record.Record) {
	ctx, cancel := w.ctx()
	defer cancel()
	client := w.client
	partial := func(what string, err error) {
		r.Status = record.Partial
		if r.Error != "" {
			r.Error += "; "
		}
		r.Error += what + ": " + err.Error()
	}

	nodes, st, err := client.Nodes(ctx)
	r.Calls = append(r.Calls, st)
	if err != nil {
		partial("nodes", err)
	} else {
		r.Nodes = nodes
	}

	indices := []esclient.CatIndex{}
	st, err = client.CatIndices(ctx, func(ci esclient.CatIndex) error { indices = append(indices, ci); return nil })
	r.Calls = append(r.Calls, st)
	if err != nil {
		partial("cat_indices", err)
	} else {
		r.Indices = indices
	}

	settings := []record.IndexSettings{}
	st, err = client.IndexSettings(ctx, w.cfg.SettingsWhitelist, func(ix string, s map[string]string) error {
		settings = append(settings, record.IndexSettings{Index: ix, Settings: s})
		return nil
	})
	r.Calls = append(r.Calls, st)
	if err != nil {
		// Settings unknown this time: the ingestion keeps the previous ones.
		if r.Error != "" {
			r.Error += "; "
		}
		r.Error += "index_settings: " + err.Error()
	} else {
		r.IndexSettings = settings
	}

	shards := []esclient.CatShard{}
	st, err = client.CatShards(ctx, func(s esclient.CatShard) error { shards = append(shards, s); return nil })
	r.Calls = append(r.Calls, st)
	if err != nil {
		partial("cat_shards", err)
	} else {
		r.Shards = shards
	}
	if r.Status == record.Partial { // only the structural calls make a full capture partial
		// Nothing structural may be closed from an incomplete picture.
		r.Nodes, r.Indices, r.Shards = nil, nil, nil
	}
}

// finish stamps, sorts and hands the record over, and updates the status.
func (w *worker) finish(r *record.Record, slot **CaptureStatus) {
	r.EndMs = w.now()
	r.Sort()
	err := w.c.sink.Record(r, w.stop)
	cs := &CaptureStatus{StartMs: r.StartMs, DurationMs: r.EndMs - r.StartMs, Status: r.Status, Trigger: r.Trigger,
		Error: r.Error, Calls: r.Calls}
	w.mu.Lock()
	*slot = cs
	if r.Error != "" {
		w.addError(fmt.Sprintf("%s %s: %s", time.UnixMilli(r.StartMs).UTC().Format(time.RFC3339), r.Kind, r.Error))
	}
	if err != nil {
		w.addError("record: " + err.Error())
	}
	w.mu.Unlock()
	if err != nil {
		w.c.log.Error("record not stored", "cluster", w.cfg.ID, "err", err)
	}
}

func (w *worker) addError(e string) {
	w.status.RecentErrors = append(w.status.RecentErrors, e)
	if n := len(w.status.RecentErrors); n > 10 {
		w.status.RecentErrors = w.status.RecentErrors[n-10:]
	}
}
