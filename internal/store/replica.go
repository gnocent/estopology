package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"estopo/internal/record"
)

// Replication support (SPEC §11): the standby copies the configuration part
// of meta.db table by table, and whole partitions when it (re)builds. The
// session, partition catalogue, alert and ha_state tables stay local: they
// describe this instance, not the shared configuration.

// ReplicatedTables are the meta.db tables a standby copies from its primary.
var ReplicatedTables = []string{"cluster", "setting", "family_rule", "user", "node_ack", "audit"}

// MetaSnapshot is the replicated content of meta.db.
type MetaSnapshot struct {
	Schema int                         `json:"schema"` // meta.db migration level; both sides must match
	Tables map[string][]map[string]any `json:"tables"`
}

func (s *Store) metaSchemaLevel(ctx context.Context) (int, error) {
	var v int
	err := s.Meta.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// ExportMeta reads the replicated tables, rows in primary-key order (the
// JSON encoding is deterministic: its hash tells whether anything changed).
func (s *Store) ExportMeta(ctx context.Context) (MetaSnapshot, error) {
	level, err := s.metaSchemaLevel(ctx)
	if err != nil {
		return MetaSnapshot{}, err
	}
	snap := MetaSnapshot{Schema: level, Tables: map[string][]map[string]any{}}
	for _, t := range ReplicatedTables {
		rows, err := s.Meta.QueryContext(ctx, `SELECT * FROM `+t+` ORDER BY 1`)
		if err != nil {
			return snap, err
		}
		cols, _ := rows.Columns()
		out := []map[string]any{}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return snap, err
			}
			row := map[string]any{}
			for i, c := range cols {
				if b, ok := vals[i].([]byte); ok {
					vals[i] = string(b)
				}
				row[c] = vals[i]
			}
			out = append(out, row)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return snap, err
		}
		snap.Tables[t] = out
	}
	return snap, nil
}

// DecodeMetaSnapshot decodes a snapshot keeping integers exact.
func DecodeMetaSnapshot(data []byte) (MetaSnapshot, error) {
	var snap MetaSnapshot
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&snap); err != nil {
		return snap, err
	}
	for _, rows := range snap.Tables {
		for _, row := range rows {
			for k, v := range row {
				if n, ok := v.(json.Number); ok {
					if i, err := n.Int64(); err == nil {
						row[k] = i
					} else if f, err := n.Float64(); err == nil {
						row[k] = f
					}
				}
			}
		}
	}
	return snap, nil
}

// ImportMeta replaces the replicated tables with a snapshot, in one
// transaction. Accounts created locally by an LDAP login and unknown to the
// primary are kept (their sessions stay valid on the standby).
func (s *Store) ImportMeta(ctx context.Context, snap MetaSnapshot) error {
	level, err := s.metaSchemaLevel(ctx)
	if err != nil {
		return err
	}
	if snap.Schema != level {
		return fmt.Errorf("meta.db schema %d on the primary, %d here: run the same estopo version on both", snap.Schema, level)
	}
	tx, err := s.Meta.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range ReplicatedTables {
		rows, ok := snap.Tables[t]
		if !ok {
			return fmt.Errorf("snapshot lacks table %s", t)
		}
		cols, err := tableColumns(ctx, tx, t)
		if err != nil {
			return err
		}
		if t == "user" {
			if _, err := tx.ExecContext(ctx, `DELETE FROM user WHERE source <> 'ldap'`); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `DELETE FROM `+t); err != nil {
			return err
		}
		for _, row := range rows {
			names := make([]string, 0, len(row))
			for k := range row {
				if !cols[k] {
					return fmt.Errorf("snapshot: unknown column %s.%s", t, k)
				}
				names = append(names, k)
			}
			sort.Strings(names)
			args := make([]any, len(names))
			for i, n := range names {
				args[i] = row[n]
			}
			q := `INSERT OR REPLACE INTO ` + t + `(` + strings.Join(names, ", ") + `) VALUES (` +
				strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ") + `)`
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return fmt.Errorf("import %s: %w", t, err)
			}
		}
	}
	return tx.Commit()
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		cols[n] = true
	}
	return cols, rows.Err()
}

