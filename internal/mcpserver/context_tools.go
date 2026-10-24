package mcpserver

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stefiix92/coachd/internal/auth"
	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/guidance"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

// Goal and note limits.
const (
	maxGoalNameRunes  = 100
	maxGoalTextRunes  = 500 // target and notes
	maxGoalYears      = 3
	maxGoalDistanceKm = 500
	maxGoalElevationM = 20000
	maxEchoRunes      = 40
)

var noteCategories = []store.NoteCategory{store.NoteInjury, store.NoteAvailability, store.NotePreference, store.NoteOther}

func (s *svc) registerContext(srv *mcp.Server) {
	read := &mcp.ToolAnnotations{ReadOnlyHint: true}

	addTool(s, srv, &mcp.Tool{Name: "get_goal", Annotations: read, Description: "The runner's active goal: the " +
		"target event's name, date, sport, distance and elevation gain, plus free-text target and notes. goal is " +
		"null when none is set. created_by says whether an agent or the runner set it." + descSuffix},
		auth.ScopeRead, s.getGoal)

	addTool(s, srv, &mcp.Tool{Name: "set_goal", Annotations: writeAnnotations(true), Description: "Set the " +
		"runner's goal; the previous goal is archived. date is today up to 3 years ahead; distance_km (above 0, at " +
		"most 500) and elevation_gain_m (0 to 20000) are optional; name is 1 to 100 characters, target and notes " +
		"at most 500. Returns the stored goal and facts: weeks until the goal date, average weekly running distance " +
		"and elevation gain over the 4 full Monday-start weeks before this week, the longest run in the last 30 " +
		"days and the weeks of stored history." + localWrite + descSuffix},
		auth.ScopeWrite, s.setGoal)

	addTool(s, srv, &mcp.Tool{Name: "get_notes", Annotations: read, Description: "Durable notes about the runner " +
		"(injury, availability, preference or other), by category then id. created_by says whether an agent or " +
		"the runner wrote each note." + descSuffix},
		auth.ScopeRead, s.getNotes)

	addTool(s, srv, &mcp.Tool{Name: "upsert_note", Annotations: writeAnnotations(false), Description: "Create a " +
		"note, or with id replace that note's category and text (repeating such an update has no further effect). " +
		"Agents can only edit notes written by agents (created_by agent); the runner's notes are read-only here, so " +
		"add a new note instead. text is 1 to 500 characters. At most 50 notes are kept: when full, update or " +
		"delete one first." + localWrite + descSuffix},
		auth.ScopeWrite, s.upsertNote)

	addTool(s, srv, &mcp.Tool{Name: "delete_note", Annotations: writeAnnotations(true), Description: "Delete the " +
		"note with this id. Agents can only delete notes written by agents (created_by agent)." + localWrite + descSuffix},
		auth.ScopeWrite, s.deleteNote)
}

// Inputs.

type setGoalInput struct {
	Name           string   `json:"name" jsonschema:"Goal event name, 1 to 100 characters"`
	Date           string   `json:"date" jsonschema:"Goal date, YYYY-MM-DD in the runner's timezone; today up to 3 years ahead"`
	Sport          string   `json:"sport" jsonschema:"running, trail_running, walking, hiking, cycling, strength or other"`
	DistanceKm     *float64 `json:"distance_km,omitempty" jsonschema:"Event distance in kilometres: above 0, at most 500"`
	ElevationGainM *float64 `json:"elevation_gain_m,omitempty" jsonschema:"Event elevation gain in metres, 0 to 20000"`
	Target         string   `json:"target,omitempty" jsonschema:"Free-text target such as a finish time, at most 500 characters"`
	Notes          string   `json:"notes,omitempty" jsonschema:"Free-text notes about the event, at most 500 characters"`
}

type upsertNoteInput struct {
	ID       int64  `json:"id,omitempty" jsonschema:"Note to update, as returned by get_notes; omit to create a note"`
	Category string `json:"category" jsonschema:"injury, availability, preference or other"`
	Text     string `json:"text" jsonschema:"Note text, 1 to 500 characters"`
}

type deleteNoteInput struct {
	ID int64 `json:"id" jsonschema:"Note to delete, as returned by get_notes"`
}

// Outputs.

