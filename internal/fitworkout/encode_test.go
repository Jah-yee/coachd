package fitworkout

import (
	"bytes"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/muktihari/fit/decoder"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/profile/untyped/mesgnum"
	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/workout"
)

var created = time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)

func ip(v int) *int { return &v }

func dur(typ string, v int) *workout.Duration {
	d := &workout.Duration{Type: typ}
	switch typ {
	case "time":
		d.TimeS = ip(v)
	case "distance":
		d.DistanceM = ip(v)
	case "hr_above", "hr_below":
		d.HRBpm = ip(v)
	}
	return d
}

func hrT(lo, hi int) *workout.Target {
	return &workout.Target{Type: "hr", LowBpm: ip(lo), HighBpm: ip(hi)}
}

func base(kind string, d *workout.Duration, t *workout.Target) workout.StepBase {
	return workout.StepBase{Kind: kind, Duration: d, Target: t}
}

func decode(t *testing.T, b []byte) (mesgdef.FileId, *mesgdef.Workout, []*mesgdef.WorkoutStep) {
	t.Helper()
	fit, err := decoder.New(bytes.NewReader(b)).Decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var fid mesgdef.FileId
	var wk *mesgdef.Workout
	var steps []*mesgdef.WorkoutStep
	for i := range fit.Messages {
		m := fit.Messages[i]
		switch m.Num {
		case mesgnum.FileId:
			fid = *mesgdef.NewFileId(&m)
		case mesgnum.Workout:
			wk = mesgdef.NewWorkout(&m)
		case mesgnum.WorkoutStep:
			steps = append(steps, mesgdef.NewWorkoutStep(&m))
		}
	}
	return fid, wk, steps
}

func encode(t *testing.T, w workout.Workout) (mesgdef.FileId, *mesgdef.Workout, []*mesgdef.WorkoutStep) {
	t.Helper()
	b, err := Encode(w, created)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return decode(t, b)
}

func TestEncodeIntervals(t *testing.T) {
	w := workout.Workout{Name: "Intervals", Sport: "running", Steps: []workout.Step{
		{StepBase: base("warmup", dur("time", 600), hrT(120, 140))},
		{StepBase: workout.StepBase{Kind: "repeat"}, Count: 6, Steps: []workout.Step2{
			{StepBase: base("active", dur("distance", 400), &workout.Target{Type: "pace", LowPaceSPerKm: ip(230), HighPaceSPerKm: ip(240)})},
			{StepBase: base("recovery", dur("distance", 200), &workout.Target{Type: "none"})},
		}},
		{StepBase: base("cooldown", &workout.Duration{Type: "open"}, nil)},
	}}
	fid, wk, st := encode(t, w)
	if fid.Type != typedef.FileWorkout || fid.Manufacturer != typedef.ManufacturerDevelopment || fid.Product != 0 || !fid.TimeCreated.Equal(created) {
		t.Fatalf("file id: %+v", fid)
	}
	if wk.Sport != typedef.SportRunning || wk.SubSport != typedef.SubSportGeneric || wk.NumValidSteps != 5 || wk.WktName != "Intervals" {
		t.Fatalf("workout: %+v", wk)
	}
	if len(st) != 5 {
		t.Fatalf("steps: %d", len(st))
	}
	for i, s := range st {
		if int(s.MessageIndex) != i {
			t.Errorf("step %d message_index %d", i, s.MessageIndex)
		}
	}
	s := st[0]
	if s.DurationType != typedef.WktStepDurationTime || s.DurationValue != 600000 || s.TargetType != typedef.WktStepTargetHeartRate ||
		s.TargetValue != 0 || s.CustomTargetValueLow != 220 || s.CustomTargetValueHigh != 240 || s.Intensity != typedef.IntensityWarmup {
		t.Errorf("step0: %+v", s)
	}
	s = st[1]
	if s.DurationType != typedef.WktStepDurationDistance || s.DurationValue != 40000 || s.TargetType != typedef.WktStepTargetSpeed ||
		s.CustomTargetValueLow != 4167 || s.CustomTargetValueHigh != 4348 || s.Intensity != typedef.IntensityActive {
		t.Errorf("step1: %+v", s)
	}
	s = st[2]
	if s.DurationValue != 20000 || s.TargetType != typedef.WktStepTargetOpen || s.Intensity != typedef.IntensityRecovery {
		t.Errorf("step2: %+v", s)
	}
	s = st[3]
	if s.DurationType != typedef.WktStepDurationRepeatUntilStepsCmplt || s.DurationValue != 1 || s.TargetValue != 6 {
		t.Errorf("step3: %+v", s)
	}
	s = st[4]
	if s.DurationType != typedef.WktStepDurationOpen || s.TargetType != typedef.WktStepTargetOpen || s.Intensity != typedef.IntensityCooldown {
		t.Errorf("step4: %+v", s)
	}
}

func TestEncodeNestedRepeatUnrolled(t *testing.T) {
	w := workout.Workout{Name: "n", Sport: "running", Steps: []workout.Step{
		{StepBase: workout.StepBase{Kind: "repeat"}, Count: 3, Steps: []workout.Step2{
			{StepBase: workout.StepBase{Kind: "repeat"}, Count: 2, Steps: []workout.Step3{
				{StepBase: base("active", dur("time", 60), nil)},
			}},
			{StepBase: base("recovery", dur("time", 30), nil)},
		}},
	}}
	_, wk, st := encode(t, w)
	if len(st) != 4 || wk.NumValidSteps != 4 {
		t.Fatalf("steps %d num %d", len(st), wk.NumValidSteps)
	}
	want := []typedef.Intensity{typedef.IntensityActive, typedef.IntensityActive, typedef.IntensityRecovery}
	for i, in := range want {
		if st[i].Intensity != in || st[i].DurationType != typedef.WktStepDurationTime {
			t.Errorf("step %d: %+v", i, st[i])
		}
	}
	r := st[3]
	if r.DurationType != typedef.WktStepDurationRepeatUntilStepsCmplt || r.DurationValue != 0 || r.TargetValue != 3 {
		t.Errorf("repeat: %+v", r)
	}
}

