// Package summaries computes deterministic, numbers-only daily metrics.
// No verdicts: the only strings produced are the HRV band positions.
package summaries

import (
	"math"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/store"
)

const (
	minWindow7  = 4  // minimum values for a 7-day mean
	minWindow60 = 30 // minimum values for a 60-day baseline
)

// Compute returns exactly one DayMetrics per date in [from, to], ascending.
// Inputs may extend outside that range; they feed the windows only.
func Compute(health []store.DailyHealth, runs []store.Activity, from, to civil.Date) []store.DayMetrics {
	byDate := make(map[civil.Date]store.DailyHealth, len(health))
	for _, h := range health {
		byDate[h.Date] = h
	}
	var rs []store.Activity
	for _, a := range runs {
		if a.Sport == store.SportRunning || a.Sport == store.SportTrailRunning {
			rs = append(rs, a)
		}
	}

	hrvOf := func(h store.DailyHealth) *int { return h.HRVOvernightMs }
	rhrOf := func(h store.DailyHealth) *int { return h.RestingHR }
	sleepOf := func(h store.DailyHealth) *int { return h.SleepS }

	// mean over [start, end] of non-nil values; nil if fewer than min.
	mean := func(sel func(store.DailyHealth) *int, start, end civil.Date, min int) *float64 {
		var sum float64
		n := 0
		for x := start; !end.Before(x); x = x.AddDays(1) {
			if h, ok := byDate[x]; ok {
				if v := sel(h); v != nil {
					sum += float64(*v)
					n++
				}
			}
		}
		if n < min || n == 0 {
			return nil
		}
		m := sum / float64(n)
		return &m
	}
	hrv7 := func(x civil.Date) *float64 { return mean(hrvOf, x.AddDays(-6), x, minWindow7) }

	var out []store.DayMetrics
	for day := from; !to.Before(day); day = day.AddDays(1) {
		m := store.DayMetrics{Date: day}
		m.HRV7dMs = hrv7(day)
		m.RestingHR7d = mean(rhrOf, day.AddDays(-6), day, minWindow7)
		m.RestingHR60d = mean(rhrOf, day.AddDays(-60), day.AddDays(-1), minWindow60)
		m.Sleep7dS = mean(sleepOf, day.AddDays(-6), day, minWindow7)

		var vals []float64
		for x := day.AddDays(-60); x.Before(day); x = x.AddDays(1) {
			if v := hrv7(x); v != nil {
				vals = append(vals, *v)
			}
		}
		if len(vals) >= minWindow60 {
			var sum float64
			for _, v := range vals {
				sum += v
			}
			mu := sum / float64(len(vals))
			var ss float64
			for _, v := range vals {
				ss += (v - mu) * (v - mu)
			}
			sd := math.Sqrt(ss / float64(len(vals)))
			lo, hi := mu-sd, mu+sd
			m.HRVBandLowMs, m.HRVBandHighMs = &lo, &hi
			if m.HRV7dMs != nil {
				switch {
				case *m.HRV7dMs < lo:
					m.HRVPosition = "below"
				case *m.HRV7dMs > hi:
					m.HRVPosition = "above"
				default:
					m.HRVPosition = "within"
				}
			}
		}

		m.LongestRun7dM, m.LongestRun7dS = longest(rs, day, 7)
		m.LongestRun30dM, m.LongestRun30dS = longest(rs, day, 30)
		m.LongestRun90dM, m.LongestRun90dS = longest(rs, day, 90)
		out = append(out, m)
	}
	return out
}

// longest returns the max distance and max moving time (independently) over
// runs dated in day-(n-1)..day.
func longest(runs []store.Activity, day civil.Date, n int) (distM, movingS float64) {
	start := day.AddDays(-(n - 1))
	for _, a := range runs {
		if a.Date.Before(start) || day.Before(a.Date) {
			continue
		}
		distM = math.Max(distM, a.DistanceM)
		movingS = math.Max(movingS, a.MovingS)
	}
	return distM, movingS
}
