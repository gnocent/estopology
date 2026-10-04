package record

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Spool stores records on disk, one file per record:
// <dir>/<cluster>/<start ms, 13 digits>-<seq>-<kind>.rec.zst (SPEC §6.1).
// Names sort in capture order. Files are written atomically.
type Spool struct {
	dir string
	mu  sync.Mutex
	seq map[string]int
}

// maxStartMs is the largest start date a file name can hold (13 digits).
const maxStartMs = 9999999999999

// OpenSpool opens (and creates) a spool directory.
func OpenSpool(dir string) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &Spool{dir: dir, seq: map[string]int{}}, nil
}

// Dir returns the spool directory.
func (s *Spool) Dir() string { return s.dir }

// Entry is one spooled record file.
type Entry struct {
	Cluster string
	Path    string
	StartMs int64
	Kind    Kind
	Size    int64
}

// Write stores r and returns its entry.
func (s *Spool) Write(r *Record) (Entry, error) {
	// Everything that makes the file name is checked: a record may come from
	// the network (HA).
	if !ValidCluster(r.Cluster) {
		return Entry{}, fmt.Errorf("spool: invalid cluster id %q", r.Cluster)
	}
	if !r.Kind.Valid() {
		return Entry{}, fmt.Errorf("spool: invalid kind %q", r.Kind)
	}
	if r.StartMs < 0 || r.StartMs > maxStartMs {
		return Entry{}, fmt.Errorf("spool: invalid start date %d", r.StartMs)
	}
	dir := filepath.Join(s.dir, r.Cluster)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Entry{}, err
	}
	s.mu.Lock()
	s.seq[r.Cluster]++
	seq := s.seq[r.Cluster] % 1000
	s.mu.Unlock()
	name := fmt.Sprintf("%013d-%03d-%s.rec.zst", r.StartMs, seq, r.Kind)
	final := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return Entry{}, err
	}
	if err := Encode(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return Entry{}, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return Entry{}, err
	}
	st, _ := tmp.Stat()
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return Entry{}, err
	}
	if err := os.Rename(tmp.Name(), final); err != nil {
		os.Remove(tmp.Name())
		return Entry{}, err
	}
	e := Entry{Cluster: r.Cluster, Path: final, StartMs: r.StartMs, Kind: r.Kind}
	if st != nil {
		e.Size = st.Size()
	}
	return e, nil
}

// List returns the records of a cluster whose start is >= sinceMs, in
// capture order.
func (s *Spool) List(cluster string, sinceMs int64) ([]Entry, error) {
	dir := filepath.Join(s.dir, cluster)
	des, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, de := range des {
		e, ok := parseName(cluster, dir, de.Name())
		if !ok || e.StartMs < sinceMs {
			continue
		}
		if info, err := de.Info(); err == nil {
			e.Size = info.Size()
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return filepath.Base(out[i].Path) < filepath.Base(out[j].Path) })
	return out, nil
}

// Clusters lists the cluster directories of the spool.
func (s *Spool) Clusters() ([]string, error) {
	des, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, de := range des {
		if de.IsDir() {
			out = append(out, de.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// Read decodes a spooled record.
func (s *Spool) Read(e Entry) (*Record, error) {
	f, err := os.Open(e.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Decode(f)
}

// Prune deletes records older than keep (SPEC: 72 h by default) and returns
// how many files were removed.
func (s *Spool) Prune(now time.Time, keep time.Duration) (int, error) {
	limit := now.Add(-keep).UnixMilli()
	clusters, err := s.Clusters()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range clusters {
		entries, err := s.List(c, 0)
		if err != nil {
			return n, err
		}
		for _, e := range entries {
			if e.StartMs < limit {
				if err := os.Remove(e.Path); err == nil {
					n++
				}
			}
		}
	}
	return n, nil
}

func parseName(cluster, dir, name string) (Entry, bool) {
	if !strings.HasSuffix(name, ".rec.zst") {
		return Entry{}, false
	}
	parts := strings.SplitN(strings.TrimSuffix(name, ".rec.zst"), "-", 3)
	if len(parts) != 3 {
		return Entry{}, false
	}
	ms, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return Entry{}, false
	}
	return Entry{Cluster: cluster, Path: filepath.Join(dir, name), StartMs: ms, Kind: Kind(parts[2])}, true
}

// Lookup returns the entry of a spooled file given its name (replication:
// the name comes from the network and is validated).
func (s *Spool) Lookup(cluster, name string) (Entry, bool) {
	if !ValidCluster(cluster) || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return Entry{}, false
	}
	dir := filepath.Join(s.dir, cluster)
	e, ok := parseName(cluster, dir, name)
	if !ok {
		return Entry{}, false
	}
	st, err := os.Stat(e.Path)
	if err != nil || !st.Mode().IsRegular() {
		return Entry{}, false
	}
	e.Size = st.Size()
	return e, true
}
