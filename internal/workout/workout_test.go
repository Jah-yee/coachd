package workout

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/store"
)

var today = civil.Date{Year: 2026, Month: 10, Day: 23}

func ip(i int) *int { return &i }

func sb(kind string, d *Duration, t *Target) Step {
	return Step{StepBase: StepBase{Kind: kind, Duration: d, Target: t}}
}

func timeStep(kind string, s int) Step { return sb(kind, &Duration{Type: "time", TimeS: ip(s)}, nil) }

func s2(s Step) Step2 { return Step2{StepBase: s.StepBase} }
func s3(s Step) Step3 { return Step3{StepBase: s.StepBase} }

func rep(n int, c ...Step) Step {
	r := Step{StepBase: StepBase{Kind: "repeat"}, Count: n}
	for _, x := range c {
		r.Steps = append(r.Steps, s2(x))
	}
	return r
}

func distStep(kind string, m int, lo, hi int) Step {
	st := sb(kind, &Duration{Type: "distance", DistanceM: ip(m)}, nil)
	if lo != 0 {
		st.Target = &Target{Type: "pace", LowPaceSPerKm: ip(lo), HighPaceSPerKm: ip(hi)}
	}
	return st
}

func base(steps ...Step) Workout {
	return Workout{Name: "Easy", Sport: "running", Date: today.String(), Steps: steps}
}

func reason(t *testing.T, w Workout) string {
	t.Helper()
	_, err := Validate(w, today, DefaultLimits)
	if err == nil {
		t.Fatalf("expected error")
	}
	var inv *Invalid
	if !errors.As(err, &inv) {
		t.Fatalf("not *Invalid: %T", err)
	}
	return inv.Reason
}

func ok(t *testing.T, w Workout) Plan {
	t.Helper()
	p, err := Validate(w, today, DefaultLimits)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	return p
}

func TestValidateHappyPath(t *testing.T) {
	w := base(
		timeStep("warmup", 600),
		rep(6, distStep("active", 400, 240, 260), timeStep("recovery", 90)),
		timeStep("cooldown", 300),
	)
	p := ok(t, w)
	if p.ExpandedSteps != 14 || p.EstimatedS != 600+6*(104+90)+300 || p.DistanceM != 2400 {
		t.Fatalf("plan %+v", p)
	}
	if p.Sport != store.SportRunning || p.Date != today || p.MaxTargetHR != nil || p.EstimatedFromDefaultPace {
		t.Fatalf("plan %+v", p)
	}
}

func TestValidateDistanceDefaultPaceAndMaxHR(t *testing.T) {
	hr := timeStep("active", 600)
	hr.Target = &Target{Type: "hr", LowBpm: ip(120), HighBpm: ip(150)}
	hd := sb("active", &Duration{Type: "hr_above", HRBpm: ip(170)}, nil)
	p := ok(t, base(distStep("active", 1000, 0, 0), hr, hd))
	if p.EstimatedS != 360+600 || p.MaxTargetHR == nil || *p.MaxTargetHR != 170 || !p.EstimatedFromDefaultPace {
		t.Fatalf("plan %+v", p)
	}
	// A distance step inside a repeat without a pace target uses the default pace too.
	p = ok(t, base(distStep("warmup", 1000, 300, 330), rep(3, distStep("active", 400, 0, 0))))
	if p.EstimatedS != 330+3*144 || !p.EstimatedFromDefaultPace {
		t.Fatalf("plan %+v", p)
	}
	// Time steps and paced distance steps are not estimated from the default pace.
	if p := ok(t, base(timeStep("warmup", 600), distStep("active", 1000, 300, 330))); p.EstimatedFromDefaultPace {
		t.Fatalf("plan %+v", p)
	}
}

func TestValidateExpandedStepCap(t *testing.T) {
	a, b := timeStep("active", 60), timeStep("recovery", 60)
	ok(t, base(rep(50, a, b))) // exactly 100
	w := base(rep(50, a, b), timeStep("cooldown", 60))
	if r := reason(t, w); !strings.Contains(r, "steps[1]") || !strings.Contains(r, "more than 100") {
		t.Fatal(r)
	}
}

