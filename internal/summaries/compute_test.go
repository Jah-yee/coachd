package summaries

import (
	"math"
	"reflect"
	"testing"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/store"
)

func d(s string) civil.Date {
	v, err := civil.Parse(s)
	if err != nil {
		panic(err)
	}
	return v
}

func ip(v int) *int { return &v }

func near(t *testing.T, name string, got *float64, want, tol float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: got nil, want %v", name, want)
	}
	if math.Abs(*got-want) > tol {
		t.Fatalf("%s: got %v, want %v (tol %v)", name, *got, want, tol)
	}
}

func last(ms []store.DayMetrics) store.DayMetrics { return ms[len(ms)-1] }

func TestHRV7dNeedsFourValues(t *testing.T) {
	today := d("2026-10-20")
	var h []store.DailyHealth
	for i := 0; i < 3; i++ {
		h = append(h, store.DailyHealth{Date: today.AddDays(-i), HRVOvernightMs: ip(50)})
	}
	if m := last(Compute(h, nil, today, today)); m.HRV7dMs != nil {
		t.Fatalf("3 values: want nil, got %v", *m.HRV7dMs)
	}
	h = append(h, store.DailyHealth{Date: today.AddDays(-3), HRVOvernightMs: ip(60)})
	m := last(Compute(h, nil, today, today))
	near(t, "hrv7d", m.HRV7dMs, 52.5, 1e-9)
}

func TestHRVBandAndPosition(t *testing.T) {
	today := d("2026-10-20")
	var h []store.DailyHealth
	for i := 1; i <= 66; i++ {
		v := 50
		if i%2 == 0 {
			v = 60
		}
		h = append(h, store.DailyHealth{Date: today.AddDays(-i), HRVOvernightMs: ip(v)})
	}
	h = append(h, store.DailyHealth{Date: today, HRVOvernightMs: ip(200)})
	m := last(Compute(h, nil, today, today))
	if m.HRV7dMs == nil || m.HRVBandLowMs == nil || m.HRVBandHighMs == nil {
		t.Fatalf("expected hrv7d and band, got %+v", m)
	}
	if m.HRVPosition != "above" {
		t.Fatalf("position = %q (hrv7d=%v band %v..%v)", m.HRVPosition, *m.HRV7dMs, *m.HRVBandLowMs, *m.HRVBandHighMs)
	}
	if *m.HRVBandLowMs >= *m.HRVBandHighMs {
		t.Fatalf("band inverted")
	}

	// a depressed week yields "below"
	var low []store.DailyHealth
	for _, x := range h[:len(h)-1] {
		if today.Sub(x.Date) >= 7 {
			low = append(low, x)
		}
	}
	for i := 0; i < 7; i++ {
		low = append(low, store.DailyHealth{Date: today.AddDays(-i), HRVOvernightMs: ip(10)})
	}
	if m = last(Compute(low, nil, today, today)); m.HRVPosition != "below" {
		t.Fatalf("want below, got %q", m.HRVPosition)
	}
}

func TestHRVBandPositionWithin(t *testing.T) {
	today := d("2026-10-20")
	// constant history: SD 0, band is a point; equal value is within.
	var h []store.DailyHealth
	for i := 0; i <= 70; i++ {
		h = append(h, store.DailyHealth{Date: today.AddDays(-i), HRVOvernightMs: ip(55)})
	}
	m := last(Compute(h, nil, today, today))
	near(t, "low", m.HRVBandLowMs, 55, 1e-9)
	near(t, "high", m.HRVBandHighMs, 55, 1e-9)
	if m.HRVPosition != "within" {
		t.Fatalf("got %q", m.HRVPosition)
	}
}

