package ha

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"estopo/internal/buildinfo"
	"estopo/internal/store"
)

// Replication API, served to the standby (GET only, bearer token):
//
//	/api/v1/replication/status                      role, version, promotion date
//	/api/v1/replication/meta                        replicated meta.db tables (ETag)
//	/api/v1/replication/clusters                    partitions and spool range per cluster
//	/api/v1/replication/partitions/{cluster}/{week} consistent copy of a partition
//	/api/v1/replication/records/{cluster}?after=ms  spooled records after a date
//	/api/v1/replication/records/{cluster}/{name}    one record file

type remoteStatus struct {
	Role       string `json:"role"`
	Version    string `json:"version"`
	PromotedMs int64  `json:"promoted_ms,omitempty"`
	InstanceID string `json:"instance_id"` // changes when the data directory is lost and created again
}

type remotePartition struct {
	Week   string `json:"week"`
	FromMs int64  `json:"from_ms"`
	Sealed bool   `json:"sealed"`
	Size   int64  `json:"size"`
}

type remoteCluster struct {
	Cluster    string            `json:"cluster"`
	Partitions []remotePartition `json:"partitions"`
	OldestMs   int64             `json:"oldest_spooled_ms,omitempty"`
	NewestMs   int64             `json:"newest_spooled_ms,omitempty"`
}

type remoteRecord struct {
	Name    string `json:"name"`
	StartMs int64  `json:"start_ms"`
	Size    int64  `json:"size"`
}

// Handler serves the replication API. Without a configured token it
// answers 404: replication is off.
func (m *Manager) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/replication/status", m.handleStatus)
	mux.HandleFunc("GET /api/v1/replication/meta", m.handleMeta)
	mux.HandleFunc("GET /api/v1/replication/clusters", m.handleClusters)
	mux.HandleFunc("GET /api/v1/replication/partitions/{cluster}/{week}", m.handlePartition)
	mux.HandleFunc("GET /api/v1/replication/records/{cluster}", m.handleRecords)
	mux.HandleFunc("GET /api/v1/replication/records/{cluster}/{name}", m.handleRecord)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.token == "" {
			http.NotFound(w, r)
			return
		}
		if !m.CheckToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")) {
			m.o.Log.Warn("replication request with a wrong token", "remote", r.RemoteAddr, "path", r.URL.Path)
			http.Error(w, "invalid replication token", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (m *Manager) fail(w http.ResponseWriter, err error) {
	m.o.Log.Error("replication API", "err", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (m *Manager) handleStatus(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	st := remoteStatus{Role: m.st.Role, Version: buildinfo.Version, PromotedMs: m.st.PromotedMs, InstanceID: m.instanceID}
	m.mu.Unlock()
	writeJSON(w, st)
}

func (m *Manager) handleMeta(w http.ResponseWriter, r *http.Request) {
	snap, err := m.o.Store.ExportMeta(r.Context())
	if err != nil {
		m.fail(w, err)
		return
	}
	b, err := json.Marshal(snap)
	if err != nil {
		m.fail(w, err)
		return
	}
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}

func (m *Manager) handleClusters(w http.ResponseWriter, r *http.Request) {
	parts, err := m.o.Store.Partitions(r.Context(), "")
	if err != nil {
		m.fail(w, err)
		return
	}
	byID := map[string]*remoteCluster{}
	get := func(id string) *remoteCluster {
		c := byID[id]
		if c == nil {
			c = &remoteCluster{Cluster: id, Partitions: []remotePartition{}}
			byID[id] = c
		}
		return c
	}
	for _, p := range parts {
		c := get(p.Cluster)
		c.Partitions = append(c.Partitions, remotePartition{Week: p.Week, FromMs: p.FromMs, Sealed: p.Sealed, Size: p.SizeBytes})
	}
	spooled, err := m.o.Spool.Clusters()
	if err != nil && !os.IsNotExist(err) {
		m.fail(w, err)
		return
	}
	for _, id := range spooled {
		es, err := m.o.Spool.List(id, 0)
		if err != nil {
			m.fail(w, err)
			return
		}
		if len(es) == 0 {
			continue
		}
		c := get(id)
		c.OldestMs, c.NewestMs = es[0].StartMs, es[len(es)-1].StartMs
	}
	out := make([]remoteCluster, 0, len(byID))
	for _, c := range byID {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cluster < out[j].Cluster })
	writeJSON(w, out)
}

func (m *Manager) handlePartition(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cluster, week := r.PathValue("cluster"), r.PathValue("week")
	parts, err := m.o.Store.Partitions(ctx, cluster)
	if err != nil {
		m.fail(w, err)
		return
	}
	var info *store.PartitionInfo
	for i := range parts {
		if parts[i].Week == week {
			info = &parts[i]
		}
	}
	if info == nil {
		http.NotFound(w, r)
		return
	}
	if err := os.MkdirAll(m.o.TmpDir, 0o750); err != nil {
		m.fail(w, err)
		return
	}
	f, err := os.CreateTemp(m.o.TmpDir, "snapshot-*.db")
	if err != nil {
		m.fail(w, err)
		return
	}
	tmp := f.Name()
	f.Close()
	defer os.Remove(tmp)
	// A copy lasts as long as the partition is large: the write deadline of
	// the server is pushed back as long as the standby keeps reading (not
	// every ResponseWriter can: then the server's own timeout applies).
	rc := http.NewResponseController(w)
	rc.SetWriteDeadline(time.Time{})
	if err := m.o.Store.SnapshotPartition(ctx, *info, tmp); err != nil {
		m.fail(w, fmt.Errorf("snapshot %s/%s: %w", cluster, week, err))
		return
	}
	src, err := os.Open(tmp)
	if err != nil {
		m.fail(w, err)
		return
	}
	defer src.Close()
	if st, err := src.Stat(); err == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	}
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	buf := make([]byte, 1<<20)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			rc.SetWriteDeadline(time.Now().Add(writeStall))
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// writeStall is how long a standby may take to read one chunk of a
// partition before the copy is abandoned.
const writeStall = 2 * time.Minute

func (m *Manager) handleRecords(w http.ResponseWriter, r *http.Request) {
	cluster := r.PathValue("cluster")
	if strings.ContainsAny(cluster, `/\.`) {
		http.NotFound(w, r)
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	es, err := m.o.Spool.List(cluster, after+1)
	if err != nil {
		m.fail(w, err)
		return
	}
	out := []remoteRecord{}
	for _, e := range es {
		if len(out) == limit {
			break
		}
		out = append(out, remoteRecord{Name: filepath.Base(e.Path), StartMs: e.StartMs, Size: e.Size})
	}
	writeJSON(w, out)
}

func (m *Manager) handleRecord(w http.ResponseWriter, r *http.Request) {
	e, ok := m.o.Spool.Lookup(r.PathValue("cluster"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/zstd")
	http.ServeFile(w, r, e.Path)
}