func TestEncodeHRDurations(t *testing.T) {
	w := workout.Workout{Name: "h", Sport: "running", Steps: []workout.Step{
		{StepBase: base("active", dur("hr_above", 160), nil)},
		{StepBase: base("recovery", dur("hr_below", 130), nil)},
	}}
	_, _, st := encode(t, w)
	if st[0].DurationType != typedef.WktStepDurationHrGreaterThan || st[0].DurationValue != 260 {
		t.Errorf("above: %+v", st[0])
	}
	if st[1].DurationType != typedef.WktStepDurationHrLessThan || st[1].DurationValue != 230 {
		t.Errorf("below: %+v", st[1])
	}
}

func TestEncodeCadenceAndEffort(t *testing.T) {
	w := workout.Workout{Name: "c", Sport: "running", Steps: []workout.Step{
		{StepBase: base("active", dur("time", 60), &workout.Target{Type: "cadence", LowSpm: ip(170), HighSpm: ip(180)})},
		{StepBase: workout.StepBase{Kind: "active", Duration: dur("time", 60), Target: &workout.Target{Type: "effort", Cue: "conversational"}, Notes: "flat route"}},
		{StepBase: workout.StepBase{Kind: "active", Duration: dur("time", 60), Notes: strings.Repeat("x", 80)}},
		{StepBase: base("active", dur("time", 60), nil)},
	}}
	_, _, st := encode(t, w)
	s := st[0]
	if s.TargetType != typedef.WktStepTargetCadence || s.TargetValue != 0 || s.CustomTargetValueLow != 170 || s.CustomTargetValueHigh != 180 {
		t.Errorf("cadence: %+v", s)
	}
	if st[1].TargetType != typedef.WktStepTargetOpen || st[1].Notes != "conversational · flat route" {
		t.Errorf("effort: %+v", st[1])
	}
	if len(st[2].Notes) != MaxNotesBytes {
		t.Errorf("truncated notes len %d", len(st[2].Notes))
	}
	if st[3].Notes != "" {
		t.Errorf("notes should be unset: %q", st[3].Notes)
	}
}

func TestEncodeTruncatesAtRuneBoundary(t *testing.T) {
	name := strings.Repeat("é", 20) // 40 bytes
	_, wk, _ := encode(t, workout.Workout{Name: name, Sport: "running", Steps: []workout.Step{
		{StepBase: base("active", dur("time", 60), nil)},
	}})
	if !utf8.ValidString(wk.WktName) || len(wk.WktName) > MaxNameBytes || len(wk.WktName) != 32 {
		t.Errorf("name %q len %d", wk.WktName, len(wk.WktName))
	}
	if got := WorkoutName(name); got != wk.WktName {
		t.Errorf("WorkoutName = %q, encoded wkt_name %q", got, wk.WktName)
	}
	if got := WorkoutName("Easy run"); got != "Easy run" {
		t.Errorf("WorkoutName of a short name = %q", got)
	}
	if got := truncate("aé", 2); got != "a" {
		t.Errorf("truncate: %q", got)
	}
}

func TestEncodeSportTable(t *testing.T) {
	cases := map[string][2]any{
		"running":       {typedef.SportRunning, typedef.SubSportGeneric},
		"trail_running": {typedef.SportRunning, typedef.SubSportTrail},
		"walking":       {typedef.SportWalking, typedef.SubSportGeneric},
		"hiking":        {typedef.SportHiking, typedef.SubSportGeneric},
		"cycling":       {typedef.SportCycling, typedef.SubSportGeneric},
		"strength":      {typedef.SportTraining, typedef.SubSportStrengthTraining},
		"other":         {typedef.SportGeneric, typedef.SubSportGeneric},
	}
	for sport, want := range cases {
		_, wk, _ := encode(t, workout.Workout{Name: "s", Sport: sport, Steps: []workout.Step{
			{StepBase: base("active", dur("time", 60), nil)},
		}})
		if wk.Sport != want[0] || wk.SubSport != want[1] {
			t.Errorf("%s: got %v/%v", sport, wk.Sport, wk.SubSport)
		}
	}
	if _, err := Encode(workout.Workout{Sport: "bogus"}, created); err == nil {
		t.Error("unknown sport should error")
	}
}

func TestEncodeDeterministic(t *testing.T) {
	w := workout.Workout{Name: "d", Sport: "running", Steps: []workout.Step{
		{StepBase: base("warmup", dur("time", 600), hrT(120, 140))},
	}}
	a, err1 := Encode(w, created)
	b, err2 := Encode(w, created)
	if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
		t.Fatal("not deterministic")
	}
}

func TestFileName(t *testing.T) {
	d, _ := civil.Parse("2026-10-28")
	if got := FileName(d, "0123456789abcdef0123"); got != "coachd-2026-10-28-01234567.fit" {
		t.Errorf("got %s", got)
	}
	if got := FileName(d, "abc"); got != "coachd-2026-10-28-abc.fit" {
		t.Errorf("short id: %s", got)
	}
}