func TestHRVBandExactly29Values(t *testing.T) {
	today := d("2026-10-20")
	var h []store.DailyHealth
	// raw days d-1 .. d-k; HRV7d defined where >=4 raw values in window.
	// Oldest raw d-k: HRV7d defined at d-j for j<=k-3 (window d-j..d-j-6 contains d-k.. needs >=4).
	// So count = k-3 (j=1..k-3). k=32 -> 29 values.
	for i := 1; i <= 32; i++ {
		h = append(h, store.DailyHealth{Date: today.AddDays(-i), HRVOvernightMs: ip(50)})
	}
	h = append(h, store.DailyHealth{Date: today, HRVOvernightMs: ip(50)})
	m := last(Compute(h, nil, today, today))
	if m.HRVBandLowMs != nil || m.HRVPosition != "" {
		t.Fatalf("29 values: want nil band, got %+v", m)
	}
	h = append(h, store.DailyHealth{Date: today.AddDays(-33), HRVOvernightMs: ip(50)})
	m = last(Compute(h, nil, today, today))
	if m.HRVBandLowMs == nil || m.HRVPosition != "within" {
		t.Fatalf("30 values: want band, got %+v", m)
	}
}

func TestRestingHRAndSleepThresholds(t *testing.T) {
	today := d("2026-10-20")
	mk := func(n int) []store.DailyHealth {
		var h []store.DailyHealth
		for i := 0; i < n; i++ {
			h = append(h, store.DailyHealth{Date: today.AddDays(-i), RestingHR: ip(50 + i), SleepS: ip(28000 + 100*i)})
		}
		return h
	}
	m := last(Compute(mk(3), nil, today, today))
	if m.RestingHR7d != nil || m.Sleep7dS != nil {
		t.Fatalf("3 values: want nil")
	}
	m = last(Compute(mk(4), nil, today, today))
	near(t, "rhr7", m.RestingHR7d, 51.5, 1e-9)
	near(t, "sleep7", m.Sleep7dS, 28150, 1e-9)

	// 60d: values on d-1..d-N
	mk60 := func(n int) []store.DailyHealth {
		var h []store.DailyHealth
		for i := 1; i <= n; i++ {
			h = append(h, store.DailyHealth{Date: today.AddDays(-i), RestingHR: ip(60)})
		}
		return h
	}
	if m := last(Compute(mk60(29), nil, today, today)); m.RestingHR60d != nil {
		t.Fatalf("29 values: want nil")
	}
	near(t, "rhr60", last(Compute(mk60(30), nil, today, today)).RestingHR60d, 60, 1e-9)
	// today's value is excluded from the 60d mean
	h := append(mk60(30), store.DailyHealth{Date: today, RestingHR: ip(100)})
	near(t, "rhr60 excl today", last(Compute(h, nil, today, today)).RestingHR60d, 60, 1e-9)
}

func TestLongestRunWindows(t *testing.T) {
	today := d("2026-10-20")
	runs := []store.Activity{
		{Date: today.AddDays(-40), Sport: store.SportRunning, DistanceM: 10000, MovingS: 3000},
		{Date: today.AddDays(-20), Sport: store.SportTrailRunning, DistanceM: 25000, MovingS: 9000},
		{Date: today.AddDays(-2), Sport: store.SportRunning, DistanceM: 8000, MovingS: 2400},
		{Date: today.AddDays(-1), Sport: store.SportCycling, DistanceM: 99000, MovingS: 99000},
	}
	m := last(Compute(nil, runs, today, today))
	if m.LongestRun7dM != 8000 || m.LongestRun30dM != 25000 || m.LongestRun90dM != 25000 {
		t.Fatalf("distances: %v %v %v", m.LongestRun7dM, m.LongestRun30dM, m.LongestRun90dM)
	}
	if m.LongestRun7dS != 2400 || m.LongestRun30dS != 9000 || m.LongestRun90dS != 9000 {
		t.Fatalf("times: %v %v %v", m.LongestRun7dS, m.LongestRun30dS, m.LongestRun90dS)
	}
	// window edges: d-6 in 7d, d-7 not
	edge := []store.Activity{
		{Date: today.AddDays(-6), Sport: store.SportRunning, DistanceM: 5000, MovingS: 1},
		{Date: today.AddDays(-7), Sport: store.SportRunning, DistanceM: 9000, MovingS: 2},
	}
	m = last(Compute(nil, edge, today, today))
	if m.LongestRun7dM != 5000 || m.LongestRun30dM != 9000 {
		t.Fatalf("edge: %v %v", m.LongestRun7dM, m.LongestRun30dM)
	}
	// independent maxima
	ind := []store.Activity{
		{Date: today, Sport: store.SportRunning, DistanceM: 20000, MovingS: 1000},
		{Date: today, Sport: store.SportRunning, DistanceM: 5000, MovingS: 5000},
	}
	m = last(Compute(nil, ind, today, today))
	if m.LongestRun7dM != 20000 || m.LongestRun7dS != 5000 {
		t.Fatalf("independent: %v %v", m.LongestRun7dM, m.LongestRun7dS)
	}
}

