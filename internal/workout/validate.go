package workout

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/store"
)

const (
	maxNameRunes     = 100
	maxWorkoutNotes  = 1000
	maxStepNotes     = 500
	maxCueRunes      = 100
	maxEchoRunes     = 40
	minRepeat        = 2
	maxRepeat        = 50
	minCadence       = 30
	maxCadence       = 250
	minPowerPct      = 20
	maxPowerPct      = 300
	minPowerW        = 1 // FIT power 1000 is % of FTP, so 0 W has no encoding
	maxPowerW        = 1500
	maxTimeS         = 36000
	maxDistanceM     = 100000
	defaultPaceSPerK = 360
)

var sports = map[string]store.Sport{
	"running": store.SportRunning, "trail_running": store.SportTrailRunning,
	"walking": store.SportWalking, "hiking": store.SportHiking,
	"cycling": store.SportCycling, "strength": store.SportStrength,
	"other": store.SportOther,
	// A trainer ride is stored as cycling, as ingest stores indoor rides;
	// only the FIT file marks it indoor.
	"indoor_cycling": store.SportCycling,
}

// sportRule holds a sport's pace bounds and the pace that estimates a
// distance step without a pace target, all in s/km.
type sportRule struct {
	sport                          store.Sport
	minPace, maxPace, estimatePace int
	noDistance, noPace             bool
}

// sportRules lists the sports with their own rules. Running, trail running
// and other use the pace bounds in Limits and defaultPaceSPerK.
var sportRules = map[store.Sport]sportRule{
	store.SportWalking:  {minPace: 300, maxPace: 2400, estimatePace: 720},
	store.SportHiking:   {minPace: 300, maxPace: 2400, estimatePace: 720},
	store.SportCycling:  {minPace: 60, maxPace: 1500, estimatePace: 144},
	store.SportStrength: {noDistance: true, noPace: true},
}

func ruleFor(sp store.Sport, l Limits) sportRule {
	r, ok := sportRules[sp]
	if !ok {
		r = sportRule{minPace: l.MinPaceSPerKm, maxPace: l.MaxPaceSPerKm, estimatePace: defaultPaceSPerK}
	}
	r.sport = sp
	return r
}

var stepKinds = map[string]bool{"warmup": true, "active": true, "recovery": true, "rest": true, "cooldown": true, "repeat": true}

// clip bounds an echoed agent string so reasons stay small.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxEchoRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxEchoRunes]) + "…"
}

func bad(format string, a ...any) error { return &Invalid{Reason: fmt.Sprintf(format, a...)} }

// CheckText enforces the rune limit and rejects control characters other
// than newline and tab.
func CheckText(path, s string, max int) error {
	if !utf8.ValidString(s) {
		return bad("%s: is not valid UTF-8", path)
	}
	if n := utf8.RuneCountInString(s); n > max {
		return bad("%s: %d characters exceeds the %d limit", path, n, max)
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return bad("%s: contains a control character (U+%04X); only newline and tab are allowed", path, r)
		}
	}
	return nil
}

// CheckLine is CheckText for single-line text such as a device or passkey
// label: tab and newline are rejected too.
func CheckLine(path, s string, max int) error {
	if err := CheckText(path, s, max); err != nil {
		return err
	}
	if strings.ContainsAny(s, "\t\n") {
		return bad("%s: must be a single line (no tab or newline)", path)
	}
	return nil
}

// CheckName is CheckText for a required name, which must also hold more
// than whitespace.
func CheckName(path, s string, max int) error {
	if s == "" {
		return bad("%s: required", path)
	}
	if err := CheckText(path, s, max); err != nil {
		return err
	}
	if strings.TrimSpace(s) == "" {
		return bad("%s: is empty (whitespace only)", path)
	}
	return nil
}

type acc struct {
	steps, estS, distM int
	maxHR              *int
	unknown            bool // some step is open or heart-rate-bounded: its duration counts as 0 s
	defaultPace        bool // some distance step is estimated at the sport's default pace
}

