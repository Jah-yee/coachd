// Package fitworkout encodes an approved workout as a FIT workout file the
// runner copies onto a Garmin watch. It is pure: no I/O, no store access.
package fitworkout

import (
	"bytes"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/muktihari/fit/encoder"
	"github.com/muktihari/fit/profile/mesgdef"
	"github.com/muktihari/fit/profile/typedef"
	"github.com/muktihari/fit/proto"
	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/workout"
)

const (
	MaxNameBytes  = 32 // wkt_name, truncated at a UTF-8 boundary
	MaxNotesBytes = 50 // workout_step notes, truncated at a UTF-8 boundary
)

type sportPair struct {
	sport typedef.Sport
	sub   typedef.SubSport
}

var sports = map[string]sportPair{
	"running":       {typedef.SportRunning, typedef.SubSportGeneric},
	"trail_running": {typedef.SportRunning, typedef.SubSportTrail},
	"walking":       {typedef.SportWalking, typedef.SubSportGeneric},
	"hiking":        {typedef.SportHiking, typedef.SubSportGeneric},
	"cycling":       {typedef.SportCycling, typedef.SubSportGeneric},
	"strength":      {typedef.SportTraining, typedef.SubSportStrengthTraining},
	"other":         {typedef.SportGeneric, typedef.SubSportGeneric},
}

var intensities = map[string]typedef.Intensity{
	"warmup": typedef.IntensityWarmup, "active": typedef.IntensityActive,
	"recovery": typedef.IntensityRecovery, "rest": typedef.IntensityRest,
	"cooldown": typedef.IntensityCooldown,
}

