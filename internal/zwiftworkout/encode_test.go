package zwiftworkout

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/workout"
)

// The goldens in testdata pin the exact bytes Zwift gets. Regenerate only for
// an intended format change, and read every file before committing:
//
//	go test ./internal/zwiftworkout -update
var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func ip(v int) *int { return &v }

func secs(s int) *workout.Duration { return &workout.Duration{Type: "time", TimeS: ip(s)} }

func step(kind string, d *workout.Duration, t *workout.Target, notes string) workout.StepBase {
	return workout.StepBase{Kind: kind, Duration: d, Target: t, Notes: notes}
}

func pw(lo, hi int) *workout.Target {
	return &workout.Target{Type: "power", LowPctFTP: ip(lo), HighPctFTP: ip(hi)}
}

func leaf(b workout.StepBase) workout.Step { return workout.Step{StepBase: b} }

func rep(n int, notes string, c ...workout.Step2) workout.Step {
	return workout.Step{StepBase: workout.StepBase{Kind: "repeat", Notes: notes}, Count: n, Steps: c}
}

func child(b workout.StepBase) workout.Step2 { return workout.Step2{StepBase: b} }

func ride(name, notes string, steps ...workout.Step) workout.Workout {
	return workout.Workout{Name: name, Sport: "cycling", Date: "2026-10-28", Notes: notes, Steps: steps}
}

func encode(t *testing.T, w workout.Workout) string {
	t.Helper()
	b, err := Encode(w)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return string(b)
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs\n got:\n%s\nwant:\n%s", name, got, want)
	}
}

func mustContain(t *testing.T, doc string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(doc, p) {
			t.Errorf("document lacks %q:\n%s", p, doc)
		}
	}
}

func TestEncodeSteadyState(t *testing.T) {
	doc := encode(t, ride("Sweet spot", "Steady",
		leaf(step("warmup", secs(600), pw(50, 60), "")),
		leaf(step("active", secs(300), pw(88, 92), "")),
		leaf(step("active", secs(300), pw(88, 91), "")),
	))
	mustContain(t, doc,
		`<author>coachd</author>`, `<name>Sweet spot</name>`, `<description>Steady</description>`, `<sportType>bike</sportType>`,
		`<SteadyState Duration="600" Power="0.55">`, `<SteadyState Duration="300" Power="0.9">`, `<SteadyState Duration="300" Power="0.895">`)
	if strings.HasPrefix(doc, "<?xml") || !strings.HasPrefix(doc, "<workout_file>") || !strings.HasSuffix(doc, "</workout_file>\n") {
		t.Errorf("document framing:\n%s", doc)
	}
	golden(t, "steady.zwo", doc)
}

func TestEncodeFreeRideText(t *testing.T) {
	doc := encode(t, ride("Text", "",
		leaf(step("warmup", secs(600), &workout.Target{Type: "hr", LowBpm: ip(120), HighBpm: ip(135)}, "spin easy")),
		leaf(step("active", secs(300), &workout.Target{Type: "pace", LowPaceSPerKm: ip(120), HighPaceSPerKm: ip(150)}, "")),
		leaf(step("active", secs(300), &workout.Target{Type: "cadence", LowSpm: ip(85), HighSpm: ip(95)}, "")),
		leaf(step("active", secs(300), &workout.Target{Type: "effort", Cue: "Zóna 2 – ľahko"}, "")),
		leaf(step("recovery", secs(120), &workout.Target{Type: "none"}, "relax")),
		leaf(step("rest", secs(60), nil, "")),
		leaf(step("active", secs(60), pw(100, 110), "hold it")),
	))
	mustContain(t, doc,
		`<FreeRide Duration="600">`, `message="HR 120-135 bpm - spin easy"`,
		`message="pace 2:00-2:30 /km"`, `message="cadence 85-95 rpm"`, `message="Zona 2 lahko"`,
		`<FreeRide Duration="120">`, `message="relax"`,
		`<SteadyState Duration="60" Power="1.05">`, `message="hold it"`)
	if n := strings.Count(doc, "<textevent "); n != 6 {
		t.Errorf("text events = %d, want 6 (none for the bare rest step)", n)
	}
	if n := strings.Count(doc, `timeoffset="0"`); n != 6 {
		t.Errorf("timeoffset=0 count = %d", n)
	}
	if strings.Contains(doc, "Cadence=") {
		t.Error("FreeRide carries a Cadence attribute")
	}
	golden(t, "freeride.zwo", doc)
}