// Validate checks w against l and returns derived facts. The error, if any,
// is *Invalid carrying the first violation in depth-first step order.
func Validate(w Workout, today civil.Date, l Limits) (Plan, error) {
	var p Plan
	if err := CheckName("name", w.Name, maxNameRunes); err != nil {
		return p, err
	}
	sport, okSport := sports[w.Sport]
	if !okSport {
		return p, bad("sport: %q is not one of running|trail_running|walking|hiking|cycling|indoor_cycling|strength|other", clip(w.Sport))
	}
	d, err := civil.Parse(w.Date)
	if err != nil {
		return p, bad("date: %q is not a valid YYYY-MM-DD date", clip(w.Date))
	}
	if d.Before(today) || d.Sub(today) > l.MaxDaysAhead {
		return p, bad("date: %s is outside the allowed window %s to %s", clip(w.Date), today, today.AddDays(l.MaxDaysAhead))
	}
	if err := CheckText("notes", w.Notes, maxWorkoutNotes); err != nil {
		return p, err
	}
	if len(w.Steps) == 0 {
		return p, bad("steps: at least one step is required")
	}
	var a acc
	if err := walk(toNodes(w.Steps), "steps", 1, l, ruleFor(sport, l), &a); err != nil {
		return p, err
	}
	// Open and heart-rate-bounded steps count as 0 s, so the minimum applies
	// only when every step has a time or a distance; the maximum always
	// applies to the known part.
	if a.estS > l.MaxTotalS {
		return p, bad("steps: estimated total %d s is above the %d s maximum (open and heart-rate-bounded steps count as 0 s)", a.estS, l.MaxTotalS)
	}
	if !a.unknown && a.estS < l.MinTotalS {
		return p, bad("steps: estimated total %d s is below the %d s minimum (open and heart-rate-bounded steps count as 0 s)", a.estS, l.MinTotalS)
	}
	return Plan{Date: d, Sport: sport, ExpandedSteps: a.steps, EstimatedS: a.estS, DistanceM: a.distM, MaxTargetHR: a.maxHR,
		Partial: a.unknown, EstimatedFromDefaultPace: a.defaultPace}, nil
}

// PlanOf re-derives the plan of a stored workout, which passed Validate when
// it was proposed. The date window is not checked again: the workout is
// judged as of its own date.
func PlanOf(w Workout, l Limits) (Plan, error) {
	d, err := civil.Parse(w.Date)
	if err != nil {
		return Plan{}, bad("date: %q is not a valid YYYY-MM-DD date", clip(w.Date))
	}
	return Validate(w, d, l)
}

// node is the internal recursive form of Step, Step2 and Step3.
type node struct {
	StepBase
	count int
	steps []node
	leaf  bool // Step3: kind=repeat is not allowed
}

func toNodes(in []Step) []node {
	out := make([]node, len(in))
	for i, s := range in {
		n := node{StepBase: s.StepBase, count: s.Count}
		for _, c := range s.Steps {
			m := node{StepBase: c.StepBase, count: c.Count}
			for _, g := range c.Steps {
				m.steps = append(m.steps, node{StepBase: g.StepBase, leaf: true})
			}
			n.steps = append(n.steps, m)
		}
		out[i] = n
	}
	return out
}

