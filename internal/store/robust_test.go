package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"estopo/internal/record"
	"estopo/internal/sim"
)

// 2026-10-05 is the Monday of W41: 4 days and 12 hours after t0.
const nextWeek = 4*24*time.Hour + 12*time.Hour + time.Minute

// The first record of a week fails after its partition was created and
// registered: the next attempt still writes the keyframe, from the state of
// the previous week.
func TestKeyframeAfterFailedWeekSwitch(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	old, err := e.st.Partition(e.ctx, "small", e.at(0))
	if err != nil {
		t.Fatal(err)
	}
	// The keyframe copies the blobs of the open intervals: without them it fails.
	if _, err := old.db.ExecContext(e.ctx, `ALTER TABLE blob RENAME TO blob_away`); err != nil {
		t.Fatal(err)
	}
	r := e.c.CaptureAt(e.at(nextWeek), "small", record.Full, record.TriggerSchedule)
	if _, err := e.in.Ingest(e.ctx, r); err == nil {
		t.Fatal("the ingestion was expected to fail")
	}
	if _, err := old.db.ExecContext(e.ctx, `ALTER TABLE blob_away RENAME TO blob`); err != nil {
		t.Fatal(err)
	}
	if parts, _ := e.st.Partitions(e.ctx, "small"); len(parts) != 2 {
		t.Fatalf("test assumption: the partition of the new week is registered, got %+v", parts)
	}

	if last, err := e.in.LastTs(e.ctx, "small"); err != nil || last != e.at(0) {
		t.Fatalf("last record after the failure: %d %v, want %d", last, err, e.at(0))
	}
	res := e.ingest(r)
	if !res.Keyframe {
		t.Fatal("no keyframe after a failed first ingestion of the week")
	}
	if n := e.count(nextWeek, `SELECT count(*) FROM capture WHERE kind = 'keyframe'`); n != 1 {
		t.Errorf("%d keyframe captures", n)
	}
	if n := e.count(nextWeek, `SELECT count(*) FROM node_presence WHERE valid_from = ?`, e.at(0)); n != len(e.c.Nodes) {
		t.Errorf("%d presences keep their original start, want %d", n, len(e.c.Nodes))
	}
	if n := e.count(nextWeek, `SELECT count(*) FROM shard_iv WHERE valid_to IS NULL`); n != e.shardCopies() {
		t.Errorf("%d open shard intervals, cluster has %d copies", n, e.shardCopies())
	}
	if parts, _ := e.st.Partitions(e.ctx, "small"); len(parts) != 2 || !parts[0].Sealed {
		t.Errorf("catalogue %+v", parts)
	}
	// After a restart the week goes on: no second keyframe.
	e.st.Close()
	e.open()
	if res := e.probe(nextWeek + 5*time.Minute); res.Keyframe {
		t.Error("second keyframe in the same week")
	}
}

// An event written by Note at the very start of a week, then a restart: the
// mirror is still the one of the previous week (the spool is not replayed
// from the beginning) and the first record of the week is a keyframe.
func TestKeyframeAfterNoteAtWeekStart(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	if err := e.in.Note(e.ctx, "small", e.at(nextWeek), EventClusterUUIDChanged, 1, map[string]string{"bound": "a", "seen": "b"}); err != nil {
		t.Fatal(err)
	}
	e.st.Close()
	e.open()
	if last, err := e.in.LastTs(e.ctx, "small"); err != nil || last != e.at(0) {
		t.Fatalf("last record: %d %v, want %d", last, err, e.at(0))
	}
	if res := e.full(nextWeek + time.Minute); !res.Keyframe {
		t.Fatal("no keyframe in a week started by Note")
	}
	if n := e.count(nextWeek, `SELECT count(*) FROM shard_iv WHERE valid_from = ?`, e.at(0)); n != e.shardCopies() {
		t.Errorf("%d shard intervals carried over, want %d", n, e.shardCopies())
	}
	if got := e.events(nextWeek, EventClusterUUIDChanged); len(got) != 1 {
		t.Errorf("event written by Note: %v", got)
	}
}

