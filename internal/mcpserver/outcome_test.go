package mcpserver

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/muktihari/fit/profile/typedef"

	"github.com/stefiix92/coachd/internal/fitdata"
	"github.com/stefiix92/coachd/internal/fitdata/fittest"
	"github.com/stefiix92/coachd/internal/fitworkout"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

// storeWorkoutRun stores activity id as a run that recorded p's workout: one
// step of 1800 s at 150 bpm, as the proposal fixture has.
func storeWorkoutRun(t *testing.T, e *env, id int64, date string, p store.Proposal) {
	t.Helper()
	ctx := context.Background()
	if err := e.db.UpsertActivity(ctx, testActivity(t, id, date, "morning run"), nil); err != nil {
		t.Fatal(err)
	}
	var w workout.Workout
	if err := json.Unmarshal(p.ContentJSON, &w); err != nil {
		t.Fatal(err)
	}
	wf, err := fitworkout.Encode(w, p.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	b, err := fittest.Build(fittest.Options{Start: testNow.Add(-24 * time.Hour).UTC(), Records: 1800, HR: 150, SpeedMS: 3,
		LapRecords: []int{1000, 800}, LapSteps: []uint16{0, 0}, Workout: wf})
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("fit", "2026", strings.Repeat("w", int(id))+".fit")
	if err := os.MkdirAll(filepath.Join(e.dir, "fit", "2026"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.dir, rel), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SetFIT(ctx, id, rel, store.FITStored); err != nil {
		t.Fatal(err)
	}
}

func TestGetActivityWorkout(t *testing.T) {
	e := newEnv(t, readIdentity())
	p := proposal(t, e, strings.Repeat("0d", 16), "2026-10-22", "Steady 30", store.ProposalDelivered, testNow.Add(-48*time.Hour), nil)
	storeWorkoutRun(t, e, 7, "2026-10-22", p)

	if _, ok := e.call(t, "get_activity", map[string]any{"id": 7}).object(t)["workout"]; ok {
		t.Fatal("summary detail carries workout")
	}
	got := e.call(t, "get_activity", map[string]any{"id": 7, "detail": "laps"}).object(t)
	w := obj(t, got["workout"])
	wantKeys(t, "workout", w, "proposal_id", "name_untrusted", "steps", "executed")
	if w["proposal_id"] != p.ID || w["name_untrusted"] != "Steady 30" {
		t.Fatalf("workout %v", w)
	}
	steps := arr(t, w["steps"])
	want := map[string]any{"index": 0.0, "kind": "active", "duration": map[string]any{"type": "time", "time_s": 1800.0},
		"target": map[string]any{"type": "none"}, "repeat_from_index": nil, "count": nil}
	if len(steps) != 1 || !reflect.DeepEqual(steps[0], want) {
		t.Fatalf("steps %v", steps)
	}
	ex := arr(t, w["executed"])
	if len(ex) != 1 {
		t.Fatalf("executed %v", ex)
	}
	s := obj(t, ex[0])
	wantKeys(t, "executed", s, "step_index", "iteration", "lap_count", "start_offset_s", "duration_s", "distance_km",
		"avg_hr_bpm", "max_hr_bpm", "avg_pace_s_per_km", "avg_power_w", "target_time")
	wantNum(t, s, "step_index", 0)
	wantNum(t, s, "iteration", 1)
	wantNum(t, s, "lap_count", 2)
	wantNum(t, s, "duration_s", 1800)
	wantNum(t, s, "distance_km", 5.4)
	wantNum(t, s, "avg_hr_bpm", 150)
	wantNum(t, s, "avg_pace_s_per_km", 333.33)
	wantNull(t, s, "avg_power_w", "target_time")

	if _, ok := e.call(t, "get_activity", map[string]any{"id": 7, "detail": "series"}).object(t)["workout"]; !ok {
		t.Fatal("series detail lacks workout")
	}
}

func TestGetActivityWorkoutUnlinked(t *testing.T) {
	e := newEnv(t, readIdentity())
	p := proposal(t, e, strings.Repeat("0a", 16), "2026-10-22", "Steady 30", store.ProposalApproved, testNow.Add(-48*time.Hour), nil)
	storeWorkoutRun(t, e, 7, "2026-10-22", p)
	w := obj(t, e.call(t, "get_activity", map[string]any{"id": 7, "detail": "laps"}).object(t)["workout"])
	wantNull(t, w, "proposal_id")
}

func TestGetActivityWithoutWorkout(t *testing.T) {
	e := newEnv(t, readIdentity())
	if err := e.db.UpsertActivity(context.Background(), testActivity(t, 5, "2026-10-22", "easy"), nil); err != nil {
		t.Fatal(err)
	}
	storeFIT(t, e, 5, 600, 3)
	if _, ok := e.call(t, "get_activity", map[string]any{"id": 5, "detail": "laps"}).object(t)["workout"]; ok {
		t.Fatal("an activity without a workout carries workout")
	}
}

func TestListWorkoutsActivityIDs(t *testing.T) {
	e := newEnv(t, readIdentity())
	done := proposal(t, e, strings.Repeat("0d", 16), "2026-10-22", "Steady 30", store.ProposalDelivered, testNow.Add(-48*time.Hour), nil)
	proposal(t, e, strings.Repeat("0e", 16), "2026-10-24", "Steady 30", store.ProposalApproved, testNow, nil)
	storeWorkoutRun(t, e, 7, "2026-10-22", done)
	// The detail is cached on first read, as the FIT drain would at ingest.
	e.call(t, "get_activity", map[string]any{"id": 7, "detail": "laps"})

	got := arr(t, e.call(t, "list_workouts", map[string]any{"from": "2026-10-20", "to": "2026-10-25"}).object(t)["workouts"])
	if len(got) != 2 {
		t.Fatalf("workouts %v", got)
	}
	if ids := arr(t, obj(t, got[0])["activity_ids"]); len(ids) != 1 || ids[0] != 7.0 {
		t.Fatalf("delivered activity_ids %v", ids)
	}
	if ids := arr(t, obj(t, got[1])["activity_ids"]); len(ids) != 0 {
		t.Fatalf("approved activity_ids %v", ids)
	}
}

func TestPlannedStep(t *testing.T) {
	const none = math.MaxUint32
	step := func(in typedef.Intensity, d typedef.WktStepDuration, dv uint32, tt typedef.WktStepTarget, tv, lo, hi uint32) fitdata.WorkoutStep {
		return fitdata.WorkoutStep{Index: 2, Intensity: uint8(in), DurationType: uint8(d), DurationValue: dv,
			TargetType: uint8(tt), TargetValue: tv, CustomLow: lo, CustomHigh: hi}
	}
	open := typedef.WktStepTargetOpen
	for name, c := range map[string]struct {
		in   fitdata.WorkoutStep
		want plannedStepOut
	}{
		"time hr": {step(typedef.IntensityWarmup, typedef.WktStepDurationTime, 600000, typedef.WktStepTargetHeartRate, 0, 210, 238),
			plannedStepOut{Index: 2, Kind: "warmup", Duration: &plannedDurationOut{Type: "time", TimeS: ptr(600)},
				Target: &plannedTargetOut{Type: "hr", LowBpm: ptr(110), HighBpm: ptr(138)}}},
		"distance pace": {step(typedef.IntensityActive, typedef.WktStepDurationDistance, 100000, typedef.WktStepTargetSpeed, 0, 3030, 3333),
			plannedStepOut{Index: 2, Kind: "active", Duration: &plannedDurationOut{Type: "distance", DistanceM: ptr(1000)},
				Target: &plannedTargetOut{Type: "pace", LowPaceSPerKm: ptr(300), HighPaceSPerKm: ptr(330)}}},
		"hr above cadence": {step(typedef.IntensityInterval, typedef.WktStepDurationHrGreaterThan, 260, typedef.WktStepTargetCadence, 0, 170, 180),
			plannedStepOut{Index: 2, Kind: "active", Duration: &plannedDurationOut{Type: "hr_above", HRBpm: ptr(160)},
				Target: &plannedTargetOut{Type: "cadence", LowSpm: ptr(170), HighSpm: ptr(180)}}},
		"hr below power": {step(typedef.IntensityRecovery, typedef.WktStepDurationHrLessThan, 230, typedef.WktStepTargetPower, 0, 55, 75),
			plannedStepOut{Index: 2, Kind: "recovery", Duration: &plannedDurationOut{Type: "hr_below", HRBpm: ptr(130)},
				Target: &plannedTargetOut{Type: "power", LowPctFTP: ptr(55), HighPctFTP: ptr(75)}}},
		"open none": {step(typedef.IntensityCooldown, typedef.WktStepDurationOpen, none, open, none, none, none),
			plannedStepOut{Index: 2, Kind: "cooldown", Duration: &plannedDurationOut{Type: "open"}, Target: &plannedTargetOut{Type: "none"}}},
		"hr zone": {step(typedef.IntensityRest, typedef.WktStepDurationCalories, 300, typedef.WktStepTargetHeartRate, 2, none, none),
			plannedStepOut{Index: 2, Kind: "rest", Duration: &plannedDurationOut{Type: "other"}, Target: &plannedTargetOut{Type: "other"}}},
		"percent of max hr": {step(typedef.IntensityActive, typedef.WktStepDurationTime, 60000, typedef.WktStepTargetHeartRate, 0, 70, 80),
			plannedStepOut{Index: 2, Kind: "active", Duration: &plannedDurationOut{Type: "time", TimeS: ptr(60)}, Target: &plannedTargetOut{Type: "other"}}},
		"power in watts": {step(typedef.IntensityActive, typedef.WktStepDurationTime, 60000, typedef.WktStepTargetPower, 0, 1200, 1250),
			plannedStepOut{Index: 2, Kind: "active", Duration: &plannedDurationOut{Type: "time", TimeS: ptr(60)},
				Target: &plannedTargetOut{Type: "power", LowW: ptr(200), HighW: ptr(250)}}},
		"power from % to watts": {step(typedef.IntensityActive, typedef.WktStepDurationTime, 60000, typedef.WktStepTargetPower, 0, 900, 1250),
			plannedStepOut{Index: 2, Kind: "active", Duration: &plannedDurationOut{Type: "time", TimeS: ptr(60)}, Target: &plannedTargetOut{Type: "other"}}},
		"unknown intensity": {step(0xFF, typedef.WktStepDurationTime, 60000, open, none, none, none),
			plannedStepOut{Index: 2, Kind: "active", Duration: &plannedDurationOut{Type: "time", TimeS: ptr(60)}, Target: &plannedTargetOut{Type: "none"}}},
		"repeat": {step(0xFF, typedef.WktStepDurationRepeatUntilStepsCmplt, 1, typedef.WktStepTargetInvalid, 7, none, none),
			plannedStepOut{Index: 2, Kind: "repeat", RepeatFromIndex: ptr(1), Count: ptr(7)}},
		"repeat until time": {step(0xFF, typedef.WktStepDurationRepeatUntilTime, 0, open, 1200000, none, none),
			plannedStepOut{Index: 2, Kind: "repeat", RepeatFromIndex: ptr(0)}},
	} {
		if got := plannedStep(c.in); !reflect.DeepEqual(got, c.want) {
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(c.want)
			t.Errorf("%s:\n got %s\nwant %s", name, gj, wj)
		}
	}
}
