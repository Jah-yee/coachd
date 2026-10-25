package outcome

import (
	"bytes"
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/fitdata"
	"github.com/stefiix92/coachd/internal/fitdata/fittest"
	"github.com/stefiix92/coachd/internal/fitworkout"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/workout"
)

var created = time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)

func date(s string) civil.Date {
	d, err := civil.Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

func ip(v int) *int { return &v }

// easyRun is 10 min warm-up, 25 min at 128–145 bpm, an open cool-down.
func easyRun(name string, activeS int) workout.Workout {
	return workout.Workout{Name: name, Sport: "running", Date: "2026-10-26", Steps: []workout.Step{
		{StepBase: workout.StepBase{Kind: "warmup", Duration: &workout.Duration{Type: "time", TimeS: ip(600)}}},
		{StepBase: workout.StepBase{Kind: "active", Duration: &workout.Duration{Type: "time", TimeS: ip(activeS)},
			Target: &workout.Target{Type: "hr", LowBpm: ip(128), HighBpm: ip(145)}, Notes: "Easy Z2"}},
		{StepBase: workout.StepBase{Kind: "cooldown", Duration: &workout.Duration{Type: "open"},
			Target: &workout.Target{Type: "effort", Cue: "Very easy"}}},
	}}
}

type env struct {
	db  *store.DB
	ctx context.Context
}

func newEnv(t *testing.T) env {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "coachd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return env{db: db, ctx: context.Background()}
}

func (e env) proposal(t *testing.T, id, day string, w workout.Workout, status store.ProposalStatus, at time.Time) store.Proposal {
	t.Helper()
	canon, err := workout.CanonicalJSON(w)
	if err != nil {
		t.Fatal(err)
	}
	p := store.Proposal{ID: id, Date: date(day), Sport: store.SportRunning, Name: w.Name, ContentJSON: canon,
		ContentHash: workout.ContentHash(canon), Status: status, StatusAt: at, CreatedAt: at,
		CreatedLane: "private", CreatedBy: "tok", CreatedClient: "test"}
	ev := store.AuditEvent{At: at, Lane: "private", Actor: "tok", Event: "proposal_" + string(status), Target: id, Outcome: "ok"}
	if err := e.db.InsertProposal(e.ctx, p, ev); err != nil {
		t.Fatal(err)
	}
	return p
}

// activity stores an activity on day that recorded w as a watch would: the
// workout's own FIT steps, one lap per step.
func (e env) activity(t *testing.T, id int64, day string, w workout.Workout) {
	t.Helper()
	wf, err := fitworkout.Encode(w, created)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 26, 6, 0, 0, 0, time.UTC)
	b, err := fittest.Build(fittest.Options{Start: start, Records: 300, HR: 135, SpeedMS: 3,
		LapRecords: []int{100, 150, 50}, LapSteps: []uint16{0, 1, 2}, Workout: wf})
	if err != nil {
		t.Fatal(err)
	}
	a, err := fitdata.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if a.Workout == nil {
		t.Fatal("no workout decoded")
	}
	act := store.Activity{ID: id, StartLocal: start, StartUTC: start, Date: date(day), WeekStart: date(day).WeekStart(),
		Sport: store.SportRunning, FITStatus: store.FITStored, FITPath: "fit/x.fit"}
	if err := e.db.UpsertActivity(e.ctx, act, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := e.db.PutActivityDetail(e.ctx, id, store.NewActivityDetail(a)); err != nil {
		t.Fatal(err)
	}
}

func (e env) proposalFor(t *testing.T, id int64) *string {
	t.Helper()
	ws, err := e.db.ActivityWorkouts(e.ctx, date("2000-01-01"), date("2100-01-01"))
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range ws {
		if w.ID == id {
			got, err := Linker{DB: e.db}.ProposalFor(e.ctx, w)
			if err != nil {
				t.Fatal(err)
			}
			return got
		}
	}
	t.Fatalf("activity %d has no workout", id)
	return nil
}

func (e env) activitiesFor(t *testing.T, ps ...store.Proposal) map[string][]int64 {
	t.Helper()
	got, err := Linker{DB: e.db}.ActivitiesFor(e.ctx, ps)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func is(p *string, id string) bool { return p != nil && *p == id }

func TestFingerprintMatchesRecordedWorkout(t *testing.T) {
	w := easyRun("Easy run 40 min – návrat", 1500)
	p := store.Proposal{CreatedAt: created}
	canon, _ := workout.CanonicalJSON(w)
	p.ContentJSON = canon
	fp, err := Fingerprint(p)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.activity(t, 1, "2026-10-26", easyRun("A watch's shorter name", 1500))
	ws, err := e.db.ActivityWorkouts(e.ctx, date("2026-10-26"), date("2026-10-26"))
	if err != nil || len(ws) != 1 {
		t.Fatalf("workouts = %v, %v", ws, err)
	}
	if ws[0].Fingerprint != fp {
		t.Fatal("the recorded workout must fingerprint like the proposal")
	}
	other, _ := workout.CanonicalJSON(easyRun("Easy", 1560))
	if fp2, _ := Fingerprint(store.Proposal{ContentJSON: other, CreatedAt: created}); fp2 == fp {
		t.Fatal("a different step must change the fingerprint")
	}
}

func TestLinkDeliveredProposal(t *testing.T) {
	e := newEnv(t)
	p := e.proposal(t, "p1", "2026-10-26", easyRun("Easy", 1500), store.ProposalDelivered, created)
	e.activity(t, 1, "2026-10-26", easyRun("Easy", 1500))
	if got := e.proposalFor(t, 1); !is(got, "p1") {
		t.Fatalf("proposal = %v, want p1", got)
	}
	if got := e.activitiesFor(t, p); !reflect.DeepEqual(got, map[string][]int64{"p1": {1}}) {
		t.Fatalf("activities = %v", got)
	}
}

func TestLinkNeedsSameStructure(t *testing.T) {
	e := newEnv(t)
	p := e.proposal(t, "p1", "2026-10-26", easyRun("Easy", 1500), store.ProposalDelivered, created)
	e.activity(t, 1, "2026-10-26", easyRun("Easy", 1560))
	if got := e.proposalFor(t, 1); got != nil {
		t.Fatalf("proposal = %v, want none", *got)
	}
	if got := e.activitiesFor(t, p); len(got["p1"]) != 0 {
		t.Fatalf("activities = %v", got)
	}
}

func TestLinkOnlyDelivered(t *testing.T) {
	e := newEnv(t)
	for i, s := range []store.ProposalStatus{store.ProposalApproved, store.ProposalSuperseded, store.ProposalCancelled, store.ProposalPending} {
		e.proposal(t, string(rune('a'+i)), "2026-10-26", easyRun("Easy", 1500), s, created)
	}
	e.activity(t, 1, "2026-10-26", easyRun("Easy", 1500))
	if got := e.proposalFor(t, 1); got != nil {
		t.Fatalf("proposal = %v, want none", *got)
	}
}

func TestLinkWindow(t *testing.T) {
	e := newEnv(t)
	e.proposal(t, "p1", "2026-10-22", easyRun("Easy", 1500), store.ProposalDelivered, created)
	e.activity(t, 1, "2026-10-25", easyRun("Easy", 1500)) // 3 days later: linked
	e.activity(t, 2, "2026-10-26", easyRun("Easy", 1500)) // 4 days later: not
	e.activity(t, 3, "2026-10-19", easyRun("Easy", 1500)) // 3 days earlier: linked
	if got := e.proposalFor(t, 1); !is(got, "p1") {
		t.Fatalf("3 days after: %v", got)
	}
	if got := e.proposalFor(t, 2); got != nil {
		t.Fatalf("4 days after: %v", *got)
	}
	if got := e.proposalFor(t, 3); !is(got, "p1") {
		t.Fatalf("3 days before: %v", got)
	}
}

// The same easy run delivered for Monday and Wednesday: each run links to
// the nearer proposal, and both directions agree.
func TestLinkNearestDate(t *testing.T) {
	e := newEnv(t)
	mon := e.proposal(t, "mon", "2026-10-26", easyRun("Easy", 1500), store.ProposalDelivered, created)
	wed := e.proposal(t, "wed", "2026-10-28", easyRun("Easy", 1500), store.ProposalDelivered, created.Add(time.Hour))
	e.activity(t, 1, "2026-10-26", easyRun("Easy", 1500))
	e.activity(t, 2, "2026-10-28", easyRun("Easy", 1500))
	e.activity(t, 3, "2026-10-29", easyRun("Easy", 1500)) // the runner did it twice
	for id, want := range map[int64]string{1: "mon", 2: "wed", 3: "wed"} {
		if got := e.proposalFor(t, id); !is(got, want) {
			t.Fatalf("activity %d: %v, want %s", id, got, want)
		}
	}
	got := e.activitiesFor(t, mon, wed)
	if !reflect.DeepEqual(got, map[string][]int64{"mon": {1}, "wed": {2, 3}}) {
		t.Fatalf("activities = %v", got)
	}
	// Asking for one proposal gives the same answer as asking for both.
	if got := e.activitiesFor(t, wed); !reflect.DeepEqual(got, map[string][]int64{"wed": {2, 3}}) {
		t.Fatalf("activities for wed alone = %v", got)
	}
}

// Equally near proposals: the earlier created wins.
func TestLinkTieEarlierCreated(t *testing.T) {
	e := newEnv(t)
	e.proposal(t, "late", "2026-10-25", easyRun("Easy", 1500), store.ProposalDelivered, created.Add(time.Hour))
	e.proposal(t, "early", "2026-10-27", easyRun("Easy", 1500), store.ProposalDelivered, created)
	e.activity(t, 1, "2026-10-26", easyRun("Easy", 1500))
	if got := e.proposalFor(t, 1); !is(got, "early") {
		t.Fatalf("proposal = %v, want early", got)
	}
}

func TestActivitiesForIgnoresUndelivered(t *testing.T) {
	e := newEnv(t)
	p := e.proposal(t, "p1", "2026-10-26", easyRun("Easy", 1500), store.ProposalApproved, created)
	e.activity(t, 1, "2026-10-26", easyRun("Easy", 1500))
	if got := e.activitiesFor(t, p); len(got) != 0 {
		t.Fatalf("activities = %v", got)
	}
	if got := e.activitiesFor(t); len(got) != 0 {
		t.Fatalf("no proposals: %v", got)
	}
}
