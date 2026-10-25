package deviceapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

func ip(v int) *int { return &v }

type detailBody struct {
	ID        string   `json:"id"`
	Date      string   `json:"date"`
	Name      string   `json:"name"`
	Sport     string   `json:"sport"`
	DurationS int      `json:"duration_s"`
	DistanceM int      `json:"distance_m"`
	Partial   bool     `json:"partial"`
	Steps     []string `json:"steps"`
	Notes     string   `json:"notes"`
	Why       string   `json:"why"`
}

func TestWorkoutDetail(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := ts.pair(t)
	w := workout.Workout{Name: "Tempo 5x1 km", Sport: "running", Date: ts.today().AddDays(2).String(), Notes: "flat route",
		Steps: []workout.Step{
			{StepBase: workout.StepBase{Kind: "warmup", Duration: &workout.Duration{Type: "time", TimeS: ip(900)},
				Target: &workout.Target{Type: "hr", LowBpm: ip(120), HighBpm: ip(140)}}},
			{StepBase: workout.StepBase{Kind: "repeat"}, Count: 5, Steps: []workout.Step2{
				{StepBase: workout.StepBase{Kind: "active", Duration: &workout.Duration{Type: "distance", DistanceM: ip(1000)},
					Target: &workout.Target{Type: "pace", LowPaceSPerKm: ip(245), HighPaceSPerKm: ip(255)}}},
				{StepBase: workout.StepBase{Kind: "recovery", Duration: &workout.Duration{Type: "time", TimeS: ip(120)}}},
			}},
			{StepBase: workout.StepBase{Kind: "cooldown", Duration: &workout.Duration{Type: "open"}, Notes: "jog home"}},
		}}
	p := ts.addWorkout(t, w, "Threshold work before the race block.", store.ProposalApproved)

	rec := ts.do(t, "GET", "/device/v1/workouts/"+p.ID, tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	got := decodeBody[detailBody](t, rec)
	want := detailBody{ID: p.ID, Date: w.Date, Name: "Tempo 5x1 km", Sport: "running",
		DurationS: 900 + 5*(255+120), DistanceM: 5000, Partial: true, // distance at the slower pace bound
		Steps: []string{"Warm-up · 15:00 · HR 120–140 bpm", "Repeat 5×", "  Active · 1 km · pace 4:05–4:15 /km",
			"  Recovery · 2:00", "Cool-down · open (lap button)", "  note: jog home"},
		Notes: "flat route", Why: "Threshold work before the race block."}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("detail\n got %+v\nwant %+v", got, want)
	}
	// Reading the detail is not a delivery: nothing moves, nothing is audited.
	if s := ts.status(t, p.ID); s != store.ProposalApproved {
		t.Errorf("status after detail = %s", s)
	}
	if n := ts.auditCount(t, "proposal_delivering", p.ID); n != 0 {
		t.Errorf("%d fetch audits", n)
	}
	if strings.Contains(ts.logs.String(), "Threshold") || strings.Contains(ts.logs.String(), "jog home") {
		t.Errorf("logs carry agent text: %s", ts.logs.String())
	}
}

func TestWorkoutDetailCaps(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := ts.pair(t)
	var steps []workout.Step
	for range 45 {
		steps = append(steps, workout.Step{StepBase: workout.StepBase{Kind: "active",
			Duration: &workout.Duration{Type: "time", TimeS: ip(60)}, Target: &workout.Target{Type: "effort", Cue: strings.Repeat("e", 100)}}})
	}
	w := workout.Workout{Name: "Many", Sport: "running", Date: ts.today().String(), Notes: strings.Repeat("n", 600), Steps: steps}
	p := ts.addWorkout(t, w, strings.Repeat("ü", 700), store.ProposalDelivered)
	got := decodeBody[detailBody](t, ts.do(t, "GET", "/device/v1/workouts/"+p.ID, tok, ""))
	if len(got.Steps) != 40 || got.Steps[39] != "+6 more" {
		t.Fatalf("%d steps, last %q", len(got.Steps), got.Steps[len(got.Steps)-1])
	}
	if l := []rune(got.Steps[0]); len(l) != 120 || l[119] != '…' {
		t.Errorf("step line %d runes: %q", len(l), got.Steps[0])
	}
	if r := []rune(got.Why); len(r) != 500 || r[499] != '…' || r[0] != 'ü' {
		t.Errorf("why %d runes", len(r))
	}
	if r := []rune(got.Notes); len(r) != 500 || r[499] != '…' {
		t.Errorf("notes %d runes", len(r))
	}
	if got.Partial || got.DurationS != 45*60 {
		t.Errorf("duration %d partial %v", got.DurationS, got.Partial)
	}
}

func TestWorkoutDetailOnlyDesired(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := ts.pair(t)
	today := ts.today()
	pending := ts.addProposal(t, "Pending", today, store.ProposalPending)
	past := ts.addProposal(t, "Past", today.AddDays(-1), store.ProposalApproved)
	gone := ts.addProposal(t, "Gone", today, store.ProposalApproved)
	ts.withdraw(t, gone.ID)
	for name, id := range map[string]string{"pending": pending.ID, "past": past.ID, "withdrawn": gone.ID,
		"unknown": strings.Repeat("0", 32), "not hex": "Zz"} {
		rec := ts.do(t, "GET", "/device/v1/workouts/"+id, tok, "")
		if rec.Code != http.StatusNotFound || jsonError(t, rec) != "not found" {
			t.Errorf("%s: status %d, body %s", name, rec.Code, rec.Body)
		}
	}
	if rec := ts.do(t, "GET", "/device/v1/workouts/"+pending.ID, "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status %d", rec.Code)
	}
}

func TestMessages(t *testing.T) {
	ts := newTestServer(t)
	tok, _ := ts.pair(t)
	if body := strings.TrimSpace(ts.do(t, "GET", "/device/v1/messages", tok, "").Body.String()); body != `{"messages":[]}` {
		t.Fatalf("empty: %s", body)
	}
	now := ts.clock.Now()
	add := func(text string, at time.Time) store.Message {
		m, err := ts.db.InsertMessage(t.Context(), store.Message{Text: text, CreatedBy: "agent:private:claude", CreatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	add("too old", now.Add(-7*24*time.Hour-time.Second))
	var ids []int64
	for i := range 6 {
		ids = append(ids, add(fmt.Sprintf("canary-msg %d", i), now.Add(time.Duration(i-6)*time.Hour)).ID)
	}
	rec := ts.do(t, "GET", "/device/v1/messages", tok, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	want := `{"messages":[`
	for i := 5; i >= 1; i-- {
		at := now.Add(time.Duration(i-6) * time.Hour).In(ts.tz).Format(time.RFC3339)
		want += fmt.Sprintf(`{"id":%d,"at":"%s","text":"canary-msg %d"}`, ids[i], at, i)
		if i > 1 {
			want += ","
		}
	}
	want += `]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Fatalf("messages\n got %s\nwant %s", got, want)
	}
	if strings.Contains(ts.logs.String(), "canary-msg") {
		t.Errorf("logs carry message text: %s", ts.logs.String())
	}
	if rec := ts.do(t, "GET", "/device/v1/messages", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: status %d", rec.Code)
	}
	if got := ts.counter(t, "coachd.device.requests", "endpoint", "outcome"); got["messages/ok"] != 2 || got["messages/unauthorized"] != 1 {
		t.Errorf("requests = %v", got)
	}
}
