package query

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
)

// Interval is a generic validity interval ([From, To), To = 0 when open).
type Interval struct {
	FromMs int64 `json:"from"`
	ToMs   int64 `json:"to,omitempty"`
}

// NodeHistory is the history of a node name over [A, B] (SPEC §8.2.3).
type NodeHistory struct {
	Name        string           `json:"name"`
	Generations []Generation     `json:"generations"`
	Infos       []NodeInfoPeriod `json:"infos"`
	Metrics     []MetricPoint    `json:"metrics"`
}

// Generation is one life of a node name (one ES id).
type Generation struct {
	ESID       string     `json:"es_id"`
	Generation int        `json:"generation"`
	FirstSeen  int64      `json:"first_seen"`
	Presence   []Interval `json:"presence"`
}

// NodeInfoPeriod is a period with constant node attributes and versions.
type NodeInfoPeriod struct {
	Interval
	ESID    string            `json:"es_id"`
	Roles   string            `json:"roles"`
	IP      string            `json:"ip"`
	Machine string            `json:"machine"`
	Version string            `json:"version"`
	OS      string            `json:"os"`
	Kernel  string            `json:"kernel"`
	JVM     string            `json:"jvm"`
	StartMs int64             `json:"start_ms"`
	Attrs   map[string]string `json:"attrs,omitempty"` // node.attr.*
}

// MetricPoint is one node metric sample (-1 = unknown).
type MetricPoint struct {
	Ts        int64   `json:"ts"`
	ProcCPU   int     `json:"proc_cpu"`
	OSCPU     int     `json:"os_cpu"`
	Load1     float64 `json:"load1"`
	HeapPct   int     `json:"heap_pct"`
	DiskUsed  int64   `json:"disk_used"`
	DiskTotal int64   `json:"disk_total"`
	Shards    int64   `json:"shards"`
}

// mergeIntervals merges intervals copied by keyframes (same start).
func mergeIntervals(in []Interval) []Interval {
	sort.Slice(in, func(i, j int) bool { return in[i].FromMs < in[j].FromMs })
	var out []Interval
	for _, iv := range in {
		if n := len(out); n > 0 && out[n-1].FromMs == iv.FromMs {
			out[n-1].ToMs = iv.ToMs // later partition knows better
			continue
		}
		out = append(out, iv)
	}
	return out
}