type goalOut struct {
	Name           string   `json:"name" jsonschema:"Goal event name"`
	Date           string   `json:"date" jsonschema:"Goal date, YYYY-MM-DD"`
	Sport          string   `json:"sport" jsonschema:"running, trail_running, walking, hiking, cycling, strength or other"`
	DistanceKm     *float64 `json:"distance_km" jsonschema:"Event distance in kilometres; null if not set"`
	ElevationGainM *float64 `json:"elevation_gain_m" jsonschema:"Event elevation gain in metres; null if not set"`
	Target         string   `json:"target" jsonschema:"Free-text target; empty if not set"`
	Notes          string   `json:"notes" jsonschema:"Free-text notes; empty if not set"`
	CreatedBy      string   `json:"created_by" jsonschema:"Who set the goal: agent or runner"`
	CreatedAt      string   `json:"created_at" jsonschema:"When the goal was set, RFC 3339 in the runner's timezone"`
}

type getGoalOut struct {
	Goal *goalOut `json:"goal" jsonschema:"The active goal; null when none is set"`
}

type goalFactsOut struct {
	WeeksUntilGoal          int     `json:"weeks_until_goal" jsonschema:"Days from today to the goal date divided by 7, rounded up; 0 when the goal is today"`
	Last4WeeksAvgKm         float64 `json:"last_4_weeks_avg_km" jsonschema:"Average weekly running and trail running distance over the 4 full Monday-start weeks before this week (missing weeks count as 0), kilometres"`
	Last4WeeksAvgElevationM int     `json:"last_4_weeks_avg_elevation_m" jsonschema:"Average weekly elevation gain over all sports in the same 4 weeks, metres"`
	LongestRun30dKm         float64 `json:"longest_run_30d_km" jsonschema:"Longest running or trail running distance dated in the last 30 days, today included, kilometres"`
	WeeksOfHistory          int     `json:"weeks_of_history" jsonschema:"Whole weeks from the first stored date to today"`
}

type setGoalOut struct {
	Goal  goalOut      `json:"goal" jsonschema:"The stored goal, now the active one"`
	Facts goalFactsOut `json:"facts" jsonschema:"Facts about the stored history at the time the goal was set"`
}

type noteOut struct {
	ID        int64  `json:"id" jsonschema:"Note ID"`
	Category  string `json:"category" jsonschema:"injury, availability, preference or other"`
	Text      string `json:"text" jsonschema:"Note text"`
	CreatedBy string `json:"created_by" jsonschema:"Who wrote the note: agent or runner"`
	CreatedAt string `json:"created_at" jsonschema:"When the note was created, RFC 3339 in the runner's timezone"`
	UpdatedAt string `json:"updated_at" jsonschema:"When the note was last changed, RFC 3339 in the runner's timezone"`
}

type notesOut struct {
	Notes []noteOut `json:"notes" jsonschema:"All notes, by category then id"`
}

type noteResultOut struct {
	Note noteOut `json:"note" jsonschema:"The stored note"`
}

type deleteNoteOut struct {
	Deleted bool `json:"deleted" jsonschema:"Always true: the note is gone"`
}

// Handlers.

func (s *svc) getGoal(ctx context.Context, _ noInput) (getGoalOut, error) {
	g, err := s.DB.ActiveGoal(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return getGoalOut{}, nil
	}
	if err != nil {
		return getGoalOut{}, err
	}
	return getGoalOut{Goal: ptr(s.goal(g))}, nil
}

func (s *svc) setGoal(ctx context.Context, in setGoalInput) (setGoalOut, error) {
	id, _ := auth.IdentityFrom(ctx)
	now := s.Now()
	today := civil.Of(now, s.TZ)
	g, err := goalFromInput(in, today)
	if err != nil {
		return setGoalOut{}, err
	}
	g.CreatedBy, g.CreatedAt = agentCreator(id), now.UTC()
	// Facts first: a failure here must not leave a stored goal behind an error.
	facts, err := s.goalFacts(ctx, today, g.Date)
	if err != nil {
		return setGoalOut{}, err
	}
	stored, err := s.DB.SetGoal(ctx, g)
	if err != nil {
		return setGoalOut{}, err
	}
	return setGoalOut{Goal: s.goal(stored), Facts: goalFactsOut{WeeksUntilGoal: facts.WeeksUntilGoal,
		Last4WeeksAvgKm: round(facts.Last4WeeksAvgKm, 1), Last4WeeksAvgElevationM: whole(facts.Last4WeeksAvgElevM),
		LongestRun30dKm: round(facts.LongestRun30dKm, 1), WeeksOfHistory: facts.WeeksOfHistory}}, nil
}

