package api

import (
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"

	"estopo/internal/buildinfo"
)

// Metrics setting (meta.db "metrics"): /metrics is off by default (SPEC
// §12.3). When on, it is served without authentication, like most
// Prometheus targets; it holds counters and dates, no topology.
type metricsSetting struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) metricsEnabled(r *http.Request) bool {
	var m metricsSetting
	s.store.GetSetting(r.Context(), "metrics", &m)
	return m.Enabled
}

// promWriter writes the Prometheus text format (version 0.0.4).
type promWriter struct {
	w    io.Writer
	seen map[string]bool
}

func (p *promWriter) metric(name, typ, help string, value float64, labels ...string) {
	if !p.seen[name] {
		fmt.Fprintf(p.w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
		p.seen[name] = true
	}
	fmt.Fprint(p.w, name)
	if len(labels) > 0 {
		parts := make([]string, 0, len(labels)/2)
		for i := 0; i+1 < len(labels); i += 2 {
			v := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(labels[i+1])
			parts = append(parts, labels[i]+`="`+v+`"`)
		}
		fmt.Fprint(p.w, "{"+strings.Join(parts, ",")+"}")
	}
	fmt.Fprintf(p.w, " %g\n", value)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.metricsEnabled(r) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	p := &promWriter{w: w, seen: map[string]bool{}}
	p.metric("estopo_build_info", "gauge", "Version of estopo.", 1, "version", buildinfo.Version, "commit", buildinfo.Commit)
	p.metric("estopo_uptime_seconds", "gauge", "Time since the start.", s.clock.Now().Sub(s.started).Seconds())
	p.metric("estopo_ha_role", "gauge", "High-availability role (1 for the current one).", 1, "role", s.role())

	if s.collector != nil {
		sts := s.collector.Status()
		sort.Slice(sts, func(i, j int) bool { return sts[i].ID < sts[j].ID })
		for _, st := range sts {
			susp := 0.0
			if st.Suspended {
				susp = 1
			}
			p.metric("estopo_cluster_suspended", "gauge", "Collection suspended (cluster identity changed).", susp, "cluster", st.ID)
			if c := st.LastProbe; c != nil {
				p.metric("estopo_probe_last_timestamp_seconds", "gauge", "Start of the last light probe.", float64(c.StartMs)/1000, "cluster", st.ID)
				p.metric("estopo_probe_last_duration_seconds", "gauge", "Duration of the last light probe.", float64(c.DurationMs)/1000, "cluster", st.ID)
				p.metric("estopo_probe_last_ok", "gauge", "1 when the last light probe succeeded.", okValue(string(c.Status)), "cluster", st.ID)
			}
			if c := st.LastFull; c != nil {
				p.metric("estopo_capture_last_timestamp_seconds", "gauge", "Start of the last full capture.", float64(c.StartMs)/1000, "cluster", st.ID)
				p.metric("estopo_capture_last_duration_seconds", "gauge", "Duration of the last full capture.", float64(c.DurationMs)/1000, "cluster", st.ID)
				p.metric("estopo_capture_last_ok", "gauge", "1 when the last full capture succeeded.", okValue(string(c.Status)), "cluster", st.ID)
			}
		}
	}

	if parts, err := s.store.Partitions(r.Context(), ""); err == nil {
		size := map[string]int64{}
		for _, pi := range parts {
			size[pi.Cluster] += pi.SizeBytes
		}
		ids := make([]string, 0, len(size))
		for id := range size {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			p.metric("estopo_storage_bytes", "gauge", "Size of the history partitions.", float64(size[id]), "cluster", id)
		}
	}
	if s.ha != nil {
		if rs := s.ha.Status().Replication; rs != nil {
			p.metric("estopo_replication_lag_seconds", "gauge", "Newest record on the primary minus newest ingested here.", float64(rs.LagMs)/1000)
			p.metric("estopo_replication_last_sync_timestamp_seconds", "gauge", "Last complete synchronisation with the primary.", float64(rs.LastSyncMs)/1000)
			blocked := 0.0
			if rs.Blocked != "" {
				blocked = 1
			}
			p.metric("estopo_replication_blocked", "gauge", "1 when the standby refuses to replicate (another or emptied primary).", blocked)
		}
	}
	if s.extraMetrics != nil {
		s.extraMetrics(p.metric)
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	p.metric("estopo_go_heap_bytes", "gauge", "Go heap in use.", float64(ms.HeapAlloc))
	p.metric("estopo_go_sys_bytes", "gauge", "Memory obtained from the system by Go.", float64(ms.Sys))
	p.metric("estopo_go_goroutines", "gauge", "Goroutines.", float64(runtime.NumGoroutine()))
}

func okValue(status string) float64 {
	if status == "ok" {
		return 1
	}
	return 0
}

func (s *Server) handleAdminMetricsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, metricsSetting{Enabled: s.metricsEnabled(r)})
}

func (s *Server) handleAdminMetricsPut(w http.ResponseWriter, r *http.Request) {
	var m metricsSetting
	if err := decodeJSON(r, &m); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.store.SetSetting(r.Context(), "metrics", m); err != nil {
		s.fail(w, r, err)
		return
	}
	s.store.Audit(r.Context(), s.user(r).Name, "metrics.save", "", fmt.Sprint(m.Enabled))
	writeJSON(w, http.StatusOK, m)
}