// Reads have their own connections: a read left open does not hold the
// ingestion back, and a snapshot does not wait for a write in progress.
func TestReadsAndWritesDoNotWaitForEachOther(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	p, err := e.st.Partition(e.ctx, "small", e.at(0))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := p.DB().QueryContext(e.ctx, `SELECT id FROM shard_iv`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("no shard")
	}
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	if _, err := e.in.Ingest(ctx, e.c.CaptureAt(e.at(5*time.Minute), "small", record.Light, record.TriggerSchedule)); err != nil {
		t.Fatalf("ingestion while a read is open: %v", err)
	}
	// The read started before the record: it does not see it; a new one does.
	if n := e.count(0, `SELECT count(*) FROM capture`); n != 2 {
		t.Errorf("%d captures read after the ingestion", n)
	}

	tx, err := p.db.BeginTx(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(e.ctx, `INSERT INTO meta(key, value) VALUES ('in_progress', '1')`); err != nil {
		t.Fatal(err)
	}
	parts, _ := e.st.Partitions(e.ctx, "small")
	dst := filepath.Join(t.TempDir(), "copy.db")
	if err := e.st.SnapshotPartition(ctx, parts[0], dst); err != nil {
		t.Fatalf("snapshot while a write is in progress: %v", err)
	}
	if err := CheckPartitionFile(e.ctx, dst, "small", parts[0].Week); err != nil {
		t.Errorf("snapshot: %v", err)
	}
}

// Queries, snapshots and the ingestion of one partition at the same time
// (meant for the race detector).
func TestConcurrentReadsSnapshotsAndIngestion(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	parts, _ := e.st.Partitions(e.ctx, "small")
	stop := make(chan struct{})
	errc := make(chan error, 16)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			dst := filepath.Join(t.TempDir(), "copy.db")
			for {
				select {
				case <-stop:
					return
				default:
				}
				p, err := e.st.OpenExisting(e.ctx, parts[0])
				if err == nil && i == 0 {
					err = e.st.SnapshotPartition(e.ctx, parts[0], dst)
				} else if err == nil {
					var n int
					err = p.DB().QueryRowContext(e.ctx, `SELECT count(*) FROM shard_iv WHERE valid_to IS NULL`).Scan(&n)
					if err == nil && n != e.shardCopies() {
						err = errors.New("open shard intervals do not match the cluster")
					}
				}
				if err != nil {
					errc <- err
					return
				}
			}
		}(i)
	}
	for i := 1; i <= 20; i++ {
		e.probe(time.Duration(i) * time.Minute)
	}
	close(stop)
	wg.Wait()
	select {
	case err := <-errc:
		t.Fatal(err)
	default:
	}
}

// A partition being deleted is not reopened by a concurrent reader: after
// the deletion nothing is left open, and the partition cannot be opened.
func TestDeletedPartitionIsNotReopened(t *testing.T) {
	e := newTEnv(t)
	for i := 0; i < 100; i++ {
		at := e.at(time.Duration(i) * 7 * 24 * time.Hour)
		if _, err := e.st.Partition(e.ctx, "small", at); err != nil {
			t.Fatal(err)
		}
		parts, _ := e.st.Partitions(e.ctx, "small")
		if len(parts) != 1 {
			t.Fatalf("partitions %+v", parts)
		}
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for r := 0; r < 4; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						e.st.OpenExisting(e.ctx, parts[0])
					}
				}
			}()
		}
		if err := e.st.DeletePartition(e.ctx, parts[0]); err != nil {
			t.Fatal(err)
		}
		close(stop)
		wg.Wait()
		e.st.mu.Lock()
		open := len(e.st.parts)
		e.st.mu.Unlock()
		if open != 0 {
			t.Fatalf("round %d: a deleted partition is open", i)
		}
		if _, err := os.Stat(filepath.Join(e.dir, filepath.FromSlash(parts[0].Path))); !os.IsNotExist(err) {
			t.Fatalf("round %d: file of the deleted partition: %v", i, err)
		}
		if _, err := e.st.OpenExisting(e.ctx, parts[0]); !errors.Is(err, ErrNoPartition) {
			t.Fatalf("round %d: deleted partition opened: %v", i, err)
		}
	}
}