// goalFromInput validates set_goal input; errors are invalid_input with the field path.
func goalFromInput(in setGoalInput, today civil.Date) (store.Goal, error) {
	if err := asInvalid(workout.CheckName("name", in.Name, maxGoalNameRunes)); err != nil {
		return store.Goal{}, err
	}
	date, err := civil.Parse(in.Date)
	if err != nil {
		return store.Goal{}, invalidf("date: %q is not a valid YYYY-MM-DD date", clip(in.Date))
	}
	last := civil.Date{Year: today.Year + maxGoalYears, Month: today.Month, Day: today.Day}.AddDays(0)
	if date.Before(today) || last.Before(date) {
		return store.Goal{}, invalidf("date: %s is outside the allowed window %s to %s", date, today, last)
	}
	sport := store.Sport(in.Sport)
	if !validSport(sport) {
		return store.Goal{}, invalidf("sport: %q is not one of %s", clip(in.Sport), joinSports("|"))
	}
	if d := in.DistanceKm; d != nil && (*d <= 0 || *d > maxGoalDistanceKm) {
		return store.Goal{}, invalidf("distance_km: %g is outside the allowed range: above 0 and at most %d", *d, maxGoalDistanceKm)
	}
	if e := in.ElevationGainM; e != nil && (*e < 0 || *e > maxGoalElevationM) {
		return store.Goal{}, invalidf("elevation_gain_m: %g is outside the allowed range 0 to %d", *e, maxGoalElevationM)
	}
	if err := checkText("target", in.Target, maxGoalTextRunes); err != nil {
		return store.Goal{}, err
	}
	if err := checkText("notes", in.Notes, maxGoalTextRunes); err != nil {
		return store.Goal{}, err
	}
	return store.Goal{Name: in.Name, Date: date, Sport: sport, DistanceKm: in.DistanceKm, ElevationGainM: in.ElevationGainM,
		Target: in.Target, Notes: in.Notes}, nil
}

// goalFacts computes set_goal's facts. The 30-day longest run comes from the
// runs themselves, not from today's day_metrics row, which does not exist
// between local midnight and the first sync of the day.
func (s *svc) goalFacts(ctx context.Context, today, goalDate civil.Date) (guidance.GoalFacts, error) {
	weeks, err := s.DB.WeeklyVolume(ctx, today.AddDays(-35), today)
	if err != nil {
		return guidance.GoalFacts{}, err
	}
	runs, err := s.DB.Runs(ctx, today.AddDays(-29), today)
	if err != nil {
		return guidance.GoalFacts{}, err
	}
	longest := store.DayMetrics{Date: today}
	for _, r := range runs {
		longest.LongestRun30dM = math.Max(longest.LongestRun30dM, r.DistanceM)
	}
	cov, err := s.DB.Coverage(ctx, today)
	if err != nil {
		return guidance.GoalFacts{}, err
	}
	return guidance.ComputeGoalFacts(today, goalDate, weeks, &longest, cov), nil
}

func (s *svc) getNotes(ctx context.Context, _ noInput) (notesOut, error) {
	notes, err := s.DB.ListNotes(ctx)
	if err != nil {
		return notesOut{}, err
	}
	out := notesOut{Notes: make([]noteOut, 0, len(notes))}
	for _, n := range notes {
		out.Notes = append(out.Notes, s.note(n))
	}
	return out, nil
}

func (s *svc) upsertNote(ctx context.Context, in upsertNoteInput) (noteResultOut, error) {
	cat := store.NoteCategory(in.Category)
	if !validNoteCategory(cat) {
		names := make([]string, len(noteCategories))
		for i, c := range noteCategories {
			names[i] = string(c)
		}
		return noteResultOut{}, invalidf("category: %q is not one of %s", clip(in.Category), strings.Join(names, "|"))
	}
	if in.Text == "" {
		return noteResultOut{}, invalidf("text: required")
	}
	if err := checkText("text", in.Text, store.MaxNoteRunes); err != nil {
		return noteResultOut{}, err
	}
	id, _ := auth.IdentityFrom(ctx)
	now := s.Now().UTC()
	n := store.Note{Category: cat, Text: in.Text, CreatedBy: agentCreator(id), CreatedAt: now, UpdatedAt: now}
	var err error
	if in.ID == 0 {
		n, err = s.DB.InsertNote(ctx, n, store.MaxNotes) // ID 0: insert
	} else {
		n.ID = in.ID
		n, err = s.DB.UpdateAgentNote(ctx, n) // the editing agent becomes the author
	}
	if errors.Is(err, store.ErrNotesFull) {
		return noteResultOut{}, s.notesFull(ctx)
	}
	if err := noteError(err, in.ID); err != nil {
		return noteResultOut{}, err
	}
	return noteResultOut{Note: s.note(n)}, nil
}

