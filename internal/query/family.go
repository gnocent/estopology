package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
)

// FamilyPoint is the size of a family at one date: what its indices held at
// their last reading before that date.
type FamilyPoint struct {
	Ts         int64 `json:"ts"`
	Docs       int64 `json:"docs"`
	StoreBytes int64 `json:"store_bytes"`
	// PriStoreBytes is the size of the primaries alone.
	PriStoreBytes int64 `json:"pri_store_bytes"`
	Indices       int   `json:"indices"`
}

// IndexSizeHistory is FamilyHistory for one index.
func (e *Engine) IndexSizeHistory(ctx context.Context, cluster, uuid string, a, b int64, points int) ([]FamilyPoint, error) {
	return e.sizeHistory(ctx, cluster, a, b, points, func(_, _, u string) bool { return u == uuid })
}

// FamilyHistory samples the documents and the size of a family between a and
// b, at `points` dates evenly spread (b included). An index counts from its
// first interval to its deletion, with its last known metric.
func (e *Engine) FamilyHistory(ctx context.Context, cluster, family string, a, b int64, points int) ([]FamilyPoint, error) {
	return e.sizeHistory(ctx, cluster, a, b, points, func(name, stored, _ string) bool { return e.familyOf(cluster, name, stored) == family })
}

// familyUUIDs returns the uuids of the indices of a partition that belong to
// a family under the rules in force (the stored family is the automatic
// one), as a JSON array: one SQL parameter whatever their number, read with
// json_each.
func (e *Engine) familyUUIDs(ctx context.Context, cluster string, p part, family string) (string, error) {
	uuids := []string{}
	if err := query(ctx, p.db(), `SELECT uuid, name, family FROM idx`, nil, func(r *sql.Rows) error {
		var uuid, name, stored string
		if err := r.Scan(&uuid, &name, &stored); err != nil {
			return err
		}
		if e.familyOf(cluster, name, stored) == family {
			uuids = append(uuids, uuid)
		}
		return nil
	}); err != nil {
		return "", err
	}
	data, err := json.Marshal(uuids)
	return string(data), err
}

// sizeHistory samples the documents and sizes of the indices kept by keep
// (name, stored family, uuid).
func (e *Engine) sizeHistory(ctx context.Context, cluster string, a, b int64, points int, keep func(name, stored, uuid string) bool) ([]FamilyPoint, error) {
	if points < 2 {
		points = 2
	}
	a, b = clampWindow(a, b)
	if b <= a {
		return []FamilyPoint{}, nil
	}
	parts, err := e.partitions(ctx, cluster, a, b)
	if err != nil {
		return nil, err
	}
	out := make([]FamilyPoint, 0, points)
	step := float64(b-a) / float64(points-1)
	next := 0 // next sample to produce
	sampleTs := func(i int) int64 { return a + int64(math.Round(float64(i)*step)) }

	for pi, p := range parts {
		db := p.db()
		// The indices kept, in this partition.
		member := map[int64]bool{}
		if err := query(ctx, db, `SELECT id, name, family, uuid FROM idx`, nil, func(r *sql.Rows) error {
			var id int64
			var name, stored, uuid string
			if err := r.Scan(&id, &name, &stored, &uuid); err != nil {
				return err
			}
			if keep(name, stored, uuid) {
				member[id] = true
			}
			return nil
		}); err != nil {
			return nil, err
		}
		// Samples of this partition: up to its end, or to b for the last one.
		end := p.info.ToMs
		if pi == len(parts)-1 || end > b {
			end = b + 1
		}
		if len(member) == 0 {
			for next < points && sampleTs(next) < end {
				out = append(out, FamilyPoint{Ts: sampleTs(next)})
				next++
			}
			continue
		}

		type life struct{ from, to int64 }
		lives := map[int64]life{}
		if err := query(ctx, db, `SELECT index_id, min(valid_from), max(coalesce(valid_to, 9223372036854775807)) FROM index_iv GROUP BY index_id`, nil,
			func(r *sql.Rows) error {
				var id int64
				var l life
				if err := r.Scan(&id, &l.from, &l.to); err != nil {
					return err
				}
				if member[id] {
					lives[id] = l
				}
				return nil
			}); err != nil {
			return nil, err
		}

		type metric struct {
			id, ts, docs, store, pri int64
		}
		var metrics []metric
		if err := query(ctx, db, `SELECT index_id, ts, coalesce(docs, -1), coalesce(store_bytes, -1), coalesce(pri_store_bytes, -1) FROM index_metric WHERE ts <= ? ORDER BY ts`,
			[]any{b}, func(r *sql.Rows) error {
				var m metric
				if err := r.Scan(&m.id, &m.ts, &m.docs, &m.store, &m.pri); err != nil {
					return err
				}
				if member[m.id] {
					metrics = append(metrics, m)
				}
				return nil
			}); err != nil {
			return nil, err
		}
		sort.SliceStable(metrics, func(i, j int) bool { return metrics[i].ts < metrics[j].ts })

		// Sweep: the last metric of each index, read at each sample date.
		last := map[int64]metric{}
		mi := 0
		for next < points && sampleTs(next) < end {
			ts := sampleTs(next)
			for mi < len(metrics) && metrics[mi].ts <= ts {
				last[metrics[mi].id] = metrics[mi]
				mi++
			}
			pt := FamilyPoint{Ts: ts}
			for id, l := range lives {
				if ts < l.from || ts >= l.to {
					continue
				}
				pt.Indices++
				if m, ok := last[id]; ok {
					if m.docs > 0 {
						pt.Docs += m.docs
					}
					if m.store > 0 {
						pt.StoreBytes += m.store
					}
					if m.pri > 0 {
						pt.PriStoreBytes += m.pri
					}
				}
			}
			out = append(out, pt)
			next++
		}
	}
	return out, nil
}
