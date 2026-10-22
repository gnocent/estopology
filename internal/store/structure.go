package store

import (
	"sort"
	"strconv"
	"strings"

	"estopo/internal/esclient"
	"estopo/internal/record"
)

// Structure of a full capture: node infos, indices, shard copies.

func (w *writer) structure(r *record.Record) error {
	if err := w.nodeInfos(r.Nodes); err != nil {
		return err
	}
	uuidByName, err := w.indices(r.Indices, r.IndexSettings)
	if err != nil {
		return err
	}
	return w.shards(r.Shards, uuidByName)
}

func (a nodeInfoVals) same(b nodeInfoVals) bool {
	a.attrMap, b.attrMap = nil, nil
	return a.roles == b.roles && a.ip == b.ip && a.transport == b.transport && a.http == b.http && a.host == b.host &&
		a.machine == b.machine && a.version == b.version && a.osName == b.osName && a.kernel == b.kernel &&
		a.arch == b.arch && a.jvm == b.jvm && a.processors == b.processors && a.startMs == b.startMs && a.attrs == b.attrs
}

func (w *writer) nodeInfos(nodes []esclient.NodeInfo) error {
	cs := w.cs
	machineAttr := w.in.opt.MachineAttr(cs.id)
	seen := map[nodeKey]bool{}
	for _, n := range nodes {
		k := nodeKey{n.Name, n.ID}
		seen[k] = true
		ns, _, err := w.node(k)
		if err != nil {
			return err
		}
		attrs := n.Attributes
		if attrs == nil {
			attrs = map[string]string{}
		}
		ah, err := w.blobJSON(attrs)
		if err != nil {
			return err
		}
		roles := append([]string(nil), n.Roles...)
		sort.Strings(roles)
		machine := n.Host
		if machineAttr != "" && attrs[machineAttr] != "" {
			machine = attrs[machineAttr]
		}
		if machine == "" {
			machine = n.IP
		}
		v := nodeInfoVals{roles: strings.Join(roles, ","), ip: n.IP, transport: n.TransportAddress, http: n.HTTPAddress,
			host: n.Host, machine: machine, version: n.Version, osName: n.OSPrettyName, kernel: n.OSKernel, arch: n.OSArch,
			jvm: n.JVMVersion, processors: n.Processors, startMs: n.JVMStartMs, attrs: ah, attrMap: attrs}
		if ns.info != nil && ns.info.same(v) {
			continue
		}
		// A node back after an absence (a restart seen by a full capture) is
		// compared with what it was before leaving: an upgrade restarts the
		// node, and must not go unnoticed because a capture fell in between.
		old := ns.info
		if old == nil {
			old, ns.prevInfo = ns.prevInfo, nil
		} else if _, err := w.exec(`UPDATE node_info SET valid_to = ? WHERE rowid = ?`, w.ts, ns.infoRow); err != nil {
			return err
		}
		if old != nil {
			if old.version != v.version || old.osName != v.osName || old.kernel != v.kernel || old.jvm != v.jvm {
				if err := w.event(evNodeUpgraded, sevInfo, "node", k.name, v.machine, cs.lastFullTs, 0, map[string]any{
					"from": map[string]string{"es": old.version, "os": old.osName, "kernel": old.kernel, "jvm": old.jvm},
					"to":   map[string]string{"es": v.version, "os": v.osName, "kernel": v.kernel, "jvm": v.jvm},
				}); err != nil {
					return err
				}
			}
			if old.roles != v.roles || old.attrs != v.attrs || old.machine != v.machine {
				if err := w.event(evNodeAttrsChanged, sevMinor, "node", k.name, v.machine, cs.lastFullTs, 0, map[string]any{
					"roles": map[string]string{"from": old.roles, "to": v.roles},
					"attrs": diffMaps(old.attrMap, v.attrMap),
				}); err != nil {
					return err
				}
			}
		}
		row, err := w.insert(`INSERT INTO node_info(node_id, valid_from, roles, ip, transport, http_addr, host, machine, es_version,
			os_name, os_kernel, os_arch, processors, jvm_version, start_ms, attrs_blob) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			ns.id, w.ts, v.roles, v.ip, v.transport, v.http, v.host, v.machine, v.version, v.osName, v.kernel, v.arch,
			v.processors, v.jvm, v.startMs, v.attrs)
		if err != nil {
			return err
		}
		vv := v
		ns.info, ns.infoRow, ns.infoSince = &vv, row, w.ts
	}
	var gone []*nodeState
	for k, ns := range cs.nodes {
		if ns.info != nil && !seen[k] {
			gone = append(gone, ns)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].id < gone[j].id })
	for _, ns := range gone {
		if _, err := w.exec(`UPDATE node_info SET valid_to = ? WHERE rowid = ?`, w.ts, ns.infoRow); err != nil {
			return err
		}
		ns.prevInfo, ns.prevSince, ns.prevTo, ns.info = ns.info, ns.infoSince, w.ts, nil
	}
	return nil
}

// Settings stored on the index dimension rather than in the settings blob.
var staticIndexSettings = map[string]bool{
	"index.uuid": true, "index.creation_date": true, "index.version.created": true, "index.hidden": true,
}

func (a *indexIV) same(b *indexIV) bool {
	return a.status == b.status && a.pri == b.pri && a.rep == b.rep && a.settings == b.settings
}

func (w *writer) indices(indices []esclient.CatIndex, settings []record.IndexSettings) (map[string]string, error) {
	cs := w.cs
	first := cs.lastFullTs == 0 && len(cs.indices) == 0 // initial import: no creation events
	var byName map[string]map[string]string
	if settings != nil {
		byName = make(map[string]map[string]string, len(settings))
		for _, s := range settings {
			byName[s.Index] = s.Settings
		}
	}
	db := w.in.opt.DeadBand
	uuidByName := make(map[string]string, len(indices))
	seen := make(map[string]bool, len(indices))
	for _, ci := range indices {
		uuidByName[ci.Name] = ci.UUID
		seen[ci.UUID] = true
		is := cs.indices[ci.UUID]
		raw := byName[ci.Name]
		if is == nil {
			is = &indexState{uuid: ci.UUID, name: ci.Name, family: w.in.opt.Family(cs.id, ci.Name), creation: ci.CreationDateMs,
				versionCreated: raw["index.version.created"], hidden: raw["index.hidden"] == "true", metricTs: 0}
			id, err := w.insert(`INSERT INTO idx(uuid, name, family, creation_date, version_created, hidden) VALUES (?,?,?,?,?,?)`,
				is.uuid, is.name, is.family, nullMissing(is.creation), nullStr(is.versionCreated), boolInt(is.hidden))
			if err != nil {
				return nil, err
			}
			is.id = id
			cs.indices[ci.UUID] = is
			w.createdIdx[ci.UUID] = true
		}
		iv := &indexIV{status: ci.Status, pri: ci.Pri, rep: ci.Rep}
		switch {
		case byName == nil && is.iv != nil: // settings unknown this time: keep the previous ones
			iv.settings, iv.settingsMap = is.iv.settings, is.iv.settingsMap
		case raw != nil:
			m := make(map[string]string, len(raw))
			for k, v := range raw {
				if !staticIndexSettings[k] {
					m[k] = v
				}
			}
			h, err := w.blobJSON(m)
			if err != nil {
				return nil, err
			}
			iv.settings, iv.settingsMap = h, m
		}
		if is.iv == nil || !is.iv.same(iv) {
			if is.iv != nil {
				if iv.rep < is.iv.rep {
					w.repDecreased[is.uuid] = true
				}
				if _, err := w.exec(`UPDATE index_iv SET valid_to = ? WHERE rowid = ?`, w.ts, is.ivRow); err != nil {
					return nil, err
				}
				if err := w.indexChangeEvents(is, is.iv, iv); err != nil {
					return nil, err
				}
			} else if !first {
				if err := w.event(evIndexCreated, sevInfo, "index", is.uuid, is.family, cs.lastFullTs, max(is.creation, 0),
					map[string]any{"name": is.name, "pri": iv.pri, "rep": iv.rep}); err != nil {
					return nil, err
				}
			}
			row, err := w.insert(`INSERT INTO index_iv(index_id, valid_from, status, pri, rep, settings_blob) VALUES (?,?,?,?,?,?)`,
				is.id, w.ts, iv.status, iv.pri, iv.rep, nullStr(iv.settings))
			if err != nil {
				return nil, err
			}
			is.iv, is.ivRow, is.ivSince = iv, row, w.ts
		}
		// Metrics (dead band); closed indices report none.
		if ci.StoreBytes >= 0 || ci.Docs >= 0 {
			m := indexMetricVals{docs: ci.Docs, store: ci.StoreBytes, priStore: ci.PriStoreBytes}
			if is.metric == nil || relChanged(is.metric.docs, m.docs, db.Rel, 0) ||
				relChanged(is.metric.store, m.store, db.Rel, db.AbsBytes) || relChanged(is.metric.priStore, m.priStore, db.Rel, db.AbsBytes) {
				if _, err := w.exec(`INSERT OR REPLACE INTO index_metric(index_id, ts, docs, store_bytes, pri_store_bytes) VALUES (?,?,?,?,?)`,
					is.id, w.ts, nullMissing(m.docs), nullMissing(m.store), nullMissing(m.priStore)); err != nil {
					return nil, err
				}
				is.metric, is.metricTs = &m, w.ts
			}
		}
	}
	var gone []*indexState
	for uuid, is := range cs.indices {
		if !seen[uuid] {
			gone = append(gone, is)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].uuid < gone[j].uuid })
	for _, is := range gone {
		if _, err := w.exec(`UPDATE index_iv SET valid_to = ? WHERE rowid = ?`, w.ts, is.ivRow); err != nil {
			return nil, err
		}
		if err := w.event(evIndexDeleted, sevMinor, "index", is.uuid, is.family, cs.lastFullTs, 0,
			map[string]any{"name": is.name}); err != nil {
			return nil, err
		}
		w.deletedIdx[is.uuid] = true
		delete(cs.indices, is.uuid)
	}
	return uuidByName, nil
}

func (w *writer) indexChangeEvents(is *indexState, old, cur *indexIV) error {
	lo := w.cs.lastFullTs
	if old.status != cur.status {
		typ := evIndexOpened
		if cur.status == "close" {
			typ = evIndexClosed
		}
		sev := sevInfo
		if typ == evIndexClosed {
			sev = sevMinor
		}
		if err := w.event(typ, sev, "index", is.uuid, is.family, lo, 0, map[string]any{"name": is.name}); err != nil {
			return err
		}
	}
	if old.settings != cur.settings || old.pri != cur.pri || old.rep != cur.rep {
		// Settings unknown on one side (_all/_settings failed, or the index
		// came after it) are not compared: learning them is not a change.
		known := old.settings != "" && cur.settings != ""
		var changes []map[string]string
		if known {
			changes = diffMaps(old.settingsMap, cur.settingsMap)
		}
		if old.rep != cur.rep && (old.settings == cur.settings || !known) {
			changes = append(changes, map[string]string{"key": "index.number_of_replicas", "from": strconv.Itoa(old.rep), "to": strconv.Itoa(cur.rep)})
		}
		if len(changes) > 0 {
			return w.event(evIndexSettingsChanged, sevInfo, "index", is.uuid, is.family, lo, 0,
				map[string]any{"name": is.name, "changes": changes})
		}
	}
	return nil
}

// shardObs is a copy observed in a capture.
type shardObs struct {
	key   shardKey
	vals  shardVals
	docs  int64
	store int64
}

// shardChange is an interval opened or closed by a capture (used for events).
type shardChange struct {
	key  shardKey
	vals shardVals
}

func (w *writer) shards(shards []esclient.CatShard, uuidByName map[string]string) error {
	cs := w.cs
	db := w.in.opt.DeadBand
	obs := make([]shardObs, 0, len(shards))
	type group struct {
		uuid    string
		shard   int
		primary bool
	}
	unassigned := map[group][]int{}
	for _, s := range shards {
		uuid := uuidByName[s.Index]
		if uuid == "" {
			continue // index created between the two calls: provisional, seen next time (SPEC §5.5)
		}
		o := shardObs{key: shardKey{uuid: uuid, shard: s.Shard, primary: s.Primary}, docs: s.Docs, store: s.StoreBytes,
			vals: shardVals{state: s.State, reason: s.UnassignedReason, at: s.UnassignedAtMs}}
		if s.Node != "" {
			o.key.node = nodeKey{s.Node, s.NodeID}
			if _, _, err := w.node(o.key.node); err != nil {
				return err
			}
		} else {
			g := group{uuid, s.Shard, s.Primary}
			unassigned[g] = append(unassigned[g], len(obs))
		}
		if s.RelocTargetNode != "" {
			o.vals.target = nodeKey{s.RelocTargetNode, s.RelocTargetID}
			if _, _, err := w.node(o.vals.target); err != nil {
				return err
			}
		}
		if s.UnassignedDetails != "" {
			h, err := w.p.putBlob(w.ctx, w.tx, []byte(s.UnassignedDetails))
			if err != nil {
				return err
			}
			o.vals.details = h
		}
		obs = append(obs, o)
	}
	// Unassigned copies of the same (index, shard, p/r) are ranked (slot) by
	// unassigned date so that they keep their identity between captures.
	for _, idxs := range unassigned {
		sort.SliceStable(idxs, func(i, j int) bool {
			a, b := obs[idxs[i]].vals, obs[idxs[j]].vals
			if a.at != b.at {
				return a.at < b.at
			}
			return a.reason+a.details < b.reason+b.details
		})
		for slot, i := range idxs {
			obs[i].key.slot = slot
		}
	}

	var opened, closed []shardChange
	seen := make(map[shardKey]bool, len(obs))
	for i := range obs {
		o := &obs[i]
		if seen[o.key] {
			continue // duplicate line: keep the first
		}
		seen[o.key] = true
		st := cs.shards[o.key]
		if st != nil && st.vals == o.vals {
			// unchanged interval
		} else {
			if st != nil {
				if _, err := w.exec(`UPDATE shard_iv SET valid_to = ? WHERE id = ?`, w.ts, st.row); err != nil {
					return err
				}
				closed = append(closed, shardChange{st.key, st.vals})
			}
			row, err := w.insertShard(o.key, o.vals, w.ts)
			if err != nil {
				return err
			}
			if st == nil {
				st = &shardState{key: o.key, docs: esclient.Missing, store: esclient.Missing}
				cs.shards[o.key] = st
			}
			st.vals, st.row, st.since = o.vals, row, w.ts
			opened = append(opened, shardChange{o.key, o.vals})
		}
		if o.key.node != (nodeKey{}) && (o.docs >= 0 || o.store >= 0) &&
			(st.metricTs == 0 || relChanged(st.docs, o.docs, db.Rel, 0) || relChanged(st.store, o.store, db.Rel, db.AbsBytes)) {
			if err := w.insertShardMetric(o.key, w.ts, o.docs, o.store); err != nil {
				return err
			}
			st.docs, st.store, st.metricTs = o.docs, o.store, w.ts
		}
	}
	var gone []*shardState
	for k, st := range cs.shards {
		if !seen[k] {
			gone = append(gone, st)
		}
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].row < gone[j].row })
	for _, st := range gone {
		if _, err := w.exec(`UPDATE shard_iv SET valid_to = ? WHERE id = ?`, w.ts, st.row); err != nil {
			return err
		}
		closed = append(closed, shardChange{st.key, st.vals})
		delete(cs.shards, st.key)
	}
	w.res.ShardsOpened += len(opened)
	w.res.ShardsClosed += len(closed)
	return w.shardEvents(opened, closed)
}

func (w *writer) insertShard(k shardKey, v shardVals, from int64) (int64, error) {
	cs := w.cs
	var nodeID, targetID any
	if k.node != (nodeKey{}) {
		nodeID = cs.nodes[k.node].id
	}
	if v.target != (nodeKey{}) {
		targetID = cs.nodes[v.target].id
	}
	prirep := "r"
	if k.primary {
		prirep = "p"
	}
	var at any
	if v.at > 0 {
		at = v.at
	}
	return w.insert(`INSERT INTO shard_iv(index_id, shard, prirep, node_id, slot, state, reloc_target_node_id,
		unassigned_reason, unassigned_at, unassigned_details_blob, valid_from) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		cs.indices[k.uuid].id, k.shard, prirep, nodeID, k.slot, v.state, targetID, nullStr(v.reason), at, nullStr(v.details), from)
}

func (w *writer) insertShardMetric(k shardKey, ts, docs, store int64) error {
	prirep := "r"
	if k.primary {
		prirep = "p"
	}
	_, err := w.exec(`INSERT OR REPLACE INTO shard_metric(index_id, shard, prirep, node_id, ts, docs, store_bytes) VALUES (?,?,?,?,?,?,?)`,
		w.cs.indices[k.uuid].id, k.shard, prirep, w.cs.nodes[k.node].id, ts, nullMissing(docs), nullMissing(store))
	return err
}
