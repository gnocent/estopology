package query

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

// Event is one journal entry (SPEC §8.2.8).
type Event struct {
	TsLo     int64           `json:"ts_lo,omitempty"` // uncertainty window ]ts_lo, ts_hi]
	TsHi     int64           `json:"ts_hi"`
	ExactTs  int64           `json:"exact_ts,omitempty"`
	Type     string          `json:"type"`
	Severity int             `json:"severity"`
	Kind     string          `json:"subject_kind"`
	Ref      string          `json:"subject_ref"`
	Group    string          `json:"group_ref,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

// EventFilter selects journal entries.
type EventFilter struct {
	From, To    int64
	Types       []string
	MaxSeverity int    // keep severity <= MaxSeverity (1 = critical only); 0 = all
	Node        string // subject or group
	Machine     string
	Family      string
	Subject     string // exact subject_ref (e.g. index uuid, "uuid/shard")
	// Group: only the events whose subject or group is one of Nodes or
	// Machines (node and machine events; shard moves are not searched).
	Group     bool
	Nodes     []string
	Machines  []string
	Limit     int
	BeforeTs  int64 // pagination: entries strictly older than (BeforeTs, BeforeKey)
	BeforeKey string
}

// EventPage is a page of the journal, newest first.
type EventPage struct {
	Events []Event `json:"events"`
	// Next cursor (empty when the end is reached).
	NextTs  int64  `json:"next_ts,omitempty"`
	NextKey string `json:"next_key,omitempty"`
}

// Events lists the journal of a cluster, newest first, across partitions.
func (e *Engine) Events(ctx context.Context, cluster string, f EventFilter) (*EventPage, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 200
	}
	return e.events(ctx, cluster, f)
}

// events is Events without the page size cap (internal computations).
func (e *Engine) events(ctx context.Context, cluster string, f EventFilter) (*EventPage, error) {
	to := f.To
	if f.BeforeTs > 0 && f.BeforeTs < to {
		to = f.BeforeTs
	}
	parts, err := e.partitions(ctx, cluster, f.From, to)
	if err != nil {
		return nil, err
	}
	// Cursor: (BeforeTs, "week#id") — keyset pagination inside the cursor's partition.
	cursorWeek, cursorID := "", int64(0)
	if w, id, ok := strings.Cut(f.BeforeKey, "#"); ok {
		cursorWeek = w
		cursorID, _ = strconv.ParseInt(id, 10, 64)
	}
	page := &EventPage{}
	type keyed struct {
		ev  Event
		key string
	}
	var all []keyed
	for i := len(parts) - 1; i >= 0 && len(all) <= f.Limit; i-- {
		p := parts[i]
		conds := []string{"ts_hi >= ?", "ts_hi <= ?"}
		args := []any{f.From, to}
		if f.BeforeTs > 0 && p.info.Week == cursorWeek {
			conds = append(conds, "(ts_hi < ? OR (ts_hi = ? AND id < ?))")
			args = append(args, f.BeforeTs, f.BeforeTs, cursorID)
		}
		if len(f.Types) > 0 {
			conds = append(conds, "type IN ("+strings.TrimSuffix(strings.Repeat("?,", len(f.Types)), ",")+")")
			for _, t := range f.Types {
				args = append(args, t)
			}
		}
		if f.MaxSeverity > 0 {
			conds = append(conds, "severity <= ?")
			args = append(args, f.MaxSeverity)
		}
		if f.Subject != "" {
			conds = append(conds, "subject_ref = ?")
			args = append(args, f.Subject)
		}
		if f.Node != "" {
			conds = append(conds, `(subject_ref = ? OR group_ref = ? OR (type LIKE 'shard_%' AND payload LIKE ? ESCAPE '\'))`)
			args = append(args, f.Node, f.Node, `%"`+likeEscape(f.Node)+`"%`)
		}
		if f.Machine != "" {
			nodes, err := nodesOnMachine(ctx, p, f.Machine)
			if err != nil {
				return nil, err
			}
			c := "(group_ref = ? OR subject_ref = ?"
			args = append(args, f.Machine, f.Machine)
			if len(nodes) > 0 {
				c += " OR subject_ref IN (" + strings.TrimSuffix(strings.Repeat("?,", len(nodes)), ",") + ")"
				for _, n := range nodes {
					args = append(args, n)
				}
			}
			conds = append(conds, c+")")
		}
		if f.Group {
			refs := append(append([]string{}, f.Nodes...), f.Machines...)
			if len(refs) == 0 {
				conds = append(conds, "0")
			} else {
				in := "(" + strings.TrimSuffix(strings.Repeat("?,", len(refs)), ",") + ")"
				// Shard events name their node as group: thousands of them would
				// bury what happened to the nodes and machines themselves.
				conds = append(conds, "type NOT LIKE 'shard_%' AND (subject_ref IN "+in+" OR group_ref IN "+in+")")
				for i := 0; i < 2; i++ {
					for _, x := range refs {
						args = append(args, x)
					}
				}
			}
		}
		if f.Family != "" {
			// The subject of an index event is its uuid, of a shard event
			// "uuid/shard": the part before the slash is looked up among the
			// indices of the family (one term whatever their number).
			conds = append(conds, `(group_ref = ? OR substr(subject_ref, 1, instr(subject_ref || '/', '/') - 1)
				IN (SELECT uuid FROM idx WHERE family = ?))`)
			args = append(args, f.Family, f.Family)
		}
		q := `SELECT id, coalesce(ts_lo, 0), ts_hi, coalesce(exact_ts, 0), type, severity, subject_kind, subject_ref,
			coalesce(group_ref, ''), coalesce(payload, '') FROM event WHERE ` + strings.Join(conds, " AND ") +
			` ORDER BY ts_hi DESC, id DESC LIMIT ?`
		args = append(args, f.Limit+1)
		if err := query(ctx, p.db(), q, args, func(r *sql.Rows) error {
			var id int64
			var ev Event
			var payload string
			if err := r.Scan(&id, &ev.TsLo, &ev.TsHi, &ev.ExactTs, &ev.Type, &ev.Severity, &ev.Kind, &ev.Ref, &ev.Group, &payload); err != nil {
				return err
			}
			if payload != "" {
				ev.Payload = json.RawMessage(payload)
			}
			key := p.info.Week + "#" + leftPad(id)
			all = append(all, keyed{ev, key})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].ev.TsHi != all[j].ev.TsHi {
			return all[i].ev.TsHi > all[j].ev.TsHi
		}
		return all[i].key > all[j].key
	})
	if len(all) > f.Limit {
		last := all[f.Limit-1]
		page.NextTs, page.NextKey = last.ev.TsHi, last.key
		all = all[:f.Limit]
	}
	for _, k := range all {
		page.Events = append(page.Events, k.ev)
	}
	return page, nil
}

