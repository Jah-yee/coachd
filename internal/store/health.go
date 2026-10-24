package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/stefiix92/coachd/internal/civil"
)

// dailyCols are the daily_health value columns, in scan/bind order after date.
var dailyCols = []string{
	"sleep_s", "hrv_overnight_ms", "hrv_weekly_avg_ms", "resting_hr", "spo2_avg", "weight_kg",
	"sleep_score", "hrv_status", "hrv_baseline_low_ms", "hrv_baseline_high_ms",
	"body_battery_max", "body_battery_min", "body_battery_charged", "body_battery_drained", "stress_avg",
}

var upsertDailySQL = func() string {
	sets := make([]string, len(dailyCols))
	for i, c := range dailyCols {
		sets[i] = fmt.Sprintf("%s=COALESCE(excluded.%s, daily_health.%s)", c, c, c)
	}
	return `INSERT INTO daily_health (date, ` + strings.Join(dailyCols, ", ") + `) VALUES (?` +
		strings.Repeat(",?", len(dailyCols)) + `) ON CONFLICT(date) DO UPDATE SET ` + strings.Join(sets, ", ")
}()

// UpsertDailyHealth merges a day's values; nil fields never overwrite existing values.
func (db *DB) UpsertDailyHealth(ctx context.Context, d DailyHealth) error {
	g := d.Garmin
	_, err := db.sql.ExecContext(ctx, upsertDailySQL, d.Date.String(),
		d.SleepS, d.HRVOvernightMs, d.HRVWeeklyAvgMs, d.RestingHR, d.SpO2Avg, d.WeightKg,
		g.SleepScore, g.HRVStatus, g.HRVBaselineLowMs, g.HRVBaselineHighMs,
		g.BodyBatteryMax, g.BodyBatteryMin, g.BodyBatteryCharged, g.BodyBatteryDrained, g.StressAvg)
	if err != nil {
		return fmt.Errorf("store: upsert daily health: %w", err)
	}
	return nil
}

func (db *DB) ListDailyHealth(ctx context.Context, from, to civil.Date) ([]DailyHealth, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT date, `+strings.Join(dailyCols, ", ")+
		` FROM daily_health WHERE date >= ? AND date <= ? ORDER BY date`, from.String(), to.String())
	if err != nil {
		return nil, fmt.Errorf("store: list daily health: %w", err)
	}
	defer rows.Close()
	var out []DailyHealth
	for rows.Next() {
		var (
			d                                         DailyHealth
			date                                      string
			sleep, hrvO, hrvW, rhr, spo2              sql.NullInt64
			weight                                    sql.NullFloat64
			score, bl, bh, bbMax, bbMin, bbC, bbD, st sql.NullInt64
			status                                    sql.NullString
		)
		if err := rows.Scan(&date, &sleep, &hrvO, &hrvW, &rhr, &spo2, &weight, &score, &status, &bl, &bh,
			&bbMax, &bbMin, &bbC, &bbD, &st); err != nil {
			return nil, err
		}
		if d.Date, err = civil.Parse(date); err != nil {
			return nil, err
		}
		d.SleepS, d.HRVOvernightMs, d.HRVWeeklyAvgMs, d.RestingHR, d.SpO2Avg, d.WeightKg =
			iptr(sleep), iptr(hrvO), iptr(hrvW), iptr(rhr), iptr(spo2), fptr(weight)
		d.Garmin = DailyGarmin{
			SleepScore: iptr(score), HRVStatus: sptr(status), HRVBaselineLowMs: iptr(bl), HRVBaselineHighMs: iptr(bh),
			BodyBatteryMax: iptr(bbMax), BodyBatteryMin: iptr(bbMin), BodyBatteryCharged: iptr(bbC),
			BodyBatteryDrained: iptr(bbD), StressAvg: iptr(st),
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ReplaceDayMetrics atomically replaces the rows in [min, max] of ms's dates
// with ms; rows outside that range are kept. Empty ms is a no-op.
func (db *DB) ReplaceDayMetrics(ctx context.Context, ms []DayMetrics) error {
	if len(ms) == 0 {
		return nil
	}
	lo, hi := ms[0].Date, ms[0].Date
	for _, m := range ms[1:] {
		if m.Date.Before(lo) {
			lo = m.Date
		}
		if hi.Before(m.Date) {
			hi = m.Date
		}
	}
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM day_metrics WHERE date >= ? AND date <= ?`, lo.String(), hi.String()); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO day_metrics (date, hrv_7d_ms, hrv_band_low_ms, hrv_band_high_ms,
		hrv_position, resting_hr_7d, resting_hr_60d, sleep_7d_s, longest_run_7d_m, longest_run_7d_s,
		longest_run_30d_m, longest_run_30d_s, longest_run_90d_m, longest_run_90d_s)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, m := range ms {
		if _, err := stmt.ExecContext(ctx, m.Date.String(), m.HRV7dMs, m.HRVBandLowMs, m.HRVBandHighMs,
			m.HRVPosition, m.RestingHR7d, m.RestingHR60d, m.Sleep7dS, m.LongestRun7dM, m.LongestRun7dS,
			m.LongestRun30dM, m.LongestRun30dS, m.LongestRun90dM, m.LongestRun90dS); err != nil {
			return fmt.Errorf("store: replace day metrics: %w", err)
		}
	}
	return tx.Commit()
}