// NodeHistory returns the generations, presence, attribute periods and
// metrics (at most maxPoints, evenly sampled) of a node name.
func (e *Engine) NodeHistory(ctx context.Context, cluster, name string, a, b int64, maxPoints int) (*NodeHistory, error) {
	parts, err := e.partitions(ctx, cluster, a, b)
	if err != nil {
		return nil, err
	}
	h := &NodeHistory{Name: name}
	gens := map[string]*Generation{}
	infos := map[string]NodeInfoPeriod{}
	for _, p := range parts {
		db := p.db()
		attrHashes := map[string]string{}
		if err := query(ctx, db, `SELECT n.es_id, n.generation, n.first_seen, pr.valid_from, pr.valid_to FROM node n
			LEFT JOIN node_presence pr ON pr.node_id = n.id AND pr.valid_from < ? AND (pr.valid_to IS NULL OR pr.valid_to > ?)
			WHERE n.name = ?`, []any{b, a, name}, func(r *sql.Rows) error {
			var esID string
			var gen int
			var first int64
			var from, to sql.NullInt64
			if err := r.Scan(&esID, &gen, &first, &from, &to); err != nil {
				return err
			}
			g := gens[esID]
			if g == nil {
				g = &Generation{ESID: esID, Generation: gen, FirstSeen: first}
				gens[esID] = g
			}
			if from.Valid {
				g.Presence = append(g.Presence, Interval{FromMs: from.Int64, ToMs: to.Int64})
			}
			return nil
		}); err != nil {
			return nil, err
		}
		if err := query(ctx, db, `SELECT n.es_id, i.valid_from, i.valid_to, i.roles, i.ip, i.machine, i.es_version, i.os_name,
			i.os_kernel, i.jvm_version, i.start_ms, coalesce(i.attrs_blob, '') FROM node_info i JOIN node n ON n.id = i.node_id
			WHERE n.name = ? AND i.valid_from < ? AND (i.valid_to IS NULL OR i.valid_to > ?)`, []any{name, b, a},
			func(r *sql.Rows) error {
				var ip NodeInfoPeriod
				var to sql.NullInt64
				var s [7]sql.NullString
				var start sql.NullInt64
				var attrs string
				if err := r.Scan(&ip.ESID, &ip.FromMs, &to, &s[0], &s[1], &s[2], &s[3], &s[4], &s[5], &s[6], &start, &attrs); err != nil {
					return err
				}
				ip.ToMs, ip.StartMs = to.Int64, start.Int64
				ip.Roles, ip.IP, ip.Machine, ip.Version, ip.OS, ip.Kernel, ip.JVM = s[0].String, s[1].String, s[2].String, s[3].String, s[4].String, s[5].String, s[6].String
				k := ip.ESID + "|" + strconv.FormatInt(ip.FromMs, 10)
				infos[k] = ip // later partition overrides (keyframe copies)
				if attrs != "" {
					attrHashes[k] = attrs
				}
				return nil
			}); err != nil {
			return nil, err
		}
		// Blobs are read once the rows are closed (one connection per partition).
		for k, hash := range attrHashes {
			ip := infos[k]
			if err := blobJSON(ctx, p, hash, &ip.Attrs); err != nil {
				return nil, err
			}
			infos[k] = ip
		}
		if err := query(ctx, db, `SELECT m.ts, m.proc_cpu, m.os_cpu, m.load1, m.heap_pct, m.disk_used, m.disk_total, m.shards
			FROM node_metric m JOIN node n ON n.id = m.node_id WHERE n.name = ? AND m.ts >= ? AND m.ts <= ? ORDER BY m.ts`,
			[]any{name, a, b}, func(r *sql.Rows) error {
				var mp MetricPoint
				var v [7]sql.NullFloat64
				if err := r.Scan(&mp.Ts, &v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6]); err != nil {
					return err
				}
				f := func(i int) float64 {
					if !v[i].Valid {
						return -1
					}
					return v[i].Float64
				}
				mp.ProcCPU, mp.OSCPU, mp.Load1, mp.HeapPct = int(f(0)), int(f(1)), f(2), int(f(3))
				mp.DiskUsed, mp.DiskTotal, mp.Shards = int64(f(4)), int64(f(5)), int64(f(6))
				if n := len(h.Metrics); n > 0 && h.Metrics[n-1].Ts == mp.Ts {
					return nil // keyframe copy of the last value
				}
				h.Metrics = append(h.Metrics, mp)
				return nil
			}); err != nil {
			return nil, err
		}
	}
	for _, g := range gens {
		g.Presence = mergeIntervals(g.Presence)
		h.Generations = append(h.Generations, *g)
	}
	sort.Slice(h.Generations, func(i, j int) bool { return h.Generations[i].Generation < h.Generations[j].Generation })
	for _, ip := range infos {
		h.Infos = append(h.Infos, ip)
	}
	sort.Slice(h.Infos, func(i, j int) bool { return h.Infos[i].FromMs < h.Infos[j].FromMs })
	h.Metrics = downsample(h.Metrics, maxPoints)
	return h, nil
}

func downsample(in []MetricPoint, n int) []MetricPoint {
	if n <= 0 || len(in) <= n {
		return in
	}
	out := make([]MetricPoint, 0, n)
	step := float64(len(in)) / float64(n)
	for i := 0; i < n; i++ {
		out = append(out, in[int(float64(i)*step)])
	}
	return out
}

// IndexPeriod is a period with constant index status and settings.
type IndexPeriod struct {
	Interval
	Status   string            `json:"status"`
	Pri      int               `json:"pri"`
	Rep      int               `json:"rep"`
	Settings map[string]string `json:"settings"`
}

