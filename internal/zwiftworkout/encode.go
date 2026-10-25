// Package zwiftworkout encodes an approved cycling workout as a Zwift workout
// file (.zwo) the runner copies into Zwift's Workouts folder. It is pure: no
// I/O, no store access. Unsupported is the one answer to whether a workout
// can be a Zwift workout, used by the approval page and the warning alike.
package zwiftworkout

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/workout"
)

const (
	MaxNameLen  = 50 // <name>, after safeText
	MaxTextLen  = 80 // each text event, after safeText
	ContentType = "application/xml"

	fallbackName = "coachd workout"
)

// Unsupported returns why w cannot be a Zwift workout, or "" when it can: the
// sport must be cycling and every step must have a time duration. The
// reason names the first other step in depth-first order, in the path style
// of validation errors.
func Unsupported(w workout.Workout) string {
	if w.Sport != "cycling" {
		return "sport: only cycling workouts export to Zwift"
	}
	return unsupportedIn(flatten(w.Steps), "steps")
}

func unsupportedIn(ns []node, path string) string {
	for i, n := range ns {
		p := fmt.Sprintf("%s[%d]", path, i)
		if n.Kind == "repeat" {
			if r := unsupportedIn(n.children, p+".steps"); r != "" {
				return r
			}
			continue
		}
		if n.Duration == nil || n.Duration.Type != "time" {
			typ := "missing"
			if n.Duration != nil {
				typ = n.Duration.Type
			}
			return fmt.Sprintf("%s.duration: %s durations have no Zwift equivalent", p, typ)
		}
	}
	return ""
}

// Encode returns the .zwo document for w, which must already pass
// workout.Validate and Unsupported.
func Encode(w workout.Workout) ([]byte, error) {
	if r := Unsupported(w); r != "" {
		return nil, fmt.Errorf("zwiftworkout: %s", r)
	}
	f := file{Author: "coachd", Name: safeText(w.Name, MaxNameLen), Description: safeText(w.Notes, 0), SportType: "bike"}
	if f.Name == "" {
		f.Name = fallbackName
	}
	for _, n := range flatten(w.Steps) {
		if err := emit(&f.Workout.Segments, n, true); err != nil {
			return nil, err
		}
	}
	b, err := xml.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("zwiftworkout: marshal: %w", err)
	}
	return append(b, '\n'), nil
}

// FileName is "coachd-<date>-<first 8 chars of id>.zwo".
func FileName(date civil.Date, id string) string {
	if len(id) > 8 {
		id = id[:8]
	}
	return fmt.Sprintf("coachd-%s-%s.zwo", date, id)
}

type file struct {
	XMLName     xml.Name `xml:"workout_file"`
	Author      string   `xml:"author"`
	Name        string   `xml:"name"`
	Description string   `xml:"description"`
	SportType   string   `xml:"sportType"`
	Workout     struct {
		Segments []any // each segment type names its own element
	} `xml:"workout"`
}

type textEvent struct {
	XMLName    xml.Name `xml:"textevent"`
	TimeOffset int      `xml:"timeoffset,attr"`
	Message    string   `xml:"message,attr"`
}

type steadyState struct {
	XMLName  xml.Name `xml:"SteadyState"`
	Duration int      `xml:"Duration,attr"`
	Power    string   `xml:"Power,attr"`
	Text     []textEvent
}

type freeRide struct {
	XMLName  xml.Name `xml:"FreeRide"`
	Duration int      `xml:"Duration,attr"`
	Text     []textEvent
}

type intervalsT struct {
	XMLName     xml.Name `xml:"IntervalsT"`
	Repeat      int      `xml:"Repeat,attr"`
	OnDuration  int      `xml:"OnDuration,attr"`
	OffDuration int      `xml:"OffDuration,attr"`
	OnPower     string   `xml:"OnPower,attr"`
	OffPower    string   `xml:"OffPower,attr"`
}

// node is a step at any nesting level, flattened to one shape.
type node struct {
	workout.StepBase
	count    int
	children []node
}

func flatten(in []workout.Step) []node {
	out := make([]node, len(in))
	for i, s := range in {
		n := node{StepBase: s.StepBase, count: s.Count}
		for _, c := range s.Steps {
			m := node{StepBase: c.StepBase, count: c.Count}
			for _, g := range c.Steps {
				m.children = append(m.children, node{StepBase: g.StepBase})
			}
			n.children = append(n.children, m)
		}
		out[i] = n
	}
	return out
}

