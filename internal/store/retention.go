package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"
)

// Retention is the history retention policy (SPEC §6.6).
type Retention struct {
	MaxAgeDays int   `json:"max_age_days"` // default 30
	MaxBytes   int64 `json:"max_bytes"`    // 0 = no size limit
}

// DefaultRetention returns the SPEC defaults.
func DefaultRetention() Retention { return Retention{MaxAgeDays: 30} }

// GetSetting reads a JSON setting of meta.db into v; it reports false when
// the setting is not set.
func (s *Store) GetSetting(ctx context.Context, key string, v any) (bool, error) {
	var raw string
	err := s.Meta.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

// SetSetting stores v as a JSON setting of meta.db.
func (s *Store) SetSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.Meta.ExecContext(ctx, `INSERT INTO setting(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(b))
	return err
}

// RetentionPolicy returns the configured policy (defaults if unset).
func (s *Store) RetentionPolicy(ctx context.Context) (Retention, error) {
	r := DefaultRetention()
	_, err := s.GetSetting(ctx, "retention", &r)
	if r.MaxAgeDays <= 0 {
		r.MaxAgeDays = DefaultRetention().MaxAgeDays
	}
	return r, err
}

// RetentionReport describes one retention pass.
type RetentionReport struct {
	DeletedByAge  []PartitionInfo
	DeletedBySize []PartitionInfo
	TotalBytes    int64 // after the pass: partitions + meta.db + spool + tmp (the whole data directory)
	OtherBytes    int64 // meta.db, spool and copies in transit (tmp), counted but never deleted here
	OverLimit     bool  // still above MaxBytes (only current partitions left)
}

// ApplyRetention deletes the partitions whose whole week is older than the
// retention, then, if the data directory exceeds MaxBytes, the oldest
// partitions of any cluster until it fits (SPEC §6.6, B13, B14). The
// partition of the current week of each cluster is never deleted.
func (s *Store) ApplyRetention(ctx context.Context, pol Retention) (RetentionReport, error) {
	var rep RetentionReport
	now := s.clock.Now().UnixMilli()
	current := WeekOf(now).Key
	parts, err := s.Partitions(ctx, "")
	if err != nil {
		return rep, err
	}
	limit := now - int64(pol.MaxAgeDays)*24*int64(time.Hour/time.Millisecond)
	var kept []PartitionInfo
	for _, p := range parts {
		if p.Week != current && p.ToMs <= limit {
			if err := s.DeletePartition(ctx, p); err != nil {
				return rep, err
			}
			rep.DeletedByAge = append(rep.DeletedByAge, p)
			continue
		}
		kept = append(kept, p)
	}
	// Sizes may be stale for open partitions: refresh them.
	for i, p := range kept {
		if part, err := s.OpenExisting(ctx, p); err == nil {
			if size, err := s.RefreshSize(ctx, part); err == nil {
				kept[i].SizeBytes = size
			}
		}
	}
	for _, p := range kept {
		rep.TotalBytes += p.SizeBytes
	}
	// The limit is on the data directory (SPEC §6.6): meta.db, the spool and tmp
	// count too, but only partitions are deleted to make room.
	rep.OtherBytes = pathSize(filepath.Join(s.dir, "meta.db")) + pathSize(filepath.Join(s.dir, "meta.db-wal")) +
		pathSize(filepath.Join(s.dir, "spool")) + pathSize(s.TmpDir())
	rep.TotalBytes += rep.OtherBytes
	if pol.MaxBytes > 0 {
		for _, p := range kept { // oldest first
			if rep.TotalBytes <= pol.MaxBytes {
				break
			}
			if p.Week == current {
				continue
			}
			if err := s.DeletePartition(ctx, p); err != nil {
				return rep, err
			}
			rep.DeletedBySize = append(rep.DeletedBySize, p)
			rep.TotalBytes -= p.SizeBytes
		}
		rep.OverLimit = rep.TotalBytes > pol.MaxBytes
		if len(rep.DeletedBySize) > 0 || rep.OverLimit {
			msg := fmt.Sprintf("size limit %d bytes: %d partition(s) deleted, %d bytes left", pol.MaxBytes, len(rep.DeletedBySize), rep.TotalBytes)
			if _, err := s.Meta.ExecContext(ctx, `INSERT INTO alert(ts, kind, message) VALUES (?, 'retention_size', ?)`, now, msg); err != nil {
				return rep, err
			}
		}
	}
	return rep, nil
}

// Purge deletes the whole history of a cluster (admin action, SPEC §6.6).
func (s *Store) Purge(ctx context.Context, cluster string) (int, error) {
	parts, err := s.Partitions(ctx, cluster)
	if err != nil {
		return 0, err
	}
	for _, p := range parts {
		if err := s.DeletePartition(ctx, p); err != nil {
			return 0, err
		}
	}
	return len(parts), nil
}

// Alert is an admin banner (retention, suspended cluster, ...).
type Alert struct {
	ID       int64  `json:"id"`
	Ts       int64  `json:"ts"`
	Kind     string `json:"kind"`
	Cluster  string `json:"cluster,omitempty"`
	Message  string `json:"message"`
	Resolved bool   `json:"resolved"`
}

// Alerts returns the unresolved alerts, newest first.
func (s *Store) Alerts(ctx context.Context) ([]Alert, error) {
	rows, err := s.Meta.QueryContext(ctx, `SELECT id, ts, kind, coalesce(cluster_id, ''), message FROM alert WHERE resolved_ms IS NULL ORDER BY ts DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.Ts, &a.Kind, &a.Cluster, &a.Message); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// pathSize returns the size of a file, or of every file under a directory
// (0 if it does not exist).
func pathSize(path string) int64 {
	var total int64
	filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}
