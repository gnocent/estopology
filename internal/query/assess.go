package query

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"estopo/internal/derive"
	"estopo/internal/model"
)

const referenceWindow = 7 * 24 * time.Hour

// ReferenceNodes returns the node names present at some point during the 7
// days before at (SPEC §7.2), plus the declared reference if any.
func (e *Engine) ReferenceNodes(ctx context.Context, cluster string, at int64, declared []string) ([]string, error) {
	from := at - referenceWindow.Milliseconds()
	parts, err := e.partitions(ctx, cluster, from, at)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, p := range parts {
		if err := query(ctx, p.db(), `SELECT DISTINCT n.name FROM node_presence pr JOIN node n ON n.id = pr.node_id
			WHERE pr.valid_from <= ? AND (pr.valid_to IS NULL OR pr.valid_to > ?)`, []any{at, from}, func(r *sql.Rows) error {
			var name string
			if err := r.Scan(&name); err != nil {
				return err
			}
			seen[name] = true
			return nil
		}); err != nil {
			return nil, err
		}
	}
	for _, d := range declared {
		seen[d] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// Acks returns the active node acknowledgements of a cluster.
func (e *Engine) Acks(ctx context.Context, cluster string) ([]derive.Ack, error) {
	var out []derive.Ack
	err := query(ctx, e.st.Meta, `SELECT node_name, from_ms FROM node_ack WHERE cluster_id = ? AND revoked_ms IS NULL`,
		[]any{cluster}, func(r *sql.Rows) error {
			var a derive.Ack
			if err := r.Scan(&a.Node, &a.FromMs); err != nil {
				return err
			}
			out = append(out, a)
			return nil
		})
	return out, err
}

// Overview is a cluster at a date with its derived verdicts.
type Overview struct {
	State       *model.State             `json:"state"`
	Missing     []string                 `json:"missing_nodes"`
	Constraints []derive.IndexConstraint `json:"-"`
	Assessment  derive.Assessment        `json:"assessment"`
}

// Overview reconstructs the state at `at` and evaluates it.
func (e *Engine) Overview(ctx context.Context, cluster string, at int64, declared []string) (*Overview, error) {
	st, err := e.StateAt(ctx, cluster, at)
	if err != nil {
		return nil, err
	}
	ref, err := e.ReferenceNodes(ctx, cluster, at, declared)
	if err != nil {
		return nil, err
	}
	acks, err := e.Acks(ctx, cluster)
	if err != nil {
		return nil, err
	}
	o := &Overview{State: st, Missing: derive.MissingNodes(st, ref, acks)}
	o.Constraints = derive.Constraints(st)
	o.Assessment = derive.Assess(st, o.Missing, o.Constraints)
	return o, nil
}
