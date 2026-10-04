package ha

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"estopo/internal/record"
	"estopo/internal/store"
)

// recordBatch is how many records are listed per request.
const recordBatch = 200

// blockedError stops the replication until an administrator decides: what
// the primary announces would destroy what this standby holds.
type blockedError struct{ reason string }

func (e *blockedError) Error() string { return "replication stopped: " + e.reason }

// SyncOnce runs one standby synchronisation: configuration, then for each
// cluster a partition copy when needed, then the records not ingested yet.
func (m *Manager) SyncOnce(ctx context.Context) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	m.mu.Lock()
	if m.st.Role != RoleStandby || m.promoting {
		m.mu.Unlock()
		return nil
	}
	base, etag, rebuildAll := m.st.StandbyOf, m.metaETag, m.rebuild
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.syncCancel = cancel
	m.mu.Unlock()

	err := m.sync(ctx, m.client(base), etag, rebuildAll)
	now := m.o.Clock.Now().UnixMilli()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncCancel = nil
	m.repl.Rebuilding = false
	if err != nil {
		var blocked *blockedError
		was := m.repl.Blocked
		m.repl.Blocked = ""
		if errors.As(err, &blocked) {
			m.repl.Blocked = blocked.reason
			if was != blocked.reason {
				m.o.Log.Error("replication stopped until an administrator rebuilds this standby", "primary", base, "reason", blocked.reason)
			}
		}
		m.repl.LastError, m.repl.LastErrorMs = err.Error(), now
		return err
	}
	m.repl.LastError, m.repl.Blocked, m.repl.LastSyncMs = "", "", now
	if rebuildAll {
		m.rebuild = false
	}
	return nil
}

// checkPrimary verifies that the primary is the instance followed so far,
// and that it has not come back empty. A primary whose data volume was lost
// restarts with the same URL and token, no configuration and no history:
// followed blindly, it would empty this standby within one cycle. An
// explicit rebuild (rebuildAll) accepts the primary as it is.
func (m *Manager) checkPrimary(ctx context.Context, st remoteStatus, remote []remoteCluster, rebuildAll bool) error {
	if st.InstanceID == "" {
		return fmt.Errorf("the primary does not announce an instance id: run the same estopo version on both")
	}
	m.mu.Lock()
	pinned := m.primaryID
	m.mu.Unlock()
	if pinned != "" && pinned != st.InstanceID {
		return &blockedError{fmt.Sprintf("the primary is another instance (%s) than the one followed so far (%s): "+
			"its data directory was replaced. Rebuild this standby to follow it, or promote this standby", st.InstanceID, pinned)}
	}
	if len(remote) == 0 && !rebuildAll {
		local, err := m.o.Store.Partitions(ctx, "")
		if err != nil {
			return err
		}
		if len(local) > 0 {
			return &blockedError{fmt.Sprintf("the primary holds no history while this standby holds %d partition(s): "+
				"nothing is purged. Rebuild this standby to empty it, or promote it", len(local))}
		}
	}
	if pinned == "" {
		if err := m.o.Store.SetHAState(ctx, map[string]string{"primary_id": st.InstanceID}); err != nil {
			return err
		}
		m.mu.Lock()
		m.primaryID = st.InstanceID
		m.mu.Unlock()
		m.o.Log.Info("following primary", "instance_id", st.InstanceID)
	}
	return nil
}

