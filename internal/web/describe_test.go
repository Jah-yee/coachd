package web

import (
	"slices"
	"testing"

	"github.com/stefiix92/coachd/internal/workout"
)

func ip(v int) *int { return &v }

func TestDescribe(t *testing.T) {
	timeD := func(s int) *workout.Duration { return &workout.Duration{Type: "time", TimeS: ip(s)} }
	distD := func(m int) *workout.Duration { return &workout.Duration{Type: "distance", DistanceM: ip(m)} }
	step := func(kind string, d *workout.Duration, tg *workout.Target) workout.StepBase {
		return workout.StepBase{Kind: kind, Duration: d, Target: tg}
	}
	w := workout.Workout{Name: "Intervals", Sport: "running", Date: "2026-10-28", Steps: []workout.Step{
		{StepBase: step("warmup", timeD(600), &workout.Target{Type: "hr", LowBpm: ip(120), HighBpm: ip(140)})},
		{StepBase: workout.StepBase{Kind: "repeat"}, Count: 6, Steps: []workout.Step2{
			{StepBase: step("active", distD(400), &workout.Target{Type: "pace", LowPaceSPerKm: ip(230), HighPaceSPerKm: ip(240)})},
			{StepBase: workout.StepBase{Kind: "recovery", Duration: &workout.Duration{Type: "open"}, Notes: "walk if needed"}},
			{StepBase: workout.StepBase{Kind: "repeat"}, Count: 2, Steps: []workout.Step3{
				{StepBase: step("active", timeD(30), &workout.Target{Type: "none"})},
				{StepBase: step("rest", timeD(59), nil)},
			}},
		}},
		{StepBase: step("active", &workout.Duration{Type: "hr_above", HRBpm: ip(160)}, nil)},
		{StepBase: step("active", &workout.Duration{Type: "hr_below", HRBpm: ip(130)}, nil)},
		{StepBase: step("active", distD(2000), &workout.Target{Type: "cadence", LowSpm: ip(170), HighSpm: ip(180)})},
		{StepBase: step("active", distD(1500), nil)},
		{StepBase: step("active", distD(1234), nil)},
		{StepBase: step("active", distD(1000), nil)},
		{StepBase: step("active", distD(999), nil)},
		{StepBase: step("active", timeD(300), &workout.Target{Type: "effort", Cue: "<conversational>"})},
		{StepBase: step("active", timeD(3600), nil)},
		{StepBase: step("active", timeD(5025), nil)},
		{StepBase: workout.StepBase{Kind: "cooldown", Duration: timeD(600), Notes: "easy"}},
	}}
	want := []Line{
		{0, "Warm-up · 10:00 · HR 120–140 bpm"},
		{0, "Repeat 6×"},
		{1, "Active · 400 m · pace 3:50–4:00 /km"},
		{1, "Recovery · open (lap button)"},
		{2, "note: walk if needed"},
		{1, "Repeat 2×"},
		{2, "Active · 0:30"},
		{2, "Rest · 0:59"},
		{0, "Active · until HR above 160 bpm"},
		{0, "Active · until HR below 130 bpm"},
		{0, "Active · 2 km · cadence 170–180 spm"},
		{0, "Active · 1.5 km"},
		{0, "Active · 1.23 km"},
		{0, "Active · 1 km"},
		{0, "Active · 999 m"},
		{0, "Active · 5:00 · effort: <conversational>"},
		{0, "Active · 1:00:00"},
		{0, "Active · 1:23:45"},
		{0, "Cool-down · 10:00"},
		{1, "note: easy"},
	}
	got := Describe(w)
	if !slices.Equal(got, want) {
		t.Fatalf("Describe:\n got %+v\nwant %+v", got, want)
	}
	if got := Describe(workout.Workout{}); len(got) != 0 {
		t.Fatalf("Describe(empty) = %+v", got)
	}
}
