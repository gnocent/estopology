package store

import (
	"context"
	"os"
	"path/filepath"
)

// Ack is a node acknowledged as removed (SPEC §7.2, B10).
type Ack struct {
	ID        int64  `json:"id"`
	Cluster   string `json:"cluster"`
	Node      string `json:"node"`
	FromMs    int64  `json:"from_ms"`
	Comment   string `json:"comment,omitempty"`
	Author    string `json:"author"`
	CreatedMs int64  `json:"created_ms"`
	RevokedMs int64  `json:"revoked_ms,omitempty"`
	RevokedBy string `json:"revoked_by,omitempty"`
}

// Acks lists the acknowledgements of a cluster (revoked ones included).
func (s *Store) Acks(ctx context.Context, cluster string) ([]Ack, error) {
	rows, err := s.Meta.QueryContext(ctx, `SELECT id, cluster_id, node_name, from_ms, coalesce(comment, ''), author, created_ms,
		coalesce(revoked_ms, 0), coalesce(revoked_by, '') FROM node_ack WHERE cluster_id = ? ORDER BY created_ms DESC`, cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Ack
	for rows.Next() {
		var a Ack
		if err := rows.Scan(&a.ID, &a.Cluster, &a.Node, &a.FromMs, &a.Comment, &a.Author, &a.CreatedMs, &a.RevokedMs, &a.RevokedBy); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddAck records an acknowledgement and returns its id.
func (s *Store) AddAck(ctx context.Context, a Ack) (int64, error) {
	res, err := s.Meta.ExecContext(ctx, `INSERT INTO node_ack(cluster_id, node_name, from_ms, comment, author, created_ms) VALUES (?,?,?,?,?,?)`,
		a.Cluster, a.Node, a.FromMs, nullStr(a.Comment), a.Author, s.clock.Now().UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RevokeAck cancels an acknowledgement.
func (s *Store) RevokeAck(ctx context.Context, id int64, by string) error {
	res, err := s.Meta.ExecContext(ctx, `UPDATE node_ack SET revoked_ms = ?, revoked_by = ? WHERE id = ? AND revoked_ms IS NULL`,
		s.clock.Now().UnixMilli(), by, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FamilyRules returns the family regexes of a cluster, in order.
func (s *Store) FamilyRules(ctx context.Context, cluster string) ([]string, error) {
	rows, err := s.Meta.QueryContext(ctx, `SELECT pattern FROM family_rule WHERE cluster_id = ? ORDER BY position`, cluster)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetFamilyRules replaces the family regexes of a cluster.
func (s *Store) SetFamilyRules(ctx context.Context, cluster string, rules []string) error {
	tx, err := s.Meta.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM family_rule WHERE cluster_id = ?`, cluster); err != nil {
		return err
	}
	for i, p := range rules {
		if _, err := tx.ExecContext(ctx, `INSERT INTO family_rule(cluster_id, position, pattern) VALUES (?,?,?)`, cluster, i, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MetaSize returns the size of meta.db (with its WAL).
func (s *Store) MetaSize() int64 { return fileSizes(s.dir, "meta.db") }

func fileSizes(dir, name string) int64 {
	var n int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if st, err := os.Stat(filepath.Join(dir, name+suffix)); err == nil {
			n += st.Size()
		}
	}
	return n
}
