// Package record defines the capture records: the normalized, compressed
// output of every probe and full capture, and the only input of the
// ingestion (SPEC §6.7). Records are spooled on disk, replayed by tests,
// shipped to the standby (HA) and kept for debugging.
package record

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/klauspost/compress/zstd"

	"estopo/internal/esclient"
)

// FormatVersion is bumped on incompatible changes of Record.
const FormatVersion = 1

// Kind of capture.
type Kind string

const (
	Light Kind = "light" // periodic probe (SPEC §5.1)
	Full  Kind = "full"  // full capture (SPEC §5.2), includes the probe calls
)

// Status of a capture (SPEC §5.5).
type Status string

const (
	OK      Status = "ok"
	Partial Status = "partial" // structure incomplete (shards or indices missing): nothing is closed
	Failed  Status = "failed"  // nothing usable: nothing is closed, "unknown" band in the UI
)

// Triggers of a full capture (SPEC §5.3); probes use TriggerSchedule.
const (
	TriggerSchedule    = "schedule"
	TriggerFirst       = "first"
	TriggerMaxInterval = "max_interval"
	TriggerHealth      = "health_changed"
	TriggerNodes       = "nodes_changed"
	TriggerShards      = "shards_changed"
	TriggerSettings    = "cluster_settings_changed"
	TriggerManual      = "manual"
)

// IndexSettings is the whitelisted settings of one index.
type IndexSettings struct {
	Index    string            `json:"index"`
	Settings map[string]string `json:"settings"`
}

// Record is one probe or full capture. Missing sections are nil.
type Record struct {
	Format  int    `json:"format"`
	Cluster string `json:"cluster"` // configuration id of the cluster
	Kind    Kind   `json:"kind"`
	Trigger string `json:"trigger"`
	StartMs int64  `json:"start_ms"` // collector clock, UTC ms
	EndMs   int64  `json:"end_ms"`
	Status  Status `json:"status"`
	Error   string `json:"error,omitempty"`

	Calls []esclient.CallStat `json:"calls"`

	Root            *esclient.Root            `json:"root,omitempty"`
	Health          *esclient.Health          `json:"health,omitempty"`
	NodesStats      []esclient.NodeStats      `json:"nodes_stats,omitempty"`
	Allocation      []esclient.Allocation     `json:"allocation,omitempty"`
	ClusterSettings *esclient.ClusterSettings `json:"cluster_settings,omitempty"`
	Remotes         []esclient.Remote         `json:"remotes,omitempty"`
	// RemotesUnavailable: _remote/info refused (e.g. coordinating node without
	// the remote_cluster_client role); not a capture failure.
	RemotesUnavailable bool `json:"remotes_unavailable,omitempty"`

	// Full captures only.
	Nodes         []esclient.NodeInfo `json:"nodes,omitempty"`
	Indices       []esclient.CatIndex `json:"indices,omitempty"`
	IndexSettings []IndexSettings     `json:"index_settings,omitempty"`
	Shards        []esclient.CatShard `json:"shards,omitempty"`
}

// HasStructure reports whether the record describes indices and shards
// completely (a full capture whose structural calls all succeeded).
func (r *Record) HasStructure() bool {
	return r.Kind == Full && r.Status == OK && r.Indices != nil && r.Shards != nil && r.Nodes != nil
}

// Sort puts the slices in a canonical order, so that the same cluster state
// always gives the same record bytes (deterministic ingestion and tests).
func (r *Record) Sort() {
	sort.Slice(r.NodesStats, func(i, j int) bool {
		return r.NodesStats[i].Name+r.NodesStats[i].ID < r.NodesStats[j].Name+r.NodesStats[j].ID
	})
	sort.Slice(r.Allocation, func(i, j int) bool { return r.Allocation[i].Node < r.Allocation[j].Node })
	sort.Slice(r.Remotes, func(i, j int) bool { return r.Remotes[i].Alias < r.Remotes[j].Alias })
	sort.Slice(r.Nodes, func(i, j int) bool { return r.Nodes[i].Name+r.Nodes[i].ID < r.Nodes[j].Name+r.Nodes[j].ID })
	sort.Slice(r.Indices, func(i, j int) bool { return r.Indices[i].UUID < r.Indices[j].UUID })
	sort.Slice(r.IndexSettings, func(i, j int) bool { return r.IndexSettings[i].Index < r.IndexSettings[j].Index })
	sort.Slice(r.Shards, func(i, j int) bool {
		a, b := &r.Shards[i], &r.Shards[j]
		if a.Index != b.Index {
			return a.Index < b.Index
		}
		if a.Shard != b.Shard {
			return a.Shard < b.Shard
		}
		if a.Primary != b.Primary {
			return a.Primary
		}
		if a.Node != b.Node {
			return a.Node < b.Node
		}
		return a.UnassignedAtMs < b.UnassignedAtMs
	})
}

