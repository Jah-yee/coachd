package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "coachd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenCreatesSchema(t *testing.T) {
	db := openTest(t)
	for _, tbl := range []string{"activities", "daily_health", "day_metrics", "raw_payloads", "sync_state", "api_tokens", "audit_log", "schema_migrations"} {
		var n int
		if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (n=%d err=%v)", tbl, n, err)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coachd.db")
	ctx := context.Background()
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version<='0004_context_proposals'`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("schema_migrations rows up to 0004 = %d err=%v, want 4", n, err)
	}
	if err := db.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if sz, err := db.SizeBytes(); err != nil || sz <= 0 {
		t.Fatalf("SizeBytes = %d err=%v", sz, err)
	}
}

func TestOpenSetsWAL(t *testing.T) {
	db := openTest(t)
	var mode string
	if err := db.sql.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q err=%v", mode, err)
	}
	var fk int
	if err := db.sql.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d err=%v", fk, err)
	}
}

func TestOpenConcurrentFreshDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coachd.db")
	ctx := context.Background()
	var wg sync.WaitGroup
	dbs := make([]*DB, 4)
	errs := make([]error, 4)
	for i := range dbs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dbs[i], errs[i] = Open(ctx, path)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		defer dbs[i].Close()
	}
	files, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := dbs[0].sql.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != len(files) {
		t.Fatalf("schema_migrations rows = %d err=%v, want %d", n, err, len(files))
	}
}

// TestOpenUpgradesFrom0001 simulates a database that only has migration 0001.
func TestOpenUpgradesFrom0001(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coachd.db")
	ctx := context.Background()
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DROP TABLE activity_detail`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`DELETE FROM schema_migrations WHERE version='0002_activity_detail'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='activity_detail'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("activity_detail after upgrade: n=%d err=%v", n, err)
	}
}

// TestOpenUpgradesFrom0002 builds a database at migrations 0001+0002 holding
// an activity with a detail row, then opens it: 0003 applies and the activity
// stays intact and live (deleted_at NULL).
func TestOpenUpgradesFrom0002(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coachd.db")
	ctx := context.Background()
	old, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := old.Exec(q, args...); err != nil {
			t.Fatalf("%.40s: %v", q, err)
		}
	}
	exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY)`)
	for _, v := range []string{"0001_init", "0002_activity_detail"} {
		body, err := migrationFS.ReadFile("migrations/" + v + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		exec(string(body))
		exec(`INSERT INTO schema_migrations(version) VALUES (?)`, v)
	}
	exec(`INSERT INTO activities (id, start_local, start_utc, date, week_start, sport, distance_m, raw_json, fit_path, fit_status)
		VALUES (5, '2026-10-20T07:30:00+02:00', '2026-10-20T05:30:00.000000000Z', '2026-10-20', '2026-10-19', 'running', 5000,
		'{"activityId":5}', 'fit/2026/5.fit', 'stored')`)
	exec(`INSERT INTO activity_detail (activity_id, laps_json, series_json, computed_at) VALUES (5, '[]', '[]', '2026-10-20T06:00:00.000000000Z')`)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version='0003_activity_deleted_at'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("0003 applied: n=%d err=%v", n, err)
	}
	a, err := db.GetActivity(ctx, 5)
	if err != nil || a.DistanceM != 5000 || a.FITStatus != FITStored || a.FITPath != "fit/2026/5.fit" {
		t.Fatalf("activity after upgrade = %+v, %v", a, err)
	}
	// 0011 resets the detail cache; it rebuilds from the stored FIT file.
	del, raw, _, details := rawActivity(t, db, 5)
	if del.Valid || string(raw) != `{"activityId":5}` || details != 0 {
		t.Fatalf("after upgrade: deleted_at %v raw %q details %d, want NULL, kept, 0", del, raw, details)
	}
}

// copyMainFile copies only the database file, as if the -wal file were lost
// (e.g. its last commits never fsynced before a power cut), and opens the copy.
func copyMainFile(t *testing.T, path string) *sql.DB {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(cp, b, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := sql.Open("sqlite", "file:"+cp)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestCheckpointWritesWALToMainFile(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 23, 6, 0, 0, 0, time.UTC)
	if err := db.CreateToken(ctx, TokenRecord{ID: "t1", Name: "n", Hash: []byte("h"), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	inMain := func() bool {
		var n int
		err := copyMainFile(t, db.path).QueryRow(`SELECT count(*) FROM api_tokens WHERE id='t1'`).Scan(&n)
		return err == nil && n == 1
	}
	if inMain() {
		t.Fatal("precondition: the commit is already in the main file (auto-checkpoint?)")
	}
	if err := db.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if !inMain() {
		t.Fatal("after Checkpoint the token is not in the main database file")
	}
	if err := db.Checkpoint(ctx); err != nil { // nothing left: still fine
		t.Fatal(err)
	}
}

// TestOpenUpgradesTo0004 builds a database at migrations 0001-0003 with an
// activity, then opens it: 0004 adds goals, notes and proposals and the
// existing data stays intact.
func TestOpenUpgradesTo0004(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coachd.db")
	ctx := context.Background()
	old, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := old.Exec(q, args...); err != nil {
			t.Fatalf("%.40s: %v", q, err)
		}
	}
	exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY)`)
	for _, v := range []string{"0001_init", "0002_activity_detail", "0003_activity_deleted_at"} {
		body, err := migrationFS.ReadFile("migrations/" + v + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		exec(string(body))
		exec(`INSERT INTO schema_migrations(version) VALUES (?)`, v)
	}
	exec(`INSERT INTO activities (id, start_local, start_utc, date, week_start, sport, distance_m, raw_json, fit_path, fit_status)
		VALUES (5, '2026-10-20T07:30:00+02:00', '2026-10-20T05:30:00.000000000Z', '2026-10-20', '2026-10-19', 'running', 5000,
		'{"activityId":5}', 'fit/2026/5.fit', 'stored')`)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tbl := range []string{"goals", "notes", "proposals"} {
		var n int
		if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing after upgrade (n=%d err=%v)", tbl, n, err)
		}
	}
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version<='0004_context_proposals'`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("schema_migrations rows up to 0004 = %d err=%v, want 4", n, err)
	}
	if a, err := db.GetActivity(ctx, 5); err != nil || a.DistanceM != 5000 {
		t.Fatalf("activity after upgrade = %+v, %v", a, err)
	}
}
