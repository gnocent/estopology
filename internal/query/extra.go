package query

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"estopo/internal/model"
)

// LastKnownNodes returns, for node names missing at `at`, their last known
// description during the reference window (machine, attributes, roles), so
// that the physical view can show them where they used to be.
func (e *Engine) LastKnownNodes(ctx context.Context, cluster string, names []string, at int64) (map[string]model.Node, error) {
	out := map[string]model.Node{}
	if len(names) == 0 {
		return out, nil
	}
	parts, err := e.partitions(ctx, cluster, at-referenceWindow.Milliseconds(), at)
	if err != nil {
		return nil, err
	}
	in := strings.TrimSuffix(strings.Repeat("?,", len(names)), ",")
	for _, p := range parts { // oldest first: later partitions override
		args := []any{at}
		for _, n := range names {
			args = append(args, n)
		}
		var rows []struct {
			n     model.Node
			attrs string
		}
		if err := query(ctx, p.db(), `SELECT n.name, n.es_id, n.generation, i.roles, i.ip, i.host, i.machine, i.es_version, i.attrs_blob
			FROM node_info i JOIN node n ON n.id = i.node_id WHERE i.valid_from <= ? AND n.name IN (`+in+`)
			ORDER BY i.valid_from`, args, func(r *sql.Rows) error {
			var x model.Node
			var roles, attrs string
			var ip, host, machine, version sql.NullString
			if err := r.Scan(&x.Name, &x.ESID, &x.Generation, &roles, &ip, &host, &machine, &version, &attrs); err != nil {
				return err
			}
			if roles != "" {
				x.Roles = strings.Split(roles, ",")
			}
			x.Tiers = model.TiersOf(x.Roles)
			x.IP, x.Host, x.Machine, x.Version = ip.String, host.String, machine.String, version.String
			rows = append(rows, struct {
				n     model.Node
				attrs string
			}{x, attrs})
			return nil
		}); err != nil {
			return nil, err
		}
		for _, row := range rows {
			x := row.n
			x.Attrs = map[string]string{}
			blobJSON(ctx, p, row.attrs, &x.Attrs)
			out[x.Name] = x
		}
	}
	return out, nil
}

// CopySize is the last known size of a shard copy.
type CopySize struct {
	Docs       int64 `json:"docs"`
	StoreBytes int64 `json:"store_bytes"`
}

// ShardSizes returns the sizes of the copies of an index at `at`, keyed by
// "shard/p|r/node name".
func (e *Engine) ShardSizes(ctx context.Context, cluster, uuid string, at int64) (map[string]CopySize, error) {
	p, tc, err := e.lastCapture(ctx, cluster, at, "kind = 'full' AND status = 'ok'")
	if err != nil || tc == 0 {
		return nil, err
	}
	out := map[string]CopySize{}
	err = query(ctx, p.db(), `SELECT m.shard, m.prirep, n.name, max(m.ts), coalesce(m.docs, -1), coalesce(m.store_bytes, -1)
		FROM shard_metric m JOIN idx i ON i.id = m.index_id JOIN node n ON n.id = m.node_id
		WHERE i.uuid = ? AND m.ts <= ? GROUP BY m.shard, m.prirep, m.node_id`, []any{uuid, tc}, func(r *sql.Rows) error {
		var shard int
		var prirep, node string
		var ts int64
		var s CopySize
		if err := r.Scan(&shard, &prirep, &node, &ts, &s.Docs, &s.StoreBytes); err != nil {
			return err
		}
		out[CopyKey(shard, prirep == "p", node)] = s
		return nil
	})
	return out, err
}

// CopyKey identifies a copy in ShardSizes.
func CopyKey(shard int, primary bool, node string) string {
	pr := "r"
	if primary {
		pr = "p"
	}
	return strconv.Itoa(shard) + "/" + pr + "/" + node
}
