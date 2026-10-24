package summaries

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stefiix92/coachd/internal/store"
)

func TestRecomputeWritesRangeAndKeepsOlder(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	today := d("2026-10-20")
	old := d("2026-01-01")
	if err := db.ReplaceDayMetrics(ctx, []store.DayMetrics{{Date: old, LongestRun7dM: 123}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := db.UpsertDailyHealth(ctx, store.DailyHealth{Date: today.AddDays(-i), RestingHR: ip(50)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Recompute(ctx, db, today, 14); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListDayMetrics(ctx, old, today)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1+15 {
		t.Fatalf("rows = %d, want 16", len(got))
	}
	if got[0].Date != old || got[0].LongestRun7dM != 123 {
		t.Fatalf("older row not preserved: %+v", got[0])
	}
	if got[1].Date != today.AddDays(-14) || got[len(got)-1].Date != today {
		t.Fatalf("range: %s..%s", got[1].Date, got[len(got)-1].Date)
	}
	near(t, "rhr7 today", got[len(got)-1].RestingHR7d, 50, 1e-9)

	if err := Recompute(ctx, db, today, 0); err == nil {
		t.Fatal("days<=0: want error")
	}
}
