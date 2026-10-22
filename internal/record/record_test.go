package record

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"estopo/internal/esclient"
)

func sample(start int64) *Record {
	return &Record{
		Format: FormatVersion, Cluster: "clu-a", Kind: Full, Trigger: TriggerFirst, StartMs: start, EndMs: start + 1500, Status: OK,
		Calls:         []esclient.CallStat{{Name: "cat_shards", Status: 200, DurationMs: 900, WireBytes: 1 << 20, Bytes: 1 << 20}},
		Root:          &esclient.Root{ClusterUUID: "u1", Version: "8.19.22"},
		Health:        &esclient.Health{Status: "green", Nodes: 2, UnassignedPrimary: esclient.Missing},
		Nodes:         []esclient.NodeInfo{{ID: "b", Name: "n2"}, {ID: "a", Name: "n1", Attributes: map[string]string{"dc": "dc1"}}},
		Indices:       []esclient.CatIndex{{UUID: "x", Name: "logs", Docs: esclient.Missing}},
		IndexSettings: []IndexSettings{{Index: "logs", Settings: map[string]string{"index.number_of_shards": "1"}}},
		Shards: []esclient.CatShard{
			{Index: "logs", Shard: 0, Primary: false, State: "STARTED", Node: "n2"},
			{Index: "logs", Shard: 0, Primary: true, State: "STARTED", Node: "n1"},
		},
		ClusterSettings: &esclient.ClusterSettings{Persistent: map[string]string{"a": "b"}, Transient: map[string]string{}},
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	r := sample(1_790_000_000_000)
	r.Sort()
	if r.Nodes[0].Name != "n1" || !r.Shards[0].Primary {
		t.Fatalf("sort: %+v %+v", r.Nodes, r.Shards)
	}
	var buf bytes.Buffer
	if err := Encode(&buf, r); err != nil {
		t.Fatal(err)
	}
	got, err := Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Errorf("round trip differs:\n got %+v\nwant %+v", got, r)
	}
	if !got.HasStructure() {
		t.Error("full ok record should have structure")
	}
	got.Status = Partial
	if got.HasStructure() {
		t.Error("partial record must not have structure")
	}
}

// An empty list (the last index deleted, the last CCS remote removed) is not
// a missing section: the decoded record says the same as the one ingested
// directly by the primary.
func TestEmptyListsSurviveRoundTrip(t *testing.T) {
	r := sample(1_790_000_000_000)
	r.Indices, r.IndexSettings, r.Shards, r.Remotes = []esclient.CatIndex{}, []IndexSettings{}, []esclient.CatShard{}, []esclient.Remote{}
	r.NodesStats, r.Allocation = []esclient.NodeStats{}, []esclient.Allocation{}
	var buf bytes.Buffer
	if err := Encode(&buf, r); err != nil {
		t.Fatal(err)
	}
	got, err := Decode(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, r) {
		t.Errorf("round trip differs:\n got %+v\nwant %+v", got, r)
	}
	if got.Remotes == nil || got.Indices == nil || got.Shards == nil || got.IndexSettings == nil || got.NodesStats == nil {
		t.Errorf("empty lists decoded as missing: %+v", got)
	}
	if !got.HasStructure() {
		t.Error("a full capture of a cluster without index has structure")
	}
	var streamed, std bytes.Buffer
	bw := bufio.NewWriter(&streamed)
	encodeStreaming(bw, r)
	bw.Flush()
	json.NewEncoder(&std).Encode(r)
	if streamed.String() != std.String() {
		t.Errorf("streamed:\n%s\nencoding/json:\n%s", streamed.String(), std.String())
	}
	// Missing sections stay missing.
	light := &Record{Format: FormatVersion, Cluster: "c", Kind: Light, Status: OK}
	buf.Reset()
	if err := Encode(&buf, light); err != nil {
		t.Fatal(err)
	}
	if got, err = Decode(&buf); err != nil || got.Remotes != nil || got.Indices != nil || got.Shards != nil || got.NodesStats != nil {
		t.Errorf("missing sections decoded as lists: %+v %v", got, err)
	}
}

func TestEncodingIsDeterministic(t *testing.T) {
	var a, b bytes.Buffer
	r := sample(1)
	r.Sort()
	Encode(&a, r)
	Encode(&b, r)
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("same record, different bytes")
	}
}

func TestDecodeRejectsOtherFormat(t *testing.T) {
	r := sample(1)
	r.Format = 99
	var buf bytes.Buffer
	Encode(&buf, r)
	if _, err := Decode(&buf); !errors.Is(err, ErrFormat) {
		t.Errorf("err = %v", err)
	}
}

func TestSpool(t *testing.T) {
	s, err := OpenSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	for i, kind := range []Kind{Light, Full, Light} {
		r := sample(base.Add(time.Duration(i) * time.Hour).UnixMilli())
		r.Kind = kind
		if _, err := s.Write(r); err != nil {
			t.Fatal(err)
		}
	}
	// Same millisecond: both kept, in write order.
	same := sample(base.Add(2 * time.Hour).UnixMilli())
	same.Kind = Full
	if _, err := s.Write(same); err != nil {
		t.Fatal(err)
	}
	entries, err := s.List("clu-a", 0)
	if err != nil || len(entries) != 4 {
		t.Fatalf("list: %d %v", len(entries), err)
	}
	if entries[0].Kind != Light || entries[1].Kind != Full || entries[3].Kind != Full || entries[0].Size == 0 {
		t.Errorf("order/kinds: %+v", entries)
	}
	if r, err := s.Read(entries[1]); err != nil || r.Kind != Full {
		t.Errorf("read: %v", err)
	}
	if later, _ := s.List("clu-a", base.Add(90*time.Minute).UnixMilli()); len(later) != 2 {
		t.Errorf("since filter: %d", len(later))
	}
	// Leftover temporary files are ignored.
	os.WriteFile(filepath.Join(s.Dir(), "clu-a", ".tmp-123"), []byte("x"), 0o600)
	n, err := s.Prune(base.Add(73*time.Hour), 72*time.Hour)
	if err != nil || n != 1 {
		t.Errorf("prune: %d %v", n, err)
	}
	if left, _ := s.List("clu-a", 0); len(left) != 3 {
		t.Errorf("after prune: %d", len(left))
	}
	if _, err := s.Write(&Record{Cluster: "../x"}); err == nil {
		t.Error("path traversal in cluster id accepted")
	}
}

// The streaming encoder produces exactly what encoding/json would.
func TestStreamingEncodingMatchesEncodingJSON(t *testing.T) {
	r := sample(42)
	r.Sort()
	var streamed bytes.Buffer
	bw := bufio.NewWriter(&streamed)
	if err := encodeStreaming(bw, r); err != nil {
		t.Fatal(err)
	}
	bw.Flush()
	var std bytes.Buffer
	json.NewEncoder(&std).Encode(r)
	if streamed.String() != std.String() {
		t.Errorf("streamed:\n%s\nencoding/json:\n%s", streamed.String(), std.String())
	}
	light := &Record{Format: FormatVersion, Cluster: "c", Kind: Light, Status: OK}
	streamed.Reset()
	std.Reset()
	bw.Reset(&streamed)
	encodeStreaming(bw, light)
	bw.Flush()
	json.NewEncoder(&std).Encode(light)
	if streamed.String() != std.String() {
		t.Errorf("light record differs: %s vs %s", streamed.String(), std.String())
	}
}
