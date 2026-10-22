package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"estopo/internal/i18n"
	"estopo/internal/query"
	"estopo/internal/store"
)

// Exports (SPEC §8.2.5, B22): the same tables as CSV, JSON or XLSX (one
// sheet per category), with labels in the UI language. Dates are ISO 8601
// UTC.

type table struct {
	sheet string   // i18n key of the sheet
	cols  []string // i18n keys of the columns
	rows  [][]any
}

func iso(ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05Z")
}

func prirep(p bool) string {
	if p {
		return "p"
	}
	return "r"
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	ctx, cluster, what := r.Context(), r.PathValue("id"), r.PathValue("what")
	if !store.ValidClusterID(cluster) { // the id ends up in the file name
		writeError(w, http.StatusNotFound, "not_found", "unknown cluster")
		return
	}
	l := lang(r)
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "" {
		format = "xlsx"
	}
	var tables []table
	var raw any
	switch what {
	case "diff":
		a, b, err := s.diffWindow(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d, err := s.engine.Diff(ctx, cluster, a, b)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		raw, tables = d, diffTables(d, l)
	case "events":
		f, err := s.eventFilter(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		f.Limit = 1000
		var all []query.Event
		for len(all) < 100000 {
			p, err := s.engine.Events(ctx, cluster, f)
			if err != nil {
				s.fail(w, r, err)
				return
			}
			all = append(all, p.Events...)
			if p.NextTs == 0 {
				break
			}
			f.BeforeTs, f.BeforeKey = p.NextTs, p.NextKey
		}
		t := table{sheet: "sheet.events", cols: []string{"col.date", "col.date_lo", "col.type", "col.severity", "col.subject", "col.group", "col.payload"}}
		for _, e := range all {
			t.rows = append(t.rows, []any{iso(e.TsHi), iso(e.TsLo), i18n.T(l, "event."+e.Type), e.Severity, e.Ref, e.Group, string(e.Payload)})
		}
		raw, tables = all, []table{t}
	case "losses":
		a, b, err := s.diffWindow(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		ls, err := s.engine.Losses(ctx, cluster, a, b)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		t := table{sheet: "sheet.losses", cols: []string{"col.group_kind", "col.group", "col.node", "col.zone", "col.index", "col.shard",
			"col.prirep", "col.size", "col.date", "col.after", "col.after_node", "col.after_date", "col.reason"}}
		for _, g := range ls.Groups {
			for _, c := range g.Copies {
				t.rows = append(t.rows, []any{g.Kind, g.Name, c.Node, c.Zone, c.Index, c.Shard, i18n.T(l, "prirep."+prirep(c.Primary)),
					c.SizeBytes, iso(c.TsHi), i18n.T(l, "after."+c.After), c.AfterNode, iso(c.AfterTs), c.AfterReason})
			}
		}
		del := table{sheet: "sheet.deleted", cols: []string{"col.date", "col.index", "col.family", "col.uuid"}}
		for _, d := range ls.Deleted {
			del.rows = append(del.rows, []any{iso(d.Ts), d.Name, d.Family, d.UUID})
		}
		raw, tables = ls, []table{t, del}
	case "constraints":
		cs, err := s.constraints(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		t := table{sheet: "sheet.constraints", cols: []string{"col.index", "col.family", "col.status", "col.reason", "col.required", "col.eligible", "col.details"}}
		for _, c := range cs {
			t.rows = append(t.rows, []any{c.Name, c.Family, i18n.T(l, "constraint."+c.Status), reasonLabel(l, c.Reason), c.Required, c.Eligible, paramsText(c.Params)})
		}
		raw, tables = cs, []table{t}
	default:
		writeError(w, http.StatusNotFound, "not_found", "unknown export "+what)
		return
	}

	name := fmt.Sprintf("estopo-%s-%s-%s", cluster, what, s.clock.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Cache-Control", "no-store")
	switch format {
	case "json":
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
		writeJSON(w, http.StatusOK, raw)
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
		w.Write([]byte("\xEF\xBB\xBF")) // BOM: Excel opens UTF-8 correctly
		writeCSV(w, tables, l)
	case "xlsx":
		f, err := xlsx(tables, l)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.xlsx"`)
		if err := f.Write(w); err != nil {
			s.log.Warn("xlsx export", "err", err)
		}
	default:
		s.fail(w, r, badRequest("format must be csv, json or xlsx"))
	}
}

func reasonLabel(l i18n.Lang, reason string) string {
	if reason == "" {
		return ""
	}
	return i18n.T(l, "reason."+reason)
}

func paramsText(p map[string]string) string {
	if len(p) == 0 {
		return ""
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(k + "=" + p[k])
	}
	return b.String()
}

func diffTables(d *query.Diff, l i18n.Lang) []table {
	sum := table{sheet: "sheet.summary", cols: []string{"col.metric", "col.value"}}
	for _, kv := range []struct {
		k string
		v int
	}{{"nodes_added", d.Summary.NodesAdded}, {"nodes_removed", d.Summary.NodesRemoved}, {"nodes_changed", d.Summary.NodesChanged},
		{"indices_added", d.Summary.IndicesAdded}, {"indices_removed", d.Summary.IndicesRemoved}, {"indices_changed", d.Summary.IndicesChanged},
		{"shards_moved", d.Summary.ShardsMoved}, {"shards_lost", d.Summary.ShardsLost}, {"shards_new", d.Summary.ShardsNew},
		{"shards_promoted", d.Summary.ShardsPromoted},
		{"settings_changed", d.Summary.SettingsChanged}, {"cluster_settings_changed", d.Summary.ClusterSettings},
		{"remotes_changed", d.Summary.RemotesChanged}} {
		sum.rows = append(sum.rows, []any{i18n.T(l, "summary."+kv.k), kv.v})
	}
	sum.rows = append(sum.rows, []any{"A", iso(d.A)}, []any{"B", iso(d.B)})

	nodes := table{sheet: "sheet.nodes", cols: []string{"col.name", "col.es_id", "col.change", "col.machine", "col.details"}}
	for _, n := range d.Nodes {
		nodes.rows = append(nodes.rows, []any{n.Name, n.ESID, i18n.T(l, "change."+n.Change), n.Machine, changesText(n.Changes)})
	}
	indices := table{sheet: "sheet.indices", cols: []string{"col.index", "col.uuid", "col.family", "col.change", "col.details"}}
	for _, x := range d.Indices {
		indices.rows = append(indices.rows, []any{x.Name, x.UUID, x.Family, i18n.T(l, "change."+x.Change), changesText(x.Changes)})
	}
	shards := table{sheet: "sheet.shards", cols: []string{"col.index", "col.shard", "col.prirep", "col.change", "col.from", "col.to", "col.family"}}
	for _, s := range d.Shards {
		shards.rows = append(shards.rows, []any{s.Index, s.Shard, i18n.T(l, "prirep."+prirep(s.Primary)), i18n.T(l, "change."+s.Change),
			strings.Join(s.From, ", "), strings.Join(s.To, ", "), s.Family})
	}
	idxSettings := table{sheet: "sheet.index_settings", cols: []string{"col.index", "col.key", "col.from", "col.to", "col.family"}}
	for _, c := range d.IndexSettingChanges() {
		idxSettings.rows = append(idxSettings.rows, []any{c.Index, c.Key, c.From, c.To, c.Family})
	}
	settings := table{sheet: "sheet.cluster_settings", cols: []string{"col.key", "col.from", "col.to"}}
	for _, c := range d.ClusterSettings {
		settings.rows = append(settings.rows, []any{c.Key, c.From, c.To})
	}
	remotes := table{sheet: "sheet.remotes", cols: []string{"col.alias", "col.from", "col.to"}}
	for _, rm := range d.Remotes {
		remotes.rows = append(remotes.rows, []any{rm.Alias, rm.From, rm.To})
	}
	return []table{sum, nodes, indices, shards, idxSettings, settings, remotes}
}

func changesText(cs []query.SettingDiff) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("%s: %s → %s", c.Key, c.From, c.To))
	}
	return strings.Join(parts, "; ")
}

func writeCSV(w http.ResponseWriter, tables []table, l i18n.Lang) {
	cw := csv.NewWriter(w)
	for i, t := range tables {
		if i > 0 {
			cw.Write(nil)
			cw.Write([]string{i18n.T(l, t.sheet)})
		}
		header := make([]string, len(t.cols))
		for j, c := range t.cols {
			header[j] = i18n.T(l, c)
		}
		cw.Write(header)
		for _, row := range t.rows {
			rec := make([]string, len(row))
			for j, v := range row {
				rec[j] = fmt.Sprint(v)
				switch v.(type) {
				case int, int32, int64, uint64, float64, bool:
				default:
					rec[j] = csvText(rec[j])
				}
			}
			cw.Write(rec)
		}
	}
	cw.Flush()
}

// csvText neutralises a text that a spreadsheet would run as a formula when
// the file is opened (names and settings come from the clusters): it gets a
// leading quote. Numbers are not concerned, nor XLSX files, whose cells are
// typed.
func csvText(s string) string {
	if s != "" && strings.IndexByte("=+-@\t\r", s[0]) >= 0 {
		return "'" + s
	}
	return s
}

func xlsx(tables []table, l i18n.Lang) (*excelize.File, error) {
	f := excelize.NewFile()
	bold, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return nil, err
	}
	for i, t := range tables {
		name := sheetName(i18n.T(l, t.sheet))
		if i == 0 {
			if err := f.SetSheetName("Sheet1", name); err != nil {
				return nil, err
			}
		} else if _, err := f.NewSheet(name); err != nil {
			return nil, err
		}
		sw, err := f.NewStreamWriter(name)
		if err != nil {
			return nil, err
		}
		// Panes must be declared before the first row of a stream writer.
		if err := sw.SetPanes(&excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
			return nil, err
		}
		header := make([]any, len(t.cols))
		for j, c := range t.cols {
			header[j] = excelize.Cell{Value: i18n.T(l, c), StyleID: bold}
		}
		if err := sw.SetRow("A1", header); err != nil {
			return nil, err
		}
		for k, row := range t.rows {
			cell, _ := excelize.CoordinatesToCellName(1, k+2)
			if err := sw.SetRow(cell, row); err != nil {
				return nil, err
			}
		}
		if err := sw.Flush(); err != nil {
			return nil, err
		}
	}
	return f, nil
}

// sheetName respects Excel's limits (31 chars, no []:*?/\).
func sheetName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) {
			return '-'
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 31 {
		s = string(r[:31])
	}
	return s
}