func TestEncodeIntervalsT(t *testing.T) {
	doc := encode(t, ride("Threshold", "",
		leaf(step("warmup", secs(600), pw(50, 60), "")),
		rep(3, "", child(step("active", secs(600), pw(88, 92), "")), child(step("recovery", secs(300), pw(50, 60), ""))),
	))
	mustContain(t, doc, `<IntervalsT Repeat="3" OnDuration="600" OffDuration="300" OnPower="0.9" OffPower="0.55">`)
	if n := strings.Count(doc, "<SteadyState "); n != 1 {
		t.Errorf("SteadyState count = %d, want 1 (the warm-up)", n)
	}
	golden(t, "intervals.zwo", doc)
}

// Only a two-step power repeat with no notes anywhere becomes IntervalsT;
// every other repeat is written out step by step.
func TestEncodeUnrolledRepeat(t *testing.T) {
	on, off := step("active", secs(60), pw(110, 120), ""), step("recovery", secs(60), pw(50, 55), "")
	noted := on
	noted.Notes = "seated"
	hr := step("recovery", secs(60), &workout.Target{Type: "hr", LowBpm: ip(100), HighBpm: ip(120)}, "")
	for name, c := range map[string]struct {
		w        workout.Workout
		elements int
	}{
		"child notes":  {ride("r", "", rep(2, "", child(noted), child(off))), 4},
		"repeat notes": {ride("r", "", rep(2, "big gear", child(on), child(off))), 4},
		"three steps":  {ride("r", "", rep(2, "", child(on), child(off), child(on))), 6},
		"one step":     {ride("r", "", rep(3, "", child(on))), 3},
		"non-power":    {ride("r", "", rep(2, "", child(on), child(hr))), 4},
	} {
		doc := encode(t, c.w)
		if strings.Contains(doc, "IntervalsT") {
			t.Errorf("%s: became IntervalsT:\n%s", name, doc)
		}
		if n := strings.Count(doc, "<SteadyState ") + strings.Count(doc, "<FreeRide "); n != c.elements {
			t.Errorf("%s: %d elements, want %d", name, n, c.elements)
		}
	}
}

func TestEncodeNestedRepeat(t *testing.T) {
	inner := workout.Step2{StepBase: workout.StepBase{Kind: "repeat"}, Count: 3, Steps: []workout.Step3{
		{StepBase: step("active", secs(30), pw(120, 130), "")},
		{StepBase: step("rest", secs(30), nil, "")},
	}}
	doc := encode(t, ride("Nested", "", rep(2, "", child(step("active", secs(60), pw(95, 105), "")), inner)))
	if strings.Contains(doc, "IntervalsT") {
		t.Errorf("nested repeat became IntervalsT")
	}
	mustContain(t, doc, `<SteadyState Duration="60" Power="1.0">`) // a whole FTP keeps its decimal point
	if s, f := strings.Count(doc, "<SteadyState "), strings.Count(doc, "<FreeRide "); s != 8 || f != 6 {
		t.Errorf("SteadyState %d, FreeRide %d; want 8 and 6", s, f)
	}
	golden(t, "nested.zwo", doc)
}

