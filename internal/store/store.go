package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"

	"estopo/internal/clock"
	"estopo/internal/record"
)

// Store is the data directory: meta.db plus the partitions.
type Store struct {
	dir   string
	clock clock.Clock
	Meta  *sql.DB

	mu    sync.Mutex
	parts map[string]*Partition // open partitions by path
}

// Open opens (or creates) the data directory.
func Open(ctx context.Context, dir string, clk clock.Clock) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "clusters"), 0o750); err != nil {
		return nil, err
	}
	meta, err := openDB(filepath.Join(dir, "meta.db"), true)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, meta, metaSchema); err != nil {
		meta.Close()
		return nil, fmt.Errorf("meta.db: %w", err)
	}
	return &Store{dir: dir, clock: clk, Meta: meta, parts: map[string]*Partition{}}, nil
}

// Dir returns the data directory.
func (s *Store) Dir() string { return s.dir }

// TmpDir is where partition copies in transit are written (HA): inside the
// data directory, so that they move into place with a rename.
func (s *Store) TmpDir() string { return filepath.Join(s.dir, "tmp") }

// CleanTmp removes what a previous run left in TmpDir (a crash during a
// partition copy). For the instance that serves the data directory, at
// start: a command line tool opening the store beside it must not call it.
func (s *Store) CleanTmp() error { return os.RemoveAll(s.TmpDir()) }

// Close closes every database.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, p := range s.parts {
		errs = append(errs, p.close())
	}
	s.parts = map[string]*Partition{}
	errs = append(errs, s.Meta.Close())
	return errors.Join(errs...)
}

// --- weeks ----------------------------------------------------------------------

// Week identifies an ISO week (UTC).
type Week struct {
	Key    string // 2026-W40
	FromMs int64  // Monday 00:00 UTC
	ToMs   int64  // next Monday 00:00 UTC (exclusive)
}

// WeekOf returns the ISO week containing ms.
func WeekOf(ms int64) Week {
	t := time.UnixMilli(ms).UTC()
	y, w := t.ISOWeek()
	wd := (int(t.Weekday()) + 6) % 7 // Monday = 0
	from := time.Date(t.Year(), t.Month(), t.Day()-wd, 0, 0, 0, 0, time.UTC)
	return Week{Key: fmt.Sprintf("%04d-W%02d", y, w), FromMs: from.UnixMilli(), ToMs: from.AddDate(0, 0, 7).UnixMilli()}
}

// --- partitions --------------------------------------------------------------------

// Partition is one cluster × week database.
type Partition struct {
	Cluster string
	Week    Week
	Path    string          // relative to the data directory
	db      *sql.DB         // the writing connection (ingestion)
	rdb     *sql.DB         // reading connections (queries, snapshots)
	blobs   map[string]bool // hashes known to be stored
	closed  atomic.Bool     // closed by the store (deleted or replaced)
}

// DB exposes the partition database for queries: connections of their own,
// which neither wait for the ingestion nor make it wait.
func (p *Partition) DB() *sql.DB { return p.rdb }

func (p *Partition) close() error {
	p.closed.Store(true)
	return errors.Join(p.rdb.Close(), p.db.Close())
}

// PartitionInfo is a catalogue row.
type PartitionInfo struct {
	Cluster   string
	Week      string
	FromMs    int64
	ToMs      int64
	Path      string
	SizeBytes int64
	Sealed    bool
}

func partitionRel(cluster, week string) string {
	return filepath.Join("clusters", cluster, week+".db")
}

// Partition opens the partition of cluster for the week containing ms,
// creating and registering it if needed.
func (s *Store) Partition(ctx context.Context, cluster string, ms int64) (*Partition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.openLocked(ctx, cluster, WeekOf(ms))
}