// walk validates steps in depth-first order. mult is how many times the
// enclosing repeats execute these steps.
func walk(steps []node, path string, mult int, l Limits, r sportRule, a *acc) error {
	for i, s := range steps {
		sp := fmt.Sprintf("%s[%d]", path, i)
		if !stepKinds[s.Kind] {
			return bad("%s.kind: %q is not one of warmup|active|recovery|rest|cooldown|repeat", sp, clip(s.Kind))
		}
		if err := CheckText(sp+".notes", s.Notes, maxStepNotes); err != nil {
			return err
		}
		if s.Kind == "repeat" {
			if s.leaf {
				return bad("%s: repeats nest at most 2 levels", sp)
			}
			if s.Duration != nil || s.Target != nil {
				return bad("%s: a repeat takes no duration or target; set them on its child steps", sp)
			}
			if s.count < minRepeat || s.count > maxRepeat {
				return bad("%s.count: %d is outside %d..%d", sp, s.count, minRepeat, maxRepeat)
			}
			if len(s.steps) == 0 {
				return bad("%s.steps: a repeat needs at least one child step", sp)
			}
			if err := walk(s.steps, sp+".steps", mult*s.count, l, r, a); err != nil {
				return err
			}
			continue
		}
		if s.count != 0 || len(s.steps) > 0 {
			return bad("%s: count and steps are for kind=repeat only", sp)
		}
		if s.Duration == nil {
			return bad("%s.duration: required unless kind=repeat", sp)
		}
		if err := checkDuration(sp+".duration", s.Duration, l, r, a); err != nil {
			return err
		}
		slowPace := 0
		if s.Target != nil {
			sl, err := checkTarget(sp+".target", s.Target, l, r, a)
			if err != nil {
				return err
			}
			slowPace = sl
		}
		a.steps += mult
		if a.steps > l.MaxSteps {
			return bad("%s: workout expands to more than %d steps; use fewer repeats or steps", sp, l.MaxSteps)
		}
		switch s.Duration.Type {
		case "time":
			a.estS += mult * *s.Duration.TimeS
		case "distance":
			m := *s.Duration.DistanceM
			pace := r.estimatePace
			if slowPace > 0 {
				pace = slowPace
			} else {
				a.defaultPace = true
			}
			a.distM += mult * m
			a.estS += mult * ((m*pace + 500) / 1000)
		default: // open, hr_above, hr_below
			a.unknown = true
		}
	}
	return nil
}

func rng(path, field string, v *int, lo, hi int, unit string) error {
	if v == nil {
		return bad("%s: %s is required", path, field)
	}
	if *v < lo {
		return bad("%s: %s %d %s below %d", path, field, *v, unit, lo)
	}
	if *v > hi {
		return bad("%s: %s %d %s above %d", path, field, *v, unit, hi)
	}
	return nil
}

func checkDuration(path string, d *Duration, l Limits, r sportRule, a *acc) error {
	only := func(allowed string) error {
		if d.TimeS != nil && allowed != "time" {
			return bad("%s: time_s is only for type=time", path)
		}
		if d.DistanceM != nil && allowed != "distance" {
			return bad("%s: distance_m is only for type=distance", path)
		}
		if d.HRBpm != nil && allowed != "hr" {
			return bad("%s: hr_bpm is only for type=hr_above|hr_below", path)
		}
		return nil
	}
	switch d.Type {
	case "time":
		if err := only("time"); err != nil {
			return err
		}
		return rng(path, "time_s", d.TimeS, 1, maxTimeS, "s")
	case "distance":
		if r.noDistance {
			return bad("%s: distance durations are not supported for %s", path, r.sport)
		}
		if err := only("distance"); err != nil {
			return err
		}
		return rng(path, "distance_m", d.DistanceM, 1, maxDistanceM, "m")
	case "open":
		return only("open")
	case "hr_above", "hr_below":
		if err := only("hr"); err != nil {
			return err
		}
		if err := rng(path, "hr_bpm", d.HRBpm, l.MinHR, l.MaxHR, "bpm"); err != nil {
			return err
		}
		if d.Type == "hr_above" {
			noteHR(a, *d.HRBpm)
		}
		return nil
	}
	return bad("%s.type: %q is not one of time|distance|open|hr_above|hr_below", path, clip(d.Type))
}

func noteHR(a *acc, v int) {
	if a.maxHR == nil || v > *a.maxHR {
		x := v
		a.maxHR = &x
	}
}

