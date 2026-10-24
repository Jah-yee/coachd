package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/stefiix92/coachd/internal/fitdata"
)

// ActivityDetail is the laps and series decoded from an activity's FIT file,
// computed once at store time. Series is already downsampled to
// fitdata.MaxSeriesPoints.
//
// The activity_detail table caches it as JSON of fitdata.Lap and
// fitdata.Point, keyed by their Go field names (TestActivityDetailGoldenJSON
// pins the shape). Any change to fitdata's output (decoding, downsampling) or
// to those field names must ship with `DELETE FROM activity_detail`: the cache
// then rebuilds lazily on get_activity and from stored FITs on each FIT drain.
type ActivityDetail struct {
	Laps   []fitdata.Lap
	Series []fitdata.Point
}

// PutActivityDetail upserts the detail for an activity. The activity row must exist.
func (db *DB) PutActivityDetail(ctx context.Context, id int64, d ActivityDetail) error {
	if d.Laps == nil {
		d.Laps = []fitdata.Lap{}
	}
	if d.Series == nil {
		d.Series = []fitdata.Point{}
	}
	laps, err := json.Marshal(d.Laps)
	if err != nil {
		return fmt.Errorf("store: encode laps: %w", err)
	}
	series, err := json.Marshal(d.Series)
	if err != nil {
		return fmt.Errorf("store: encode series: %w", err)
	}
	_, err = db.sql.ExecContext(ctx, `
INSERT INTO activity_detail (activity_id, laps_json, series_json, computed_at) VALUES (?,?,?,?)
ON CONFLICT(activity_id) DO UPDATE SET laps_json=excluded.laps_json,
	series_json=excluded.series_json, computed_at=excluded.computed_at`,
		id, string(laps), string(series), fmtTime(time.Now()))
	if err != nil {
		return fmt.Errorf("store: put activity detail: %w", err)
	}
	return nil
}

// GetActivityDetail returns the stored detail of a live activity, or
// ErrNotFound when absent or the activity is soft-deleted.
func (db *DB) GetActivityDetail(ctx context.Context, id int64) (ActivityDetail, error) {
	var laps, series string
	err := db.sql.QueryRowContext(ctx, `SELECT d.laps_json, d.series_json FROM activity_detail d
		JOIN activities a ON a.id = d.activity_id WHERE d.activity_id=? AND a.`+liveActivity, id).Scan(&laps, &series)
	if errors.Is(err, sql.ErrNoRows) {
		return ActivityDetail{}, ErrNotFound
	}
	if err != nil {
		return ActivityDetail{}, fmt.Errorf("store: get activity detail: %w", err)
	}
	var d ActivityDetail
	if err := json.Unmarshal([]byte(laps), &d.Laps); err != nil {
		return ActivityDetail{}, fmt.Errorf("store: decode laps: %w", err)
	}
	if err := json.Unmarshal([]byte(series), &d.Series); err != nil {
		return ActivityDetail{}, fmt.Errorf("store: decode series: %w", err)
	}
	return d, nil
}

// StoredFITsWithoutDetail returns live activities whose FIT is stored but that
// have no detail row, newest first.
func (db *DB) StoredFITsWithoutDetail(ctx context.Context, limit int) ([]Activity, error) {
	if limit <= 0 {
		limit = defaultListLimit
	}
	return db.queryActivities(ctx,
		`fit_status='stored' AND NOT EXISTS (SELECT 1 FROM activity_detail d WHERE d.activity_id = activities.id)`,
		nil, "ORDER BY date DESC, id DESC LIMIT "+strconv.Itoa(limit))
}
