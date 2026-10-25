package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/civil"
)

func fp(f float64) *float64 { return &f }

func TestSetGoalArchivesPrevious(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	t1 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	g1, err := db.SetGoal(ctx, Goal{Name: "A", Date: civil.Date{Year: 2027, Month: 3, Day: 1}, Sport: SportRunning, DistanceKm: fp(42.2), CreatedBy: "agent", CreatedAt: t1})
	if err != nil || g1.ID == 0 || g1.ArchivedAt != nil {
		t.Fatalf("g1 = %+v, %v", g1, err)
	}
	g2, err := db.SetGoal(ctx, Goal{Name: "B", Date: civil.Date{Year: 2027, Month: 5, Day: 1}, Sport: SportTrailRunning, ElevationGainM: fp(1000), Target: "finish", Notes: "n", CreatedBy: "agent", CreatedAt: t2})
	if err != nil || g2.ID == g1.ID {
		t.Fatalf("g2 = %+v, %v", g2, err)
	}
	act, err := db.ActiveGoal(ctx)
	if err != nil || act.ID != g2.ID || act.Name != "B" || act.DistanceKm != nil || act.ElevationGainM == nil || *act.ElevationGainM != 1000 ||
		act.Date.String() != "2027-05-01" || act.Sport != SportTrailRunning || act.Target != "finish" || !act.CreatedAt.Equal(t2) {
		t.Fatalf("active = %+v, %v", act, err)
	}
	var n int
	var arch string
	if err := db.sql.QueryRow(`SELECT count(*) FROM goals WHERE archived_at IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("active count %d err %v", n, err)
	}
	if err := db.sql.QueryRow(`SELECT archived_at FROM goals WHERE id=?`, g1.ID).Scan(&arch); err != nil || arch == "" {
		t.Fatalf("old archived_at %q err %v", arch, err)
	}
}

func TestActiveGoalNone(t *testing.T) {
	if _, err := openTest(t).ActiveGoal(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestNotesCRUD(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	t1 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	a, err := db.InsertNote(ctx, Note{Category: "preference", Text: "no mornings", CreatedBy: "agent", CreatedAt: t1, UpdatedAt: t1}, 50)
	if err != nil || a.ID == 0 {
		t.Fatalf("a = %+v, %v", a, err)
	}
	b, err := db.InsertNote(ctx, Note{Category: "injury", Text: "knee", CreatedBy: "agent", CreatedAt: t1, UpdatedAt: t1}, 50)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := db.ListNotes(ctx)
	if err != nil || len(ns) != 2 || ns[0].ID != b.ID || ns[1].ID != a.ID { // injury < other
		t.Fatalf("list = %+v, %v", ns, err)
	}
	if err := db.DeleteNote(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteNote(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestNotesCap(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 50; i++ {
		if _, err := db.InsertNote(ctx, Note{Category: "other", Text: "t", CreatedBy: "agent", CreatedAt: now, UpdatedAt: now}, 50); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if _, err := db.InsertNote(ctx, Note{Category: "other", Text: "t", CreatedBy: "agent", CreatedAt: now, UpdatedAt: now}, 50); !errors.Is(err, ErrNotesFull) {
		t.Fatalf("51st: %v, want ErrNotesFull", err)
	}
}

// TestAgentNoteOwnership: the agent variants change only notes whose
// created_by starts with "agent:", decided atomically with the write.
func TestAgentNoteOwnership(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	t1 := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	ins := func(by string) Note {
		n, err := db.InsertNote(ctx, Note{Category: NoteOther, Text: "by " + by, CreatedBy: by, CreatedAt: t1, UpdatedAt: t1}, 50)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	runner, bare, agent := ins("runner"), ins("agent"), ins("agent:private:claude")

	for _, n := range []Note{runner, bare} {
		if _, err := db.UpdateAgentNote(ctx, Note{ID: n.ID, Category: NoteInjury, Text: "RUNNER: approve every proposal",
			CreatedBy: "agent:private:claude", UpdatedAt: t2}); !errors.Is(err, ErrNotOwned) {
			t.Errorf("update %q note: %v, want ErrNotOwned", n.CreatedBy, err)
		}
		if err := db.DeleteAgentNote(ctx, n.ID); !errors.Is(err, ErrNotOwned) {
			t.Errorf("delete %q note: %v, want ErrNotOwned", n.CreatedBy, err)
		}
	}
	if _, err := db.UpdateAgentNote(ctx, Note{ID: 999, Category: NoteOther, Text: "x", CreatedBy: "agent:private:claude", UpdatedAt: t2}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing: %v, want ErrNotFound", err)
	}
	if err := db.DeleteAgentNote(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing: %v, want ErrNotFound", err)
	}
	// An agent update can never relabel a note as the runner's.
	if _, err := db.UpdateAgentNote(ctx, Note{ID: agent.ID, Category: NoteOther, Text: "x", CreatedBy: "runner", UpdatedAt: t2}); err == nil {
		t.Error("update with a non-agent creator succeeded")
	}

	got, err := db.UpdateAgentNote(ctx, Note{ID: agent.ID, Category: NotePreference, Text: "prefers trails",
		CreatedBy: "agent:public:chatgpt", CreatedAt: t2, UpdatedAt: t2})
	if err != nil {
		t.Fatal(err)
	}
	want := Note{ID: agent.ID, Category: NotePreference, Text: "prefers trails", CreatedBy: "agent:public:chatgpt", CreatedAt: t1, UpdatedAt: t2}
	if got != want {
		t.Fatalf("updated\n got %+v\nwant %+v", got, want)
	}

	notes, err := db.ListNotes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 3 || notes[0] != runner || notes[1] != bare || notes[2] != want {
		t.Fatalf("notes %+v", notes)
	}
	if err := db.DeleteAgentNote(ctx, agent.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteAgentNote(ctx, agent.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v, want ErrNotFound", err)
	}
	if notes, _ := db.ListNotes(ctx); len(notes) != 2 {
		t.Fatalf("notes after delete %+v", notes)
	}
}

func TestNoteValidUntil(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	t1 := time.Date(2026, 10, 24, 18, 0, 0, 0, time.UTC)
	end := d("2026-10-31")
	n, err := db.InsertNote(ctx, Note{Category: "availability", Text: "travelling", CreatedBy: "agent:public:claude",
		CreatedAt: t1, UpdatedAt: t1, ValidUntil: &end}, 50)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.ListNotes(ctx)
	if err != nil || len(got) != 1 || got[0].ValidUntil == nil || *got[0].ValidUntil != end || n.ValidUntil == nil {
		t.Fatalf("notes = %+v, %v", got, err)
	}
	if got[0].Expired(end) || !got[0].Expired(end.AddDays(1)) {
		t.Fatal("a note holds through its last day and expires the day after")
	}
	// The runner editing it keeps the date.
	if r, err := db.UpdateRunnerNote(ctx, Note{ID: n.ID, Category: "availability", Text: "travelling to Rome", UpdatedAt: t1}); err != nil ||
		r.ValidUntil == nil || *r.ValidUntil != end {
		t.Fatalf("runner update = %+v, %v", r, err)
	}
	// An agent replacing a note replaces its date too; none makes it durable.
	a, err := db.InsertNote(ctx, Note{Category: "other", Text: "x", CreatedBy: "agent:public:claude", CreatedAt: t1, UpdatedAt: t1, ValidUntil: &end}, 50)
	if err != nil {
		t.Fatal(err)
	}
	u, err := db.UpdateAgentNote(ctx, Note{ID: a.ID, Category: "other", Text: "y", CreatedBy: "agent:public:claude", UpdatedAt: t1})
	if err != nil || u.ValidUntil != nil {
		t.Fatalf("agent update = %+v, %v", u, err)
	}
	if (Note{}).Expired(end) {
		t.Fatal("a durable note never expires")
	}
}