// IndexSizePoint is one index metric sample.
type IndexSizePoint struct {
	Ts         int64 `json:"ts"`
	Docs       int64 `json:"docs"`
	StoreBytes int64 `json:"store_bytes"`
}

// IndexHistory is the settings history and sizes of an index (by UUID).
type IndexHistory struct {
	UUID       string           `json:"uuid"`
	Name       string           `json:"name"`
	Family     string           `json:"family"`
	CreationMs int64            `json:"creation_ms"`
	Periods    []IndexPeriod    `json:"periods"`
	Sizes      []IndexSizePoint `json:"sizes"`
}

// IndexHistory returns the history of an index over [a, b].
func (e *Engine) IndexHistory(ctx context.Context, cluster, uuid string, a, b int64) (*IndexHistory, error) {
	parts, err := e.partitions(ctx, cluster, a, b)
	if err != nil {
		return nil, err
	}
	h := &IndexHistory{UUID: uuid}
	periods := map[int64]IndexPeriod{}
	for _, p := range parts {
		db := p.db()
		var id int64
		var creation sql.NullInt64
		err := db.QueryRowContext(ctx, `SELECT id, name, family, creation_date FROM idx WHERE uuid = ?`, uuid).Scan(&id, &h.Name, &h.Family, &creation)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		h.CreationMs = creation.Int64
		var hashes []string
		var froms []int64
		if err := query(ctx, db, `SELECT valid_from, valid_to, status, pri, rep, coalesce(settings_blob, '') FROM index_iv
			WHERE index_id = ? AND valid_from < ? AND (valid_to IS NULL OR valid_to > ?)`, []any{id, b, a}, func(r *sql.Rows) error {
			var ip IndexPeriod
			var to sql.NullInt64
			var hash string
			if err := r.Scan(&ip.FromMs, &to, &ip.Status, &ip.Pri, &ip.Rep, &hash); err != nil {
				return err
			}
			ip.ToMs = to.Int64
			periods[ip.FromMs] = ip
			hashes, froms = append(hashes, hash), append(froms, ip.FromMs)
			return nil
		}); err != nil {
			return nil, err
		}
		for i, hash := range hashes {
			ip := periods[froms[i]]
			if err := blobJSON(ctx, p, hash, &ip.Settings); err != nil {
				return nil, err
			}
			periods[froms[i]] = ip
		}
		if err := query(ctx, db, `SELECT ts, coalesce(docs, -1), coalesce(store_bytes, -1) FROM index_metric
			WHERE index_id = ? AND ts >= ? AND ts <= ? ORDER BY ts`, []any{id, a, b}, func(r *sql.Rows) error {
			var sp IndexSizePoint
			if err := r.Scan(&sp.Ts, &sp.Docs, &sp.StoreBytes); err != nil {
				return err
			}
			if n := len(h.Sizes); n == 0 || h.Sizes[n-1].Ts != sp.Ts {
				h.Sizes = append(h.Sizes, sp)
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	if h.Name == "" {
		return nil, ErrNoData
	}
	for _, ip := range periods {
		h.Periods = append(h.Periods, ip)
	}
	sort.Slice(h.Periods, func(i, j int) bool { return h.Periods[i].FromMs < h.Periods[j].FromMs })
	return h, nil
}

// IndexLives returns the UUIDs that carried an index name (B11: a
// recreated index is another entity).
func (e *Engine) IndexLives(ctx context.Context, cluster, name string) ([]Hit, error) {
	parts, err := e.partitions(ctx, cluster, 0, 1<<62)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Hit
	for _, p := range parts {
		if err := query(ctx, p.db(), `SELECT uuid, coalesce(creation_date, 0) FROM idx WHERE name = ?`, []any{name}, func(r *sql.Rows) error {
			var uuid string
			var creation int64
			r.Scan(&uuid, &creation)
			if !seen[uuid] {
				seen[uuid] = true
				out = append(out, Hit{Cluster: e.clusterName(p), Kind: "uuid", Value: uuid, Label: strconv.FormatInt(creation, 10)})
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (e *Engine) clusterName(p part) string { return p.info.Cluster }
