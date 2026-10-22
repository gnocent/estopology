package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"estopo/internal/esclient"
)

// Markers kept in the partition meta table so that the mirror can be
// rebuilt exactly after a restart.
func (w *writer) saveMarkers() error {
	for k, v := range map[string]int64{
		"last_ts": w.ts, "last_presence_ts": w.cs.lastPresenceTs, "last_full_ts": w.cs.lastFullTs,
		"last_health_ts": w.cs.lastHealthTs, "last_settings_ts": w.cs.lastSettingsTs, "last_remotes_ts": w.cs.lastRemotesTs,
	} {
		if _, err := w.exec(`INSERT OR REPLACE INTO meta(key, value) VALUES (?, ?)`, k, strconv.FormatInt(v, 10)); err != nil {
			return err
		}
	}
	return nil
}

// keyframe copies every open interval, the last metric values and the
// referenced blobs into the new partition, so that it can be read alone
// (SPEC §6.4). Intervals keep their original start. The last closed info of
// a node absent from the last full capture is copied too, so that the
// mirror rebuilt from this partition alone (load) is the one in memory.
func (w *writer) keyframe(old *Partition) error {
	cs := w.cs
	if _, err := w.exec(`INSERT INTO capture(ts, end_ts, kind, trigger, status, duration_ms) VALUES (?,?,'keyframe','week_start','ok',0)`,
		w.ts, w.ts); err != nil {
		return err
	}
	// Blobs referenced by open state.
	hashes := map[string]bool{cs.settingsHash: true, cs.defaultsHash: true}
	for _, ns := range cs.nodes {
		if ns.info != nil {
			hashes[ns.info.attrs] = true
		}
		if ns.prevInfo != nil {
			hashes[ns.prevInfo.attrs] = true
		}
	}
	for _, is := range cs.indices {
		if is.iv != nil {
			hashes[is.iv.settings] = true
		}
	}
	for _, st := range cs.shards {
		hashes[st.vals.details] = true
	}
	for _, r := range cs.remotes {
		hashes[r.vals.config] = true
	}
	delete(hashes, "")
	if old != nil {
		keys := make([]string, 0, len(hashes))
		for h := range hashes {
			keys = append(keys, h)
		}
		sort.Strings(keys)
		for _, h := range keys {
			var data []byte
			if err := old.db.QueryRowContext(w.ctx, `SELECT data FROM blob WHERE hash = ?`, h).Scan(&data); err != nil {
				return fmt.Errorf("blob %s: %w", h, err)
			}
			if _, err := w.exec(`INSERT OR IGNORE INTO blob(hash, data) VALUES (?, ?)`, h, data); err != nil {
				return err
			}
			w.p.blobs[h] = true
		}
	}

	// Nodes.
	nodes := make([]*nodeState, 0, len(cs.nodes))
	for _, ns := range cs.nodes {
		nodes = append(nodes, ns)
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].key.name+"\x00"+nodes[i].key.esID < nodes[j].key.name+"\x00"+nodes[j].key.esID
	})
	cs.nodeBy = map[int64]*nodeState{}
	for _, ns := range nodes {
		id, err := w.insert(`INSERT INTO node(name, es_id, generation, first_seen) VALUES (?,?,?,?)`, ns.key.name, ns.key.esID, ns.gen, ns.firstSeen)
		if err != nil {
			return err
		}
		ns.id = id
		cs.nodeBy[id] = ns
		if ns.present {
			if ns.presenceRow, err = w.insert(`INSERT INTO node_presence(node_id, valid_from) VALUES (?,?)`, id, ns.presentSince); err != nil {
				return err
			}
		}
		if v := ns.info; v != nil {
			if ns.infoRow, err = w.insert(`INSERT INTO node_info(node_id, valid_from, roles, ip, transport, http_addr, host, machine,
				es_version, os_name, os_kernel, os_arch, processors, jvm_version, start_ms, attrs_blob) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				id, ns.infoSince, v.roles, v.ip, v.transport, v.http, v.host, v.machine, v.version, v.osName, v.kernel, v.arch,
				v.processors, v.jvm, v.startMs, v.attrs); err != nil {
				return err
			}
		} else if v := ns.prevInfo; v != nil {
			// A node absent from the last full capture: its last info, closed (it
			// is not in the state of the week), is what it is compared with when
			// it comes back (node_upgraded), also after a restart.
			if _, err = w.exec(`INSERT INTO node_info(node_id, valid_from, valid_to, roles, ip, transport, http_addr, host, machine,
				es_version, os_name, os_kernel, os_arch, processors, jvm_version, start_ms, attrs_blob) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				id, ns.prevSince, ns.prevTo, v.roles, v.ip, v.transport, v.http, v.host, v.machine, v.version, v.osName, v.kernel, v.arch,
				v.processors, v.jvm, v.startMs, v.attrs); err != nil {
				return err
			}
		}
		if m := ns.metric; m != nil {
			if _, err := w.exec(`INSERT INTO node_metric(node_id, ts, proc_cpu, os_cpu, load1, cgroup_cpu_ns, heap_pct, disk_total,
				disk_used, disk_avail, disk_indices, shards, io_r_ops, io_w_ops, io_r_kb, io_w_kb) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				id, ns.metricTs, nullMissing(int64(m.procCPU)), nullMissing(int64(m.osCPU)), nullMissingF(m.load1), nullMissing(m.cgroupNs),
				nullMissing(int64(m.heap)), nullMissing(m.diskTotal), nullMissing(m.diskUsed), nullMissing(m.diskAvail),
				nullMissing(m.diskIdx), nullMissing(m.shards), nullMissing(m.ioROps), nullMissing(m.ioWOps),
				nullMissing(m.ioRKB), nullMissing(m.ioWKB)); err != nil {
				return err
			}
		}
	}

	// Last health.
	if h := cs.lastHealth; h != nil {
		if _, err := w.exec(`INSERT OR REPLACE INTO health(ts, status, nodes, data_nodes, active_primary, active, relocating,
			initializing, unassigned, unassigned_primary, delayed_unassigned, pending_tasks) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			cs.lastHealthTs, h.Status, h.Nodes, h.DataNodes, h.ActivePrimary, h.Active, h.Relocating, h.Initializing,
			h.Unassigned, nullMissing(int64(h.UnassignedPrimary)), h.DelayedUnassigned, h.PendingTasks); err != nil {
			return err
		}
	}

	// Indices.
	indices := make([]*indexState, 0, len(cs.indices))
	for _, is := range cs.indices {
		indices = append(indices, is)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i].uuid < indices[j].uuid })
	for _, is := range indices {
		id, err := w.insert(`INSERT INTO idx(uuid, name, family, creation_date, version_created, hidden) VALUES (?,?,?,?,?,?)`,
			is.uuid, is.name, is.family, nullMissing(is.creation), nullStr(is.versionCreated), boolInt(is.hidden))
		if err != nil {
			return err
		}
		is.id = id
		if iv := is.iv; iv != nil {
			if is.ivRow, err = w.insert(`INSERT INTO index_iv(index_id, valid_from, status, pri, rep, settings_blob) VALUES (?,?,?,?,?,?)`,
				id, is.ivSince, iv.status, iv.pri, iv.rep, nullStr(iv.settings)); err != nil {
				return err
			}
		}
		if m := is.metric; m != nil {
			if _, err := w.exec(`INSERT INTO index_metric(index_id, ts, docs, store_bytes, pri_store_bytes) VALUES (?,?,?,?,?)`,
				id, is.metricTs, nullMissing(m.docs), nullMissing(m.store), nullMissing(m.priStore)); err != nil {
				return err
			}
		}
	}

	// Shard copies.
	shards := make([]*shardState, 0, len(cs.shards))
	for _, st := range cs.shards {
		shards = append(shards, st)
	}
	sort.Slice(shards, func(i, j int) bool { return shardKeyLess(shards[i].key, shards[j].key) })
	for _, st := range shards {
		row, err := w.insertShard(st.key, st.vals, st.since)
		if err != nil {
			return err
		}
		st.row = row
		if st.metricTs > 0 && st.key.node != (nodeKey{}) {
			if err := w.insertShardMetric(st.key, st.metricTs, st.docs, st.store); err != nil {
				return err
			}
		}
	}

	// Cluster settings, defaults, remotes.
	if cs.settingsHash != "" {
		row, err := w.insert(`INSERT INTO cluster_settings_iv(valid_from, blob) VALUES (?,?)`, cs.settingsSince, cs.settingsHash)
		if err != nil {
			return err
		}
		cs.settingsRow = row
	}
	if cs.defaultsHash != "" {
		if _, err := w.exec(`INSERT OR REPLACE INTO meta(key, value) VALUES ('cluster_defaults', ?)`, cs.defaultsHash); err != nil {
			return err
		}
	}
	aliases := make([]string, 0, len(cs.remotes))
	for a := range cs.remotes {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	for _, a := range aliases {
		r := cs.remotes[a]
		row, err := w.insert(`INSERT INTO remote_iv(alias, valid_from, mode, connected, nodes_connected, blob) VALUES (?,?,?,?,?,?)`,
			a, r.since, r.vals.mode, boolInt(r.vals.connected), r.vals.nodes, r.vals.config)
		if err != nil {
			return err
		}
		r.row = row
	}
	return nil
}