// Encode returns a FIT workout file for w, which must already pass
// workout.Validate. createdAt becomes FileId.TimeCreated.
func Encode(w workout.Workout, createdAt time.Time) ([]byte, error) {
	sp, ok := sports[w.Sport]
	if !ok {
		return nil, fmt.Errorf("fitworkout: unknown sport %q", w.Sport)
	}
	var steps []*mesgdef.WorkoutStep
	if err := emit(&steps, w.Steps, toTop); err != nil {
		return nil, err
	}

	msgs := []proto.Message{
		mesgdef.NewFileId(nil).SetType(typedef.FileWorkout).SetManufacturer(typedef.ManufacturerDevelopment).
			SetProduct(0).SetTimeCreated(createdAt).ToMesg(nil),
		mesgdef.NewWorkout(nil).SetSport(sp.sport).SetSubSport(sp.sub).
			SetNumValidSteps(uint16(len(steps))).SetWktName(WorkoutName(w.Name)).ToMesg(nil),
	}
	for _, s := range steps {
		msgs = append(msgs, s.ToMesg(nil))
	}
	var buf bytes.Buffer
	if err := encoder.New(&buf).Encode(&proto.FIT{Messages: msgs}); err != nil {
		return nil, fmt.Errorf("fitworkout: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// WorkoutName is the wkt_name Encode writes for name: name cut to
// MaxNameBytes at a UTF-8 rune boundary.
func WorkoutName(name string) string { return truncate(name, MaxNameBytes) }

// FileName is "coachd-<date>-<first 8 chars of id>.fit".
func FileName(date civil.Date, id string) string {
	if len(id) > 8 {
		id = id[:8]
	}
	return fmt.Sprintf("coachd-%s-%s.fit", date, id)
}

// node is a step at any nesting level, flattened to one shape.
type node struct {
	workout.StepBase
	count    int
	children []node
}

func toTop(s workout.Step) node {
	n := node{StepBase: s.StepBase, count: s.Count}
	for _, c := range s.Steps {
		n.children = append(n.children, node{StepBase: c.StepBase, count: c.Count, children: leaves(c.Steps)})
	}
	return n
}

func leaves(in []workout.Step3) []node {
	out := make([]node, len(in))
	for i, l := range in {
		out[i] = node{StepBase: l.StepBase}
	}
	return out
}

func emit(out *[]*mesgdef.WorkoutStep, in []workout.Step, conv func(workout.Step) node) error {
	for _, s := range in {
		if err := emitNode(out, conv(s), false); err != nil {
			return err
		}
	}
	return nil
}

// emitNode appends n. Inside a repeat body (inBody) a nested repeat is
// unrolled, so the file has at most one repeat level.
func emitNode(out *[]*mesgdef.WorkoutStep, n node, inBody bool) error {
	if n.Kind != "repeat" {
		st, err := leafStep(n, len(*out))
		if err != nil {
			return err
		}
		*out = append(*out, st)
		return nil
	}
	if inBody {
		for i := 0; i < n.count; i++ {
			for _, c := range n.children {
				if err := emitNode(out, c, true); err != nil {
					return err
				}
			}
		}
		return nil
	}
	first := len(*out)
	for _, c := range n.children {
		if err := emitNode(out, c, true); err != nil {
			return err
		}
	}
	*out = append(*out, mesgdef.NewWorkoutStep(nil).
		SetMessageIndex(typedef.MessageIndex(len(*out))).
		SetDurationType(typedef.WktStepDurationRepeatUntilStepsCmplt).
		SetDurationValue(uint32(first)).
		SetTargetType(typedef.WktStepTargetOpen).
		SetTargetValue(uint32(n.count)))
	return nil
}

func val(p *int) (uint32, bool) {
	if p == nil || *p < 0 {
		return 0, false
	}
	return uint32(*p), true
}

func leafStep(n node, idx int) (*mesgdef.WorkoutStep, error) {
	in, ok := intensities[n.Kind]
	if !ok {
		return nil, fmt.Errorf("fitworkout: unknown step kind %q", n.Kind)
	}
	st := mesgdef.NewWorkoutStep(nil).SetMessageIndex(typedef.MessageIndex(idx)).SetIntensity(in)

	d := n.Duration
	if d == nil {
		return nil, fmt.Errorf("fitworkout: step %d has no duration", idx)
	}
	need := func(p *int, mul, add uint32) (uint32, error) {
		v, ok := val(p)
		if !ok {
			return 0, fmt.Errorf("fitworkout: step %d: %s duration value missing", idx, d.Type)
		}
		return v*mul + add, nil
	}
	var v uint32
	var err error
	switch d.Type {
	case "time":
		if v, err = need(d.TimeS, 1000, 0); err == nil {
			st.SetDurationType(typedef.WktStepDurationTime).SetDurationValue(v)
		}
	case "distance":
		if v, err = need(d.DistanceM, 100, 0); err == nil {
			st.SetDurationType(typedef.WktStepDurationDistance).SetDurationValue(v)
		}
	case "open":
		st.SetDurationType(typedef.WktStepDurationOpen)
	case "hr_above":
		if v, err = need(d.HRBpm, 1, 100); err == nil {
			st.SetDurationType(typedef.WktStepDurationHrGreaterThan).SetDurationValue(v)
		}
	case "hr_below":
		if v, err = need(d.HRBpm, 1, 100); err == nil {
			st.SetDurationType(typedef.WktStepDurationHrLessThan).SetDurationValue(v)
		}
	default:
		err = fmt.Errorf("fitworkout: step %d: unknown duration %q", idx, d.Type)
	}
	if err != nil {
		return nil, err
	}

	notes := n.Notes
	t := n.Target
	st.SetTargetType(typedef.WktStepTargetOpen)
	if t != nil {
		bad := fmt.Errorf("fitworkout: step %d: %s target bounds missing", idx, t.Type)
		switch t.Type {
		case "none":
		case "effort":
			if t.Cue != "" {
				if notes != "" {
					notes = t.Cue + " · " + notes
				} else {
					notes = t.Cue
				}
			}
		case "hr":
			lo, ok1 := val(t.LowBpm)
			hi, ok2 := val(t.HighBpm)
			if !ok1 || !ok2 {
				return nil, bad
			}
			st.SetTargetType(typedef.WktStepTargetHeartRate).SetTargetValue(0).
				SetCustomTargetValueLow(lo + 100).SetCustomTargetValueHigh(hi + 100)
		case "pace":
			lo, ok1 := val(t.LowPaceSPerKm)
			hi, ok2 := val(t.HighPaceSPerKm)
			if !ok1 || !ok2 || lo == 0 || hi == 0 {
				return nil, bad
			}
			st.SetTargetType(typedef.WktStepTargetSpeed).SetTargetValue(0).
				SetCustomTargetValueLow(uint32(math.Round(1e6 / float64(hi)))).
				SetCustomTargetValueHigh(uint32(math.Round(1e6 / float64(lo))))
		case "cadence":
			lo, ok1 := val(t.LowSpm)
			hi, ok2 := val(t.HighSpm)
			if !ok1 || !ok2 {
				return nil, bad
			}
			st.SetTargetType(typedef.WktStepTargetCadence).SetTargetValue(0).
				SetCustomTargetValueLow(lo).SetCustomTargetValueHigh(hi)
		case "power":
			// Custom power values 0–1000 are % of FTP.
			lo, ok1 := val(t.LowPctFTP)
			hi, ok2 := val(t.HighPctFTP)
			if !ok1 || !ok2 {
				return nil, bad
			}
			st.SetTargetType(typedef.WktStepTargetPower).SetTargetValue(0).
				SetCustomTargetValueLow(lo).SetCustomTargetValueHigh(hi)
		default:
			return nil, fmt.Errorf("fitworkout: step %d: unknown target %q", idx, t.Type)
		}
	}
	if notes = truncate(notes, MaxNotesBytes); notes != "" {
		st.SetNotes(notes)
	}
	return st, nil
}

// truncate cuts s at the last UTF-8 rune boundary that fits max bytes.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	i := max
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}
