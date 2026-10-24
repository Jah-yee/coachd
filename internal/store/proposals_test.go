package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/civil"
)

func mkProp(id, date string, created time.Time, status ProposalStatus) Proposal {
	d, _ := civil.Parse(date)
	return Proposal{ID: id, Date: d, Sport: SportRunning, Name: "easy " + id, ContentJSON: []byte(`{"steps":[]}`), ContentHash: "h" + id,
		Rationale: "r", WarningsJSON: []byte(`["w"]`), Status: status, StatusAt: created, CreatedAt: created,
		CreatedLane: "private", CreatedBy: "agent", CreatedClient: "claude"}
}

func insertProp(t *testing.T, db *DB, p Proposal) {
	t.Helper()
	ev := AuditEvent{At: p.CreatedAt, Lane: p.CreatedLane, Actor: p.CreatedBy, Client: p.CreatedClient, Event: "proposal_pending", Target: p.ID, Outcome: "ok"}
	if err := db.InsertProposal(context.Background(), p, ev); err != nil {
		t.Fatal(err)
	}
}

func propAuditCount(t *testing.T, db *DB, event, target string) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM audit_log WHERE event=? AND target=?`, event, target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestProposalInsertGetList(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	p1 := mkProp("a1", "2026-10-25", t0, ProposalPending)
	p2 := mkProp("a2", "2026-10-24", t0.Add(time.Minute), ProposalPending)
	p3 := mkProp("a3", "2026-10-25", t0.Add(2*time.Minute), ProposalPending)
	rep := "a1"
	p3.Replaces = &rep
	p3.Status = ProposalCancelled
	for _, p := range []Proposal{p1, p2, p3} {
		insertProp(t, db, p)
	}
	if propAuditCount(t, db, "proposal_pending", "a1") != 1 {
		t.Fatal("audit row missing")
	}
	got, err := db.GetProposal(ctx, "a3")
	if err != nil || got.Replaces == nil || *got.Replaces != "a1" || got.Status != ProposalCancelled || string(got.ContentJSON) != `{"steps":[]}` ||
		string(got.WarningsJSON) != `["w"]` || got.Date.String() != "2026-10-25" || !got.CreatedAt.Equal(p3.CreatedAt) || got.CreatedClient != "claude" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := db.GetProposal(ctx, "zz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	from, _ := civil.Parse("2026-10-24")
	to, _ := civil.Parse("2026-10-25")
	all, err := db.ListProposals(ctx, from, to, nil)
	if err != nil || len(all) != 3 || all[0].ID != "a2" || all[1].ID != "a1" || all[2].ID != "a3" {
		t.Fatalf("list all = %+v, %v", all, err)
	}
	pend, err := db.ListProposals(ctx, from, to, []ProposalStatus{ProposalPending})
	if err != nil || len(pend) != 2 {
		t.Fatalf("list pending = %d, %v", len(pend), err)
	}
	one, err := db.ListProposals(ctx, from, from, nil)
	if err != nil || len(one) != 1 || one[0].ID != "a2" {
		t.Fatalf("list one day = %+v, %v", one, err)
	}
	// duplicate id fails and leaves no extra audit row
	if err := db.InsertProposal(ctx, p1, AuditEvent{At: t0, Event: "proposal_pending", Target: "a1", Outcome: "ok"}); err == nil {
		t.Fatal("duplicate insert succeeded")
	}
	if propAuditCount(t, db, "proposal_pending", "a1") != 1 {
		t.Fatal("audit row leaked on failed insert")
	}
}

func TestTransitionIsCompareAndSwap(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	insertProp(t, db, mkProp("p1", "2026-10-25", t0, ProposalPending))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	tos := []ProposalStatus{ProposalCancelled, ProposalExpired}
	for i := range tos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = db.TransitionProposal(ctx, "p1", Transition{From: []ProposalStatus{ProposalPending}, To: tos[i], At: t0.Add(time.Hour), Lane: "private", Actor: "agent", Client: "c"})
		}()
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			ok++
		case errors.Is(e, ErrConflict):
			conflict++
		default:
			t.Fatalf("unexpected err %v", e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM audit_log WHERE target='p1' AND event != 'proposal_pending'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("transition audit rows = %d err %v, want 1", n, err)
	}
	if _, err := db.TransitionProposal(ctx, "nope", Transition{From: []ProposalStatus{ProposalPending}, To: ProposalCancelled, At: t0}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id: %v", err)
	}
}

func TestTransitionAuditRow(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	insertProp(t, db, mkProp("p1", "2026-10-25", t0, ProposalPending))
	at := t0.Add(time.Hour)
	p, err := db.TransitionProposal(ctx, "p1", Transition{From: []ProposalStatus{ProposalPending, ProposalApproved}, To: ProposalCancelled, At: at, Lane: "private", Actor: "agent", Client: "claude"})
	if err != nil || p.Status != ProposalCancelled || !p.StatusAt.Equal(at) {
		t.Fatalf("p = %+v, %v", p, err)
	}
	var atS, lane, actor, client, target, outcome string
	if err := db.sql.QueryRow(`SELECT at, lane, actor, client, target, outcome FROM audit_log WHERE event='proposal_cancelled'`).Scan(&atS, &lane, &actor, &client, &target, &outcome); err != nil {
		t.Fatal(err)
	}
	if atS != fmtTime(at) || lane != "private" || actor != "agent" || client != "claude" || target != "p1" || outcome != "ok" {
		t.Fatalf("audit = %q %q %q %q %q %q", atS, lane, actor, client, target, outcome)
	}
	// wrong From → conflict, no audit
	if _, err := db.TransitionProposal(ctx, "p1", Transition{From: []ProposalStatus{ProposalPending}, To: ProposalExpired, At: at}); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	if propAuditCount(t, db, "proposal_expired", "p1") != 0 {
		t.Fatal("audit written on conflict")
	}
}

func TestExpireProposals(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	insertProp(t, db, mkProp("old-pending", "2026-10-22", t0, ProposalPending))
	insertProp(t, db, mkProp("old-approved", "2026-10-21", t0, ProposalApproved))
	insertProp(t, db, mkProp("old-delivered", "2026-10-21", t0, ProposalDelivered))
	insertProp(t, db, mkProp("today", "2026-10-23", t0, ProposalPending))
	insertProp(t, db, mkProp("future", "2026-10-30", t0, ProposalPending))
	before, _ := civil.Parse("2026-10-23")
	at := t0.Add(72 * time.Hour)
	n, err := db.ExpireProposals(ctx, before, at)
	if err != nil || n != 2 {
		t.Fatalf("n = %d, %v", n, err)
	}
	for id, want := range map[string]ProposalStatus{"old-pending": ProposalExpired, "old-approved": ProposalExpired, "old-delivered": ProposalDelivered, "today": ProposalPending, "future": ProposalPending} {
		p, err := db.GetProposal(ctx, id)
		if err != nil || p.Status != want {
			t.Fatalf("%s = %v, %v want %s", id, p.Status, err, want)
		}
	}
	var lane, actor string
	if err := db.sql.QueryRow(`SELECT lane, actor FROM audit_log WHERE event='proposal_expired' AND target='old-pending'`).Scan(&lane, &actor); err != nil || lane != "system" || actor != "system" {
		t.Fatalf("audit %q %q %v", lane, actor, err)
	}
	if propAuditCount(t, db, "proposal_expired", "old-approved") != 1 || propAuditCount(t, db, "proposal_expired", "today") != 0 {
		t.Fatal("audit rows wrong")
	}
	if n, err := db.ExpireProposals(ctx, before, at); err != nil || n != 0 {
		t.Fatalf("second run n=%d err=%v", n, err)
	}
}

// TestPendingProposalsLimitAndOrder: only pending rows, by date, creation
// time, then id, at most limit, with only id, date and name.
func TestPendingProposalsLimitAndOrder(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	if got, err := db.PendingProposals(ctx, 20); err != nil || len(got) != 0 {
		t.Fatalf("empty store: %+v, %v", got, err)
	}
	t0 := time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)
	for _, p := range []Proposal{
		mkProp("c", "2026-10-25", t0, ProposalPending),
		mkProp("b", "2026-10-24", t0.Add(2*time.Minute), ProposalPending),
		mkProp("d", "2026-10-24", t0.Add(time.Minute), ProposalPending), // inserted first, same creation time as a:
		mkProp("a", "2026-10-24", t0.Add(time.Minute), ProposalPending), // id breaks the tie
		mkProp("x", "2026-10-23", t0, ProposalCancelled),
		mkProp("y", "2026-10-23", t0, ProposalApproved),
	} {
		insertProp(t, db, p)
	}
	got, err := db.PendingProposals(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	d24 := civil.Date{Year: 2026, Month: 10, Day: 24}
	want := []ProposalSummary{{ID: "a", Date: d24, Name: "easy a"}, {ID: "d", Date: d24, Name: "easy d"}, {ID: "b", Date: d24, Name: "easy b"}}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
	if all, err := db.PendingProposals(ctx, 20); err != nil || len(all) != 4 || all[3].ID != "c" {
		t.Fatalf("all pending %+v, %v", all, err)
	}
}