// emit appends n's segments. A top-level repeat of exactly two power steps
// with no notes becomes IntervalsT; every other repeat, and every nested
// one, is written out step by step. A repeat's own notes are not written,
// as in the FIT file.
func emit(out *[]any, n node, top bool) error {
	if n.Kind != "repeat" {
		seg, err := segment(n)
		if err != nil {
			return err
		}
		*out = append(*out, seg)
		return nil
	}
	if top && intervalsShape(n) {
		on, off := n.children[0], n.children[1]
		*out = append(*out, intervalsT{Repeat: n.count, OnDuration: *on.Duration.TimeS, OffDuration: *off.Duration.TimeS,
			OnPower: power(on.Target), OffPower: power(off.Target)})
		return nil
	}
	for i := 0; i < n.count; i++ {
		for _, c := range n.children {
			if err := emit(out, c, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func intervalsShape(n node) bool {
	if n.Notes != "" || len(n.children) != 2 {
		return false
	}
	for _, c := range n.children {
		if c.Kind == "repeat" || c.Notes != "" || c.Target == nil || c.Target.Type != "power" {
			return false
		}
	}
	return true
}

func segment(n node) (any, error) {
	if n.Duration == nil || n.Duration.TimeS == nil {
		return nil, fmt.Errorf("zwiftworkout: %s step without a time duration", n.Kind)
	}
	d := *n.Duration.TimeS
	var text []textEvent
	if msg := message(n); msg != "" {
		text = []textEvent{{Message: msg}}
	}
	if n.Target != nil && n.Target.Type == "power" {
		if n.Target.LowPctFTP == nil || n.Target.HighPctFTP == nil {
			return nil, fmt.Errorf("zwiftworkout: power target bounds missing")
		}
		return steadyState{Duration: d, Power: power(n.Target), Text: text}, nil
	}
	return freeRide{Duration: d, Text: text}, nil
}

// power is the band's midpoint as a fraction of FTP, always with a decimal
// point: 88–92 % gives "0.9", 95–105 % gives "1.0".
func power(t *workout.Target) string {
	s := strconv.FormatFloat(float64(*t.LowPctFTP+*t.HighPctFTP)/200, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// message is the step's one text event: the target text and the step notes
// joined with " - ", as safe text cut to MaxTextLen.
func message(n node) string {
	var parts []string
	for _, p := range []string{targetText(n.Target), n.Notes} {
		if s := safeText(p, 0); s != "" {
			parts = append(parts, s)
		}
	}
	return safeText(strings.Join(parts, " - "), MaxTextLen)
}

func targetText(t *workout.Target) string {
	if t == nil {
		return ""
	}
	switch t.Type {
	case "hr":
		return "HR " + num(t.LowBpm) + "-" + num(t.HighBpm) + " bpm"
	case "pace":
		return "pace " + clock(t.LowPaceSPerKm) + "-" + clock(t.HighPaceSPerKm) + " /km"
	case "cadence":
		return "cadence " + num(t.LowSpm) + "-" + num(t.HighSpm) + " rpm"
	case "effort":
		return t.Cue
	}
	return "" // none and power: power is the segment itself
}

func num(p *int) string {
	if p == nil {
		return "?"
	}
	return strconv.Itoa(*p)
}

func clock(p *int) string {
	if p == nil || *p < 0 {
		return "?"
	}
	return fmt.Sprintf("%d:%02d", *p/60, *p%60)
}

// safeText is s in the most conservative form Zwift is known to show:
// accents removed, every character other than ASCII letters, digits and
// "-_.,()%/:+" turned into a space, runs of spaces collapsed, ends trimmed,
// and the result cut to max bytes (no cut when max is 0).
func safeText(s string, max int) string {
	var b strings.Builder
	gap := false
	for _, r := range norm.NFD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r): // a combining accent of the letter before it
		case keep(r):
			if gap && b.Len() > 0 {
				b.WriteByte(' ')
			}
			gap = false
			b.WriteRune(r)
		default:
			gap = true
		}
	}
	out := b.String()
	if max > 0 && len(out) > max {
		out = strings.TrimRight(out[:max], " ")
	}
	return out
}

func keep(r rune) bool {
	if r >= utf8.RuneSelf {
		return false
	}
	return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' || strings.ContainsRune("-_.,()%/:+", r)
}
