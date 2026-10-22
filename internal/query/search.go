package query

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"sync"
	"time"
)

// Search (SPEC §8.2.9): nodes, IPs, machines, index names and UUIDs,
// families. A catalogue of names is built per cluster from every partition
// and refreshed when captures arrive.

// Hit is one completion.
type Hit struct {
	Cluster string `json:"cluster"`
	Kind    string `json:"kind"` // node, ip, machine, index, uuid, family
	Value   string `json:"value"`
	Label   string `json:"label,omitempty"` // e.g. the index name for a uuid
}

type catalogue struct {
	built  time.Time
	lastTs int64
	gen    uint64 // engine generation it was built with
	items  []Hit  // sorted by lower-cased value
	lower  []string
}

type searchIndex struct {
	mu   sync.Mutex
	byCl map[string]*catalogue
}

const catalogueTTL = time.Minute

// Search returns up to limit completions for q (case-insensitive prefix
// matches first, then substring matches) over the given clusters.
func (e *Engine) Search(ctx context.Context, clusters []string, q string, limit int) ([]Hit, error) {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var prefix, sub []Hit
	for _, cl := range clusters {
		cat, err := e.catalogue(ctx, cl)
		if err != nil {
			return nil, err
		}
		i := sort.SearchStrings(cat.lower, q)
		for ; i < len(cat.lower) && strings.HasPrefix(cat.lower[i], q) && len(prefix) < limit; i++ {
			prefix = append(prefix, cat.items[i])
		}
		if len(prefix) < limit && len(q) >= 3 {
			for j, v := range cat.lower {
				if len(prefix)+len(sub) >= limit {
					break
				}
				if !strings.HasPrefix(v, q) && strings.Contains(v, q) {
					sub = append(sub, cat.items[j])
				}
			}
		}
	}
	out := append(prefix, sub...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (e *Engine) catalogue(ctx context.Context, cluster string) (*catalogue, error) {
	e.mu.Lock()
	if e.search == nil {
		e.search = &searchIndex{byCl: map[string]*catalogue{}}
	}
	si := e.search
	e.mu.Unlock()

	last, err := e.LastCaptureTs(ctx, cluster)
	if err != nil {
		return nil, err
	}
	si.mu.Lock()
	defer si.mu.Unlock()
	gen := e.Generation() // family rules changed: rebuild
	if c := si.byCl[cluster]; c != nil && c.gen == gen && (c.lastTs == last || time.Since(c.built) < catalogueTTL) {
		return c, nil
	}
	parts, err := e.partitions(ctx, cluster, 0, 1<<62)
	if err != nil {
		return nil, err
	}
	seen := map[Hit]bool{}
	add := func(h Hit) {
		if h.Value != "" && !seen[h] {
			seen[h] = true
		}
	}
	for _, p := range parts {
		db := p.db()
		if err := query(ctx, db, `SELECT DISTINCT name FROM node`, nil, func(r *sql.Rows) error {
			var v string
			r.Scan(&v)
			add(Hit{Cluster: cluster, Kind: "node", Value: v})
			return nil
		}); err != nil {
			return nil, err
		}
		if err := query(ctx, db, `SELECT DISTINCT i.ip, i.machine, n.name FROM node_info i JOIN node n ON n.id = i.node_id`, nil,
			func(r *sql.Rows) error {
				var ip, machine, node sql.NullString
				r.Scan(&ip, &machine, &node)
				add(Hit{Cluster: cluster, Kind: "ip", Value: ip.String, Label: node.String})
				add(Hit{Cluster: cluster, Kind: "machine", Value: machine.String})
				return nil
			}); err != nil {
			return nil, err
		}
		if err := query(ctx, db, `SELECT uuid, name, family FROM idx`, nil, func(r *sql.Rows) error {
			var uuid, name, family string
			r.Scan(&uuid, &name, &family)
			add(Hit{Cluster: cluster, Kind: "index", Value: name, Label: uuid})
			add(Hit{Cluster: cluster, Kind: "uuid", Value: uuid, Label: name})
			add(Hit{Cluster: cluster, Kind: "family", Value: e.familyOf(cluster, name, family)})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	c := &catalogue{built: time.Now(), lastTs: last, gen: gen}
	for h := range seen {
		c.items = append(c.items, h)
	}
	kindRank := map[string]int{"node": 0, "machine": 1, "family": 2, "index": 3, "ip": 4, "uuid": 5}
	sort.Slice(c.items, func(i, j int) bool {
		a, b := strings.ToLower(c.items[i].Value), strings.ToLower(c.items[j].Value)
		if a != b {
			return a < b
		}
		if c.items[i].Kind != c.items[j].Kind {
			return kindRank[c.items[i].Kind] < kindRank[c.items[j].Kind]
		}
		return c.items[i].Label < c.items[j].Label
	})
	c.lower = make([]string, len(c.items))
	for i, h := range c.items {
		c.lower[i] = strings.ToLower(h.Value)
	}
	si.byCl[cluster] = c
	return c, nil
}