func TestValidatePaceBounds(t *testing.T) {
	for _, tc := range []struct {
		lo, hi int
		want   string
	}{
		{120, 200, "steps[0].target"},
		{200, 1600, "steps[0].target"},
		{300, 250, "low"},
	} {
		st := distStep("active", 1000, tc.lo, tc.hi)
		r := reason(t, base(st))
		if !strings.Contains(r, tc.want) {
			t.Fatalf("%+v: %s", tc, r)
		}
	}
	if r := reason(t, base(distStep("active", 1000, 120, 200))); !strings.Contains(r, "120") || !strings.Contains(r, "150") {
		t.Fatal(r)
	}
	st := distStep("active", 1000, 240, 0)
	st.Target.HighPaceSPerKm = nil
	if r := reason(t, base(st)); !strings.Contains(r, "high_pace_s_per_km") {
		t.Fatal(r)
	}
	ok(t, base(distStep("active", 1000, 150, 1500)))
}

func inSport(sport string, steps ...Step) Workout {
	w := base(steps...)
	w.Sport = sport
	return w
}

// TestValidateSportPace: pace bounds and the pace that estimates a distance
// step without a pace target depend on the sport.
func TestValidateSportPace(t *testing.T) {
	for _, tc := range []struct {
		sport              string
		min, max, estimate int
	}{
		{"running", 150, 1500, 360}, {"trail_running", 150, 1500, 360}, {"other", 150, 1500, 360},
		{"walking", 300, 2400, 720}, {"hiking", 300, 2400, 720},
		{"cycling", 60, 1500, 144},
	} {
		ok(t, inSport(tc.sport, distStep("active", 1000, tc.min, tc.max))) // bounds are inclusive
		has(t, inSport(tc.sport, distStep("active", 1000, tc.min-1, tc.max)),
			fmt.Sprintf("steps[0].target: low_pace_s_per_km %d s/km below %d", tc.min-1, tc.min))
		has(t, inSport(tc.sport, distStep("active", 1000, tc.min, tc.max+1)),
			fmt.Sprintf("steps[0].target: high_pace_s_per_km %d s/km above %d", tc.max+1, tc.max))
		if p := ok(t, inSport(tc.sport, distStep("active", 10000, 0, 0))); p.EstimatedS != 10*tc.estimate || p.DistanceM != 10000 {
			t.Fatalf("%s: 10 km without a pace target: %+v", tc.sport, p)
		}
	}
	// A 120 km ride is estimated at 144 s/km (4 h 48 min), inside the 10 h maximum.
	if p := ok(t, inSport("cycling", distStep("active", 100000, 0, 0), distStep("active", 20000, 0, 0))); p.EstimatedS != 17280 {
		t.Fatalf("120 km ride: %+v", p)
	}
	ok(t, inSport("cycling", distStep("active", 40000, 100, 120))) // 30 to 36 km/h
	has(t, inSport("running", distStep("active", 40000, 100, 120)), "low_pace_s_per_km 100 s/km below 150")
}

// TestValidateStrength: strength takes no distance durations and no pace targets.
func TestValidateStrength(t *testing.T) {
	hr := timeStep("active", 600)
	hr.Target = &Target{Type: "hr", LowBpm: ip(100), HighBpm: ip(140)}
	ok(t, inSport("strength", hr, rep(3, timeStep("active", 45), timeStep("rest", 60)), sb("cooldown", &Duration{Type: "open"}, nil)))
	has(t, inSport("strength", timeStep("warmup", 600), distStep("active", 1000, 0, 0)),
		"steps[1].duration: distance durations are not supported for strength")
	pace := timeStep("active", 600)
	pace.Target = &Target{Type: "pace", LowPaceSPerKm: ip(300), HighPaceSPerKm: ip(330)}
	has(t, inSport("strength", pace), "steps[0].target: pace targets are not supported for strength")
}