// A file that is not the expected partition is refused before anything is
// touched: the local partition stays as it was.
func TestInstallPartitionRefusesWrongFile(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	e.ingest(e.c.CaptureAt(e.at(time.Minute), "other", record.Full, record.TriggerSchedule))
	captures := e.count(0, `SELECT count(*) FROM capture`)
	others, _ := e.st.Partitions(e.ctx, "other")
	week, from := others[0].Week, others[0].FromMs
	dir := t.TempDir()

	foreign := filepath.Join(dir, "foreign.db")
	if err := e.st.SnapshotPartition(e.ctx, others[0], foreign); err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(dir, "garbage.db")
	os.WriteFile(garbage, []byte("not a database"), 0o600)
	newer := filepath.Join(dir, "newer.db")
	parts, _ := e.st.Partitions(e.ctx, "small")
	if err := e.st.SnapshotPartition(e.ctx, parts[0], newer); err != nil {
		t.Fatal(err)
	}
	db, err := openDB(newer, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	for name, src := range map[string]string{"another cluster": foreign, "not a database": garbage, "newer schema": newer} {
		if err := e.st.InstallPartition(e.ctx, "small", week, from, src, false); err == nil {
			t.Errorf("%s: installed", name)
		}
		if _, err := os.Stat(src); err != nil {
			t.Errorf("%s: the refused file was moved: %v", name, err)
		}
		if n := e.count(0, `SELECT count(*) FROM capture`); n != captures {
			t.Errorf("%s: %d captures left in the local partition, want %d", name, n, captures)
		}
		if parts, _ := e.st.Partitions(e.ctx, "small"); len(parts) != 1 {
			t.Errorf("%s: catalogue %+v", name, parts)
		}
	}
	if last, err := e.in.LastTs(e.ctx, "small"); err != nil || last != e.at(0) {
		t.Errorf("last record after the refusals: %d %v", last, err)
	}
}

// A purge (or anything that deletes the partition the mirror was loaded
// from) leaves the ingester consistent: the next record starts a new
// history instead of continuing the deleted one.
func TestIngestAfterPurge(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	e.probe(5 * time.Minute)
	if n, err := e.st.Purge(e.ctx, "small"); err != nil || n != 1 {
		t.Fatalf("purge: %d %v", n, err)
	}
	if last, err := e.in.LastTs(e.ctx, "small"); err != nil || last != 0 {
		t.Errorf("last record after the purge: %d %v", last, err)
	}
	res, err := e.in.Ingest(e.ctx, e.c.CaptureAt(e.at(10*time.Minute), "small", record.Full, record.TriggerSchedule))
	if err != nil {
		t.Fatalf("first record after the purge: %v", err)
	}
	if res.Keyframe || res.ShardsOpened != e.shardCopies() || res.Events != 0 {
		t.Errorf("first record after the purge: %+v", res)
	}
	if n := e.count(10*time.Minute, `SELECT count(*) FROM capture`); n != 1 {
		t.Errorf("%d captures after the purge", n)
	}
}

// A node gone for good is copied by the keyframes only as long as a
// partition where it was present is kept.
func TestKeyframeDropsNodesOutOfRetention(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	victim := "small-hot-dc1-m01-2"
	e.c.Do(func(c *sim.Cluster) { c.NodeLeave(victim) })
	e.probe(time.Minute)
	e.full(time.Hour)
	const named = `SELECT count(*) FROM node WHERE name = ?`

	w41 := nextWeek
	e.full(w41)
	if n := e.count(w41, named, victim); n != 1 {
		t.Fatalf("W41: %d rows for the node that left in W40", n)
	}
	// The retention deletes W40, the only partition where the node was present.
	parts, _ := e.st.Partitions(e.ctx, "small")
	if err := e.st.DeletePartition(e.ctx, parts[0]); err != nil {
		t.Fatal(err)
	}
	w42 := w41 + 7*24*time.Hour
	if res := e.full(w42); !res.Keyframe {
		t.Fatal("no keyframe")
	}
	if n := e.count(w42, named, victim); n != 0 {
		t.Errorf("W42: the node out of the history is still copied (%d rows)", n)
	}
	if n := e.count(w42, `SELECT count(*) FROM node`); n != len(e.c.Nodes)-1 {
		t.Errorf("W42: %d nodes, want %d", n, len(e.c.Nodes)-1)
	}
	// The mirror stays consistent with the partition: same result after a restart.
	before := e.intervalCounts(w42)
	e.st.Close()
	e.open()
	e.c.Do(func(c *sim.Cluster) { c.NodeJoin(victim) })
	e.full(w42 + time.Hour)
	if n := e.count(w42, named, victim); n != 1 {
		t.Errorf("the node back after the pruning: %d rows", n)
	}
	if after := e.intervalCounts(w42); after["node"] != before["node"]+1 {
		t.Errorf("nodes before %d, after %d", before["node"], after["node"])
	}
	if got := e.events(w42, evNodeJoined); len(got) != 1 {
		t.Errorf("node_joined events: %v", got)
	}
}

// Partition copies in transit count in the size of the data directory and
// are removed by CleanTmp.
func TestTmpDirCountedAndCleaned(t *testing.T) {
	e := newTEnv(t)
	e.full(0)
	base, err := e.st.ApplyRetention(e.ctx, Retention{MaxAgeDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(e.st.TmpDir(), 0o750)
	left := filepath.Join(e.st.TmpDir(), "partition-123.db")
	if err := os.WriteFile(left, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := e.st.ApplyRetention(e.ctx, Retention{MaxAgeDays: 30})
	if err != nil || rep.OtherBytes-base.OtherBytes != 1<<20 {
		t.Errorf("tmp not counted: %d then %d, %v", base.OtherBytes, rep.OtherBytes, err)
	}
	if err := e.st.CleanTmp(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Errorf("file left in tmp: %v", err)
	}
}
