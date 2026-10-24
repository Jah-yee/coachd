// Package store persists coachd state in SQLite (pure Go, cgo-free).
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite" // driver "sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// DB wraps the SQLite handle.
type DB struct {
	sql  *sql.DB
	path string
}

// Open opens (creating if needed) the database at path and applies migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	q := url.Values{}
	// busy_timeout first so concurrent first-opens wait instead of failing on the WAL switch.
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	// Safe with WAL: power loss may drop the last commit but cannot corrupt the DB.
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	dsn := "file:" + path + "?" + q.Encode()
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db := &DB{sql: sdb, path: path}
	if err := db.pingRetryBusy(ctx); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	if err := db.migrate(ctx); err != nil {
		sdb.Close()
		return nil, err
	}
	return db, nil
}

// pingRetryBusy retries while another process holds the lock during the first
// WAL switch (SQLite does not honour busy_timeout for that transition).
func (db *DB) pingRetryBusy(ctx context.Context) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := db.Ping(ctx)
		if err == nil || !strings.Contains(err.Error(), "SQLITE_BUSY") || time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (db *DB) Close() error { return db.sql.Close() }

func (db *DB) Ping(ctx context.Context) error { return db.sql.PingContext(ctx) }

// SizeBytes returns the size of the database file plus its -wal file.
func (db *DB) SizeBytes() (int64, error) {
	var total int64
	for _, p := range []string{db.path, db.path + "-wal"} {
		fi, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return 0, err
		}
		total += fi.Size()
	}
	return total, nil
}

// Checkpoint copies every WAL frame into the database file and syncs it
// (PRAGMA wal_checkpoint(FULL)). With synchronous=NORMAL a power cut can
// roll back the last commits; after Checkpoint they are durable. It returns
// an error if another connection kept the checkpoint from completing.
func (db *DB) Checkpoint(ctx context.Context) error {
	var busy, frames, done int
	if err := db.sql.QueryRowContext(ctx, `PRAGMA wal_checkpoint(FULL)`).Scan(&busy, &frames, &done); err != nil {
		return fmt.Errorf("store: checkpoint: %w", err)
	}
	if busy != 0 {
		return errors.New("store: checkpoint: blocked by another connection")
	}
	return nil
}

func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY)`); err != nil {
		return fmt.Errorf("store: migrations table: %w", err)
	}
	for _, version := range migrationVersions() {
		body, err := migrationFS.ReadFile("migrations/" + version + ".sql")
		if err != nil {
			return err
		}
		if err := db.applyMigration(ctx, version, string(body)); err != nil {
			return fmt.Errorf("store: migration %s: %w", version, err)
		}
	}
	return nil
}

// migrationVersions lists the embedded migrations (file names without .sql) in order.
var migrationVersions = sync.OnceValue(func() []string {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		panic(err) // unreachable: go:embed guarantees the directory exists
	}
	var versions []string
	for _, e := range entries {
		if v, ok := strings.CutSuffix(e.Name(), ".sql"); ok {
			versions = append(versions, v)
		}
	}
	sort.Strings(versions)
	return versions
})

func (db *DB) applyMigration(ctx context.Context, version, body string) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Re-check under the write lock: another process may have applied it.
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version=?`, version).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, version); err != nil {
		return err
	}
	return tx.Commit()
}

// Time and date encoding helpers: fixed-width UTC RFC 3339 so text order is time order.
const timeLayout = "2006-01-02T15:04:05.000000000Z"

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

func parseTime(s string) (time.Time, error) { return time.Parse(timeLayout, s) }

func parseTimePtr(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := parseTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