// openLocked returns the open partition of cluster and week, opening,
// creating and registering it as needed (s.mu held).
func (s *Store) openLocked(ctx context.Context, cluster string, w Week) (*Partition, error) {
	if !record.ValidCluster(cluster) { // it names a directory
		return nil, fmt.Errorf("invalid cluster id %q", cluster)
	}
	rel := partitionRel(cluster, w.Key)
	if p, ok := s.parts[rel]; ok {
		return p, nil
	}
	abs := filepath.Join(s.dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return nil, err
	}
	db, err := openDB(abs, true)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, db, partitionSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s: %w", rel, err)
	}
	for k, v := range map[string]string{"cluster": cluster, "week": w.Key,
		"from_ms": fmt.Sprint(w.FromMs), "to_ms": fmt.Sprint(w.ToMs)} {
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO meta(key, value) VALUES (?, ?)`, k, v); err != nil {
			db.Close()
			return nil, err
		}
	}
	if _, err := s.Meta.ExecContext(ctx, `INSERT OR IGNORE INTO partition(cluster_id, week, from_ms, to_ms, path, created_ms)
		VALUES (?, ?, ?, ?, ?, ?)`, cluster, w.Key, w.FromMs, w.ToMs, filepath.ToSlash(rel), s.clock.Now().UnixMilli()); err != nil {
		db.Close()
		return nil, err
	}
	rdb, err := openDB(abs, false)
	if err != nil {
		db.Close()
		return nil, err
	}
	p := &Partition{Cluster: cluster, Week: w, Path: rel, db: db, rdb: rdb, blobs: map[string]bool{}}
	s.parts[rel] = p
	return p, nil
}

// ErrNoPartition is returned by OpenExisting for a partition that is no
// longer in the catalogue (deleted since the caller listed it).
var ErrNoPartition = errors.New("partition not registered")

// OpenExisting opens a registered partition (an error if it was deleted
// meanwhile or if its file does not exist: nothing is created).
func (s *Store) OpenExisting(ctx context.Context, info PartitionInfo) (*Partition, error) {
	rel := filepath.FromSlash(info.Path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.parts[rel]; ok {
		return p, nil
	}
	// DeletePartition holds the same lock from the catalogue update to the
	// removal of the file: what is still registered here had its file.
	var n int
	if err := s.Meta.QueryRowContext(ctx, `SELECT count(*) FROM partition WHERE cluster_id = ? AND week = ?`,
		info.Cluster, info.Week).Scan(&n); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, fmt.Errorf("%s/%s: %w", info.Cluster, info.Week, ErrNoPartition)
	}
	if _, err := os.Stat(filepath.Join(s.dir, rel)); errors.Is(err, fs.ErrNotExist) {
		// Registered without a file: a deletion by an older version interrupted
		// between the two steps, or a file removed by hand. The partition is
		// deleted; a row left behind would stop every reader of the cluster.
		if _, err := s.Meta.ExecContext(ctx, `DELETE FROM partition WHERE cluster_id = ? AND week = ?`, info.Cluster, info.Week); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s/%s: file missing, removed from the catalogue: %w", info.Cluster, info.Week, ErrNoPartition)
	} else if err != nil {
		return nil, err
	}
	return s.openLocked(ctx, info.Cluster, WeekOf(info.FromMs))
}

// Partitions lists the catalogue, oldest first (optionally for one cluster).
func (s *Store) Partitions(ctx context.Context, cluster string) ([]PartitionInfo, error) {
	q := `SELECT cluster_id, week, from_ms, to_ms, path, size_bytes, sealed FROM partition`
	var args []any
	if cluster != "" {
		q += ` WHERE cluster_id = ?`
		args = append(args, cluster)
	}
	rows, err := s.Meta.QueryContext(ctx, q+` ORDER BY from_ms, cluster_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PartitionInfo
	for rows.Next() {
		var p PartitionInfo
		if err := rows.Scan(&p.Cluster, &p.Week, &p.FromMs, &p.ToMs, &p.Path, &p.SizeBytes, &p.Sealed); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LastIngested returns the date of the last record ingested for a cluster
// (0 if none), read from the last_ts marker of its latest marked partition,
// as Ingester.load picks it. Unlike Ingester.LastTs, it neither waits for an
// ingestion in progress nor loads the mirror: the marker is written in the
// transaction of each record, and read on the reading connections.
func (s *Store) LastIngested(ctx context.Context, cluster string) (int64, error) {
	parts, err := s.Partitions(ctx, cluster)
	if err != nil {
		return 0, err
	}
	for i := len(parts) - 1; i >= 0; i-- {
		p, err := s.OpenExisting(ctx, parts[i])
		if errors.Is(err, ErrNoPartition) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("open partition %s of %s: %w", parts[i].Week, cluster, err)
		}
		var v string
		err = p.rdb.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'last_ts'`).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return 0, err
		}
		return strconv.ParseInt(v, 10, 64)
	}
	return 0, nil
}

// ClockTolerance is how far the last ingested record of a cluster may be
// ahead of the clock (SPEC §6.7). Beyond it, the clock went back after
// records were ingested: the next ones are refused as older until the clock
// catches up.
const ClockTolerance = time.Hour

// aheadOf returns how far lastMs is ahead of now, 0 within ClockTolerance.
func aheadOf(lastMs int64, now time.Time) time.Duration {
	if d := time.UnixMilli(lastMs).Sub(now); d > ClockTolerance {
		return d
	}
	return 0
}

// HistoryAhead returns the date of the last record ingested for a cluster
// and how far it is ahead of the clock, both 0 within ClockTolerance.
func (s *Store) HistoryAhead(ctx context.Context, cluster string) (int64, time.Duration, error) {
	last, err := s.LastIngested(ctx, cluster)
	if err != nil {
		return 0, 0, err
	}
	d := aheadOf(last, s.clock.Now())
	if d == 0 {
		return 0, 0, nil
	}
	return last, d, nil
}

// RefreshSize records the on-disk size of a partition (database + WAL).
func (s *Store) RefreshSize(ctx context.Context, p *Partition) (int64, error) {
	abs := filepath.Join(s.dir, p.Path)
	var size int64
	for _, f := range []string{abs, abs + "-wal", abs + "-shm"} {
		if st, err := os.Stat(f); err == nil {
			size += st.Size()
		}
	}
	_, err := s.Meta.ExecContext(ctx, `UPDATE partition SET size_bytes = ? WHERE cluster_id = ? AND week = ?`, size, p.Cluster, p.Week.Key)
	return size, err
}

// Seal marks a partition as complete (a later week has started).
func (s *Store) Seal(ctx context.Context, p *Partition) error {
	if _, err := p.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return err
	}
	if _, err := s.RefreshSize(ctx, p); err != nil {
		return err
	}
	_, err := s.Meta.ExecContext(ctx, `UPDATE partition SET sealed = 1 WHERE cluster_id = ? AND week = ?`, p.Cluster, p.Week.Key)
	return err
}

// DeletePartition unregisters, closes and removes a partition file. The
// lock is held throughout: a concurrent OpenExisting must not reopen the
// partition between its close and the removal of its file. The catalogue
// goes first: a crash in between leaves an unregistered file, which nothing
// reads, rather than a row without its file.
func (s *Store) DeletePartition(ctx context.Context, info PartitionInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.Meta.ExecContext(ctx, `DELETE FROM partition WHERE cluster_id = ? AND week = ?`, info.Cluster, info.Week); err != nil {
		return err
	}
	return s.removeLocked(filepath.FromSlash(info.Path), false)
}

// removeLocked closes a partition and removes its files (s.mu held). With
// keepMain the database file stays, for a rename that replaces it: only its
// WAL goes, which must not be replayed over the new file.
func (s *Store) removeLocked(rel string, keepMain bool) error {
	if p, ok := s.parts[rel]; ok {
		p.close()
		delete(s.parts, rel)
	}
	abs := filepath.Join(s.dir, rel)
	files := []string{abs + "-wal", abs + "-shm"}
	if !keepMain {
		files = append(files, abs)
	}
	for _, f := range files {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// --- blobs ------------------------------------------------------------------------

var (
	blobEnc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	blobDec, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
)

// BlobHash returns the content hash used as blob key.
func BlobHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:16])
}

// putBlob stores data (compressed, deduplicated) and returns its hash.
func (p *Partition) putBlob(ctx context.Context, tx *sql.Tx, data []byte) (string, error) {
	h := BlobHash(data)
	if p.blobs[h] {
		return h, nil
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO blob(hash, data) VALUES (?, ?)`, h, blobEnc.EncodeAll(data, nil)); err != nil {
		return "", err
	}
	p.blobs[h] = true
	return h, nil
}

// Blob returns the content of a blob.
func (p *Partition) Blob(ctx context.Context, hash string) ([]byte, error) {
	var z []byte
	if err := p.rdb.QueryRowContext(ctx, `SELECT data FROM blob WHERE hash = ?`, hash).Scan(&z); err != nil {
		return nil, err
	}
	return blobDec.DecodeAll(z, nil)
}