// Encode writes r as zstd-compressed JSON. The large arrays are written
// element by element, so that a 250k-shard capture is never held twice in
// memory (encoding/json would first build the whole document).
func Encode(w io.Writer, r *Record) error {
	enc, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(enc, 256<<10)
	if err := encodeStreaming(bw, r); err != nil {
		enc.Close()
		return err
	}
	if err := bw.Flush(); err != nil {
		enc.Close()
		return err
	}
	return enc.Close()
}

// ErrFormat is returned for records written by an incompatible version.
var ErrFormat = errors.New("record: unsupported format version")

// MaxDecodedBytes bounds the JSON of one record once decompressed (a full
// capture of 250k shards is about 100 MB): a record also comes from the
// network (HA), and a small compressed file can expand without limit.
const MaxDecodedBytes = 1 << 30

// ErrTooLarge is returned by Decode for a record above MaxDecodedBytes.
var ErrTooLarge = errors.New("record: larger than the decoding limit")

// Decode reads a record written by Encode.
func Decode(rd io.Reader) (*Record, error) {
	return decode(rd, MaxDecodedBytes)
}

func decode(rd io.Reader, limit int64) (*Record, error) {
	// The encoder window is 8 MiB at most; a larger one is not ours.
	dec, err := zstd.NewReader(rd, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(64<<20))
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	lr := &io.LimitedReader{R: dec, N: limit + 1}
	var r Record
	if err := json.NewDecoder(bufio.NewReaderSize(lr, 256<<10)).Decode(&r); err != nil {
		if lr.N <= 0 {
			return nil, ErrTooLarge
		}
		return nil, fmt.Errorf("record: %w", err)
	}
	if r.Format != FormatVersion {
		return nil, fmt.Errorf("%w %d", ErrFormat, r.Format)
	}
	// Cluster and kind end up in file names (spool, partitions).
	if !ValidCluster(r.Cluster) {
		return nil, fmt.Errorf("record: invalid cluster id %q", r.Cluster)
	}
	if !r.Kind.Valid() {
		return nil, fmt.Errorf("record: invalid kind %q", r.Kind)
	}
	return &r, nil
}

// Valid reports whether k is a known capture kind.
func (k Kind) Valid() bool { return k == Light || k == Full }

// ValidCluster reports whether id is a cluster configuration id: lower case
// letters, digits, '-' and '_' (not first), 63 characters at most. It names
// the spool and partition directories.
func ValidCluster(id string) bool {
	if id == "" || len(id) > 63 || id[0] == '-' || id[0] == '_' {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// encodeStreaming writes the same JSON document as json.Encoder would
// (same field order), streaming the large arrays.
func encodeStreaming(w *bufio.Writer, r *Record) error {
	small := *r
	small.Indices, small.IndexSettings, small.Shards = nil, nil, nil
	head, err := json.Marshal(&small)
	if err != nil {
		return err
	}
	w.Write(head[:len(head)-1]) // without the closing brace
	if err := writeArray(w, "indices", r.Indices); err != nil {
		return err
	}
	if err := writeArray(w, "index_settings", r.IndexSettings); err != nil {
		return err
	}
	if err := writeArray(w, "shards", r.Shards); err != nil {
		return err
	}
	_, err = w.WriteString("}\n")
	return err
}

// writeArray writes `,"name":[...]`, like the omitempty field would be
// (nothing for a nil slice, [] for an empty one is also omitted).
func writeArray[T any](w *bufio.Writer, name string, items []T) error {
	if len(items) == 0 {
		return nil
	}
	w.WriteString(`,"` + name + `":[`)
	for i := range items {
		if i > 0 {
			w.WriteByte(',')
		}
		b, err := json.Marshal(&items[i])
		if err != nil {
			return err
		}
		w.Write(b)
	}
	return w.WriteByte(']')
}