func (db *DB) ListDayMetrics(ctx context.Context, from, to civil.Date) ([]DayMetrics, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT date, hrv_7d_ms, hrv_band_low_ms, hrv_band_high_ms, hrv_position,
		resting_hr_7d, resting_hr_60d, sleep_7d_s, longest_run_7d_m, longest_run_7d_s, longest_run_30d_m,
		longest_run_30d_s, longest_run_90d_m, longest_run_90d_s
		FROM day_metrics WHERE date >= ? AND date <= ? ORDER BY date`, from.String(), to.String())
	if err != nil {
		return nil, fmt.Errorf("store: list day metrics: %w", err)
	}
	defer rows.Close()
	var out []DayMetrics
	for rows.Next() {
		var (
			m                       DayMetrics
			date                    string
			h7, hl, hh, r7, r60, s7 sql.NullFloat64
		)
		if err := rows.Scan(&date, &h7, &hl, &hh, &m.HRVPosition, &r7, &r60, &s7, &m.LongestRun7dM, &m.LongestRun7dS,
			&m.LongestRun30dM, &m.LongestRun30dS, &m.LongestRun90dM, &m.LongestRun90dS); err != nil {
			return nil, err
		}
		if m.Date, err = civil.Parse(date); err != nil {
			return nil, err
		}
		m.HRV7dMs, m.HRVBandLowMs, m.HRVBandHighMs = fptr(h7), fptr(hl), fptr(hh)
		m.RestingHR7d, m.RestingHR60d, m.Sleep7dS = fptr(r7), fptr(r60), fptr(s7)
		out = append(out, m)
	}
	return out, rows.Err()
}

// Coverage reports history depth (over live activities and daily health) and
// daily-health gaps in the last 90 days.
func (db *DB) Coverage(ctx context.Context, today civil.Date) (Coverage, error) {
	var first sql.NullString
	err := db.sql.QueryRowContext(ctx, `SELECT min(d) FROM (
		SELECT min(date) AS d FROM activities WHERE `+liveActivity+` UNION ALL SELECT min(date) FROM daily_health)`).Scan(&first)
	if err != nil {
		return Coverage{}, fmt.Errorf("store: coverage: %w", err)
	}
	if !first.Valid {
		return Coverage{}, nil
	}
	fd, err := civil.Parse(first.String)
	if err != nil {
		return Coverage{}, err
	}
	c := Coverage{FirstDate: &fd}
	if w := today.Sub(fd) / 7; w > 0 {
		c.WeeksOfHistory = w
	}
	start := today.AddDays(-89)
	if start.Before(fd) {
		start = fd
	}
	days := today.Sub(start) + 1
	if days <= 0 {
		return c, nil
	}
	var have, hrv, sleep, rhr int
	if err := db.sql.QueryRowContext(ctx, `SELECT count(*), count(hrv_overnight_ms), count(sleep_s), count(resting_hr)
		FROM daily_health WHERE date >= ? AND date <= ?`,
		start.String(), today.String()).Scan(&have, &hrv, &sleep, &rhr); err != nil {
		return Coverage{}, fmt.Errorf("store: coverage: %w", err)
	}
	c.DailyHealthGaps90d = max(days-have, 0)
	c.HRVGaps90d, c.SleepGaps90d, c.RestingHRGaps90d = max(days-hrv, 0), max(days-sleep, 0), max(days-rhr, 0)
	return c, nil
}

// SaveRaw stores the latest raw JSON payload for (source, key).
func (db *DB) SaveRaw(ctx context.Context, source, key string, raw []byte) error {
	_, err := db.sql.ExecContext(ctx, `INSERT INTO raw_payloads (source, key, fetched_at, json) VALUES (?,?,?,?)
		ON CONFLICT(source, key) DO UPDATE SET fetched_at=excluded.fetched_at, json=excluded.json`,
		source, key, fmtTime(time.Now()), raw)
	if err != nil {
		return fmt.Errorf("store: save raw: %w", err)
	}
	return nil
}