func (m *Manager) sync(ctx context.Context, c *client, etag string, rebuildAll bool) error {
	st, err := c.status(ctx)
	if err != nil {
		return fmt.Errorf("primary unreachable: %w", err)
	}
	if st.Role != RolePrimary {
		return fmt.Errorf("%s is not a primary (role %s)", c.base, st.Role)
	}
	remote, err := c.clusters(ctx)
	if err != nil {
		return err
	}
	// Before anything is imported or purged.
	if err := m.checkPrimary(ctx, st, remote, rebuildAll); err != nil {
		return err
	}

	data, tag, changed, err := c.meta(ctx, etag)
	if err != nil {
		return err
	}
	if changed {
		snap, err := store.DecodeMetaSnapshot(data)
		if err != nil {
			return fmt.Errorf("configuration snapshot: %w", err)
		}
		if err := m.o.Store.ImportMeta(ctx, snap); err != nil {
			return err
		}
		// Logins and admin actions change the audit trail (and last logins)
		// often: the collector and query caches only follow the tables that
		// shape the topology views.
		cfg := configHash(snap)
		m.mu.Lock()
		m.metaETag = tag
		cfgChanged := cfg != m.configHash
		m.configHash = cfg
		m.mu.Unlock()
		if cfgChanged {
			if err := m.o.Hooks.Changed(ctx); err != nil {
				m.o.Log.Error("apply replicated configuration", "err", err)
			}
			m.o.Log.Info("configuration replicated from the primary")
		}
	}

	seen := map[string]bool{}
	var lag int64
	for _, rc := range remote {
		seen[rc.Cluster] = true
		cursor, err := m.cursor(ctx, rc.Cluster)
		if err != nil {
			return err
		}
		// No history here, or behind what the spool still holds: copy the
		// partitions, then catch up with the records.
		if len(rc.Partitions) > 0 && (rebuildAll || cursor == 0 || (rc.OldestMs > 0 && cursor < rc.OldestMs)) {
			if err := m.rebuildCluster(ctx, c, rc); err != nil {
				return fmt.Errorf("rebuild %s: %w", rc.Cluster, err)
			}
			m.o.Hooks.Forget(rc.Cluster)
			if cursor, err = m.o.Hooks.LastTs(ctx, rc.Cluster); err != nil {
				return err
			}
			m.setCursor(rc.Cluster, cursor, 0)
		}
		for {
			recs, err := c.records(ctx, rc.Cluster, cursor, recordBatch)
			if err != nil {
				return err
			}
			for _, rr := range recs {
				b, err := c.record(ctx, rc.Cluster, rr.Name)
				if err != nil {
					return err
				}
				r, err := record.Decode(bytes.NewReader(b))
				if err != nil {
					return fmt.Errorf("record %s/%s: %w", rc.Cluster, rr.Name, err)
				}
				if r.Cluster != rc.Cluster {
					return fmt.Errorf("record %s/%s belongs to cluster %q", rc.Cluster, rr.Name, r.Cluster)
				}
				// Spooled here, then ingested in order by the pipeline, which
				// tries again a record whose ingestion fails: the cursor is
				// what this standby holds, in its spool, not what is ingested.
				if err := m.o.Hooks.Ingest(r); err != nil {
					return err
				}
				cursor = max(cursor, r.StartMs)
				m.setCursor(rc.Cluster, cursor, 1)
			}
			if len(recs) < recordBatch {
				break
			}
		}
		m.mu.Lock()
		cr := m.repl1(rc.Cluster)
		cr.PrimaryMs = rc.NewestMs
		if rc.NewestMs > cursor {
			lag = max(lag, rc.NewestMs-cursor)
		}
		m.mu.Unlock()
	}

	// Clusters purged on the primary are purged here too.
	local, err := m.o.Store.Partitions(ctx, "")
	if err != nil {
		return err
	}
	gone := map[string]bool{}
	for _, p := range local {
		if !seen[p.Cluster] {
			gone[p.Cluster] = true
		}
	}
	for id := range gone {
		if err := m.waitIdle(ctx); err != nil {
			return fmt.Errorf("purge %s: %w", id, err)
		}
		if _, err := m.o.Store.Purge(ctx, id); err != nil {
			return err
		}
		m.o.Hooks.Forget(id)
		m.mu.Lock()
		delete(m.cursors, id)
		delete(m.repls, id)
		m.mu.Unlock()
		m.o.Log.Info("cluster purged on the primary, purged here", "cluster", id)
	}
	m.mu.Lock()
	m.repl.LagMs = lag
	m.mu.Unlock()
	return nil
}

