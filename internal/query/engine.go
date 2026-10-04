// Package query answers the questions of the UI from the stored history
// (SPEC §6.4, §7, §8): state at a date, differences and path between two
// dates, losses, events, search. Partitions are read through the store;
// reconstructed states are cached.
package query

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"

	"estopo/internal/store"
)

// Engine runs the queries.
type Engine struct {
	st  *store.Store
	log *slog.Logger

	mu         sync.Mutex
	structures *lru // structure halves by (cluster, full capture): large, few
	probes     *lru // probe halves by (cluster, probe): small, many
	search     *searchIndex
	generation atomic.Uint64 // bumped by Invalidate (derived caches follow it)

	// family, when set, overrides the stored family of an index (per-cluster
	// regex rules, SPEC §7.3).
	family func(cluster, index string) (string, bool)
}

// New returns an engine keeping up to structures reconstructed structures
// (a few per cluster: 250k shards take ~20 MB) and many probe halves.
func New(st *store.Store, log *slog.Logger, structures int) *Engine {
	if structures <= 0 {
		structures = 8
	}
	return &Engine{st: st, log: log, structures: newLRU(structures), probes: newLRU(256)}
}

// ErrNoData is returned when a cluster has no capture before the date.
var ErrNoData = fmt.Errorf("no data for this cluster at this date")

// part is an open partition with its catalogue entry.
type part struct {
	info store.PartitionInfo
	p    *store.Partition
}

func (x part) db() *sql.DB { return x.p.DB() }

// partitions returns the partitions of a cluster overlapping [from, to],
// oldest first.
func (e *Engine) partitions(ctx context.Context, cluster string, from, to int64) ([]part, error) {
	infos, err := e.st.Partitions(ctx, cluster)
	if err != nil {
		return nil, err
	}
	var out []part
	for _, info := range infos {
		if info.ToMs <= from || info.FromMs > to {
			continue
		}
		p, err := e.st.OpenExisting(ctx, info)
		if err != nil {
			e.log.Warn("partition unavailable", "path", info.Path, "err", err)
			continue
		}
		out = append(out, part{info: info, p: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].info.FromMs < out[j].info.FromMs })
	return out, nil
}

// maxTs is the latest date a query window may reach ("no upper bound").
const maxTs = 1 << 62

// clampWindow brings a window given by a caller into [0, maxTs], so that
// b-a and the bucket arithmetic built on it cannot overflow.
func clampWindow(a, b int64) (int64, int64) {
	return min(max(a, 0), maxTs), min(max(b, 0), maxTs)
}

// lastCapture finds, walking back from `at`, the last capture matching cond
// (a SQL condition on the capture table); it returns its partition and ts.
func (e *Engine) lastCapture(ctx context.Context, cluster string, at int64, cond string) (part, int64, error) {
	infos, err := e.st.Partitions(ctx, cluster)
	if err != nil {
		return part{}, 0, err
	}
	for i := len(infos) - 1; i >= 0; i-- {
		info := infos[i]
		if info.FromMs > at {
			continue
		}
		p, err := e.st.OpenExisting(ctx, info)
		if err != nil {
			continue
		}
		var ts sql.NullInt64
		if err := p.DB().QueryRowContext(ctx, `SELECT max(ts) FROM capture WHERE ts <= ? AND `+cond, at).Scan(&ts); err != nil {
			return part{}, 0, err
		}
		if ts.Valid {
			return part{info: info, p: p}, ts.Int64, nil
		}
	}
	return part{}, 0, nil
}

// Clusters lists the clusters that have history.
func (e *Engine) Clusters(ctx context.Context) ([]string, error) {
	infos, err := e.st.Partitions(ctx, "")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, i := range infos {
		if !seen[i.Cluster] {
			seen[i.Cluster] = true
			out = append(out, i.Cluster)
		}
	}
	sort.Strings(out)
	return out, nil
}

// LastCaptureTs returns the date of the last capture of a cluster (any
// status), 0 if none.
func (e *Engine) LastCaptureTs(ctx context.Context, cluster string) (int64, error) {
	_, ts, err := e.lastCapture(ctx, cluster, 1<<62, "1=1")
	return ts, err
}

// firstFullCapture returns the date of the oldest successful full capture
// of a cluster, 0 if none.
func (e *Engine) firstFullCapture(ctx context.Context, cluster string) (int64, error) {
	infos, err := e.st.Partitions(ctx, cluster)
	if err != nil {
		return 0, err
	}
	for _, info := range infos {
		p, err := e.st.OpenExisting(ctx, info)
		if err != nil {
			continue
		}
		var ts sql.NullInt64
		if err := p.DB().QueryRowContext(ctx, `SELECT min(ts) FROM capture WHERE kind = 'full' AND status = 'ok'`).Scan(&ts); err != nil {
			return 0, err
		}
		if ts.Valid {
			return ts.Int64, nil
		}
	}
	return 0, nil
}

// SetFamilyFunc installs the family override (regex rules per cluster) and
// drops the cached states.
func (e *Engine) SetFamilyFunc(f func(cluster, index string) (string, bool)) {
	e.mu.Lock()
	e.family = f
	e.mu.Unlock()
	e.Invalidate()
}

// Invalidate drops every cached state (configuration changed).
func (e *Engine) Invalidate() {
	e.generation.Add(1)
	e.structures.clear()
	e.probes.clear()
}

func (e *Engine) familyOf(cluster, index, stored string) string {
	e.mu.Lock()
	f := e.family
	e.mu.Unlock()
	if f != nil {
		if fam, ok := f(cluster, index); ok {
			return fam
		}
	}
	return stored
}

// Generation changes each time the caches are invalidated (configuration
// change): caches of derived results key on it.
func (e *Engine) Generation() uint64 { return e.generation.Load() }
