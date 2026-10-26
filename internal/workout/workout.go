// Package workout is the structured workout model an agent proposes, with
// physical-plausibility checks, step expansion and a canonical content hash.
// It holds no coaching logic: it only rejects input that cannot be a real
// workout, with an actionable reason.
package workout

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/store"
)

// Workout is the agent-supplied structured workout. Its JSON form is the MCP
// input schema.
type Workout struct {
	Name  string `json:"name" jsonschema:"Short title, ≤100 chars"`
	Sport string `json:"sport" jsonschema:"running|trail_running|walking|hiking|cycling|indoor_cycling|strength|other; indoor_cycling is a ride on a smart trainer: the watch opens its indoor bike activity"`
	Date  string `json:"date" jsonschema:"Scheduled date YYYY-MM-DD in the runner's timezone; today up to +14 days"`
	Notes string `json:"notes,omitempty" jsonschema:"Optional, ≤1000 chars"`
	Steps []Step `json:"steps" jsonschema:"Ordered steps; repeats nest"`
}

// StepBase holds the fields shared by every step level.
type StepBase struct {
	Kind     string    `json:"kind" jsonschema:"warmup|active|recovery|rest|cooldown|repeat (repeat only at the top two levels)"`
	Duration *Duration `json:"duration,omitempty" jsonschema:"Required unless kind is repeat"`
	Target   *Target   `json:"target,omitempty" jsonschema:"Optional intensity target"`
	Notes    string    `json:"notes,omitempty" jsonschema:"≤500 chars"`
}

// Step is a top-level step or, for kind=repeat, a group of Step2 children.
// The nesting is typed (not recursive) so a JSON schema can be inferred.
type Step struct {
	StepBase
	Count int     `json:"count,omitempty" jsonschema:"Only for kind=repeat: 2..50"`
	Steps []Step2 `json:"steps,omitempty" jsonschema:"Only for kind=repeat: child steps"`
}

// Step2 is a step inside a top-level repeat; it may itself be a repeat.
type Step2 struct {
	StepBase
	Count int     `json:"count,omitempty" jsonschema:"Only for kind=repeat: 2..50"`
	Steps []Step3 `json:"steps,omitempty" jsonschema:"Only for kind=repeat: child steps (leaf steps, no further repeat)"`
}

// Step3 is a leaf step inside a nested repeat; kind=repeat is not allowed.
type Step3 struct {
	StepBase
}

// Duration says when a step ends.
type Duration struct {
	Type      string `json:"type" jsonschema:"time|distance|open|hr_above|hr_below"`
	TimeS     *int   `json:"time_s,omitempty" jsonschema:"Only for type time: 1..36000 seconds"`
	DistanceM *int   `json:"distance_m,omitempty" jsonschema:"Only for type distance: 1..100000 metres"`
	HRBpm     *int   `json:"hr_bpm,omitempty" jsonschema:"Only for type hr_above or hr_below: heart-rate threshold in bpm"`
}

// Target is the intensity target of a step.
type Target struct {
	Type           string `json:"type" jsonschema:"none|hr|pace|cadence|effort|power"`
	LowBpm         *int   `json:"low_bpm,omitempty" jsonschema:"Only for type hr: lower bound in bpm"`
	HighBpm        *int   `json:"high_bpm,omitempty" jsonschema:"Only for type hr: upper bound in bpm"`
	LowPaceSPerKm  *int   `json:"low_pace_s_per_km,omitempty" jsonschema:"Only for type pace: faster bound in seconds per km (smaller number)"`
	HighPaceSPerKm *int   `json:"high_pace_s_per_km,omitempty" jsonschema:"Only for type pace: slower bound in seconds per km (larger number)"`
	LowSpm         *int   `json:"low_spm,omitempty" jsonschema:"Only for type cadence: lower bound in steps per minute (rpm for cycling)"`
	HighSpm        *int   `json:"high_spm,omitempty" jsonschema:"Only for type cadence: upper bound in steps per minute (rpm for cycling)"`
	Cue            string `json:"cue,omitempty" jsonschema:"Only for type effort: free-text effort cue, ≤100 chars"`
	LowPctFTP      *int   `json:"low_pct_ftp,omitempty" jsonschema:"Only for type power (cycling and indoor_cycling): lower bound in % of FTP"`
	HighPctFTP     *int   `json:"high_pct_ftp,omitempty" jsonschema:"Only for type power (cycling and indoor_cycling): upper bound in % of FTP"`
	LowW           *int   `json:"low_w,omitempty" jsonschema:"Only for type power (cycling and indoor_cycling), instead of low_pct_ftp: lower bound in watts"`
	HighW          *int   `json:"high_w,omitempty" jsonschema:"Only for type power (cycling and indoor_cycling), instead of high_pct_ftp: upper bound in watts"`
}

// Limits are the physical-plausibility bounds. The pace bounds are those of
// running, trail running and other; walking, hiking, cycling and strength
// have their own fixed pace rules.
type Limits struct {
	MinTotalS, MaxTotalS, MinPaceSPerKm, MaxPaceSPerKm, MinHR, MaxHR, MaxSteps, MaxDaysAhead int
}

// DefaultLimits are the production bounds.
var DefaultLimits = Limits{MinTotalS: 60, MaxTotalS: 36000, MinPaceSPerKm: 150, MaxPaceSPerKm: 1500, MinHR: 40, MaxHR: 230, MaxSteps: 100, MaxDaysAhead: 14}

// Plan holds facts derived from a valid workout, used for warnings.
type Plan struct {
	Date          civil.Date
	Sport         store.Sport
	ExpandedSteps int
	EstimatedS    int // the known part: open and heart-rate-bounded steps count as 0 s
	DistanceM     int
	MaxTargetHR   *int
	// Partial is set when some step is open or heart-rate-bounded, so the
	// real duration is longer than EstimatedS by an unknown amount.
	Partial bool
	// EstimatedFromDefaultPace is set when some distance step has no pace
	// target, so its share of EstimatedS uses the sport's default pace.
	EstimatedFromDefaultPace bool
}

// Invalid is returned by Validate for implausible input.
type Invalid struct{ Reason string }

func (e *Invalid) Error() string { return e.Reason }

// CanonicalJSON returns the stable JSON form of w: struct field order, no
// insignificant whitespace, and nil and empty step lists identical.
func CanonicalJSON(w Workout) ([]byte, error) {
	return json.Marshal(normalize(w))
}

func normalize(w Workout) Workout {
	if w.Steps == nil {
		w.Steps = []Step{} // child lists are omitempty, so nil and empty already agree
	}
	return w
}

// ContentHash is the hex SHA-256 of canonical JSON.
func ContentHash(canonical []byte) string {
	h := sha256.Sum256(canonical)
	return hex.EncodeToString(h[:])
}
