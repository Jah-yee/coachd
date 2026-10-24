package store

import (
	"context"
	"testing"
)

func ip(v int) *int { return &v }

func TestUpsertDailyHealthMergesSources(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	date := d("2026-10-20")
	score := 80
	if err := db.UpsertDailyHealth(ctx, DailyHealth{Date: date, SleepS: ip(27000), Garmin: DailyGarmin{SleepScore: &score}}); err != nil {
		t.Fatal(err)
	}
	status := "BALANCED"
	if err := db.UpsertDailyHealth(ctx, DailyHealth{Date: date, HRVOvernightMs: ip(55), Garmin: DailyGarmin{HRVStatus: &status, BodyBatteryMax: ip(90)}}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListDailyHealth(ctx, date, date)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	h := got[0]
	if h.SleepS == nil || *h.SleepS != 27000 || h.HRVOvernightMs == nil || *h.HRVOvernightMs != 55 {
		t.Fatalf("%+v", h)
	}
	if h.Garmin.SleepScore == nil || *h.Garmin.SleepScore != 80 || h.Garmin.HRVStatus == nil || *h.Garmin.HRVStatus != "BALANCED" || h.Garmin.BodyBatteryMax == nil {
		t.Fatalf("%+v", h.Garmin)
	}
	if h.RestingHR != nil || h.WeightKg != nil {
		t.Fatalf("unexpected values %+v", h)
	}
	// later value overwrites
	db.UpsertDailyHealth(ctx, DailyHealth{Date: date, SleepS: ip(28000)})
	got, _ = db.ListDailyHealth(ctx, date, date)
	if *got[0].SleepS != 28000 {
		t.Fatalf("sleep %d", *got[0].SleepS)
	}
}

func TestDayMetricsReplaceAndList(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	h := 50.0
	seed := []DayMetrics{
		{Date: d("2026-10-18"), LongestRun7dM: 1},
		{Date: d("2026-10-20"), HRV7dMs: &h, HRVPosition: "within", LongestRun7dM: 10000},
		{Date: d("2026-10-21"), LongestRun30dS: 3600},
		{Date: d("2026-10-22"), LongestRun7dM: 2},
		{Date: d("2026-10-25"), LongestRun7dM: 3},
	}
	if err := db.ReplaceDayMetrics(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceDayMetrics(ctx, nil); err != nil {
		t.Fatal(err)
	}
	// Rewrite [10-20, 10-22] without 10-21: 10-21 removed, 10-18 and 10-25 survive.
	if err := db.ReplaceDayMetrics(ctx, []DayMetrics{seed[1], seed[3]}); err != nil {
		t.Fatal(err)
	}
	got, err := db.ListDayMetrics(ctx, d("2026-10-01"), d("2026-10-31"))
	if err != nil {
		t.Fatal(err)
	}
	var dates []string
	for _, m := range got {
		dates = append(dates, m.Date.String())
	}
	want := []string{"2026-10-18", "2026-10-20", "2026-10-22", "2026-10-25"}
	if len(dates) != len(want) {
		t.Fatalf("dates %v want %v", dates, want)
	}
	for i := range want {
		if dates[i] != want[i] {
			t.Fatalf("dates %v want %v", dates, want)
		}
	}
	m := got[1]
	if m.HRVPosition != "within" || *m.HRV7dMs != 50 || m.LongestRun7dM != 10000 || m.RestingHR7d != nil {
		t.Fatalf("%+v", m)
	}
}

func TestCoverage(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	today := d("2026-10-23")
	c, err := db.Coverage(ctx, today)
	if err != nil || c.FirstDate != nil || c.WeeksOfHistory != 0 {
		t.Fatalf("empty: %+v %v", c, err)
	}
	db.UpsertActivity(ctx, mkAct(1, "2026-08-01", SportRunning, 1), nil)
	db.UpsertDailyHealth(ctx, DailyHealth{Date: d("2026-09-01"), SleepS: ip(1)})
	db.UpsertDailyHealth(ctx, DailyHealth{Date: d("2026-10-22"), SleepS: ip(1)})
	c, err = db.Coverage(ctx, today)
	if err != nil {
		t.Fatal(err)
	}
	if c.FirstDate == nil || *c.FirstDate != d("2026-08-01") || c.WeeksOfHistory != 11 {
		t.Fatalf("%+v", c)
	}
	// window [2026-07-26, 2026-10-23] = 90 days; on/after 08-01 = 84 days; 2 rows -> 82 gaps
	if c.DailyHealthGaps90d != 82 {
		t.Fatalf("gaps %d", c.DailyHealthGaps90d)
	}
}

func TestSaveRaw(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	db.SaveRaw(ctx, "sleep", "2026-10-20", []byte(`{"a":1}`))
	db.SaveRaw(ctx, "sleep", "2026-10-20", []byte(`{"a":2}`))
	var n int
	var j string
	db.sql.QueryRow(`SELECT count(*), max(json) FROM raw_payloads`).Scan(&n, &j)
	if n != 1 || j != `{"a":2}` {
		t.Fatalf("%d %s", n, j)
	}
}

func TestCoverageMeasurementGaps(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	today := d("2026-10-23")
	for i := 0; i < 90; i++ {
		h := DailyHealth{Date: today.AddDays(-i), SleepS: ip(1), HRVOvernightMs: ip(50), RestingHR: ip(48)}
		if i%18 == 0 && i > 0 || i == 1 { // 5 days: 1, 18, 36, 54, 72
			h.HRVOvernightMs = nil
		}
		if i == 2 || i == 20 || i == 40 { // 3 days
			h.SleepS = nil
		}
		if err := db.UpsertDailyHealth(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	c, err := db.Coverage(ctx, today)
	if err != nil {
		t.Fatal(err)
	}
	if c.HRVGaps90d != 5 || c.SleepGaps90d != 3 || c.RestingHRGaps90d != 0 || c.DailyHealthGaps90d != 0 {
		t.Fatalf("%+v", c)
	}
}