func leftPad(id int64) string {
	s := []byte("000000000000")
	n := id
	for i := len(s) - 1; i >= 0 && n > 0; i-- {
		s[i] = byte('0' + n%10)
		n /= 10
	}
	return string(s)
}

func nodesOnMachine(ctx context.Context, p part, machine string) ([]string, error) {
	var out []string
	err := query(ctx, p.db(), `SELECT DISTINCT n.name FROM node_info i JOIN node n ON n.id = i.node_id WHERE i.machine = ?`,
		[]any{machine}, func(r *sql.Rows) error {
			var n string
			if err := r.Scan(&n); err != nil {
				return err
			}
			out = append(out, n)
			return nil
		})
	return out, err
}

// likeEscape makes s literal in a LIKE pattern using ESCAPE '\'.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Bucket is one slot of the time band (SPEC §8.2.2): worst health, unknown
// (failed captures), event density.
type Bucket struct {
	FromMs  int64  `json:"from"`
	ToMs    int64  `json:"to"`
	Status  string `json:"status,omitempty"` // worst health seen, "" = no probe
	Unknown bool   `json:"unknown"`
	Events  int    `json:"events"`
	Severe  int    `json:"severe"` // events of severity 1-2
}

// Timeline returns n buckets covering [from, to).
func (e *Engine) Timeline(ctx context.Context, cluster string, from, to int64, n int) ([]Bucket, error) {
	if n <= 0 || n > 2000 {
		n = 200
	}
	from, to = clampWindow(from, to)
	if to <= from {
		return nil, nil
	}
	width := (to - from) / int64(n)
	if (to-from)%int64(n) != 0 {
		width++
	}
	out := make([]Bucket, n)
	for i := range out {
		out[i].FromMs = from + int64(i)*width
		out[i].ToMs = min(out[i].FromMs+width, to)
	}
	rank := map[string]int{"green": 1, "yellow": 2, "red": 3}
	parts, err := e.partitions(ctx, cluster, from, to)
	if err != nil {
		return nil, err
	}
	for _, p := range parts {
		if err := query(ctx, p.db(), `SELECT (ts - ?) / ?, status FROM health WHERE ts >= ? AND ts < ?`,
			[]any{from, width, from, to}, func(r *sql.Rows) error {
				var i int
				var status string
				if err := r.Scan(&i, &status); err != nil {
					return err
				}
				if i >= 0 && i < n && rank[status] > rank[out[i].Status] {
					out[i].Status = status
				}
				return nil
			}); err != nil {
			return nil, err
		}
		if err := query(ctx, p.db(), `SELECT (ts - ?) / ?, count(*) FROM capture WHERE status = 'failed' AND ts >= ? AND ts < ? GROUP BY 1`,
			[]any{from, width, from, to}, func(r *sql.Rows) error {
				var i, c int
				if err := r.Scan(&i, &c); err != nil {
					return err
				}
				if i >= 0 && i < n {
					out[i].Unknown = true
				}
				return nil
			}); err != nil {
			return nil, err
		}
		if err := query(ctx, p.db(), `SELECT (ts_hi - ?) / ?, count(*), sum(severity <= 2) FROM event WHERE ts_hi >= ? AND ts_hi < ? GROUP BY 1`,
			[]any{from, width, from, to}, func(r *sql.Rows) error {
				var i, c, sev int
				if err := r.Scan(&i, &c, &sev); err != nil {
					return err
				}
				if i >= 0 && i < n {
					out[i].Events += c
					out[i].Severe += sev
				}
				return nil
			}); err != nil {
			return nil, err
		}
	}
	return out, nil
}
