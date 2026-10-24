package store

import (
	"context"
	"testing"
	"time"
)

func TestSyncStateRoundTrip(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	z, err := db.GetSyncState(ctx, "sleep")
	if err != nil || z.LastAttemptAt != nil || z.DataType != "" && z.DataType != "sleep" {
		t.Fatalf("absent: %+v %v", z, err)
	}
	now := time.Date(2026, 10, 23, 6, 0, 0, 123, time.UTC)
	s := SyncState{DataType: "sleep", LastAttemptAt: &now, LastSuccessAt: &now, LastOutcome: "ok", Cursor: "2026-10-22", BackfillDone: true}
	if err := db.PutSyncState(ctx, s); err != nil {
		t.Fatal(err)
	}
	s.LastOutcome = "rate_limited"
	s.BackfillDone = false
	db.PutSyncState(ctx, s)
	db.PutSyncState(ctx, SyncState{DataType: "hrv"})
	got, _ := db.GetSyncState(ctx, "sleep")
	if got.LastOutcome != "rate_limited" || got.BackfillDone || got.Cursor != "2026-10-22" || got.LastAttemptAt == nil || !got.LastAttemptAt.Equal(now) || got.FreshestDataAt != nil {
		t.Fatalf("%+v", got)
	}
	all, err := db.ListSyncStates(ctx)
	if err != nil || len(all) != 2 || all[0].DataType != "hrv" {
		t.Fatalf("%+v %v", all, err)
	}
}