// SnapshotPartition writes a consistent copy of a partition to dst. VACUUM
// INTO reads the partition in one read transaction, on a read connection:
// the ingestion, which has its own connection, goes on meanwhile (the WAL is
// not truncated until the copy ends).
func (s *Store) SnapshotPartition(ctx context.Context, info PartitionInfo, dst string) error {
	p, err := s.OpenExisting(ctx, info)
	if err != nil {
		return err
	}
	os.Remove(dst)
	_, err = p.rdb.ExecContext(ctx, `VACUUM INTO ?`, dst)
	return err
}

// CheckPartitionFile verifies that path is a sound partition of cluster and
// week, written by a version this one can read: what a standby does with
// each file received before it replaces anything. The file is not modified.
func CheckPartitionFile(ctx context.Context, path, cluster, week string) error {
	return checkPartitionFile(ctx, path, cluster, week, true)
}

// checkPartitionFile checks the identity of a partition file and, when
// sound is set, its integrity (the whole file is read).
func checkPartitionFile(ctx context.Context, path, cluster, week string, sound bool) error {
	db, err := sql.Open("sqlite", "file:"+path+"?"+url.Values{"mode": {"ro"}, "immutable": {"1"}}.Encode())
	if err != nil {
		return err
	}
	defer db.Close()
	var level int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&level); err != nil {
		return fmt.Errorf("partition %s/%s: %w", cluster, week, err)
	}
	if level > len(partitionSchema) {
		return fmt.Errorf("partition %s/%s: schema %d, %d here: run the same estopo version on both", cluster, week, level, len(partitionSchema))
	}
	if sound {
		var check string
		if err := db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&check); err != nil || check != "ok" {
			return errors.Join(fmt.Errorf("partition %s/%s: damaged file (%s)", cluster, week, check), err)
		}
	}
	got := map[string]string{}
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM meta WHERE key IN ('cluster', 'week')`)
	if err != nil {
		return fmt.Errorf("partition %s/%s: %w", cluster, week, err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		got[k] = v
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if got["cluster"] != cluster || got["week"] != week {
		return fmt.Errorf("partition %s/%s: file of %q/%q", cluster, week, got["cluster"], got["week"])
	}
	return nil
}

// InstallPartition puts a partition file received from the primary in
// place of the local one (if any) and registers it. The file is verified
// first: a file that is refused leaves the local partition as it was.
func (s *Store) InstallPartition(ctx context.Context, cluster, week string, fromMs int64, src string, sealed bool) error {
	w := WeekOf(fromMs)
	if w.Key != week || !record.ValidCluster(cluster) {
		return fmt.Errorf("install partition: invalid %s/%s", cluster, week)
	}
	if err := checkPartitionFile(ctx, src, cluster, week, false); err != nil {
		return fmt.Errorf("install %w", err)
	}
	rel := partitionRel(cluster, w.Key)
	abs := filepath.Join(s.dir, rel)
	s.mu.Lock()
	defer s.mu.Unlock()
	// No reader may reopen the old file, nor see a half-installed one: the
	// lock is held from the close to the registration.
	if err := s.removeLocked(rel, true); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return err
	}
	if err := os.Rename(src, abs); err != nil { // replaces the old file, if any
		return err
	}
	syncDir(filepath.Dir(abs))
	p, err := s.openLocked(ctx, cluster, w)
	if err != nil {
		return err
	}
	if _, err := s.Meta.ExecContext(ctx, `UPDATE partition SET sealed = ? WHERE cluster_id = ? AND week = ?`, boolInt(sealed), cluster, w.Key); err != nil {
		return err
	}
	_, err = s.RefreshSize(ctx, p)
	return err
}

// syncDir makes a rename in dir durable. Best effort: not every platform
// can synchronise a directory.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}

// HAState returns the local high-availability state (never replicated).
func (s *Store) HAState(ctx context.Context) (map[string]string, error) {
	rows, err := s.Meta.QueryContext(ctx, `SELECT key, value FROM ha_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// SetHAState writes keys of the local high-availability state (an empty
// value deletes the key).
func (s *Store) SetHAState(ctx context.Context, kv map[string]string) error {
	tx, err := s.Meta.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range kv {
		if v == "" {
			_, err = tx.ExecContext(ctx, `DELETE FROM ha_state WHERE key = ?`, k)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO ha_state(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v)
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}
