package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"estopo/internal/esclient"
	"estopo/internal/model"
)

// StateAt reconstructs a cluster at date at (SPEC §6.4): the structure seen
// by the last successful full capture ≤ at, presence, health, settings and
// metrics from the last probe ≤ at. Both halves are cached separately: the
// structure changes at most with full captures, the probe half is cheap.
func (e *Engine) StateAt(ctx context.Context, cluster string, at int64) (*model.State, error) {
	pc, tc, err := e.lastCapture(ctx, cluster, at, "kind = 'full' AND status = 'ok'")
	if err != nil {
		return nil, err
	}
	pp, tp, err := e.lastCapture(ctx, cluster, at, "kind IN ('light', 'full') AND status != 'failed'")
	if err != nil {
		return nil, err
	}
	_, tl, err := e.lastCapture(ctx, cluster, at, "kind IN ('light', 'full')")
	if err != nil {
		return nil, err
	}
	if tp == 0 && tc == 0 {
		return nil, ErrNoData
	}
	var base, probe *model.State
	if tc > 0 {
		key := fmt.Sprintf("%s|%d", cluster, tc)
		if base = e.structures.get(key); base == nil {
			base = newHalf(cluster)
			b := &builder{e: e, ctx: ctx, s: base, nodeIdx: map[nodeKey]int{}}
			if err := b.structureSide(pc, tc); err != nil {
				return nil, err
			}
			e.structures.put(key, base)
		}
	}
	if tp > 0 {
		key := fmt.Sprintf("%s|%d", cluster, tp)
		if probe = e.probes.get(key); probe == nil {
			probe = newHalf(cluster)
			b := &builder{e: e, ctx: ctx, s: probe, nodeIdx: map[nodeKey]int{}}
			if err := b.probeSide(pp, tp); err != nil {
				return nil, err
			}
			e.probes.put(key, probe)
		}
	}
	s := newHalf(cluster)
	s.AtMs, s.CaptureMs, s.ProbeMs, s.Unknown = at, tc, tp, tl > tp
	idx := map[nodeKey]int{}
	if base != nil {
		s.Indices, s.Shards, s.Defaults = base.Indices, base.Shards, base.Defaults // shared, read-only
		s.Nodes = append(make([]model.Node, 0, len(base.Nodes)+8), base.Nodes...)
		for i := range s.Nodes {
			idx[nodeKey{s.Nodes[i].Name, s.Nodes[i].ESID}] = i
		}
	}
	if probe != nil {
		s.Health, s.ClusterSettings, s.Persistent, s.Transient, s.Remotes =
			probe.Health, probe.ClusterSettings, probe.Persistent, probe.Transient, probe.Remotes
		for _, pn := range probe.Nodes {
			if i, ok := idx[nodeKey{pn.Name, pn.ESID}]; ok {
				s.Nodes[i].Present, s.Nodes[i].Metrics = true, pn.Metrics
			} else {
				s.Nodes = append(s.Nodes, pn)
			}
		}
	}
	return s, nil
}

func newHalf(cluster string) *model.State {
	return &model.State{Cluster: cluster, ClusterSettings: map[string]string{}, Persistent: map[string]string{},
		Transient: map[string]string{}, Defaults: map[string]string{}}
}

type nodeKey struct{ name, esID string }

type builder struct {
	e       *Engine
	ctx     context.Context
	s       *model.State
	nodeIdx map[nodeKey]int
}

func (b *builder) node(name, esID string, gen int) int {
	k := nodeKey{name, esID}
	if i, ok := b.nodeIdx[k]; ok {
		return i
	}
	b.s.Nodes = append(b.s.Nodes, model.Node{Name: name, ESID: esID, Generation: gen, Attrs: map[string]string{}})
	i := len(b.s.Nodes) - 1
	b.nodeIdx[k] = i
	return i
}