// notesFull is the error for a new note when the cap is reached. Agents can
// free a slot only among agent-written notes (store.UpdateAgentNote and
// DeleteAgentNote change only created_by "agent:..." rows), so when there is
// none the runner has to.
func (s *svc) notesFull(ctx context.Context) error {
	notes, err := s.DB.ListNotes(ctx)
	if err != nil {
		return err
	}
	for _, n := range notes {
		if strings.HasPrefix(n.CreatedBy, "agent:") {
			return invalidf("notes are full (%d of %d); update or delete an existing note first", store.MaxNotes, store.MaxNotes)
		}
	}
	return invalidf("notes are full (%d) and all are runner-written; the runner can remove notes on coachd's page", store.MaxNotes)
}

// deleteNote deletes a note an agent wrote; the runner's notes are refused.
func (s *svc) deleteNote(ctx context.Context, in deleteNoteInput) (deleteNoteOut, error) {
	if err := noteError(s.DB.DeleteAgentNote(ctx, in.ID), in.ID); err != nil {
		return deleteNoteOut{}, err
	}
	return deleteNoteOut{Deleted: true}, nil
}

// noteError maps the agent note writes' errors to tool errors.
func noteError(err error, id int64) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return invalidf("note not found")
	case errors.Is(err, store.ErrNotOwned):
		return invalidf("note %d was written by the runner; agents can't edit or delete it — add a new note instead", id)
	}
	return err
}

func (s *svc) goal(g store.Goal) goalOut {
	return goalOut{Name: g.Name, Date: g.Date.String(), Sport: string(g.Sport), DistanceKm: g.DistanceKm,
		ElevationGainM: g.ElevationGainM, Target: g.Target, Notes: g.Notes, CreatedBy: creatorLabel(g.CreatedBy),
		CreatedAt: s.ts(g.CreatedAt)}
}

func (s *svc) note(n store.Note) noteOut {
	return noteOut{ID: n.ID, Category: string(n.Category), Text: n.Text, CreatedBy: creatorLabel(n.CreatedBy),
		CreatedAt: s.ts(n.CreatedAt), UpdatedAt: s.ts(n.UpdatedAt)}
}

func validNoteCategory(c store.NoteCategory) bool {
	for _, v := range noteCategories {
		if v == c {
			return true
		}
	}
	return false
}

// agentCreator is the stored created_by of an agent write: agent:<lane>:<client>.
func agentCreator(id auth.Identity) string { return "agent:" + string(id.Lane) + ":" + id.Client }

// creatorLabel is the created_by agents see: "runner" only when the stored
// value starts with "runner", "agent" for everything else (prefix match only;
// "agent:public:runner-ish" is an agent), so text of unknown origin is never
// presented as the runner's own. Agents may change only rows whose stored
// value starts with "agent:" (store.UpdateAgentNote), so no row is both
// labelled runner and editable by an agent.
func creatorLabel(stored string) string {
	if strings.HasPrefix(stored, "runner") {
		return "runner"
	}
	return "agent"
}

// checkText applies workout.CheckText (rune limit, no control characters but
// newline and tab) as an invalid_input error.
func checkText(path, s string, max int) error {
	return asInvalid(workout.CheckText(path, s, max))
}

// asInvalid turns a *workout.Invalid into an invalid_input error.
func asInvalid(err error) error {
	var inv *workout.Invalid
	if errors.As(err, &inv) {
		return invalidf("%s", inv.Reason)
	}
	return err
}

// clip bounds an echoed agent string so error messages stay small.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxEchoRunes {
		return s
	}
	return string([]rune(s)[:maxEchoRunes]) + "…"
}

func joinSports(sep string) string {
	names := make([]string, len(sports))
	for i, sp := range sports {
		names[i] = string(sp)
	}
	return strings.Join(names, sep)
}
