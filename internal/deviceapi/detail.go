package deviceapi

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/stefiix92/coachd/internal/fitworkout"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

// Detail and message limits: what a watch screen can show.
const (
	maxDetailLines   = 40  // the last line says how many more there are
	maxLineRunes     = 120 // one step line, indent included
	maxDetailText    = 500 // notes and why
	maxMessages      = 5
	messageMaxAge    = 7 * 24 * time.Hour
	detailIndentUnit = "  "
)

// workoutDetail answers GET /device/v1/workouts/{id}. Notes, Why and the
// effort cues and step notes in Steps are agent text.
type workoutDetail struct {
	ID        string   `json:"id"`
	Date      string   `json:"date"`
	Name      string   `json:"name"` // the FIT wkt_name, as in the desired set
	Sport     string   `json:"sport"`
	DurationS int      `json:"duration_s"` // the known part (workout.Plan.EstimatedS)
	DistanceM int      `json:"distance_m"`
	Partial   bool     `json:"partial"` // open or heart-rate-bounded steps: the duration is longer
	Steps     []string `json:"steps"`
	Notes     string   `json:"notes"`
	Why       string   `json:"why"`
}

// detail serves GET /device/v1/workouts/{id}: 404 unless the id is in the
// current desired set. It records nothing: reading a detail is not a delivery.
func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validHexID(id) {
		notFound(w, r)
		return
	}
	_, ps, err := s.desiredWorkouts(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	i := slices.IndexFunc(ps, func(p store.Proposal) bool { return p.ID == id })
	if i < 0 {
		notFound(w, r)
		return
	}
	p := ps[i]
	var wk workout.Workout
	if err := json.Unmarshal(p.ContentJSON, &wk); err != nil {
		s.internalError(w, r, err)
		return
	}
	plan, err := workout.PlanOf(wk, workout.DefaultLimits)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, workoutDetail{ID: p.ID, Date: p.Date.String(), Name: fitworkout.WorkoutName(p.Name),
		Sport: string(p.Sport), DurationS: plan.EstimatedS, DistanceM: plan.DistanceM, Partial: plan.Partial,
		Steps: detailLines(workout.Describe(wk)), Notes: cut(wk.Notes, maxDetailText), Why: cut(p.Rationale, maxDetailText)})
}

// detailLines indents the described lines, cuts each to maxLineRunes and
// keeps at most maxDetailLines, the last of which then counts the rest.
func detailLines(lines []workout.Line) []string {
	out := make([]string, 0, min(len(lines), maxDetailLines))
	for i, l := range lines {
		if len(lines) > maxDetailLines && i == maxDetailLines-1 {
			return append(out, "+"+strconv.Itoa(len(lines)-i)+" more")
		}
		out = append(out, cut(strings.Repeat(detailIndentUnit, l.Indent)+l.Text, maxLineRunes))
	}
	return out
}

// cut returns s, or its first n-1 runes and "…" when it is longer than n runes.
func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// messageList answers GET /device/v1/messages. Text is agent text.
type messageList struct {
	Messages []deviceMessage `json:"messages"`
}

type deviceMessage struct {
	ID   int64  `json:"id"`
	At   string `json:"at"` // RFC 3339 in TZ
	Text string `json:"text"`
}

// messages serves GET /device/v1/messages: the newest maxMessages from the
// last 7 days, newest first.
func (s *Server) messages(w http.ResponseWriter, r *http.Request) {
	ms, err := s.d.DB.RecentMessages(r.Context(), s.d.Now().Add(-messageMaxAge), maxMessages)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := messageList{Messages: make([]deviceMessage, 0, len(ms))}
	for _, m := range ms {
		out.Messages = append(out.Messages, deviceMessage{ID: m.ID, At: m.CreatedAt.In(s.d.TZ).Format(time.RFC3339), Text: m.Text})
	}
	writeJSON(w, http.StatusOK, out)
}
