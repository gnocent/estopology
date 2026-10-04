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
// in `points` slices of equal length; a slice without reading is left out.
// In a slice, each node counts for the mean of its readings.
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
	type acc struct {
		n [7]int
		v [7]float64
	}
	slices := make([]map[string]*acc, points)
	width := float64(b-a) / float64(points)
	args := make([]any, 0, len(nodes)+2)
	for _, n := range nodes {
		args = append(args, n)
	}
	args = append(args, a, b)
	in := strings.TrimSuffix(strings.Repeat("?,", len(nodes)), ",")
	seen := map[string]int64{} // last reading of each node: keyframes copy it
	for _, p := range parts {
		if err := query(ctx, p.db(), `SELECT n.name, m.ts, m.proc_cpu, m.os_cpu, m.load1, m.heap_pct, m.disk_used, m.disk_total, m.shards
			FROM node_metric m JOIN node n ON n.id = m.node_id WHERE n.name IN (`+in+`) AND m.ts >= ? AND m.ts <= ? ORDER BY m.ts`,
			args, func(r *sql.Rows) error {
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
				return nil
			}); err != nil {
			return nil, err
		}
	}
	for i, s := range slices {
		if len(s) == 0 {
			continue
		}
		pt := GroupMetricPoint{Ts: a + int64((float64(i)+0.5)*width), Nodes: len(s)}
		var sum [7]float64 // sum over the nodes of their mean
		var cnt [7]int
		for _, x := range s {
			for k := range x.n {
				if x.n[k] > 0 {
					sum[k] += x.v[k] / float64(x.n[k])
					cnt[k]++
				}
			}
		}
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
