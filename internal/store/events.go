package store

import (
	"encoding/json"
	"sort"
)

// Event types derived at ingestion (SPEC §6.5).
const (
	evHealthChanged          = "health_changed"
	evNodeLeft               = "node_left"
	evNodeJoined             = "node_joined"
	evNodeNewGeneration      = "node_new_generation"
	evNodeNameDuplicate      = "node_name_duplicate" // two nodes with the same name at once (SPEC §5.4)
	evNodeUpgraded           = "node_upgraded"
	evNodeAttrsChanged       = "node_attrs_changed"
	evMachineLost            = "machine_lost"
	evIndexCreated           = "index_created"
	evIndexDeleted           = "index_deleted"
	evIndexClosed            = "index_closed"
	evIndexOpened            = "index_opened"
	evIndexSettingsChanged   = "index_settings_changed"
	evClusterSettingsChanged = "cluster_settings_changed"
	evShardRelocated         = "shard_relocated"
	evShardPromoted          = "shard_promoted"
	evShardLost              = "shard_lost"
	evShardUnassigned        = "shard_unassigned"
	evShardAssigned          = "shard_assigned"
	evRemoteDisconnected     = "remote_disconnected"
	evRemoteConnected        = "remote_connected"
	evCaptureFailed          = "capture_failed"
	evClusterUUIDChanged     = "cluster_uuid_changed"
)

// EventTypes lists every event type (API filters, i18n catalogues).
var EventTypes = []string{
	evHealthChanged, evNodeLeft, evNodeJoined, evNodeNewGeneration, evNodeNameDuplicate, evNodeUpgraded, evNodeAttrsChanged,
	evMachineLost, evIndexCreated, evIndexDeleted, evIndexClosed, evIndexOpened, evIndexSettingsChanged,
	evClusterSettingsChanged, evShardRelocated, evShardPromoted, evShardLost, evShardUnassigned, evShardAssigned,
	evRemoteDisconnected, evRemoteConnected, evCaptureFailed, evClusterUUIDChanged,
}

// Event severities (1 = most severe).
const (
	sevCritical = 1
	sevMajor    = 2
	sevMinor    = 3
	sevInfo     = 4
)

func sevForNodeEvent(typ string) int {
	if typ == evNodeNewGeneration {
		return sevMinor
	}
	return sevInfo
}

// event writes one event. tsLo is the last capture where the old state was
// seen (0 if unknown); exact is a date given by ES (0 if none).
func (w *writer) event(typ string, sev int, kind, ref, group string, tsLo, exact int64, payload any) error {
	var p any
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		p = string(b)
	}
	var lo any
	if tsLo > 0 && tsLo < w.ts {
		lo = tsLo
	}
	var ex any
	if exact > 0 {
		ex = exact
	}
	_, err := w.exec(`INSERT INTO event(ts_lo, ts_hi, exact_ts, type, severity, subject_kind, subject_ref, group_ref, payload)
		VALUES (?,?,?,?,?,?,?,?,?)`, lo, w.ts, ex, typ, sev, kind, ref, nullStr(group), p)
	if err == nil {
		w.res.Events++
	}
	return err
}

// machinesLost emits machine_lost when every node of a machine that was
// present left in the same window (SPEC §6.5, B3).
func (w *writer) machinesLost(left []*nodeState) error {
	if len(left) < 2 {
		return nil
	}
	byMachine := map[string][]string{}
	for _, ns := range left {
		if m := machineOf(ns); m != "" {
			byMachine[m] = append(byMachine[m], ns.key.name)
		}
	}
	machines := make([]string, 0, len(byMachine))
	for m := range byMachine {
		machines = append(machines, m)
	}
	sort.Strings(machines)
	for _, m := range machines {
		gone := byMachine[m]
		if len(gone) < 2 {
			continue
		}
		remaining := 0
		for _, ns := range w.cs.nodes {
			if ns.present && machineOf(ns) == m {
				remaining++
			}
		}
		if remaining > 0 {
			continue
		}
		sort.Strings(gone)
		if err := w.event(evMachineLost, sevCritical, "machine", m, m, w.cs.lastPresenceTs, 0,
			map[string]any{"nodes": gone}); err != nil {
			return err
		}
	}
	return nil
}
