package store

import (
	"context"
	"testing"
	"time"
)

func TestPurgeAudit(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		if err := db.AppendAudit(ctx, AuditEvent{At: base.Add(time.Duration(i) * 24 * time.Hour), Lane: "private", Actor: "t1", Event: "tool_call", Target: "get_activity", Outcome: "ok"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := db.PurgeAudit(ctx, base.Add(2*24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("purged %d %v", n, err)
	}
	var left int
	db.sql.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&left)
	if left != 2 {
		t.Fatalf("left %d", left)
	}
}
