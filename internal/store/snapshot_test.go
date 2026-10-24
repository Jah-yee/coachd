package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSnapshotToDuringWrites(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var writes atomic.Int64
	var writeErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := db.SaveRaw(ctx, "test", strconv.Itoa(i), []byte(`{"n":1}`)); err != nil {
				writeErr = err
				return
			}
			writes.Add(1)
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for writes.Load() < 50 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	path := filepath.Join(t.TempDir(), "snap.db")
	err := db.SnapshotTo(ctx, path)
	before := writes.Load()
	for writes.Load() <= before && time.Now().Before(deadline) { // the writer outlives the snapshot
		time.Sleep(time.Millisecond)
	}
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if writeErr != nil {
		t.Fatalf("writer: %v", writeErr)
	}
	// The writer ran before SnapshotTo was called and after it returned; a
	// write during the VACUUM itself is likely but not checked.
	if before < 50 || writes.Load() <= before {
		t.Fatalf("writes before the snapshot returned %d, total %d: want 50+ before it and more after", before, writes.Load())
	}
	info, err := VerifySnapshot(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if info.SchemaVersion != LatestSchemaVersion() {
		t.Fatalf("schema %q, want %q", info.SchemaVersion, LatestSchemaVersion())
	}
	snap, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	var n int64
	if err := snap.QueryRow(`SELECT count(*) FROM raw_payloads`).Scan(&n); err != nil || n < 50 || n >= writes.Load() {
		t.Fatalf("snapshot rows %d err=%v, want 50..%d", n, err, writes.Load()-1)
	}
}

func TestSnapshotToRefusesExistingPath(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	path := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.SnapshotTo(ctx, path); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("err %v, want fs.ErrExist", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() != 0 {
		t.Fatalf("existing file changed: %v %v", fi, err)
	}
}

func TestVerifySnapshotRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.db")
	b := make([]byte, 8192)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range b {
		b[i] = byte(r.Uint32())
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := VerifySnapshot(context.Background(), path)
	if !errors.Is(err, ErrCorruptSnapshot) && !errors.Is(err, ErrNotCoachdDB) {
		t.Fatalf("err %v", err)
	}
}

func TestVerifySnapshotRejectsForeignSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "foreign.db")
	f, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Exec(`CREATE TABLE t (x INTEGER); INSERT INTO t VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifySnapshot(context.Background(), path); !errors.Is(err, ErrNotCoachdDB) {
		t.Fatalf("err %v, want ErrNotCoachdDB", err)
	}
}

func TestVerifySnapshotIsReadOnly(t *testing.T) {
	ctx := context.Background()
	live := filepath.Join(t.TempDir(), "coachd.db")
	db, err := Open(ctx, live)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveRaw(ctx, "test", "k", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	snap := filepath.Join(t.TempDir(), "snap.db")
	if err := db.SnapshotTo(ctx, snap); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// The VACUUM INTO copy, and a closed live database whose header says WAL.
	for _, path := range []string{snap, live} {
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifySnapshot(ctx, path); err != nil {
			t.Fatalf("%s: %v", filepath.Base(path), err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("%s: VerifySnapshot changed the file", filepath.Base(path))
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
				t.Fatalf("%s%s exists (err=%v)", filepath.Base(path), suffix, err)
			}
		}
	}
}

func TestKnownSchemaVersion(t *testing.T) {
	latest := LatestSchemaVersion()
	if latest == "" || !KnownSchemaVersion("0001_init") || !KnownSchemaVersion(latest) {
		t.Fatalf("latest %q not known, or 0001_init not known", latest)
	}
	if KnownSchemaVersion("9999_future") || KnownSchemaVersion("") {
		t.Fatal("unknown version reported as known")
	}
}