func (m *Manager) cursor(ctx context.Context, cluster string) (int64, error) {
	m.mu.Lock()
	cur, ok := m.cursors[cluster]
	m.mu.Unlock()
	if ok {
		return cur, nil
	}
	cur, err := m.o.Hooks.LastTs(ctx, cluster)
	if err != nil {
		return 0, err
	}
	m.setCursor(cluster, cur, 0)
	return cur, nil
}

func (m *Manager) setCursor(cluster string, ms int64, records int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursors[cluster] = ms
	cr := m.repl1(cluster)
	cr.CursorMs = ms
	cr.Records += records
}

// repl1 returns the status entry of a cluster (m.mu held).
func (m *Manager) repl1(cluster string) *ClusterRepl {
	cr := m.repls[cluster]
	if cr == nil {
		cr = &ClusterRepl{Cluster: cluster}
		m.repls[cluster] = cr
	}
	return cr
}

// rebuildCluster replaces the local partitions of a cluster by copies of
// the primary's (SPEC §11: sealed partitions plus a snapshot of the current
// one). Every partition is downloaded and verified before the first local
// one is touched: if the primary fails during the copy, this standby keeps
// the history it had, and can still be promoted with it.
func (m *Manager) rebuildCluster(ctx context.Context, c *client, rc remoteCluster) error {
	m.mu.Lock()
	m.repl.Rebuilding = true
	m.mu.Unlock()
	start := time.Now()
	if err := os.MkdirAll(m.o.TmpDir, 0o750); err != nil {
		return err
	}
	parts := append([]remotePartition(nil), rc.Partitions...)
	sort.Slice(parts, func(i, j int) bool { return parts[i].FromMs < parts[j].FromMs })
	files := make([]string, 0, len(parts))
	defer func() { // whatever was not installed
		for _, f := range files {
			os.Remove(f)
		}
	}()
	var bytes int64
	for _, p := range parts {
		f, err := os.CreateTemp(m.o.TmpDir, "partition-*.db")
		if err != nil {
			return err
		}
		tmp := f.Name()
		f.Close()
		files = append(files, tmp)
		if err := c.partition(ctx, rc.Cluster, p.Week, tmp); err != nil {
			return err
		}
		if err := store.CheckPartitionFile(ctx, tmp, rc.Cluster, p.Week); err != nil {
			return err
		}
		if st, err := os.Stat(tmp); err == nil {
			bytes += st.Size()
		}
	}

	// Nothing of this cluster may be in the ingestion queue while its
	// partitions change.
	if err := m.waitIdle(ctx); err != nil {
		return err
	}
	local, err := m.o.Store.Partitions(ctx, rc.Cluster)
	if err != nil {
		return err
	}
	weeks := map[string]bool{}
	for i, p := range parts {
		if err := m.o.Store.InstallPartition(ctx, rc.Cluster, p.Week, p.FromMs, files[i], p.Sealed); err != nil {
			m.o.Hooks.Forget(rc.Cluster) // some partitions were replaced
			return err
		}
		weeks[p.Week] = true
	}
	// Weeks the primary no longer has (retention).
	for _, p := range local {
		if !weeks[p.Week] {
			if err := m.o.Store.DeletePartition(ctx, p); err != nil {
				return err
			}
		}
	}
	m.o.Hooks.Forget(rc.Cluster)
	m.mu.Lock()
	m.repl1(rc.Cluster).Rebuilds++
	m.mu.Unlock()
	m.o.Log.Info("partitions copied from the primary", "cluster", rc.Cluster, "partitions", len(parts),
		"bytes", bytes, "duration", time.Since(start).Round(time.Millisecond))
	return m.o.Hooks.Changed(ctx)
}

func sortRepl(cs []ClusterRepl) {
	sort.Slice(cs, func(i, j int) bool { return cs[i].Cluster < cs[j].Cluster })
}

// configHash digests the replicated tables that shape the topology views
// (not the audit trail nor the accounts).
func configHash(snap store.MetaSnapshot) string {
	part := map[string]any{}
	for _, t := range []string{"cluster", "setting", "family_rule", "node_ack"} {
		part[t] = snap.Tables[t]
	}
	b, _ := json.Marshal(part)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
