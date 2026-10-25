package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/civil"
)

var rt0 = time.Date(2026, 10, 24, 7, 0, 0, 0, time.UTC)

func approval(p Proposal, nonce string) Approval {
	return Approval{ID: p.ID, ContentHash: p.ContentHash, Today: rtToday, Nonce: nonce,
		NonceExpires: rt0.Add(time.Hour), At: rt0, Lane: "public", Client: "passkey:1"}
}

func auditRows(t *testing.T, db *DB, event, target string) int {
	t.Helper()
	return propAuditCount(t, db, event, target)
}

func TestApprovePending(t *testing.T) {
	db := openTest(t)
	p := mkProp("a1", "2026-10-25", rt0.Add(-time.Hour), ProposalPending)
	insertProp(t, db, p)
	res, err := db.ApproveProposal(context.Background(), approval(p, "n1"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Proposal.Status != ProposalApproved || !res.Proposal.StatusAt.Equal(rt0) || res.Replaced != nil {
		t.Fatalf("res = %+v", res)
	}
	var lane, actor, client, outcome string
	if err := db.sql.QueryRow(`SELECT lane, actor, client, outcome FROM audit_log WHERE event='proposal_approved' AND target='a1'`).Scan(&lane, &actor, &client, &outcome); err != nil {
		t.Fatal(err)
	}
	if lane != "public" || actor != "runner" || client != "passkey:1" || outcome != "ok" {
		t.Fatalf("audit = %s %s %s %s", lane, actor, client, outcome)
	}
}

func TestApproveNonceSingleUse(t *testing.T) {
	db := openTest(t)
	p := mkProp("a1", "2026-10-25", rt0.Add(-time.Hour), ProposalPending)
	insertProp(t, db, p)
	if _, err := db.ApproveProposal(context.Background(), approval(p, "n1")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ApproveProposal(context.Background(), approval(p, "n1")); !errors.Is(err, ErrNonceUsed) {
		t.Fatalf("second = %v, want ErrNonceUsed", err)
	}
	if n := auditRows(t, db, "proposal_approved", "a1"); n != 1 {
		t.Fatalf("approved rows = %d", n)
	}
}

func TestApproveDatePassed(t *testing.T) {
	db := openTest(t)
	p := mkProp("a1", "2026-10-23", rt0.Add(-time.Hour), ProposalPending)
	insertProp(t, db, p)
	_, err := db.ApproveProposal(context.Background(), approval(p, "n1"))
	var ac *ApprovalConflict
	if !errors.As(err, &ac) || ac.Reason != "date_passed" || !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	got, _ := db.GetProposal(context.Background(), "a1")
	if got.Status != ProposalPending {
		t.Fatalf("status = %s", got.Status)
	}
	if err := db.ConsumeNonce(context.Background(), "n1", rt0, rt0.Add(time.Hour)); err != nil {
		t.Fatalf("nonce not released: %v", err)
	}
}

// The owner's common case: approving a workout planned for today. Only a
// date before today is refused.
func TestApproveToday(t *testing.T) {
	db := openTest(t)
	p := mkProp("a1", rtToday.String(), rt0.Add(-time.Hour), ProposalPending)
	insertProp(t, db, p)
	res, err := db.ApproveProposal(context.Background(), approval(p, "n1"))
	if err != nil {
		t.Fatalf("approve today: %v", err)
	}
	if res.Proposal.Status != ProposalApproved || res.Proposal.Date != rtToday {
		t.Fatalf("res = %+v", res.Proposal)
	}
	if n := auditRows(t, db, "proposal_approved", "a1"); n != 1 {
		t.Fatalf("approved rows = %d", n)
	}
}

func TestApproveNotPending(t *testing.T) {
	db := openTest(t)
	p := mkProp("a1", "2026-10-25", rt0.Add(-time.Hour), ProposalCancelled)
	insertProp(t, db, p)
	_, err := db.ApproveProposal(context.Background(), approval(p, "n1"))
	var ac *ApprovalConflict
	if !errors.As(err, &ac) || ac.Reason != "not_pending" || ac.Status != ProposalCancelled || !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v", err)
	}
	if ac.Error() == "" {
		t.Fatal("empty message")
	}
}

func TestApproveContentChanged(t *testing.T) {
	db := openTest(t)
	p := mkProp("a1", "2026-10-25", rt0.Add(-time.Hour), ProposalPending)
	insertProp(t, db, p)
	a := approval(p, "n1")
	a.ContentHash = "other"
	_, err := db.ApproveProposal(context.Background(), a)
	var ac *ApprovalConflict
	if !errors.As(err, &ac) || ac.Reason != "content_changed" {
		t.Fatalf("err = %v", err)
	}
}

func approveReplacement(t *testing.T, db *DB, oldStatus ProposalStatus) (ApproveResult, error) {
	t.Helper()
	old := mkProp("old", "2026-10-25", rt0.Add(-2*time.Hour), oldStatus)
	insertProp(t, db, old)
	n := mkProp("new", "2026-10-25", rt0.Add(-time.Hour), ProposalPending)
	n.Replaces = &old.ID
	insertProp(t, db, n)
	return db.ApproveProposal(context.Background(), approval(n, "n1"))
}

func TestApproveReplacesPending(t *testing.T) {
	db := openTest(t)
	res, err := approveReplacement(t, db, ProposalPending)
	if err != nil {
		t.Fatal(err)
	}
	if res.Replaced == nil || res.Replaced.Status != ProposalSuperseded || !res.Replaced.StatusAt.Equal(rt0) {
		t.Fatalf("replaced = %+v", res.Replaced)
	}
	if auditRows(t, db, "proposal_superseded", "old") != 1 || auditRows(t, db, "proposal_approved", "new") != 1 {
		t.Fatal("audit rows missing")
	}
}

func TestApproveReplacesDelivered(t *testing.T) {
	db := openTest(t)
	res, err := approveReplacement(t, db, ProposalDelivered)
	if err != nil {
		t.Fatal(err)
	}
	if res.Replaced == nil || res.Replaced.Status != ProposalWithdrawn {
		t.Fatalf("replaced = %+v", res.Replaced)
	}
	if auditRows(t, db, "proposal_withdrawn", "old") != 1 {
		t.Fatal("withdrawn audit missing")
	}
	got, _ := db.GetProposal(context.Background(), "old")
	if got.Status != ProposalWithdrawn {
		t.Fatalf("stored = %s", got.Status)
	}
}

func TestApproveReplacesTerminal(t *testing.T) {
	db := openTest(t)
	res, err := approveReplacement(t, db, ProposalDeclined)
	if err != nil {
		t.Fatal(err)
	}
	if res.Replaced == nil || res.Replaced.Status != ProposalDeclined {
		t.Fatalf("replaced = %+v", res.Replaced)
	}
	var n int
	db.sql.QueryRow(`SELECT count(*) FROM audit_log WHERE target='old' AND event != 'proposal_pending'`).Scan(&n)
	if n != 0 {
		t.Fatalf("extra audit rows = %d", n)
	}
}

func TestUpdateRunnerNoteRelabels(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	n, err := db.InsertNote(ctx, Note{Category: NoteOther, Text: "agent text", CreatedBy: "agent:private:claude", CreatedAt: rt0, UpdatedAt: rt0}, 50)
	if err != nil {
		t.Fatal(err)
	}
	t2 := rt0.Add(time.Hour)
	got, err := db.UpdateRunnerNote(ctx, Note{ID: n.ID, Category: NoteInjury, Text: "runner text", UpdatedAt: t2})
	if err != nil || got.CreatedBy != "runner" || got.Text != "runner text" || got.Category != NoteInjury || !got.UpdatedAt.Equal(t2) || !got.CreatedAt.Equal(rt0) {
		t.Fatalf("got = %+v, %v", got, err)
	}
	if _, err := db.UpdateRunnerNote(ctx, Note{ID: 9999, Category: NoteOther, Text: "x", UpdatedAt: t2}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestInsertNoteRejectsID(t *testing.T) {
	db := openTest(t)
	if _, err := db.InsertNote(context.Background(), Note{ID: 5, Category: NoteOther, Text: "x", CreatedBy: "runner", CreatedAt: rt0, UpdatedAt: rt0}, 50); err == nil {
		t.Fatal("want error")
	}
}

func TestProposalCountsAllStatuses(t *testing.T) {
	db := openTest(t)
	insertProp(t, db, mkProp("a", "2026-10-25", rt0, ProposalPending))
	insertProp(t, db, mkProp("b", "2026-10-25", rt0, ProposalPending))
	insertProp(t, db, mkProp("c", "2026-10-25", rt0, ProposalDeclined))
	m, err := db.ProposalCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != len(ProposalStatuses) {
		t.Fatalf("len = %d: %v", len(m), m)
	}
	if m[ProposalPending] != 2 || m[ProposalDeclined] != 1 || m[ProposalExpired] != 0 {
		t.Fatalf("m = %v", m)
	}
}

func TestListAuditNewestFirst(t *testing.T) {
	db := openTest(t)
	for i := 0; i < 3; i++ {
		insertProp(t, db, mkProp(string(rune('a'+i)), "2026-10-25", rt0.Add(time.Duration(i)*time.Minute), ProposalPending))
	}
	ev, err := db.ListAudit(context.Background(), 2)
	if err != nil || len(ev) != 2 || ev[0].Target != "c" || ev[1].Target != "b" {
		t.Fatalf("ev = %+v, %v", ev, err)
	}
	if ev, _ := db.ListAudit(context.Background(), 0); len(ev) != 1 {
		t.Fatalf("limit 0 -> %d, want 1", len(ev))
	}
	if ev, _ := db.ListAudit(context.Background(), 10000); len(ev) != 3 {
		t.Fatalf("limit big -> %d", len(ev))
	}
}

func TestReplacementsOf(t *testing.T) {
	db := openTest(t)
	insertProp(t, db, mkProp("old", "2026-10-25", rt0, ProposalPending))
	for i, id := range []string{"r2", "r1"} {
		p := mkProp(id, "2026-10-25", rt0.Add(time.Duration(i+1)*time.Minute), ProposalPending)
		o := "old"
		p.Replaces = &o
		insertProp(t, db, p)
	}
	insertProp(t, db, mkProp("other", "2026-10-25", rt0, ProposalPending))
	got, err := db.ReplacementsOf(context.Background(), "old")
	if err != nil || len(got) != 2 || got[0].ID != "r2" || got[1].ID != "r1" {
		t.Fatalf("got = %+v, %v", got, err)
	}
}

var rtToday = func() civil.Date { d, _ := civil.Parse("2026-10-24"); return d }()

func TestApproveReplacesDelivering(t *testing.T) {
	for _, s := range []ProposalStatus{ProposalDelivering, ProposalDeliveryFailed} {
		db := openTest(t)
		res, err := approveReplacement(t, db, s)
		if err != nil {
			t.Fatal(err)
		}
		if res.Replaced == nil || res.Replaced.Status != ProposalWithdrawn || auditRows(t, db, "proposal_withdrawn", "old") != 1 {
			t.Fatalf("%s: replaced = %+v", s, res.Replaced)
		}
	}
}

func approvalSet(nonce string, ps ...Proposal) ApprovalSet {
	a := ApprovalSet{Today: rtToday, Nonce: nonce, NonceExpires: rt0.Add(time.Hour), At: rt0, Lane: "public", Client: "passkey:1"}
	for _, p := range ps {
		a.Items = append(a.Items, ApprovalItem{ID: p.ID, ContentHash: p.ContentHash})
	}
	return a
}

func TestApproveProposalsAll(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	old := mkProp("old", "2026-10-26", rt0.Add(-3*time.Hour), ProposalDelivered)
	insertProp(t, db, old)
	a := mkProp("a", "2026-10-25", rt0.Add(-2*time.Hour), ProposalPending)
	b := mkProp("b", "2026-10-26", rt0.Add(-time.Hour), ProposalPending)
	b.Replaces = &old.ID
	insertProp(t, db, a)
	insertProp(t, db, b)

	res, err := db.ApproveProposals(ctx, approvalSet("n1", a, b))
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Proposal.ID != "a" || res[1].Proposal.ID != "b" || res[1].Replaced == nil ||
		res[1].Replaced.Status != ProposalWithdrawn {
		t.Fatalf("res = %+v", res)
	}
	for id, want := range map[string]ProposalStatus{"a": ProposalApproved, "b": ProposalApproved, "old": ProposalWithdrawn} {
		if got := status(t, db, id); got != want {
			t.Fatalf("%s = %s, want %s", id, got, want)
		}
	}
	if auditRows(t, db, "proposal_approved", "a") != 1 || auditRows(t, db, "proposal_approved", "b") != 1 ||
		auditRows(t, db, "proposal_withdrawn", "old") != 1 {
		t.Fatal("audit rows missing")
	}
	if used, err := db.NonceUsed(ctx, "n1"); err != nil || !used {
		t.Fatalf("nonce used = %v, %v", used, err)
	}
	if _, err := db.ApproveProposals(ctx, approvalSet("n1", a)); !errors.Is(err, ErrNonceUsed) {
		t.Fatalf("replay = %v, want ErrNonceUsed", err)
	}
}

// Any conflict rolls back every approval of the set, and the nonce.
func TestApproveProposalsAllOrNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		edit   func(p *Proposal)
		reason string
	}{
		"not pending":     {func(p *Proposal) { p.Status = ProposalDeclined }, "not_pending"},
		"content changed": {func(p *Proposal) { p.ContentHash = "other" }, "content_changed"},
		"date passed":     {func(p *Proposal) { p.Date, _ = civil.Parse("2026-10-23") }, "date_passed"},
		"not found":       {nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			db := openTest(t)
			ctx := context.Background()
			a := mkProp("a", "2026-10-25", rt0.Add(-2*time.Hour), ProposalPending)
			insertProp(t, db, a)
			b := mkProp("b", "2026-10-26", rt0.Add(-time.Hour), ProposalPending)
			stored := b
			if tc.edit != nil {
				tc.edit(&stored)
				insertProp(t, db, stored)
			}
			_, err := db.ApproveProposals(ctx, approvalSet("n1", a, b))
			var sc *SetConflict
			if !errors.As(err, &sc) || sc.ID != "b" {
				t.Fatalf("err = %v, want a SetConflict on b", err)
			}
			var ac *ApprovalConflict
			switch {
			case tc.reason == "" && !errors.Is(err, ErrNotFound):
				t.Fatalf("err = %v, want ErrNotFound", err)
			case tc.reason != "" && (!errors.As(err, &ac) || ac.Reason != tc.reason || !errors.Is(err, ErrConflict)):
				t.Fatalf("err = %v, want reason %s", err, tc.reason)
			}
			if got := status(t, db, "a"); got != ProposalPending {
				t.Fatalf("a = %s, want pending: the set must roll back", got)
			}
			if auditRows(t, db, "proposal_approved", "a") != 0 {
				t.Fatal("audit row kept after rollback")
			}
			if used, _ := db.NonceUsed(ctx, "n1"); used {
				t.Fatal("nonce consumed after rollback")
			}
		})
	}
}

func TestApproveProposalsEmpty(t *testing.T) {
	db := openTest(t)
	if _, err := db.ApproveProposals(context.Background(), approvalSet("n1")); err == nil {
		t.Fatal("an empty set must be refused")
	}
}