func shardKeyLess(a, b shardKey) bool {
	if a.uuid != b.uuid {
		return a.uuid < b.uuid
	}
	if a.shard != b.shard {
		return a.shard < b.shard
	}
	if a.primary != b.primary {
		return a.primary
	}
	if a.node != b.node {
		return a.node.name+"\x00"+a.node.esID < b.node.name+"\x00"+b.node.esID
	}
	return a.slot < b.slot
}

// pruneNodes drops from the mirror, when a week starts, the nodes the
// history no longer refers to, so that the keyframe does not copy every
// node ever seen into every partition for ever. A node is kept while it is
// present, described by the last full capture, holds or receives a shard
// copy, or was present at some time in a partition still in the catalogue:
// the reference is the retention itself (by age or by size), no other delay
// to configure. Once its last presence has been deleted with its partition,
// nothing can be shown about the node; if its name comes back, generations
// are counted from what is left.
func (in *Ingester) pruneNodes(ctx context.Context, cs *cstate, next *Partition) {
	keep := map[nodeKey]bool{}
	for k, ns := range cs.nodes {
		if ns.present || ns.info != nil {
			keep[k] = true
		}
	}
	if len(keep) == len(cs.nodes) {
		return
	}
	for _, st := range cs.shards {
		keep[st.key.node], keep[st.vals.target] = true, true
	}
	parts, err := in.st.Partitions(ctx, cs.id)
	if err != nil {
		return // nothing pruned this week
	}
	for _, info := range parts {
		if info.Week == next.Week.Key {
			continue
		}
		p, err := in.st.OpenExisting(ctx, info)
		if errors.Is(err, ErrNoPartition) {
			continue // deleted: what it held no longer counts
		}
		if err != nil {
			return
		}
		rows, err := p.rdb.QueryContext(ctx, `SELECT name, es_id FROM node WHERE id IN (SELECT node_id FROM node_presence)`)
		if err != nil {
			return
		}
		for rows.Next() {
			var k nodeKey
			if err := rows.Scan(&k.name, &k.esID); err != nil {
				rows.Close()
				return
			}
			keep[k] = true
		}
		rows.Close()
		if rows.Err() != nil {
			return
		}
	}
	gens := map[string]int{}
	for k, ns := range cs.nodes {
		if !keep[k] {
			delete(cs.nodes, k)
			delete(cs.nodeBy, ns.id)
			continue
		}
		gens[k.name] = max(gens[k.name], ns.gen)
	}
	cs.nodeGen = gens
}

