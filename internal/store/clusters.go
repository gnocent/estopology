package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"estopo/internal/esclient"
)

// Cluster is the configuration of a collected cluster (SPEC §9). The ES
// connection holds paths to secrets, never their values.
type Cluster struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	ES            esclient.Config `json:"es"`
	Enabled       bool            `json:"enabled"`
	Suspended     bool            `json:"suspended"`
	SuspendReason string          `json:"suspend_reason,omitempty"`
	BoundUUID     string          `json:"bound_uuid,omitempty"`
	Schedule      json.RawMessage `json:"schedule,omitempty"`  // collector.Schedule, null = defaults
	Hierarchy     json.RawMessage `json:"hierarchy,omitempty"` // []derive.Level, null = defaults
	MachineAttr   string          `json:"machine_attr,omitempty"`
	NodeReference []string        `json:"node_reference,omitempty"`
	CreatedMs     int64           `json:"created_ms"`
	RemovedMs     int64           `json:"removed_ms,omitempty"`
}

var clusterID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// ErrNotFound is returned for an unknown configuration entry.
var ErrNotFound = errors.New("not found")

// ValidClusterID reports whether id can name directories and URLs.
func ValidClusterID(id string) bool { return clusterID.MatchString(id) }

// Clusters returns the configured clusters (removed ones included when all).
func (s *Store) Clusters(ctx context.Context, all bool) ([]Cluster, error) {
	q := `SELECT id, name, config, enabled, suspended, coalesce(suspend_reason, ''), coalesce(bound_uuid, ''),
		coalesce(schedule, ''), coalesce(hierarchy, ''), coalesce(machine_attr, ''), coalesce(node_reference, ''),
		created_ms, coalesce(removed_ms, 0) FROM cluster`
	if !all {
		q += ` WHERE removed_ms IS NULL`
	}
	rows, err := s.Meta.QueryContext(ctx, q+` ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Cluster
	for rows.Next() {
		c, err := scanCluster(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Cluster returns one configured cluster.
func (s *Store) Cluster(ctx context.Context, id string) (Cluster, error) {
	rows, err := s.Meta.QueryContext(ctx, `SELECT id, name, config, enabled, suspended, coalesce(suspend_reason, ''),
		coalesce(bound_uuid, ''), coalesce(schedule, ''), coalesce(hierarchy, ''), coalesce(machine_attr, ''),
		coalesce(node_reference, ''), created_ms, coalesce(removed_ms, 0) FROM cluster WHERE id = ?`, id)
	if err != nil {
		return Cluster{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return Cluster{}, ErrNotFound
	}
	return scanCluster(rows)
}

func scanCluster(r *sql.Rows) (Cluster, error) {
	var c Cluster
	var cfg, schedule, hierarchy, ref string
	if err := r.Scan(&c.ID, &c.Name, &cfg, &c.Enabled, &c.Suspended, &c.SuspendReason, &c.BoundUUID, &schedule, &hierarchy,
		&c.MachineAttr, &ref, &c.CreatedMs, &c.RemovedMs); err != nil {
		return c, err
	}
	if err := json.Unmarshal([]byte(cfg), &c.ES); err != nil {
		return c, fmt.Errorf("cluster %s config: %w", c.ID, err)
	}
	if schedule != "" {
		c.Schedule = json.RawMessage(schedule)
	}
	if hierarchy != "" {
		c.Hierarchy = json.RawMessage(hierarchy)
	}
	if ref != "" {
		json.Unmarshal([]byte(ref), &c.NodeReference)
	}
	return c, nil
}

// SaveCluster creates or updates a configuration entry (identity binding
// and suspension are kept: see SetBound, SetSuspended).
func (s *Store) SaveCluster(ctx context.Context, c Cluster) error {
	if !ValidClusterID(c.ID) {
		return fmt.Errorf("invalid cluster id %q (lowercase letters, digits, '-' and '_')", c.ID)
	}
	cfg, err := json.Marshal(c.ES)
	if err != nil {
		return err
	}
	var ref any
	if len(c.NodeReference) > 0 {
		b, _ := json.Marshal(c.NodeReference)
		ref = string(b)
	}
	now := s.clock.Now().UnixMilli()
	_, err = s.Meta.ExecContext(ctx, `INSERT INTO cluster(id, name, config, enabled, schedule, hierarchy, machine_attr, node_reference, created_ms)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, config = excluded.config, enabled = excluded.enabled,
		schedule = excluded.schedule, hierarchy = excluded.hierarchy, machine_attr = excluded.machine_attr,
		node_reference = excluded.node_reference, removed_ms = NULL`,
		c.ID, c.Name, string(cfg), c.Enabled, rawOrNil(c.Schedule), rawOrNil(c.Hierarchy), nullStr(c.MachineAttr), ref, now)
	return err
}

func rawOrNil(r json.RawMessage) any {
	if len(r) == 0 || string(r) == "null" {
		return nil
	}
	return string(r)
}

// SetBound records the cluster_uuid a configuration is bound to.
func (s *Store) SetBound(ctx context.Context, id, uuid string) error {
	_, err := s.Meta.ExecContext(ctx, `UPDATE cluster SET bound_uuid = ? WHERE id = ?`, nullStr(uuid), id)
	return err
}

// SetSuspended suspends or resumes a cluster; suspending raises an alert.
func (s *Store) SetSuspended(ctx context.Context, id string, suspended bool, reason string) error {
	if _, err := s.Meta.ExecContext(ctx, `UPDATE cluster SET suspended = ?, suspend_reason = ? WHERE id = ?`,
		suspended, nullStr(reason), id); err != nil {
		return err
	}
	now := s.clock.Now().UnixMilli()
	if suspended {
		_, err := s.Meta.ExecContext(ctx, `INSERT INTO alert(ts, kind, cluster_id, message) VALUES (?, 'cluster_suspended', ?, ?)`, now, id, reason)
		return err
	}
	_, err := s.Meta.ExecContext(ctx, `UPDATE alert SET resolved_ms = ? WHERE cluster_id = ? AND kind = 'cluster_suspended' AND resolved_ms IS NULL`, now, id)
	return err
}

// RemoveCluster takes a cluster out of the configuration; its history is
// kept until retention (or Purge).
func (s *Store) RemoveCluster(ctx context.Context, id string) error {
	res, err := s.Meta.ExecContext(ctx, `UPDATE cluster SET removed_ms = ?, enabled = 0 WHERE id = ? AND removed_ms IS NULL`,
		s.clock.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Audit records an admin action (SPEC §9).
func (s *Store) Audit(ctx context.Context, actor, action, target, details string) error {
	_, err := s.Meta.ExecContext(ctx, `INSERT INTO audit(ts, actor, action, target, details) VALUES (?,?,?,?,?)`,
		s.clock.Now().UnixMilli(), actor, action, nullStr(target), nullStr(details))
	return err
}

// TLSDefaults are the global TLS settings (SPEC §5.6).
type TLSDefaults struct {
	CAPath string `json:"ca_path"` // CA file or directory used by clusters without their own
}

// ApplyTLSDefaults fills the cluster connection settings left empty with
// the global ones.
func (s *Store) ApplyTLSDefaults(ctx context.Context, es esclient.Config) esclient.Config {
	var d TLSDefaults
	s.GetSetting(ctx, "tls", &d)
	if es.CAPath == "" {
		es.CAPath = d.CAPath
	}
	return es
}

// CollectorSettings are the global collection settings (SPEC §9).
type CollectorSettings struct {
	Parallelism int `json:"parallelism"` // simultaneous full captures (memory and cluster load); applied at restart
}

// DefaultCacheStructures is the default number of reconstructed states kept
// in memory (a 250k-shard structure takes about 20 MB).
const DefaultCacheStructures = 64

// CacheSettings size the query caches (SPEC §14); applied at restart.
type CacheSettings struct {
	Structures int `json:"structures"`
}
