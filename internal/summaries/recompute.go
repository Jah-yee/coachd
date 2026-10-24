package summaries

import (
	"context"
	"fmt"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/store"
)

// Recompute loads [today-days-90, today] of health and runs, computes metrics
// for [today-days, today] and replaces those stored rows.
func Recompute(ctx context.Context, db *store.DB, today civil.Date, days int) error {
	if days <= 0 {
		return fmt.Errorf("summaries: recompute: days must be positive, got %d", days)
	}
	from := today.AddDays(-days)
	load := from.AddDays(-90)
	health, err := db.ListDailyHealth(ctx, load, today)
	if err != nil {
		return fmt.Errorf("summaries: recompute: load health: %w", err)
	}
	runs, err := db.Runs(ctx, load, today)
	if err != nil {
		return fmt.Errorf("summaries: recompute: load runs: %w", err)
	}
	if err := db.ReplaceDayMetrics(ctx, Compute(health, runs, from, today)); err != nil {
		return fmt.Errorf("summaries: recompute: store: %w", err)
	}
	return nil
}