// checkTarget validates a target and returns the slow pace bound (0 if none).
func checkTarget(path string, t *Target, l Limits, r sportRule, a *acc) (int, error) {
	switch t.Type {
	case "none", "hr", "pace", "cadence", "effort", "power":
	default:
		return 0, bad("%s.type: %q is not one of none|hr|pace|cadence|effort|power", path, clip(t.Type))
	}
	if t.Type != "effort" && t.Cue != "" {
		return 0, bad("%s: cue set for type=%s; cue is only for type=effort", path, t.Type)
	}
	other := func(allowed string) error {
		for _, f := range []struct {
			name, typ string
			set       bool
		}{
			{"low_bpm", "hr", t.LowBpm != nil}, {"high_bpm", "hr", t.HighBpm != nil},
			{"low_pace_s_per_km", "pace", t.LowPaceSPerKm != nil}, {"high_pace_s_per_km", "pace", t.HighPaceSPerKm != nil},
			{"low_spm", "cadence", t.LowSpm != nil}, {"high_spm", "cadence", t.HighSpm != nil},
			{"low_pct_ftp", "power", t.LowPctFTP != nil}, {"high_pct_ftp", "power", t.HighPctFTP != nil},
			{"low_w", "power", t.LowW != nil}, {"high_w", "power", t.HighW != nil},
		} {
			if f.set && f.typ != allowed {
				return bad("%s: %s set for type=%s", path, f.name, t.Type)
			}
		}
		return nil
	}
	pair := func(lf, hf string, lo, hi *int, min, max int, unit string) error {
		if err := rng(path, lf, lo, min, max, unit); err != nil {
			return err
		}
		if err := rng(path, hf, hi, min, max, unit); err != nil {
			return err
		}
		if *lo > *hi {
			return bad("%s: %s %d is above %s %d; low must not exceed high", path, lf, *lo, hf, *hi)
		}
		return nil
	}
	switch t.Type {
	case "none":
		return 0, other("none")
	case "hr":
		if err := other("hr"); err != nil {
			return 0, err
		}
		if err := pair("low_bpm", "high_bpm", t.LowBpm, t.HighBpm, l.MinHR, l.MaxHR, "bpm"); err != nil {
			return 0, err
		}
		noteHR(a, *t.HighBpm)
		return 0, nil
	case "pace":
		if r.noPace {
			return 0, bad("%s: pace targets are not supported for %s", path, r.sport)
		}
		if err := other("pace"); err != nil {
			return 0, err
		}
		if err := pair("low_pace_s_per_km", "high_pace_s_per_km", t.LowPaceSPerKm, t.HighPaceSPerKm, r.minPace, r.maxPace, "s/km"); err != nil {
			return 0, err
		}
		return *t.HighPaceSPerKm, nil
	case "cadence":
		if err := other("cadence"); err != nil {
			return 0, err
		}
		return 0, pair("low_spm", "high_spm", t.LowSpm, t.HighSpm, minCadence, maxCadence, "spm")
	case "power":
		if r.sport != store.SportCycling {
			return 0, bad("%s: power targets are only supported for cycling and indoor_cycling", path)
		}
		if err := other("power"); err != nil {
			return 0, err
		}
		pct := t.LowPctFTP != nil || t.HighPctFTP != nil
		watts := t.LowW != nil || t.HighW != nil
		switch {
		case pct && watts:
			return 0, bad("%s: set low_pct_ftp and high_pct_ftp, or low_w and high_w, not both", path)
		case watts:
			return 0, pair("low_w", "high_w", t.LowW, t.HighW, minPowerW, maxPowerW, "W")
		case !pct:
			return 0, bad("%s: power needs low_pct_ftp and high_pct_ftp, or low_w and high_w", path)
		}
		return 0, pair("low_pct_ftp", "high_pct_ftp", t.LowPctFTP, t.HighPctFTP, minPowerPct, maxPowerPct, "% FTP")
	case "effort":
		if err := other("effort"); err != nil {
			return 0, err
		}
		if t.Cue == "" {
			return 0, bad("%s.cue: required for type=effort", path)
		}
		return 0, CheckText(path+".cue", t.Cue, maxCueRunes)
	}
	return 0, bad("%s.type: %q is not one of none|hr|pace|cadence|effort|power", path, clip(t.Type))
}
