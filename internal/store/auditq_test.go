package store

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"
)

func auditCount(t *testing.T, db *DB) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func qLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func ev(i int) AuditEvent {
	return AuditEvent{At: time.Date(2026, 10, 1, 0, 0, i, 0, time.UTC), Lane: "private", Actor: "t1", Event: "tool_call", Target: "x", Outcome: "ok"}
}

// lockWriter holds a write lock on the same file via a second handle.
func lockWriter(t *testing.T, db *DB) (release func()) {
	t.Helper()
	other, err := sql.Open("sqlite", "file:"+db.path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	var once bool
	return func() {
		if once {
			return
		}
		once = true
		_, _ = conn.ExecContext(context.Background(), "COMMIT")
		conn.Close()
		other.Close()
	}
}

func TestAuditQueueWritesAllEvents(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	q := NewAuditQueue(db, qLog(), 256)
	for i := 0; i < 500; i++ {
		if err := q.AppendAudit(ctx, ev(i%60)); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if n := auditCount(t, db); n != 500 {
		t.Fatalf("rows %d, want 500", n)
	}
}

func TestAuditQueueDoesNotBlockCaller(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	q := NewAuditQueue(db, qLog(), 16)
	release := lockWriter(t, db)
	defer release()
	t0 := time.Now()
	if err := q.AppendAudit(ctx, ev(0)); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 50*time.Millisecond {
		t.Fatalf("AppendAudit took %v, want < 50ms", d)
	}
	release()
	if err := q.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if n := auditCount(t, db); n != 1 {
		t.Fatalf("rows %d, want 1", n)
	}
}

func TestAuditQueueFullFallsBackToSync(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	q := NewAuditQueue(db, qLog(), 1)
	release := lockWriter(t, db)
	defer release()
	go func() { time.Sleep(200 * time.Millisecond); release() }()
	for i := 0; i < 6; i++ {
		if err := q.AppendAudit(ctx, ev(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if n := auditCount(t, db); n != 6 {
		t.Fatalf("rows %d, want 6", n)
	}
}

func TestAuditQueueAfterCloseWritesSync(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	q := NewAuditQueue(db, qLog(), 4)
	if err := q.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.AppendAudit(ctx, ev(0)); err != nil {
		t.Fatal(err)
	}
	if n := auditCount(t, db); n != 1 {
		t.Fatalf("rows %d, want 1", n)
	}
	if err := q.Close(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
}
