package web

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/stefiix92/coachd/internal/workout"
)

// Line is one line of a described workout. Indent is the nesting level:
// 0 for a top-level step, 1 inside a repeat, 2 inside a nested repeat; a
// step's note sits one level below its step.
type Line struct {
	Indent int
	Text   string
}

// kindLabels names each step kind as the page shows it.
var kindLabels = map[string]string{
	"warmup": "Warm-up", "active": "Active", "recovery": "Recovery", "rest": "Rest", "cooldown": "Cool-down",
}

// Describe renders a workout as plain lines for the page, e.g.
// "Warm-up · 10:00 · HR 120–140 bpm", "Repeat 6×", "Active · 400 m · pace 3:50–4:00 /km",
// "Recovery · open (lap button)", "Active · until HR above 160 bpm",
// "Active · 2 km · cadence 170–180 spm", "Active · 5:00 · effort: <cue>"; step notes become an
// extra line "note: <notes>" at Indent+1.
//
// Cues and notes are agent-written: the page must show the lines escaped and
// labelled. Describe never fails; a missing value reads "?".
func Describe(w workout.Workout) []Line {
	var out []Line
	for _, s := range w.Steps {
		out = describeStep(out, s.StepBase, s.Count, 0)
		for _, c := range s.Steps {
			out = describeStep(out, c.StepBase, c.Count, 1)
			for _, g := range c.Steps {
				out = describeStep(out, g.StepBase, 0, 2)
			}
		}
	}
	return out
}

func describeStep(out []Line, s workout.StepBase, count, indent int) []Line {
	text := fmt.Sprintf("Repeat %d×", count)
	if s.Kind != "repeat" {
		label, ok := kindLabels[s.Kind]
		if !ok {
			label = s.Kind
		}
		parts := []string{label}
		if d := describeDuration(s.Duration); d != "" {
			parts = append(parts, d)
		}
		if t := describeTarget(s.Target); t != "" {
			parts = append(parts, t)
		}
		text = strings.Join(parts, " · ")
	}
	out = append(out, Line{Indent: indent, Text: text})
	if s.Notes != "" {
		out = append(out, Line{Indent: indent + 1, Text: "note: " + s.Notes})
	}
	return out
}

func describeDuration(d *workout.Duration) string {
	if d == nil {
		return ""
	}
	switch d.Type {
	case "time":
		return clock(d.TimeS)
	case "distance":
		return distance(d.DistanceM)
	case "open":
		return "open (lap button)"
	case "hr_above":
		return "until HR above " + num(d.HRBpm) + " bpm"
	case "hr_below":
		return "until HR below " + num(d.HRBpm) + " bpm"
	}
	return d.Type
}

func describeTarget(t *workout.Target) string {
	if t == nil {
		return ""
	}
	switch t.Type {
	case "none", "":
		return ""
	case "hr":
		return "HR " + num(t.LowBpm) + "–" + num(t.HighBpm) + " bpm"
	case "pace":
		return "pace " + clock(t.LowPaceSPerKm) + "–" + clock(t.HighPaceSPerKm) + " /km"
	case "cadence":
		return "cadence " + num(t.LowSpm) + "–" + num(t.HighSpm) + " spm"
	case "effort":
		return "effort: " + t.Cue
	}
	return t.Type
}

func num(p *int) string {
	if p == nil {
		return "?"
	}
	return strconv.Itoa(*p)
}

// clock formats seconds as m:ss, or h:mm:ss from one hour.
func clock(p *int) string {
	if p == nil || *p < 0 {
		return "?"
	}
	s := *p
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// distance formats metres: "400 m" below 1 km, otherwise km rounded to 10 m
// with trailing zeros trimmed ("1.5 km", "2 km").
func distance(p *int) string {
	if p == nil || *p < 0 {
		return "?"
	}
	m := *p
	if m < 1000 {
		return strconv.Itoa(m) + " m"
	}
	c := (m + 5) / 10 // hundredths of a km
	km := strconv.Itoa(c / 100)
	if frac := c % 100; frac != 0 {
		km += strings.TrimRight(fmt.Sprintf(".%02d", frac), "0")
	}
	return km + " km"
}
