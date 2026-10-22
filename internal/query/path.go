package query

import (
	"context"
	"database/sql"
	"sort"
)

// Segment is one validity interval of a shard copy (SPEC §7.7).
type Segment struct {
	Index       string `json:"index"`
	UUID        string `json:"uuid"`
	Shard       int    `json:"shard"`
	Primary     bool   `json:"primary"`
	Node        string `json:"node,omitempty"` // "" = unassigned
	Slot        int    `json:"slot,omitempty"`
	State       string `json:"state"`
	RelocTarget string `json:"reloc_target,omitempty"`
	Reason      string `json:"reason,omitempty"`
	FromMs      int64  `json:"from"`
	ToMs        int64  `json:"to,omitempty"` // 0 = still open
}

// PathQuery selects what to follow: one index (optionally one shard) or a
// whole family.
type PathQuery struct {
	UUID   string
	Family string
	Shard  int // -1 = every shard
	A, B   int64
	Limit  int
}

// PathResult is the path between A and B.
type PathResult struct {
	Segments []Segment `json:"segments"`
	// Truncated: more than Limit segments; the first ones are kept (oldest
	// partition first, in (uuid, shard, p/r, start) order within each), the
	// result is incomplete (narrow the query).
	Truncated bool `json:"truncated,omitempty"`
}

// Path returns the intervals of the copies between A and B, including the
// moves a simple A/B diff cannot see (B6). Intervals copied by keyframes
// into later partitions are merged.
func (e *Engine) Path(ctx context.Context, cluster string, q PathQuery) (*PathResult, error) {
	if q.Limit <= 0 || q.Limit > 100000 {
		q.Limit = 20000
	}
	parts, err := e.partitions(ctx, cluster, q.A, q.B)
	if err != nil {
		return nil, err
	}
	type key struct {
		uuid        string
		shard       int
		primary     bool
		node, state string
		slot        int
		from        int64
	}
	merged := map[key]*Segment{}
	var order []key
	res := &PathResult{}
	for _, p := range parts {
		cond, args := "i.uuid = ?", []any{q.UUID}
		if q.Family != "" {
			// The family under the rules in force, not the one stored (SPEC §7.3).
			uuids, err := e.familyUUIDs(ctx, cluster, p, q.Family)
			if err != nil {
				return nil, err
			}
			cond, args = "i.uuid IN (SELECT value FROM json_each(?))", []any{uuids}
		}
		// Valid at B included, as in the state at B (SPEC §6.4).
		sq := `SELECT i.uuid, i.name, s.shard, s.prirep, coalesce(n.name, ''), s.slot, s.state, coalesce(t.name, ''),
			coalesce(s.unassigned_reason, ''), s.valid_from, s.valid_to
			FROM shard_iv s JOIN idx i ON i.id = s.index_id
			LEFT JOIN node n ON n.id = s.node_id LEFT JOIN node t ON t.id = s.reloc_target_node_id
			WHERE ` + cond + ` AND s.valid_from <= ? AND (s.valid_to IS NULL OR s.valid_to > ?)`
		args = append(args, q.B, q.A)
		if q.Shard >= 0 {
			sq += ` AND s.shard = ?`
			args = append(args, q.Shard)
		}
		// Ordered, so that the segments kept under the limit are not arbitrary.
		sq += ` ORDER BY i.uuid, s.shard, s.prirep, s.valid_from`
		if err := query(ctx, p.db(), sq, args, func(r *sql.Rows) error {
			var s Segment
			var prirep string
			var to sql.NullInt64
			if err := r.Scan(&s.UUID, &s.Index, &s.Shard, &prirep, &s.Node, &s.Slot, &s.State, &s.RelocTarget, &s.Reason, &s.FromMs, &to); err != nil {
				return err
			}
			s.Primary, s.ToMs = prirep == "p", to.Int64
			k := key{s.UUID, s.Shard, s.Primary, s.Node, s.State, s.Slot, s.FromMs}
			if m, ok := merged[k]; ok {
				// Same interval seen in a later partition (keyframe copy):
				// the later partition knows whether it was closed since.
				m.ToMs = s.ToMs
				return nil
			}
			if len(order) >= q.Limit {
				res.Truncated = true
				return nil
			}
			seg := s
			merged[k] = &seg
			order = append(order, k)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	out := make([]Segment, 0, len(order))
	for _, k := range order {
		out = append(out, *merged[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Index != b.Index {
			return a.Index < b.Index
		}
		if a.Shard != b.Shard {
			return a.Shard < b.Shard
		}
		if a.Primary != b.Primary {
			return a.Primary
		}
		return a.FromMs < b.FromMs
	})
	res.Segments = out
	return res, nil
}
