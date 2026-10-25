package fitdata

import (
	"bytes"
	"math"
	"testing"
	"time"

	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"

	"github.com/stefiix92/coachd/internal/fitdata/fittest"
)

const noStep = 0xFFFF

// intervalSteps is warm-up 10 min (HR 110–138), then 2 × (1 km at
// 3.0–3.3 m/s, 1 min recovery), then an open cool-down.
var intervalSteps = []fittest.Step{
	{Intensity: typedef.IntensityWarmup, Duration: typedef.WktStepDurationTime, DurationValue: 600000,
		Target: typedef.WktStepTargetHeartRate, Low: 210, High: 238},
	{Intensity: typedef.IntensityActive, Duration: typedef.WktStepDurationDistance, DurationValue: 100000,
		Target: typedef.WktStepTargetSpeed, Low: 3000, High: 3300},
	{Intensity: typedef.IntensityRecovery, Duration: typedef.WktStepDurationTime, DurationValue: 60000,
		Target: typedef.WktStepTargetOpen},
	{Duration: typedef.WktStepDurationRepeatUntilStepsCmplt, DurationValue: 1,
		Target: typedef.WktStepTargetOpen, TargetValue: 2},
	{Intensity: typedef.IntensityCooldown, Duration: typedef.WktStepDurationOpen, Target: typedef.WktStepTargetOpen},
}