func TestValidateHRBounds(t *testing.T) {
	st := timeStep("active", 600)
	st.Target = &Target{Type: "hr", LowBpm: ip(30), HighBpm: ip(150)}
	if r := reason(t, base(st)); !strings.Contains(r, "steps[0].target") {
		t.Fatal(r)
	}
	st.Target = &Target{Type: "hr", LowBpm: ip(150), HighBpm: ip(240)}
	has(t, base(st), "high_bpm 240 bpm above 230")
	st.Target = &Target{Type: "hr", LowBpm: ip(160), HighBpm: ip(150)}
	has(t, base(st), "low must not exceed high")
	st.Target = &Target{Type: "hr", LowBpm: ip(100), HighBpm: ip(150), LowPaceSPerKm: ip(240)}
	has(t, base(st), "low_pace_s_per_km set for type=hr")
	d := sb("active", &Duration{Type: "hr_below", HRBpm: ip(300)}, nil)
	if r := reason(t, base(timeStep("warmup", 600), d)); !strings.Contains(r, "steps[1].duration") {
		t.Fatal(r)
	}
	ok(t, base(timeStep("warmup", 600), sb("active", &Duration{Type: "hr_below", HRBpm: ip(140)}, nil)))
	c := timeStep("active", 600)
	c.Target = &Target{Type: "cadence", LowSpm: ip(20), HighSpm: ip(180)}
	has(t, base(c), "low_spm 20 spm below 30")
	c.Target = &Target{Type: "cadence", LowSpm: ip(160), HighSpm: ip(180)}
	ok(t, base(c))
}

func TestValidateDateWindow(t *testing.T) {
	for _, tc := range []struct {
		off int
		ok  bool
	}{{-1, false}, {0, true}, {14, true}, {15, false}} {
		w := base(timeStep("active", 600))
		w.Date = today.AddDays(tc.off).String()
		_, err := Validate(w, today, DefaultLimits)
		if (err == nil) != tc.ok {
			t.Fatalf("off %d: %v", tc.off, err)
		}
	}
	w := base(timeStep("active", 600))
	w.Date = "nope"
	has(t, w, "date")
}

func TestValidateTotalDuration(t *testing.T) {
	has(t, base(timeStep("active", 30)),
		"steps: estimated total 30 s is below the 60 s minimum (open and heart-rate-bounded steps count as 0 s)")
	has(t, base(timeStep("active", 36000), timeStep("rest", 100)),
		"steps: estimated total 36100 s is above the 36000 s maximum (open and heart-rate-bounded steps count as 0 s)")
	ok(t, base(timeStep("active", 36000)))
	ok(t, base(timeStep("active", 60)))
	has(t, base(timeStep("active", 36001)), "steps[0].duration")
	has(t, base(distStep("active", 100001, 0, 0)), "distance_m")
}

// TestValidateUnknownDurations: open and heart-rate-bounded steps count as
// 0 s, so the minimum total applies only when every step has a time or a
// distance; the maximum always applies to the known part.
func TestValidateUnknownDurations(t *testing.T) {
	free := sb("active", &Duration{Type: "open"}, &Target{Type: "effort", Cue: "easy"})
	if p := ok(t, base(free)); p.EstimatedS != 0 || p.ExpandedSteps != 1 || p.DistanceM != 0 || p.MaxTargetHR != nil {
		t.Fatalf("free run: %+v", p)
	}
	untilHR := sb("active", &Duration{Type: "hr_above", HRBpm: ip(160)}, nil)
	if p := ok(t, base(untilHR)); p.EstimatedS != 0 || p.ExpandedSteps != 1 || p.MaxTargetHR == nil || *p.MaxTargetHR != 160 {
		t.Fatalf("run until HR > 160: %+v", p)
	}
	hrReps := rep(6, sb("active", &Duration{Type: "hr_above", HRBpm: ip(165)}, nil),
		sb("recovery", &Duration{Type: "hr_below", HRBpm: ip(130)}, nil))
	if p := ok(t, base(hrReps)); p.EstimatedS != 0 || p.ExpandedSteps != 12 || p.MaxTargetHR == nil || *p.MaxTargetHR != 165 {
		t.Fatalf("6×(hr_above 165, hr_below 130): %+v", p)
	}
	// A known part below the minimum is fine next to an open step; EstimatedS is the known part.
	if p := ok(t, base(timeStep("warmup", 30), free)); p.EstimatedS != 30 || p.ExpandedSteps != 2 {
		t.Fatalf("30 s plus open: %+v", p)
	}
	// Every step known and 30 s in total: still rejected.
	has(t, base(timeStep("warmup", 10), rep(2, timeStep("active", 10))), "steps: estimated total 30 s is below the 60 s minimum")
	// The maximum applies to the known part even next to open steps.
	has(t, base(timeStep("active", 36000), timeStep("rest", 100), free),
		"steps: estimated total 36100 s is above the 36000 s maximum (open and heart-rate-bounded steps count as 0 s)")
}

