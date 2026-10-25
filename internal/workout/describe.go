package workout

import (
	"fmt"
	"strconv"
	"strings"
)

// Line is one line of a described  Indent is the nesting level:
// 0 for a top-level step, 1 inside a repeat, 2 inside a nested repeat; a
// step's note sits one level below its step. Kind is the step kind the page
// styles the line by: a kindLabels key, "repeat", "note", or "" for a kind
// it does not know.
type Line struct {
	Indent int
	Kind   string
	Text   string
}

// kindLabels names each step kind as the page shows it.
var kindLabels = map[string]string{
	"warmup": "Warm-up", "active": "Active", "recovery": "Recovery", "rest": "Rest", "cooldown": "Cool-down",
}

// Describe renders a workout as plain lines, e.g.
// "Warm-up · 10:00 · HR 120–140 bpm", "Repeat 6×", "Active · 400 m · pace 3:50–4:00 /km",
// "Recovery · open (lap button)", "Active · until HR above 160 bpm",
// "Active · 2 km · cadence 170–180 spm", "Active · 5:00 · effort: <cue>",
// "Active · 5:00 · power 88–92% FTP"; step notes become an
// extra line "note: <notes>" at Indent+1.
//
// Cues and notes are agent-written: a page must show the lines escaped and
// labelled. Describe never fails; a missing value reads "?".
func Describe(w Workout) []Line {
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

func describeStep(out []Line, s StepBase, count, indent int) []Line {
	text, kind := fmt.Sprintf("Repeat %d×", count), "repeat"
	if s.Kind != "repeat" {
		label, ok := kindLabels[s.Kind]
		kind = s.Kind
		if !ok {
			label, kind = s.Kind, ""
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
	out = append(out, Line{Indent: indent, Kind: kind, Text: text})
	if s.Notes != "" {
		out = append(out, Line{Indent: indent + 1, Kind: "note", Text: "note: " + s.Notes})
	}
	return out
}

func describeDuration(d *Duration) string {
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

func describeTarget(t *Target) string {
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
	case "power":
		return "power " + num(t.LowPctFTP) + "–" + num(t.HighPctFTP) + "% FTP"
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
