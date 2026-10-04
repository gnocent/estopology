package record

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// What makes a spool file name is validated: a record received from a
// primary must not be written outside the spool directory.
func TestSpoolRejectsForgedNames(t *testing.T) {
	root := t.TempDir()
	sp, err := OpenSpool(filepath.Join(root, "data", "spool"))
	if err != nil {
		t.Fatal(err)
	}
	for name, forge := range map[string]func(*Record){
		"kind":          func(r *Record) { r.Kind = "x/../../../../escaped" },
		"unknown kind":  func(r *Record) { r.Kind = "keyframe" },
		"cluster":       func(r *Record) { r.Cluster = "../escaped" },
		"cluster dot":   func(r *Record) { r.Cluster = "a.b" },
		"cluster case":  func(r *Record) { r.Cluster = "Clu" },
		"empty cluster": func(r *Record) { r.Cluster = "" },
		"start":         func(r *Record) { r.StartMs = -1 },
		"late start":    func(r *Record) { r.StartMs = maxStartMs + 1 },
	} {
		r := sample(1_790_000_000_000)
		forge(r)
		if e, err := sp.Write(r); err == nil {
			t.Errorf("%s: written to %s", name, e.Path)
		}
		// A forged record is refused when decoded too (start dates excepted:
		// only the spool names files after them).
		var buf bytes.Buffer
		if err := Encode(&buf, r); err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(&buf); err == nil && r.StartMs == 1_790_000_000_000 {
			t.Errorf("%s: decoded", name)
		}
	}
	var files []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if len(files) != 0 {
		t.Errorf("files written by refused records: %v", files)
	}
	if _, err := sp.Write(sample(1_790_000_000_000)); err != nil {
		t.Errorf("valid record: %v", err)
	}
}

// A record that expands beyond the limit is refused with an explicit error.
func TestDecodeLimit(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, sample(1_790_000_000_000)); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	if _, err := decode(bytes.NewReader(data), 1<<20); err != nil {
		t.Fatalf("under the limit: %v", err)
	}
	if _, err := decode(bytes.NewReader(data), 100); !errors.Is(err, ErrTooLarge) {
		t.Errorf("over the limit: %v", err)
	}
}