func has(t *testing.T, w Workout, sub string) {
	t.Helper()
	if r := reason(t, w); !strings.Contains(r, sub) {
		t.Fatalf("reason %q lacks %q", r, sub)
	}
}

func TestValidateRepeatShape(t *testing.T) {
	a := timeStep("active", 120)
	has(t, base(rep(1, a)), "steps[0].count")
	has(t, base(rep(51, a)), "steps[0].count")
	has(t, base(rep(3)), "steps[0].steps")
	d := rep(3, a)
	d.Duration = &Duration{Type: "open"}
	has(t, base(d), "steps[0]")
	x := a
	x.Count = 2
	has(t, base(x), "kind=repeat only")
	x = a
	x.Steps = []Step2{s2(a)}
	has(t, base(x), "kind=repeat only")
	has(t, base(Step{StepBase: StepBase{Kind: "active"}}), "steps[0].duration")
	has(t, base(), "at least one step")
	has(t, base(sb("bogus", a.Duration, nil)), "steps[0].kind")
	has(t, base(sb("active", &Duration{Type: "bogus"}, nil)), "steps[0].duration.type")
	b := a
	b.Duration = &Duration{Type: "time", TimeS: ip(60), DistanceM: ip(5)}
	has(t, base(b), "distance_m")
	// nesting: two levels allowed, a third is not
	two := Step{StepBase: StepBase{Kind: "repeat"}, Count: 2, Steps: []Step2{{
		StepBase: StepBase{Kind: "repeat"}, Count: 2, Steps: []Step3{s3(a)}}}}
	p := ok(t, base(two))
	if p.ExpandedSteps != 4 || p.EstimatedS != 480 {
		t.Fatalf("%+v", p)
	}
	three := two
	three.Steps = []Step2{{StepBase: StepBase{Kind: "repeat"}, Count: 2,
		Steps: []Step3{{StepBase{Kind: "repeat"}}}}}
	has(t, base(three), "steps[0].steps[0].steps[0]: repeats nest at most 2 levels")
}

func TestFirstViolationWins(t *testing.T) {
	bad1 := timeStep("active", 600)
	bad1.Notes = strings.Repeat("a", 501)
	bad2 := sb("active", &Duration{Type: "bogus"}, nil)
	has(t, base(timeStep("warmup", 600), bad1, bad2), "steps[1].notes")
	r := rep(2, timeStep("active", 60), bad2)
	r.Steps[0].Notes = "a\x00"
	has(t, base(r), "steps[0].steps[0].notes")
}

func TestReasonsClipEchoedInput(t *testing.T) {
	long := strings.Repeat("x", 5000)
	w := base(timeStep("active", 600))
	w.Sport = long
	if r := reason(t, w); len(r) > 400 || !strings.Contains(r, "…") {
		t.Fatalf("len %d", len(r))
	}
	w = base(sb(long, nil, nil))
	if r := reason(t, w); len(r) > 400 {
		t.Fatal("kind not clipped")
	}
	w = base(timeStep("active", 600))
	w.Date = long
	if r := reason(t, w); len(r) > 400 {
		t.Fatal("date not clipped")
	}
	w = base(sb("active", &Duration{Type: long}, nil))
	if r := reason(t, w); len(r) > 400 {
		t.Fatal("duration type not clipped")
	}
	w = base(sb("active", &Duration{Type: "time", TimeS: ip(600)}, &Target{Type: long}))
	if r := reason(t, w); len(r) > 400 {
		t.Fatal("target type not clipped")
	}
}

func TestInvalidUTF8Rejected(t *testing.T) {
	w := base(timeStep("active", 600))
	w.Notes = "a\xffb"
	has(t, w, "notes: is not valid UTF-8")
	w = base(timeStep("active", 600))
	w.Name = "\xff"
	has(t, w, "name")
	s := timeStep("active", 600)
	s.Notes = "\xc3"
	has(t, base(s), "steps[0].notes")
}