func TestUnsupportedReasons(t *testing.T) {
	ok := leaf(step("active", secs(600), pw(80, 90), ""))
	dist := leaf(step("active", &workout.Duration{Type: "distance", DistanceM: ip(5000)}, nil, ""))
	running := ride("r", "", ok)
	running.Sport = "running"
	indoor := ride("r", "", ok)
	indoor.Sport = "indoor_cycling"
	watts := step("active", secs(600), &workout.Target{Type: "power", LowW: ip(200), HighW: ip(220)}, "")
	nested := workout.Step2{StepBase: workout.StepBase{Kind: "repeat"}, Count: 2, Steps: []workout.Step3{
		{StepBase: step("active", &workout.Duration{Type: "hr_above", HRBpm: ip(150)}, nil, "")},
	}}
	for _, c := range []struct {
		w    workout.Workout
		want string
	}{
		{ride("r", "", ok, ok), ""},
		{running, "sport: only cycling workouts export to Zwift"},
		{indoor, ""},
		{ride("r", "", ok, rep(2, "", child(step("active", secs(60), pw(100, 110), "")), child(watts))),
			"steps[1].steps[1].target: watt targets have no Zwift equivalent (Zwift takes % of FTP)"},
		{ride("r", "", ok, dist), "steps[1].duration: distance durations have no Zwift equivalent"},
		{ride("r", "", rep(2, "", child(step("active", secs(60), nil, "")), child(step("rest", &workout.Duration{Type: "open"}, nil, "")))),
			"steps[0].steps[1].duration: open durations have no Zwift equivalent"},
		{ride("r", "", rep(2, "", nested)), "steps[0].steps[0].steps[0].duration: hr_above durations have no Zwift equivalent"},
	} {
		if got := Unsupported(c.w); got != c.want {
			t.Errorf("Unsupported = %q, want %q", got, c.want)
		}
		if _, err := Encode(c.w); (err != nil) != (c.want != "") {
			t.Errorf("Encode error = %v for reason %q", err, c.want)
		}
	}
}

func TestSafeTextFallbacks(t *testing.T) {
	for in, want := range map[string]string{
		"Zóna 2 – ľahko":      "Zona 2 lahko",
		`a<b>&"c"`:            "a b c",
		"a\nb\tc":             "a b c",
		"  3x10 @ 90% (FTP) ": "3x10 90% (FTP)",
		"🚴💪":                  "",
	} {
		if got := safeText(in, 50); got != want {
			t.Errorf("safeText(%q) = %q, want %q", in, got, want)
		}
	}
	long := safeText(strings.Repeat("abc ", 30), MaxTextLen)
	if len(long) > MaxTextLen || strings.HasSuffix(long, " ") {
		t.Errorf("cut text %q (%d bytes)", long, len(long))
	}
	doc := encode(t, ride("🚴💪", "", leaf(step("active", secs(60), &workout.Target{Type: "effort", Cue: "🔥"}, "")),
		leaf(step("active", secs(60), &workout.Target{Type: "effort", Cue: "🔥"}, "relax"))))
	mustContain(t, doc, "<name>coachd workout</name>", `message="relax"`)
	if n := strings.Count(doc, "<textevent "); n != 1 {
		t.Errorf("text events = %d, want 1 (an emptied cue is omitted)", n)
	}
	name := encode(t, ride(strings.Repeat("n", 80), "", leaf(step("active", secs(60), nil, ""))))
	mustContain(t, name, "<name>"+strings.Repeat("n", MaxNameLen)+"</name>")
}

func TestEncodeDeterministic(t *testing.T) {
	w := ride("Same", "notes", leaf(step("active", secs(600), pw(80, 90), "x")),
		rep(4, "", child(step("active", secs(60), pw(120, 120), "")), child(step("rest", secs(60), pw(40, 50), ""))))
	a, _ := Encode(w)
	b, _ := Encode(w)
	if !bytes.Equal(a, b) {
		t.Fatal("two encodes differ")
	}
}

func TestFileName(t *testing.T) {
	d := civil.Date{Year: 2026, Month: 10, Day: 24}
	if got := FileName(d, "abcdefghijkl"); got != "coachd-2026-10-24-abcdefgh.zwo" {
		t.Errorf("FileName = %q", got)
	}
	if got := FileName(d, "abc"); got != "coachd-2026-10-24-abc.zwo" {
		t.Errorf("short id FileName = %q", got)
	}
}
