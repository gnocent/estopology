// Package i18n holds the server-side FR/EN catalogues: labels of exports
// (SPEC §15 B22) and of the codes the API returns (event types, concerns,
// constraint reasons). The web UI has its own catalogues for the screens.
package i18n

import (
	"sort"
	"strings"
)

// Lang is a supported language.
type Lang string

const (
	FR Lang = "fr"
	EN Lang = "en"
)

// Normalize maps "fr-FR", "EN", "" ... to a supported language (default FR).
func Normalize(s string) Lang {
	s = strings.ToLower(strings.TrimSpace(s))
	if strings.HasPrefix(s, "en") {
		return EN
	}
	return FR
}

// T returns the label of key, or the key itself when unknown.
func T(l Lang, key string) string {
	if v, ok := catalogues[l][key]; ok {
		return v
	}
	return key
}

// Keys returns the sorted keys of a catalogue (tests, UI export).
func Keys(l Lang) []string {
	out := make([]string, 0, len(catalogues[l]))
	for k := range catalogues[l] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var catalogues = map[Lang]map[string]string{FR: fr, EN: en}

var fr = map[string]string{
	// Event types (SPEC §6.5).
	"event.health_changed":           "Changement de santé",
	"event.node_left":                "Nœud parti",
	"event.node_joined":              "Nœud arrivé",
	"event.node_new_generation":      "Nouvelle génération de nœud",
	"event.node_name_duplicate":      "Même nom porté par plusieurs nœuds",
	"event.node_upgraded":            "Nœud mis à jour",
	"event.node_attrs_changed":       "Attributs de nœud modifiés",
	"event.machine_lost":             "Machine perdue",
	"event.index_created":            "Index créé",
	"event.index_deleted":            "Index supprimé",
	"event.index_closed":             "Index fermé",
	"event.index_opened":             "Index ouvert",
	"event.index_settings_changed":   "Settings d'index modifiés",
	"event.cluster_settings_changed": "Cluster settings modifiés",
	"event.shard_relocated":          "Shard déplacé",
	"event.shard_promoted":           "Réplica promu",
	"event.shard_lost":               "Copie perdue",
	"event.shard_unassigned":         "Copie non assignée",
	"event.shard_assigned":           "Copie assignée",
	"event.remote_disconnected":      "Remote CCS déconnecté",
	"event.remote_connected":         "Remote CCS connecté",
	"event.capture_failed":           "Capture en échec",
	"event.cluster_uuid_changed":     "Identité du cluster changée",
	// Severity (SPEC §7.1).
	"severity.1": "Critique", "severity.2": "Majeur", "severity.3": "Mineur", "severity.4": "Info",
	"concern.unassigned_primaries":   "Primaires non assignés",
	"concern.missing_nodes":          "Nœuds manquants",
	"concern.unassigned_replicas":    "Réplicas non assignés",
	"concern.high_watermark":         "Nœuds au-dessus du watermark high",
	"concern.infeasible_constraints": "Contraintes d'allocation infaisables",
	"concern.moving_shards":          "Shards en initialisation ou relocation",
	// Allocation constraints (SPEC §7.5).
	"constraint.infeasible":                "Infaisable",
	"constraint.tight":                     "Tendu",
	"constraint.ok":                        "OK",
	"reason.no_eligible_node":              "Aucun nœud éligible",
	"reason.not_enough_nodes":              "Pas assez de nœuds éligibles",
	"reason.same_host":                     "Pas assez de machines (same_shard.host)",
	"reason.total_shards_per_node":         "total_shards_per_node de l'index trop bas",
	"reason.cluster_total_shards_per_node": "total_shards_per_node du cluster atteint",
	"reason.awareness":                     "Awareness : pas assez de valeurs disponibles",
	"reason.lose_node":                     "Infaisable si l'on perd un nœud",
	"reason.lose_machine":                  "Infaisable si l'on perd une machine",
	// Diff (SPEC §8.2.5) and losses (§7.6).
	"change.added": "Ajouté", "change.removed": "Supprimé", "change.changed": "Modifié",
	"change.moved": "Déplacé", "change.lost": "Perdu", "change.new": "Nouveau", "change.promoted": "Promu",
	"after.promoted": "Réplica promu", "after.reallocated": "Réalloué", "after.unassigned": "Toujours non assigné",
	"after.deleted": "Index supprimé",
	"sheet.summary": "Synthèse", "sheet.nodes": "Nœuds", "sheet.indices": "Indices", "sheet.shards": "Shards",
	"sheet.index_settings": "Settings d’index", "sheet.cluster_settings": "Cluster settings", "sheet.remotes": "CCS", "sheet.events": "Événements",
	"sheet.losses": "Pertes", "sheet.deleted": "Suppressions", "sheet.constraints": "Contraintes",
	"col.metric": "Mesure", "col.value": "Valeur", "col.name": "Nom", "col.es_id": "Id ES", "col.change": "Changement",
	"col.machine": "Machine", "col.details": "Détails", "col.uuid": "UUID", "col.index": "Index", "col.family": "Famille",
	"col.shard": "Shard", "col.prirep": "P/R", "col.from": "Avant", "col.to": "Après", "col.key": "Clé",
	"col.alias": "Alias", "col.date": "Date", "col.date_lo": "Au plus tôt", "col.type": "Type", "col.severity": "Gravité",
	"col.subject": "Sujet", "col.group": "Groupe", "col.payload": "Données", "col.node": "Nœud", "col.zone": "Zone",
	"col.size": "Taille (octets)", "col.after": "Après la perte", "col.after_node": "Nouveau nœud",
	"col.after_date": "Date de réallocation", "col.reason": "Raison", "col.status": "Statut", "col.required": "Copies requises",
	"col.eligible": "Nœuds éligibles", "col.explanation": "Explication", "col.group_kind": "Groupé par",
	"summary.nodes_added": "Nœuds ajoutés", "summary.nodes_removed": "Nœuds retirés", "summary.nodes_changed": "Nœuds modifiés",
	"summary.indices_added": "Indices créés", "summary.indices_removed": "Indices supprimés", "summary.indices_changed": "Indices modifiés",
	"summary.shards_moved": "Copies déplacées", "summary.shards_lost": "Copies perdues", "summary.shards_new": "Nouvelles copies",
	"summary.settings_changed": "Indices aux settings modifiés", "summary.cluster_settings_changed": "Cluster settings modifiés",
	"summary.remotes_changed": "Remotes CCS modifiés", "summary.shards_promoted": "Réplicas promus",
	"prirep.p": "Primaire", "prirep.r": "Réplica",
}

var en = map[string]string{
	"event.health_changed":           "Health changed",
	"event.node_left":                "Node left",
	"event.node_joined":              "Node joined",
	"event.node_new_generation":      "New node generation",
	"event.node_name_duplicate":      "Same name carried by several nodes",
	"event.node_upgraded":            "Node upgraded",
	"event.node_attrs_changed":       "Node attributes changed",
	"event.machine_lost":             "Machine lost",
	"event.index_created":            "Index created",
	"event.index_deleted":            "Index deleted",
	"event.index_closed":             "Index closed",
	"event.index_opened":             "Index opened",
	"event.index_settings_changed":   "Index settings changed",
	"event.cluster_settings_changed": "Cluster settings changed",
	"event.shard_relocated":          "Shard relocated",
	"event.shard_promoted":           "Replica promoted",
	"event.shard_lost":               "Copy lost",
	"event.shard_unassigned":         "Copy unassigned",
	"event.shard_assigned":           "Copy assigned",
	"event.remote_disconnected":      "CCS remote disconnected",
	"event.remote_connected":         "CCS remote connected",
	"event.capture_failed":           "Capture failed",
	"event.cluster_uuid_changed":     "Cluster identity changed",
	"severity.1":                     "Critical", "severity.2": "Major", "severity.3": "Minor", "severity.4": "Info",
	"concern.unassigned_primaries":         "Unassigned primaries",
	"concern.missing_nodes":                "Missing nodes",
	"concern.unassigned_replicas":          "Unassigned replicas",
	"concern.high_watermark":               "Nodes above the high watermark",
	"concern.infeasible_constraints":       "Infeasible allocation constraints",
	"concern.moving_shards":                "Initializing or relocating shards",
	"constraint.infeasible":                "Infeasible",
	"constraint.tight":                     "Tight",
	"constraint.ok":                        "OK",
	"reason.no_eligible_node":              "No eligible node",
	"reason.not_enough_nodes":              "Not enough eligible nodes",
	"reason.same_host":                     "Not enough machines (same_shard.host)",
	"reason.total_shards_per_node":         "Index total_shards_per_node too low",
	"reason.cluster_total_shards_per_node": "Cluster total_shards_per_node reached",
	"reason.awareness":                     "Awareness: not enough available values",
	"reason.lose_node":                     "Infeasible if a node is lost",
	"reason.lose_machine":                  "Infeasible if a machine is lost",
	"change.added":                         "Added", "change.removed": "Removed", "change.changed": "Changed",
	"change.moved": "Moved", "change.lost": "Lost", "change.new": "New", "change.promoted": "Promoted",
	"after.promoted": "Replica promoted", "after.reallocated": "Reallocated", "after.unassigned": "Still unassigned",
	"after.deleted": "Index deleted",
	"sheet.summary": "Summary", "sheet.nodes": "Nodes", "sheet.indices": "Indices", "sheet.shards": "Shards",
	"sheet.index_settings": "Index settings", "sheet.cluster_settings": "Cluster settings", "sheet.remotes": "CCS", "sheet.events": "Events",
	"sheet.losses": "Losses", "sheet.deleted": "Deletions", "sheet.constraints": "Constraints",
	"col.metric": "Metric", "col.value": "Value", "col.name": "Name", "col.es_id": "ES id", "col.change": "Change",
	"col.machine": "Machine", "col.details": "Details", "col.uuid": "UUID", "col.index": "Index", "col.family": "Family",
	"col.shard": "Shard", "col.prirep": "P/R", "col.from": "Before", "col.to": "After", "col.key": "Key",
	"col.alias": "Alias", "col.date": "Date", "col.date_lo": "Not before", "col.type": "Type", "col.severity": "Severity",
	"col.subject": "Subject", "col.group": "Group", "col.payload": "Data", "col.node": "Node", "col.zone": "Zone",
	"col.size": "Size (bytes)", "col.after": "After the loss", "col.after_node": "New node",
	"col.after_date": "Reallocation date", "col.reason": "Reason", "col.status": "Status", "col.required": "Required copies",
	"col.eligible": "Eligible nodes", "col.explanation": "Explanation", "col.group_kind": "Grouped by",
	"summary.nodes_added": "Nodes added", "summary.nodes_removed": "Nodes removed", "summary.nodes_changed": "Nodes changed",
	"summary.indices_added": "Indices created", "summary.indices_removed": "Indices deleted", "summary.indices_changed": "Indices changed",
	"summary.shards_moved": "Copies moved", "summary.shards_lost": "Copies lost", "summary.shards_new": "New copies",
	"summary.settings_changed": "Indices with changed settings", "summary.cluster_settings_changed": "Cluster settings changed",
	"summary.remotes_changed": "CCS remotes changed", "summary.shards_promoted": "Replicas promoted",
	"prirep.p": "Primary", "prirep.r": "Replica",
}