func TestWorkoutSchemaInfers(t *testing.T) {
	sch, err := jsonschema.For[Workout](&jsonschema.ForOptions{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := sch.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"name":"x","sport":"running","date":"2026-10-24","steps":[
	  {"kind":"warmup","duration":{"type":"time","time_s":600}},
	  {"kind":"repeat","count":3,"steps":[{"kind":"active","duration":{"type":"distance","distance_m":400},
	    "target":{"type":"pace","low_pace_s_per_km":240,"high_pace_s_per_km":260}}]}]}`
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	if err := res.Validate(v); err != nil {
		t.Fatalf("valid sample rejected: %v", err)
	}
	v["bogus"] = 1
	if err := res.Validate(v); err == nil {
		t.Fatal("unknown property accepted")
	}
}

func TestValidateTargetCue(t *testing.T) {
	a := timeStep("active", 600)
	a.Target = &Target{Type: "effort"}
	has(t, base(a), "steps[0].target.cue")
	a.Target = &Target{Type: "effort", Cue: "conversational"}
	ok(t, base(a))
	a.Target = &Target{Type: "none", Cue: "x"}
	has(t, base(a), "cue set for type=none")
	a.Target = &Target{Type: "none"}
	ok(t, base(a))
	a.Target = &Target{Type: "none", LowBpm: ip(100)}
	has(t, base(a), "low_bpm set for type=none")
	a.Target = &Target{Type: "bogus"}
	has(t, base(a), "steps[0].target.type")
	a.Target = &Target{Type: "bogus", Cue: "x"} // type problem reported before cue
	has(t, base(a), "target.type")
}

func TestValidateTextLimits(t *testing.T) {
	a := timeStep("active", 600)
	w := base(a)
	w.Name = strings.Repeat("é", 100)
	ok(t, w)
	w.Name = strings.Repeat("é", 101)
	if r := reason(t, w); !strings.Contains(r, "name") {
		t.Fatal(r)
	}
	w.Name = ""
	has(t, w, "name: required")
	for _, blank := range []string{" ", " \t\n ", "\u00a0\u3000"} {
		w.Name = blank
		has(t, w, "name: is empty (whitespace only)")
	}
	w.Name = " Easy "
	ok(t, w)
	w = base(a)
	w.Notes = strings.Repeat("a", 1001)
	has(t, w, "notes")
	w.Notes = strings.Repeat("a", 1000)
	ok(t, w)
	w.Notes = "line1\n\tline2"
	ok(t, w)
	for _, bad := range []string{"a\x00b", "a\rb", "a\x1bb", "a\u0085b"} {
		w.Notes = bad
		has(t, w, "control character")
	}
	w = base(a)
	w.Name = "bad\x07"
	has(t, w, "name")
	s := a
	s.Notes = strings.Repeat("a", 501)
	if r := reason(t, base(s)); !strings.Contains(r, "steps[0].notes") {
		t.Fatal(r)
	}
	s.Notes = "a\x00"
	has(t, base(s), "steps[0].notes")
	s = a
	s.Target = &Target{Type: "effort", Cue: strings.Repeat("a", 101)}
	has(t, base(s), "steps[0].target.cue")
	w = base(a)
	w.Sport = "swimming"
	has(t, w, "sport")
}

func TestCanonicalJSONStable(t *testing.T) {
	a := Workout{Name: "x", Sport: "running", Date: "2026-10-24"}
	b := Workout{Name: "x", Sport: "running", Date: "2026-10-24", Steps: []Step{}}
	ca, err := CanonicalJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, _ := CanonicalJSON(b)
	if string(ca) != string(cb) || ContentHash(ca) != ContentHash(cb) {
		t.Fatalf("%s vs %s", ca, cb)
	}
	if len(ContentHash(ca)) != 64 {
		t.Fatal("hash len")
	}
	rep1 := Step{StepBase: StepBase{Kind: "repeat"}, Count: 2, Steps: nil}
	rep2 := Step{StepBase: StepBase{Kind: "repeat"}, Count: 2, Steps: []Step2{}}
	c1, _ := CanonicalJSON(Workout{Steps: []Step{rep1}})
	c2, _ := CanonicalJSON(Workout{Steps: []Step{rep2}})
	if string(c1) != string(c2) {
		t.Fatalf("%s vs %s", c1, c2)
	}
	// differing content differs
	b.Name = "y"
	cc, _ := CanonicalJSON(b)
	if ContentHash(cc) == ContentHash(ca) {
		t.Fatal("collision")
	}
}

func TestInvalidReasonNamesStepPath(t *testing.T) {
	inner := distStep("active", 400, 120, 200)
	w := base(timeStep("warmup", 600), timeStep("rest", 60),
		rep(3, inner))
	r := reason(t, w)
	if !strings.HasPrefix(r, "steps[2].steps[0].target") {
		t.Fatal(r)
	}
	var e error = &Invalid{Reason: "r"}
	if e.Error() != "r" {
		t.Fatal()
	}
}

func TestCheckLine(t *testing.T) {
	for _, s := range []string{"", "Fenix 7S", strings.Repeat("é", 40)} {
		if err := CheckLine("label", s, 40); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for s, want := range map[string]string{
		"a\tb":                  "label: must be a single line (no tab or newline)",
		"a\nb":                  "label: must be a single line (no tab or newline)",
		"a\x00b":                "label: contains a control character (U+0000); only newline and tab are allowed",
		strings.Repeat("x", 41): "label: 41 characters exceeds the 40 limit",
	} {
		var inv *Invalid
		if err := CheckLine("label", s, 40); !errors.As(err, &inv) || inv.Reason != want {
			t.Errorf("%q: %v, want %q", s, err, want)
		}
	}
}

func powerStep(lo, hi *int) Step {
	return sb("active", &Duration{Type: "time", TimeS: ip(600)}, &Target{Type: "power", LowPctFTP: lo, HighPctFTP: hi})
}

func cycling(steps ...Step) Workout {
	w := base(steps...)
	w.Sport = "cycling"
	return w
}

func TestPowerTarget(t *testing.T) {
	ok(t, cycling(powerStep(ip(88), ip(92))))
	ok(t, cycling(powerStep(ip(20), ip(300))))
	for _, c := range []struct {
		w    Workout
		want string
	}{
		{base(powerStep(ip(88), ip(92))), "steps[0].target: power targets are only supported for cycling"},
		{cycling(powerStep(ip(10), ip(92))), "steps[0].target: low_pct_ftp 10 % FTP below 20"},
		{cycling(powerStep(ip(88), ip(301))), "steps[0].target: high_pct_ftp 301 % FTP above 300"},
		{cycling(powerStep(ip(95), ip(90))), "steps[0].target: low_pct_ftp 95 is above high_pct_ftp 90; low must not exceed high"},
		{cycling(powerStep(ip(88), nil)), "steps[0].target: high_pct_ftp is required"},
		{cycling(sb("active", &Duration{Type: "time", TimeS: ip(600)}, &Target{Type: "hr", LowBpm: ip(120), HighBpm: ip(140), LowPctFTP: ip(50)})),
			"steps[0].target: low_pct_ftp set for type=hr"},
		{cycling(sb("active", &Duration{Type: "time", TimeS: ip(600)}, &Target{Type: "watts"})),
			`steps[0].target.type: "watts" is not one of none|hr|pace|cadence|effort|power`},
	} {
		if got := reason(t, c.w); got != c.want {
			t.Errorf("reason = %q, want %q", got, c.want)
		}
	}
}

// Existing proposals hash their canonical JSON; the power fields must not
// change the bytes of a workout that does not use them.
func TestCanonicalJSONUnchangedWithoutPower(t *testing.T) {
	w := cycling(sb("active", &Duration{Type: "time", TimeS: ip(600)}, &Target{Type: "hr", LowBpm: ip(120), HighBpm: ip(140)}))
	b, err := CanonicalJSON(w)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"name":"Easy","sport":"cycling","date":"2026-10-23","steps":[{"kind":"active","duration":{"type":"time","time_s":600},"target":{"type":"hr","low_bpm":120,"high_bpm":140}}]}`
	if string(b) != want {
		t.Fatalf("canonical JSON\n got %s\nwant %s", b, want)
	}
}
