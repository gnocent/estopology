// Package store persists the topology history (SPEC §6): meta.db for the
// configuration and the partition catalogue, and one SQLite partition per
// cluster and ISO week holding validity intervals, metrics and events.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"time"

	_ "modernc.org/sqlite" // pure Go SQLite driver (CGO_ENABLED=0)
)

// Connections of one database. SQLite has a single writer: the writing pool
// holds one connection (the ingestion serializes). In WAL mode readers do
// not wait for the writer nor the writer for them, so reads get their own
// pool. Each connection has its own page cache, allocated as pages are read
// and freed when the connection closes: idle connections are closed, so that
// a partition nobody reads or writes any more (a past week) keeps no memory.
const (
	// 32 MiB: on the reference cluster (250k shards) the first full capture
	// and the weekly keyframe take within 10 % of what they take with 64 MiB.
	writerCacheKiB = 32 << 10
	readerCacheKiB = 8 << 10 // 8 MiB per read connection
	readConns      = 4       // simultaneous reads of one database
	connIdle       = 10 * time.Minute
)

// openDB opens a SQLite database in WAL mode: the writing pool (one
// connection) when writer is set, else the reading pool of the same file.
func openDB(path string, writer bool) (*sql.DB, error) {
	cache := readerCacheKiB
	if writer {
		cache = writerCacheKiB
	}
	q := url.Values{}
	for _, p := range []string{
		"journal_mode(WAL)",
		"synchronous(NORMAL)", // durable at checkpoints; a crash loses at most the last records, replayable from the spool
		"busy_timeout(10000)",
		"foreign_keys(OFF)",
		"temp_store(MEMORY)",
		fmt.Sprintf("cache_size(-%d)", cache), // page cache of each connection
	} {
		q.Add("_pragma", p)
	}
	dsn := "file:" + path + "?" + q.Encode()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if writer {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(readConns)
	}
	db.SetConnMaxIdleTime(connIdle)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return db, nil
}

// migrate applies the numbered schema steps not yet applied (PRAGMA
// user_version holds the number of applied steps).
func migrate(ctx context.Context, db *sql.DB, steps []string) error {
	var v int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(steps); i++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, steps[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