// load rebuilds the mirror of a cluster from the latest partition that
// holds a record.
func (in *Ingester) load(ctx context.Context, id string) (*cstate, error) {
	cs := newCState(id)
	parts, err := in.st.Partitions(ctx, id)
	if err != nil {
		return cs, err
	}
	// A later partition without the last_ts marker was created by an
	// ingestion that failed, or by Note at the very start of a week: it is
	// not the state of the cluster. Loading the one before makes the first
	// record of that week a keyframe, as it must be.
	var p *Partition
	for i := len(parts) - 1; i >= 0 && p == nil; i-- {
		c, err := in.st.OpenExisting(ctx, parts[i])
		if errors.Is(err, ErrNoPartition) {
			continue // deleted since listed, or its file is gone
		}
		if err != nil {
			return nil, fmt.Errorf("open partition %s of %s: %w", parts[i].Week, id, err)
		}
		var marked int
		if err := c.db.QueryRowContext(ctx, `SELECT count(*) FROM meta WHERE key = 'last_ts'`).Scan(&marked); err != nil {
			return nil, err
		}
		if marked > 0 {
			p = c
		}
	}
	if p == nil {
		return cs, nil
	}
	cs.part = p
	db := p.db
	blobJSON := func(hash string, v any) error {
		if hash == "" {
			return nil
		}
		b, err := p.Blob(ctx, hash)
		if err != nil {
			return fmt.Errorf("blob %s: %w", hash, err)
		}
		return json.Unmarshal(b, v)
	}
	q := func(query string, scan func(*sql.Rows) error) error {
		rows, err := db.QueryContext(ctx, query)
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

	// Markers. Partitions written before the per-section markers existed
	// fall back to the last record, as these windows then did.
	cs.lastSettingsTs, cs.lastRemotesTs = -1, -1
	if err := q(`SELECT key, value FROM meta WHERE key LIKE 'last_%' OR key = 'cluster_defaults'`, func(r *sql.Rows) error {
		var k, v string
		if err := r.Scan(&k, &v); err != nil {
			return err
		}
		n, _ := strconv.ParseInt(v, 10, 64)
		switch k {
		case "last_ts":
			cs.lastTs = n
		case "last_presence_ts":
			cs.lastPresenceTs = n
		case "last_full_ts":
			cs.lastFullTs = n
		case "last_health_ts":
			cs.lastHealthTs = n
		case "last_settings_ts":
			cs.lastSettingsTs = n
		case "last_remotes_ts":
			cs.lastRemotesTs = n
		case "cluster_defaults":
			cs.defaultsHash = v
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if cs.lastSettingsTs < 0 {
		cs.lastSettingsTs = cs.lastTs
	}
	if cs.lastRemotesTs < 0 {
		cs.lastRemotesTs = cs.lastTs
	}
	if cs.lastHealthTs > 0 {
		h := esclient.Health{UnassignedPrimary: esclient.Missing}
		var up sql.NullInt64
		err := db.QueryRowContext(ctx, `SELECT status, nodes, data_nodes, active_primary, active, relocating, initializing, unassigned,
			unassigned_primary, delayed_unassigned, pending_tasks FROM health WHERE ts = ?`, cs.lastHealthTs).Scan(&h.Status, &h.Nodes,
			&h.DataNodes, &h.ActivePrimary, &h.Active, &h.Relocating, &h.Initializing, &h.Unassigned, &up, &h.DelayedUnassigned, &h.PendingTasks)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if err == nil {
			if up.Valid {
				h.UnassignedPrimary = int(up.Int64)
			}
			cs.lastHealth = &h
		}
	}

	// Nodes.
	if err := q(`SELECT id, name, es_id, generation, first_seen FROM node`, func(r *sql.Rows) error {
		ns := &nodeState{}
		if err := r.Scan(&ns.id, &ns.key.name, &ns.key.esID, &ns.gen, &ns.firstSeen); err != nil {
			return err
		}
		cs.nodes[ns.key] = ns
		cs.nodeBy[ns.id] = ns
		cs.nodeGen[ns.key.name] = max(cs.nodeGen[ns.key.name], ns.gen)
		return nil
	}); err != nil {
		return nil, err
	}
	if err := q(`SELECT rowid, node_id, valid_from FROM node_presence WHERE valid_to IS NULL`, func(r *sql.Rows) error {
		var row, nid, from int64
		if err := r.Scan(&row, &nid, &from); err != nil {
			return err
		}
		if ns := cs.nodeBy[nid]; ns != nil {
			ns.present, ns.presenceRow, ns.presentSince = true, row, from
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := q(`SELECT rowid, node_id, valid_from, roles, ip, transport, http_addr, host, machine, es_version, os_name, os_kernel,
		os_arch, processors, jvm_version, start_ms, attrs_blob FROM node_info WHERE valid_to IS NULL`, func(r *sql.Rows) error {
		var row, nid, from int64
		var v nodeInfoVals
		if err := r.Scan(&row, &nid, &from, &v.roles, &v.ip, &v.transport, &v.http, &v.host, &v.machine, &v.version, &v.osName,
			&v.kernel, &v.arch, &v.processors, &v.jvm, &v.startMs, &v.attrs); err != nil {
			return err
		}
		if ns := cs.nodeBy[nid]; ns != nil {
			ns.info, ns.infoRow, ns.infoSince = &v, row, from
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// Nodes absent at the last full capture: their last closed info, to detect
	// what changed when they come back.
	if err := q(`SELECT node_id, valid_from, valid_to, roles, ip, transport, http_addr, host, machine, es_version, os_name, os_kernel,
		os_arch, processors, jvm_version, start_ms, attrs_blob FROM node_info WHERE valid_to IS NOT NULL ORDER BY valid_to`, func(r *sql.Rows) error {
		var nid, from, to int64
		var v nodeInfoVals
		if err := r.Scan(&nid, &from, &to, &v.roles, &v.ip, &v.transport, &v.http, &v.host, &v.machine, &v.version, &v.osName,
			&v.kernel, &v.arch, &v.processors, &v.jvm, &v.startMs, &v.attrs); err != nil {
			return err
		}
		if ns := cs.nodeBy[nid]; ns != nil && ns.info == nil {
			ns.prevInfo, ns.prevSince, ns.prevTo = &v, from, to // the latest closed one wins
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for _, ns := range cs.nodes {
		for _, in := range []*nodeInfoVals{ns.info, ns.prevInfo} {
			if in != nil {
				if err := blobJSON(in.attrs, &in.attrMap); err != nil {
					return nil, err
				}
			}
		}
	}
	if err := q(`SELECT node_id, max(ts), proc_cpu, os_cpu, load1, cgroup_cpu_ns, heap_pct, disk_total, disk_used, disk_avail,
		disk_indices, shards, io_r_ops, io_w_ops, io_r_kb, io_w_kb FROM node_metric GROUP BY node_id`, func(r *sql.Rows) error {
		var nid, ts int64
		var v [14]sql.NullFloat64
		dst := []any{&nid, &ts}
		for i := range v {
			dst = append(dst, &v[i])
		}
		if err := r.Scan(dst...); err != nil {
			return err
		}
		ns := cs.nodeBy[nid]
		if ns == nil {
			return nil
		}
		iv := func(i int) int64 {
			if !v[i].Valid {
				return esclient.Missing
			}
			return int64(v[i].Float64)
		}
		m := nodeMetricVals{procCPU: int(iv(0)), osCPU: int(iv(1)), load1: float64(iv(2)), cgroupNs: iv(3), heap: int(iv(4)),
			diskTotal: iv(5), diskUsed: iv(6), diskAvail: iv(7), diskIdx: iv(8), shards: iv(9),
			ioROps: iv(10), ioWOps: iv(11), ioRKB: iv(12), ioWKB: iv(13)}
		if v[2].Valid {
			m.load1 = v[2].Float64
		}
		ns.metric, ns.metricTs = &m, ts
		return nil
	}); err != nil {
		return nil, err
	}

	// Indices (only those with an open interval are alive).
	byID := map[int64]*indexState{}
	if err := q(`SELECT i.id, i.uuid, i.name, i.family, i.creation_date, i.version_created, i.hidden,
		v.rowid, v.valid_from, v.status, v.pri, v.rep, v.settings_blob
		FROM idx i JOIN index_iv v ON v.index_id = i.id AND v.valid_to IS NULL`, func(r *sql.Rows) error {
		is := &indexState{iv: &indexIV{}}
		var creation sql.NullInt64
		var vc, settings sql.NullString
		if err := r.Scan(&is.id, &is.uuid, &is.name, &is.family, &creation, &vc, &is.hidden,
			&is.ivRow, &is.ivSince, &is.iv.status, &is.iv.pri, &is.iv.rep, &settings); err != nil {
			return err
		}
		is.creation = esclient.Missing
		if creation.Valid {
			is.creation = creation.Int64
		}
		is.versionCreated, is.iv.settings = vc.String, settings.String
		cs.indices[is.uuid] = is
		byID[is.id] = is
		return nil
	}); err != nil {
		return nil, err
	}
	for _, is := range cs.indices {
		if err := blobJSON(is.iv.settings, &is.iv.settingsMap); err != nil {
			return nil, err
		}
	}
	if err := q(`SELECT index_id, max(ts), docs, store_bytes, pri_store_bytes FROM index_metric GROUP BY index_id`, func(r *sql.Rows) error {
		var id, ts int64
		var d, s, ps sql.NullInt64
		if err := r.Scan(&id, &ts, &d, &s, &ps); err != nil {
			return err
		}
		if is := byID[id]; is != nil {
			is.metric = &indexMetricVals{docs: nullToMissing(d), store: nullToMissing(s), priStore: nullToMissing(ps)}
			is.metricTs = ts
		}
		return nil
	}); err != nil {
		return nil, err
	}

	// Shard copies.
	if err := q(`SELECT id, index_id, shard, prirep, node_id, slot, state, reloc_target_node_id, unassigned_reason, unassigned_at,
		unassigned_details_blob, valid_from FROM shard_iv WHERE valid_to IS NULL`, func(r *sql.Rows) error {
		st := &shardState{docs: esclient.Missing, store: esclient.Missing}
		var indexID int64
		var prirep string
		var node, target, at sql.NullInt64
		var reason, details sql.NullString
		if err := r.Scan(&st.row, &indexID, &st.key.shard, &prirep, &node, &st.key.slot, &st.vals.state, &target, &reason, &at,
			&details, &st.since); err != nil {
			return err
		}
		is := byID[indexID]
		if is == nil {
			return nil
		}
		st.key.uuid, st.key.primary = is.uuid, prirep == "p"
		if node.Valid {
			if ns := cs.nodeBy[node.Int64]; ns != nil {
				st.key.node = ns.key
			}
		}
		if target.Valid {
			if ns := cs.nodeBy[target.Int64]; ns != nil {
				st.vals.target = ns.key
			}
		}
		st.vals.reason, st.vals.at, st.vals.details = reason.String, at.Int64, details.String
		cs.shards[st.key] = st
		return nil
	}); err != nil {
		return nil, err
	}
	if err := q(`SELECT index_id, shard, prirep, node_id, max(ts), docs, store_bytes FROM shard_metric GROUP BY index_id, shard, prirep, node_id`,
		func(r *sql.Rows) error {
			var indexID, nodeID, ts int64
			var shard int
			var prirep string
			var d, s sql.NullInt64
			if err := r.Scan(&indexID, &shard, &prirep, &nodeID, &ts, &d, &s); err != nil {
				return err
			}
			is, ns := byID[indexID], cs.nodeBy[nodeID]
			if is == nil || ns == nil {
				return nil
			}
			// Assigned copies always have slot 0 (slots rank unassigned copies).
			if st := cs.shards[shardKey{uuid: is.uuid, shard: shard, primary: prirep == "p", node: ns.key}]; st != nil {
				st.docs, st.store, st.metricTs = nullToMissing(d), nullToMissing(s), ts
			}
			return nil
		}); err != nil {
		return nil, err
	}

	// Cluster settings and remotes.
	var settingsBlob string
	err = db.QueryRowContext(ctx, `SELECT rowid, valid_from, blob FROM cluster_settings_iv WHERE valid_to IS NULL`).
		Scan(&cs.settingsRow, &cs.settingsSince, &settingsBlob)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if err == nil {
		var m map[string]map[string]string
		if err := blobJSON(settingsBlob, &m); err != nil {
			return nil, err
		}
		cs.settingsHash, cs.settingsMap = settingsBlob, map[string]string{}
		for k, v := range m["persistent"] {
			cs.settingsMap["p:"+k] = v
		}
		for k, v := range m["transient"] {
			cs.settingsMap["t:"+k] = v
		}
	}
	if err := q(`SELECT rowid, alias, valid_from, mode, connected, nodes_connected, blob FROM remote_iv WHERE valid_to IS NULL`,
		func(r *sql.Rows) error {
			rs := &remoteState{}
			var alias string
			var connected int
			if err := r.Scan(&rs.row, &alias, &rs.since, &rs.vals.mode, &connected, &rs.vals.nodes, &rs.vals.config); err != nil {
				return err
			}
			rs.vals.connected = connected == 1
			cs.remotes[alias] = rs
			return nil
		}); err != nil {
		return nil, err
	}
	return cs, nil
}

func nullToMissing(v sql.NullInt64) int64 {
	if !v.Valid {
		return esclient.Missing
	}
	return v.Int64
}