func TestComputeAcrossDST(t *testing.T) {
	to := d("2026-10-27")
	var h []store.DailyHealth
	for i := 0; i < 7; i++ {
		h = append(h, store.DailyHealth{Date: to.AddDays(-i), RestingHR: ip(50)})
	}
	// window d-6..d for d=2026-10-27 spans 10-21..10-27 and crosses 10-25.
	m := last(Compute(h, nil, to, to))
	near(t, "rhr7", m.RestingHR7d, 50, 1e-9)
	// Exactly 7 dates: dropping any one of the 7 leaves 6 (still >=4), so verify by
	// adding an 8th outlier on d-7, which must be excluded.
	h = append(h, store.DailyHealth{Date: to.AddDays(-7), RestingHR: ip(1000)})
	m = last(Compute(h, nil, to, to))
	near(t, "rhr7 excl d-7", m.RestingHR7d, 50, 1e-9)
	// d-6 is inside the window: 6 values of 50 plus one 1000 -> mean 192.857...
	var win []store.DailyHealth
	for i := 0; i < 6; i++ {
		win = append(win, store.DailyHealth{Date: to.AddDays(-i), RestingHR: ip(50)})
	}
	win = append(win, store.DailyHealth{Date: to.AddDays(-6), RestingHR: ip(1000)})
	m = last(Compute(win, nil, to, to))
	near(t, "rhr7 incl d-6", m.RestingHR7d, (6*50+1000)/7.0, 1e-9)
	// Date sequence across the change is contiguous.
	ms := Compute(nil, nil, d("2026-10-23"), d("2026-10-28"))
	want := []string{"2026-10-23", "2026-10-24", "2026-10-25", "2026-10-26", "2026-10-27", "2026-10-28"}
	if len(ms) != len(want) {
		t.Fatalf("len %d", len(ms))
	}
	for i, m := range ms {
		if m.Date.String() != want[i] {
			t.Fatalf("row %d = %s want %s", i, m.Date, want[i])
		}
	}
}

func TestOneRowPerDateIncludingEmpty(t *testing.T) {
	from, to := d("2026-10-01"), d("2026-10-10")
	ms := Compute(nil, nil, from, to)
	if len(ms) != 10 {
		t.Fatalf("len %d", len(ms))
	}
	for _, m := range ms {
		if m.HRV7dMs != nil || m.RestingHR7d != nil || m.HRVPosition != "" || m.LongestRun90dM != 0 {
			t.Fatalf("expected empty row: %+v", m)
		}
	}
	if got := Compute(nil, nil, to, from); len(got) != 0 {
		t.Fatalf("from after to: want empty")
	}
}

func TestInputOutsideRangeProducesNoRows(t *testing.T) {
	from, to := d("2026-10-10"), d("2026-10-12")
	h := []store.DailyHealth{{Date: d("2026-08-01"), RestingHR: ip(50)}, {Date: d("2026-11-01"), RestingHR: ip(50)}}
	if ms := Compute(h, nil, from, to); len(ms) != 3 {
		t.Fatalf("len %d", len(ms))
	}
}

func TestNoVerdictStrings(t *testing.T) {
	today := d("2026-10-20")
	var h []store.DailyHealth
	for i := 0; i <= 80; i++ {
		h = append(h, store.DailyHealth{Date: today.AddDays(-i), HRVOvernightMs: ip(40 + (i*7)%30)})
	}
	allowed := map[string]bool{"below": true, "within": true, "above": true, "": true}
	for _, m := range Compute(h, nil, today.AddDays(-20), today) {
		v := reflect.ValueOf(m)
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).Kind() == reflect.String && !allowed[v.Field(i).String()] {
				t.Fatalf("field %s = %q", v.Type().Field(i).Name, v.Field(i).String())
			}
		}
	}
}