func workoutFile(t *testing.T, name string, steps []fittest.Step) []byte {
	t.Helper()
	b, err := fittest.WorkoutFile(name, steps)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// intervalRun records intervalSteps: the warm-up split by an auto-lap, both
// iterations, the cool-down and a lap after the workout.
func intervalRun(t *testing.T, modify func([]proto.Message)) Activity {
	t.Helper()
	laps := []int{300, 300, 330, 60, 330, 60, 200, 50}
	total := 0
	for _, n := range laps {
		total += n
	}
	b, err := fittest.Build(fittest.Options{
		Start: start, Records: total, HR: 140, SpeedMS: 3,
		LapRecords: laps,
		LapSteps:   []uint16{0, 0, 1, 2, 1, 2, 4, noStep},
		Workout:    workoutFile(t, "Intervals", intervalSteps),
		Modify:     modify,
		HRAt: func(i int) uint8 {
			switch {
			case i < 100:
				return 100 // below the warm-up band
			case i < 500:
				return 125
			case i < 600:
				return 150 // above
			}
			return 140
		},
		SpeedAt: func(i int) float64 {
			switch {
			case i >= 990 && i < 1020:
				return 3.5 // faster than the band, in iteration 2
			case i >= 1020 && i < 1030:
				return 0.1 // nearly stationary: no pace
			}
			return 3.15
		},
		LapPower: func(lap int) uint16 { return uint16(200 + 10*lap) },
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestDecodeWithoutWorkout(t *testing.T) {
	a, err := Decode(bytes.NewReader(build(t, 2, 120, 140, 3.0)))
	if err != nil {
		t.Fatal(err)
	}
	if a.Workout != nil {
		t.Fatalf("workout = %+v, want nil", a.Workout)
	}
}

func TestDecodeWorkoutPlannedSteps(t *testing.T) {
	w := intervalRun(t, nil).Workout
	if w == nil {
		t.Fatal("no workout")
	}
	if w.NameUntrusted != "Intervals" || len(w.Steps) != 5 {
		t.Fatalf("workout = %q, %d steps", w.NameUntrusted, len(w.Steps))
	}
	s := w.Steps[1]
	if s.Index != 1 || s.DurationType != uint8(typedef.WktStepDurationDistance) || s.DurationValue != 100000 ||
		s.TargetType != uint8(typedef.WktStepTargetSpeed) || s.CustomLow != 3000 || s.CustomHigh != 3300 ||
		s.Intensity != uint8(typedef.IntensityActive) {
		t.Fatalf("step 1 = %+v", s)
	}
	if r := w.Steps[3]; r.DurationType != uint8(typedef.WktStepDurationRepeatUntilStepsCmplt) || r.DurationValue != 1 || r.TargetValue != 2 {
		t.Fatalf("repeat = %+v", r)
	}
	if w.Fingerprint == "" {
		t.Fatal("no fingerprint")
	}
}

func TestDecodeWorkoutExecutedSteps(t *testing.T) {
	ex := intervalRun(t, nil).Workout.Executed
	type key struct{ step, iter, laps int }
	want := []key{{0, 1, 2}, {1, 1, 1}, {2, 1, 1}, {1, 2, 1}, {2, 2, 1}, {4, 1, 1}}
	if len(ex) != len(want) {
		t.Fatalf("executed = %+v", ex)
	}
	for i, w := range want {
		if got := (key{ex[i].StepIndex, ex[i].Iteration, ex[i].LapCount}); got != w {
			t.Fatalf("executed[%d] = %+v, want %+v", i, got, w)
		}
	}
	w0 := ex[0]
	if w0.StartOffsetS != 0 || w0.DurationS != 600 || math.Abs(w0.DistanceKm-1.8) > 1e-9 {
		t.Fatalf("warm-up = %+v", w0)
	}
	if ex[1].StartOffsetS != 600 || ex[5].StartOffsetS != 1380 || ex[5].DurationS != 200 {
		t.Fatalf("offsets = %v %v %v", ex[1].StartOffsetS, ex[5].StartOffsetS, ex[5].DurationS)
	}
	if w0.AvgHRBpm == nil || *w0.AvgHRBpm != 140 || w0.MaxHRBpm == nil || *w0.MaxHRBpm != 140 {
		t.Fatalf("warm-up hr = %v %v", w0.AvgHRBpm, w0.MaxHRBpm)
	}
	if w0.AvgPaceSPerKm == nil || math.Abs(*w0.AvgPaceSPerKm-333.33) > 0.01 {
		t.Fatalf("warm-up pace = %v", w0.AvgPaceSPerKm)
	}
	// Laps 0 and 1 have 200 W and 210 W over 300 s each.
	if w0.AvgPowerW == nil || *w0.AvgPowerW != 205 {
		t.Fatalf("warm-up power = %v", w0.AvgPowerW)
	}
}

func TestDecodeWorkoutTimeInTarget(t *testing.T) {
	ex := intervalRun(t, nil).Workout.Executed
	tt := ex[0].TargetTime
	if tt == nil || tt.MeasuredS != 600 || tt.BelowS != 100 || tt.InS != 400 || tt.AboveS != 100 {
		t.Fatalf("warm-up target time = %+v", tt)
	}
	if tt := ex[1].TargetTime; tt == nil || tt.MeasuredS != 330 || tt.InS != 330 {
		t.Fatalf("iteration 1 target time = %+v", tt)
	}
	// Iteration 2 starts at record 990: 30 s faster (below the pace band's
	// lower bound), then 10 s with no pace, then in the band.
	if tt := ex[3].TargetTime; tt == nil || tt.MeasuredS != 320 || tt.BelowS != 30 || tt.InS != 290 || tt.AboveS != 0 {
		t.Fatalf("iteration 2 target time = %+v", tt)
	}
	if ex[2].TargetTime != nil || ex[5].TargetTime != nil {
		t.Fatal("open targets must have no target time")
	}
}

func TestDecodeWorkoutPauseCapped(t *testing.T) {
	b, err := fittest.Build(fittest.Options{
		Start: start, Records: 600, HR: 125, SpeedMS: 3,
		LapRecords: []int{600}, LapSteps: []uint16{0},
		Workout: workoutFile(t, "Easy", intervalSteps[:1]),
		Gaps:    map[int]time.Duration{300: 60 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	// 599 records count 1 s each; the one before the pause counts 10 s, not 61 s.
	if tt := a.Workout.Executed[0].TargetTime; tt == nil || tt.MeasuredS != 609 || tt.InS != 609 {
		t.Fatalf("target time = %+v", tt)
	}
}

func TestDecodeWorkoutInvalidIndexSplitsRuns(t *testing.T) {
	b, err := fittest.Build(fittest.Options{
		Start: start, Records: 300, HR: 125, SpeedMS: 3,
		LapRecords: []int{100, 100, 100}, LapSteps: []uint16{0, noStep, 0},
		Workout: workoutFile(t, "Easy", intervalSteps[:1]),
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	ex := a.Workout.Executed
	if len(ex) != 2 || ex[0].Iteration != 1 || ex[1].Iteration != 2 || ex[1].StartOffsetS != 200 {
		t.Fatalf("executed = %+v", ex)
	}
	if tt := ex[0].TargetTime; tt == nil || tt.MeasuredS != 100 {
		t.Fatalf("first run must end where the unassigned lap starts: %+v", tt)
	}
}

func TestDecodeWorkoutNeedsStepLaps(t *testing.T) {
	b, err := fittest.Build(fittest.Options{
		Start: start, Records: 100, HR: 125, SpeedMS: 3, Laps: 1,
		Workout: workoutFile(t, "Easy", intervalSteps[:1]),
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if a.Workout != nil {
		t.Fatalf("a workout message without step laps must give no workout: %+v", a.Workout)
	}
}

func TestDecodeWorkoutFile(t *testing.T) {
	steps, err := DecodeWorkout(bytes.NewReader(workoutFile(t, "Intervals", intervalSteps)))
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 5 || Fingerprint(steps) != intervalRun(t, nil).Workout.Fingerprint {
		t.Fatalf("steps = %d, fingerprints differ", len(steps))
	}
	if _, err := DecodeWorkout(bytes.NewReader(build(t, 1, 10, 120, 3))); err == nil {
		t.Fatal("an activity file must not decode as a workout")
	}
	if _, err := Decode(bytes.NewReader(workoutFile(t, "Intervals", intervalSteps))); err == nil {
		t.Fatal("a workout file must not decode as an activity")
	}
}

func TestFingerprint(t *testing.T) {
	fp := func(name string, edit func(s []fittest.Step)) string {
		t.Helper()
		s := append([]fittest.Step(nil), intervalSteps...)
		if edit != nil {
			edit(s)
		}
		steps, err := DecodeWorkout(bytes.NewReader(workoutFile(t, name, s)))
		if err != nil {
			t.Fatal(err)
		}
		return Fingerprint(steps)
	}
	base := fp("Intervals", nil)
	if fp("Other name", nil) != base {
		t.Fatal("the name must not change the fingerprint")
	}
	if fp("Intervals", func(s []fittest.Step) { s[1].Intensity = typedef.IntensityInterval }) != base {
		t.Fatal("intensity must not change the fingerprint")
	}
	if fp("Intervals", func(s []fittest.Step) { s[4].DurationValue = 12345 }) != base {
		t.Fatal("an open step's value must not change the fingerprint")
	}
	if fp("Intervals", func(s []fittest.Step) { s[2].TargetValue = 7 }) != base {
		t.Fatal("an open target's value must not change the fingerprint")
	}
	if fp("Intervals", func(s []fittest.Step) { s[3].Target = typedef.WktStepTargetInvalid }) != base {
		t.Fatal("a repeat's target type must not change the fingerprint")
	}
	for name, edit := range map[string]func(s []fittest.Step){
		"duration": func(s []fittest.Step) { s[0].DurationValue = 610000 },
		"target":   func(s []fittest.Step) { s[0].High = 240 },
		"count":    func(s []fittest.Step) { s[3].TargetValue = 3 },
		"type":     func(s []fittest.Step) { s[1].Target = typedef.WktStepTargetHeartRate },
	} {
		if fp("Intervals", edit) == base {
			t.Errorf("a changed %s must change the fingerprint", name)
		}
	}
}

// TestFingerprintSurvivesWatchCopy pins that the activity's recorded steps
// fingerprint like the workout file even when a watch rewrites names.
func TestFingerprintSurvivesWatchCopy(t *testing.T) {
	steps, err := DecodeWorkout(bytes.NewReader(workoutFile(t, "Intervals", intervalSteps)))
	if err != nil {
		t.Fatal(err)
	}
	if got := intervalRun(t, nil).Workout.Fingerprint; got != Fingerprint(steps) {
		t.Fatal("activity and workout file fingerprints differ")
	}
}