func blobJSON(ctx context.Context, p part, hash string, v any) error {
	if hash == "" {
		return nil
	}
	data, err := p.p.Blob(ctx, hash)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func query(ctx context.Context, db *sql.DB, q string, args []any, scan func(*sql.Rows) error) error {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

const openAt = `valid_from <= ? AND (valid_to IS NULL OR valid_to > ?)`

// probeSide loads health, presence, node metrics, settings and remotes.
func (b *builder) probeSide(p part, tp int64) error {
	ctx, s, db := b.ctx, b.s, p.db()
	h := &model.Health{}
	var up sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT status, nodes, data_nodes, active_primary, active, relocating, initializing,
		unassigned, unassigned_primary, delayed_unassigned, pending_tasks FROM health WHERE ts <= ? ORDER BY ts DESC LIMIT 1`, tp).
		Scan(&h.Status, &h.Nodes, &h.DataNodes, &h.ActivePrimary, &h.Active, &h.Relocating, &h.Initializing,
			&h.Unassigned, &up, &h.DelayedUnassigned, &h.PendingTasks)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		h.UnassignedPrimary = -1
		if up.Valid {
			h.UnassignedPrimary = int(up.Int64)
		}
		s.Health = h
	}
	// Present nodes.
	byID := map[int64]int{}
	if err := query(ctx, db, `SELECT n.id, n.name, n.es_id, n.generation FROM node_presence p JOIN node n ON n.id = p.node_id
		WHERE p.`+openAt+` ORDER BY n.name, n.es_id`, []any{tp, tp}, func(r *sql.Rows) error {
		var id int64
		var name, esID string
		var gen int
		if err := r.Scan(&id, &name, &esID, &gen); err != nil {
			return err
		}
		i := b.node(name, esID, gen)
		s.Nodes[i].Present = true
		byID[id] = i
		return nil
	}); err != nil {
		return err
	}
	// Node metrics: last row ≤ tp per node.
	if err := query(ctx, db, `SELECT node_id, max(ts), proc_cpu, os_cpu, load1, heap_pct, disk_total, disk_used, disk_avail,
		disk_indices, shards FROM node_metric WHERE ts <= ? GROUP BY node_id`, []any{tp}, func(r *sql.Rows) error {
		var id, ts int64
		var v [9]sql.NullFloat64
		if err := r.Scan(&id, &ts, &v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6], &v[7], &v[8]); err != nil {
			return err
		}
		i, ok := byID[id]
		if !ok {
			return nil
		}
		f := func(k int) int64 {
			if !v[k].Valid {
				return -1
			}
			return int64(v[k].Float64)
		}
		m := &model.NodeMetrics{Ts: ts, ProcCPU: int(f(0)), OSCPU: int(f(1)), Load1: v[2].Float64, HeapPct: int(f(3)),
			DiskTotal: f(4), DiskUsed: f(5), DiskAvail: f(6), DiskIndices: f(7), Shards: f(8)}
		if !v[2].Valid {
			m.Load1 = -1
		}
		s.Nodes[i].Metrics = m
		return nil
	}); err != nil {
		return err
	}
	// Cluster settings.
	var hash string
	err = db.QueryRowContext(ctx, `SELECT blob FROM cluster_settings_iv WHERE `+openAt+` ORDER BY valid_from DESC LIMIT 1`, tp, tp).Scan(&hash)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil {
		var m map[string]map[string]string
		if err := blobJSON(ctx, p, hash, &m); err != nil {
			return err
		}
		for k, v := range m["persistent"] {
			s.Persistent[k] = v
			s.ClusterSettings[k] = v
		}
		for k, v := range m["transient"] {
			s.Transient[k] = v
			s.ClusterSettings[k] = v
		}
	}
	// Remotes.
	return query(ctx, db, `SELECT alias, mode, connected, nodes_connected, valid_from FROM remote_iv WHERE `+openAt+` ORDER BY alias`,
		[]any{tp, tp}, func(r *sql.Rows) error {
			var rm model.Remote
			var connected int
			if err := r.Scan(&rm.Alias, &rm.Mode, &connected, &rm.NodesConnected, &rm.SinceMs); err != nil {
				return err
			}
			rm.Connected = connected == 1
			s.Remotes = append(s.Remotes, rm)
			return nil
		})
}

// structureSide loads node infos, indices and shard copies at tc.
func (b *builder) structureSide(p part, tc int64) error {
	ctx, s, db := b.ctx, b.s, p.db()
	var defaults string
	if err := db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'cluster_defaults'`).Scan(&defaults); err == nil {
		blobJSON(ctx, p, defaults, &s.Defaults)
	}
	byID := map[int64]int{}
	type infoRow struct {
		i     int
		attrs string
	}
	var infos []infoRow
	if err := query(ctx, db, `SELECT n.id, n.name, n.es_id, n.generation, i.roles, i.ip, i.host, i.machine, i.es_version,
		i.os_name, i.os_kernel, i.jvm_version, i.start_ms, i.attrs_blob
		FROM node_info i JOIN node n ON n.id = i.node_id WHERE i.`+openAt+` ORDER BY n.name, n.es_id`, []any{tc, tc},
		func(r *sql.Rows) error {
			var id int64
			var name, esID, roles, attrs string
			var gen int
			var ip, host, machine, version, os, kernel, jvm sql.NullString
			var start sql.NullInt64
			if err := r.Scan(&id, &name, &esID, &gen, &roles, &ip, &host, &machine, &version, &os, &kernel, &jvm, &start, &attrs); err != nil {
				return err
			}
			i := b.node(name, esID, gen)
			n := &s.Nodes[i]
			if roles != "" {
				n.Roles = strings.Split(roles, ",")
			}
			n.Tiers = model.TiersOf(n.Roles)
			n.IP, n.Host, n.Machine, n.Version, n.OS, n.Kernel, n.JVM, n.StartMs =
				ip.String, host.String, machine.String, version.String, os.String, kernel.String, jvm.String, start.Int64
			byID[id] = i
			infos = append(infos, infoRow{i, attrs})
			return nil
		}); err != nil {
		return err
	}
	for _, ir := range infos {
		if err := blobJSON(ctx, p, ir.attrs, &s.Nodes[ir.i].Attrs); err != nil {
			return err
		}
	}
	// Nodes of this partition without open info (a shard may sit on one). They
	// are all read now: a query issued while the shard rows below are open
	// would wait for the connection those rows hold.
	type nodeRow struct {
		name, esID string
		gen        int
	}
	others := map[int64]nodeRow{}
	if err := query(ctx, db, `SELECT id, name, es_id, generation FROM node`, nil, func(r *sql.Rows) error {
		var id int64
		var n nodeRow
		if err := r.Scan(&id, &n.name, &n.esID, &n.gen); err != nil {
			return err
		}
		if _, ok := byID[id]; !ok {
			others[id] = n
		}
		return nil
	}); err != nil {
		return err
	}
	nodeByID := func(id int64) int {
		if i, ok := byID[id]; ok {
			return i
		}
		n, ok := others[id]
		if !ok {
			return -1
		}
		i := b.node(n.name, n.esID, n.gen)
		byID[id] = i
		return i
	}

	// Indices.
	idxPos := map[int64]int32{}
	type setRow struct {
		i    int
		hash string
	}
	var sets []setRow
	if err := query(ctx, db, `SELECT i.id, i.uuid, i.name, i.family, i.creation_date, i.hidden, v.status, v.pri, v.rep, v.settings_blob
		FROM index_iv v JOIN idx i ON i.id = v.index_id WHERE v.`+openAt+` ORDER BY i.name, i.uuid`, []any{tc, tc},
		func(r *sql.Rows) error {
			var id int64
			var ix model.Index
			var creation sql.NullInt64
			var hidden int
			var settings sql.NullString
			if err := r.Scan(&id, &ix.UUID, &ix.Name, &ix.Family, &creation, &hidden, &ix.Status, &ix.Pri, &ix.Rep, &settings); err != nil {
				return err
			}
			ix.CreationMs, ix.Hidden, ix.System = creation.Int64, hidden == 1, strings.HasPrefix(ix.Name, ".")
			ix.Family = b.e.familyOf(s.Cluster, ix.Name, ix.Family)
			ix.Docs, ix.StoreBytes, ix.PriStoreBytes = -1, -1, -1
			ix.Settings = map[string]string{}
			s.Indices = append(s.Indices, ix)
			pos := len(s.Indices) - 1
			idxPos[id] = int32(pos)
			if settings.String != "" {
				sets = append(sets, setRow{pos, settings.String})
			}
			return nil
		}); err != nil {
		return err
	}
	blobCache := map[string]map[string]string{}
	for _, sr := range sets {
		m, ok := blobCache[sr.hash]
		if !ok {
			if err := blobJSON(ctx, p, sr.hash, &m); err != nil {
				return err
			}
			blobCache[sr.hash] = m
		}
		s.Indices[sr.i].Settings = m // shared, read-only
	}
	if err := query(ctx, db, `SELECT index_id, max(ts), docs, store_bytes, pri_store_bytes FROM index_metric WHERE ts <= ? GROUP BY index_id`,
		[]any{tc}, func(r *sql.Rows) error {
			var id, ts int64
			var d, st, ps sql.NullInt64
			if err := r.Scan(&id, &ts, &d, &st, &ps); err != nil {
				return err
			}
			if pos, ok := idxPos[id]; ok {
				ix := &s.Indices[pos]
				ix.Docs, ix.StoreBytes, ix.PriStoreBytes = orMissing(d), orMissing(st), orMissing(ps)
			}
			return nil
		}); err != nil {
		return err
	}

	// Shard copies.
	s.Shards = make([]model.Shard, 0, 1024)
	return query(ctx, db, `SELECT index_id, shard, prirep, node_id, state, reloc_target_node_id, unassigned_reason, unassigned_at, valid_from
		FROM shard_iv WHERE `+openAt, []any{tc, tc}, func(r *sql.Rows) error {
		var indexID int64
		var sh model.Shard
		var prirep string
		var node, target, at sql.NullInt64
		var reason sql.NullString
		if err := r.Scan(&indexID, &sh.Shard, &prirep, &node, &sh.State, &target, &reason, &at, &sh.SinceMs); err != nil {
			return err
		}
		pos, ok := idxPos[indexID]
		if !ok {
			return nil
		}
		sh.Index, sh.Primary, sh.Node, sh.RelocTarget = pos, prirep == "p", -1, -1
		if node.Valid {
			sh.Node = int32(nodeByID(node.Int64))
		}
		if target.Valid {
			sh.RelocTarget = int32(nodeByID(target.Int64))
		}
		sh.Reason, sh.UnassignedAtMs = reason.String, at.Int64
		s.Shards = append(s.Shards, sh)
		return nil
	})
}

func orMissing(v sql.NullInt64) int64 {
	if !v.Valid {
		return esclient.Missing
	}
	return v.Int64
}

// SortShards orders copies by index name, shard, primary first.
func SortShards(s *model.State, shards []model.Shard) {
	sort.Slice(shards, func(i, j int) bool {
		a, b := shards[i], shards[j]
		if a.Index != b.Index {
			return s.Indices[a.Index].Name < s.Indices[b.Index].Name
		}
		if a.Shard != b.Shard {
			return a.Shard < b.Shard
		}
		return a.Primary && !b.Primary
	})
}
