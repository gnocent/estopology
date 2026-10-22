package query

import (
	"context"
	"database/sql"
	"strings"
)

// GroupMetricPoint is what a set of nodes measured around one date:
// percentages are the mean of the nodes, sizes and shards their sum.
type GroupMetricPoint struct {
	Ts        int64   `json:"ts"`
	Nodes     int     `json:"nodes"` // nodes measured
	ProcCPU   float64 `json:"proc_cpu"`
	OSCPU     float64 `json:"os_cpu"`
	Load1     float64 `json:"load1"`
	HeapPct   float64 `json:"heap_pct"`
	DiskUsed  int64   `json:"disk_used"`
	DiskTotal int64   `json:"disk_total"`
	Shards    int64   `json:"shards"`
}

// maxGroupPoints bounds the slices of GroupMetrics (one map each).
const maxGroupPoints = 10000

// GroupMetrics gathers the node metrics of the named nodes between a and b
// in `points` slices of equal length; a slice where no node counts is left
// out. In a slice, each node counts for the mean of its readings or, without
// reading, for its last known one while it is present: the dead band writes
// nothing as long as the values hold.
func (e *Engine) GroupMetrics(ctx context.Context, cluster string, nodes []string, a, b int64, points int) ([]GroupMetricPoint, error) {
	out := []GroupMetricPoint{}
	a, b = clampWindow(a, b)
	if len(nodes) == 0 || b <= a || points < 1 {
		return out, nil
	}
	points = min(points, maxGroupPoints)
	parts, err := e.partitions(ctx, cluster, a, b)
	if err != nil {
		return nil, err
	}
	type reading struct {
		ts int64
		v  [7]sql.NullFloat64
	}
	type acc struct {
		n    [7]int
		v    [7]float64
		last [7]sql.NullFloat64 // last reading of the slice
	}
	slices := make([]map[string]*acc, points)
	width := float64(b-a) / float64(points)
	names := make([]any, 0, len(nodes))
	unique := map[string]bool{}
	for _, n := range nodes {
		if !unique[n] {
			unique[n] = true
			names = append(names, n)
		}
	}
	with := func(extra ...any) []any {
		return append(append(make([]any, 0, len(names)+len(extra)), names...), extra...)
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	seen := map[string]int64{}               // last reading of each node: keyframes copy it
	before := map[string]reading{}           // last reading of each node before a
	presence := map[string]map[int64]int64{} // node -> start -> end (0 = open)
	for _, p := range parts {
		db := p.db()
		if err := query(ctx, db, `SELECT n.name, max(m.ts), m.proc_cpu, m.os_cpu, m.load1, m.heap_pct, m.disk_used, m.disk_total, m.shards
			FROM node_metric m JOIN node n ON n.id = m.node_id WHERE n.name IN (`+in+`) AND m.ts < ? GROUP BY m.node_id`,
			with(a), func(r *sql.Rows) error {
				var name string
				var rd reading
				if err := r.Scan(&name, &rd.ts, &rd.v[0], &rd.v[1], &rd.v[2], &rd.v[3], &rd.v[4], &rd.v[5], &rd.v[6]); err != nil {
					return err
				}
				if old, ok := before[name]; !ok || rd.ts >= old.ts {
					before[name] = rd
				}
				return nil
			}); err != nil {
			return nil, err
		}
		if err := query(ctx, db, `SELECT n.name, p.valid_from, p.valid_to FROM node_presence p JOIN node n ON n.id = p.node_id
			WHERE n.name IN (`+in+`) AND p.valid_from <= ? AND (p.valid_to IS NULL OR p.valid_to > ?)`,
			with(b, a), func(r *sql.Rows) error {
				var name string
				var from int64
				var to sql.NullInt64
				if err := r.Scan(&name, &from, &to); err != nil {
					return err
				}
				if presence[name] == nil {
					presence[name] = map[int64]int64{}
				}
				presence[name][from] = to.Int64 // later partition knows better (keyframe copy)
				return nil
			}); err != nil {
			return nil, err
		}
		if err := query(ctx, db, `SELECT n.name, m.ts, m.proc_cpu, m.os_cpu, m.load1, m.heap_pct, m.disk_used, m.disk_total, m.shards
			FROM node_metric m JOIN node n ON n.id = m.node_id WHERE n.name IN (`+in+`) AND m.ts >= ? AND m.ts <= ? ORDER BY m.ts`,
			with(a, b), func(r *sql.Rows) error {
				var name string
				var ts int64
				var v [7]sql.NullFloat64
				if err := r.Scan(&name, &ts, &v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6]); err != nil {
					return err
				}
				if last, ok := seen[name]; ok && ts <= last {
					return nil
				}
				seen[name] = ts
				i := min(max(int(float64(ts-a)/width), 0), points-1)
				if slices[i] == nil {
					slices[i] = map[string]*acc{}
				}
				x := slices[i][name]
				if x == nil {
					x = &acc{}
					slices[i][name] = x
				}
				for k := range v {
					if v[k].Valid && v[k].Float64 >= 0 {
						x.n[k]++
						x.v[k] += v[k].Float64
					}
				}
				x.last = v
				return nil
			}); err != nil {
			return nil, err
		}
	}
	present := func(name string, from, to int64) bool {
		for start, end := range presence[name] {
			if start < to && (end == 0 || end > from) {
				return true
			}
		}
		return false
	}
	carry := map[string][7]sql.NullFloat64{} // last known reading of each node
	for name, rd := range before {
		carry[name] = rd.v
	}
	for i, s := range slices {
		from, to := a+int64(float64(i)*width), a+int64(float64(i+1)*width)
		var sum [7]float64 // sum over the nodes of their mean
		var cnt [7]int
		n := 0
		for _, x := range names {
			name := x.(string)
			if r := s[name]; r != nil {
				for k := range r.n {
					if r.n[k] > 0 {
						sum[k] += r.v[k] / float64(r.n[k])
						cnt[k]++
					}
				}
				carry[name] = r.last
			} else if v, ok := carry[name]; ok && present(name, from, to) {
				for k := range v {
					if v[k].Valid && v[k].Float64 >= 0 {
						sum[k] += v[k].Float64
						cnt[k]++
					}
				}
			} else {
				continue
			}
			n++
		}
		if n == 0 {
			continue
		}
		pt := GroupMetricPoint{Ts: a + int64((float64(i)+0.5)*width), Nodes: n}
		mean := func(k int) float64 {
			if cnt[k] == 0 {
				return -1
			}
			return sum[k] / float64(cnt[k])
		}
		pt.ProcCPU, pt.OSCPU, pt.Load1, pt.HeapPct = mean(0), mean(1), mean(2), mean(3)
		pt.DiskUsed, pt.DiskTotal, pt.Shards = int64(sum[4]), int64(sum[5]), int64(sum[6])
		out = append(out, pt)
	}
	return out, nil
}
