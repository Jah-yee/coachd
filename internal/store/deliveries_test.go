package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/civil"
)

func mustDate(s string) civil.Date {
	d, err := civil.Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

var dt0 = time.Date(2026, 10, 24, 7, 0, 0, 0, time.UTC)

func mkDevice(t *testing.T, db *DB) int64 {
	t.Helper()
	res, err := db.sql.Exec(`INSERT INTO devices (label, token_hash, created_at) VALUES ('w', randomblob(32), ?)`, fmtTime(dt0))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func status(t *testing.T, db *DB, id string) ProposalStatus {
	t.Helper()
	p, err := db.GetProposal(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p.Status
}

func TestDesiredWindow(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	today := mustDate("2026-10-24")
	for i, d := range []string{"2026-10-23", "2026-10-24", "2026-11-07", "2026-11-08"} {
		insertProp(t, db, mkProp("p"+string(rune('a'+i)), d, dt0.Add(time.Duration(i)*time.Second), ProposalApproved))
	}
	insertProp(t, db, mkProp("pending", "2026-10-25", dt0, ProposalPending))
	insertProp(t, db, mkProp("wd", "2026-10-25", dt0, ProposalWithdrawn))
	got, err := db.DesiredWorkouts(ctx, today, today.AddDays(14), 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "pb" || got[1].ID != "pc" {
		t.Fatalf("got %+v", got)
	}
	if lim, _ := db.DesiredWorkouts(ctx, today, today.AddDays(14), 1); len(lim) != 1 {
		t.Fatalf("limit: %d", len(lim))
	}
}

func TestRecordFetchTransitions(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	dev := mkDevice(t, db)
	insertProp(t, db, mkProp("a", "2026-10-25", dt0, ProposalApproved))
	p, err := db.RecordFetch(ctx, "a", dev, dt0)
	if err != nil || p.Status != ProposalDelivering || !p.StatusAt.Equal(dt0) {
		t.Fatalf("%+v %v", p, err)
	}
	if propAuditCount(t, db, "proposal_delivering", "a") != 1 {
		t.Fatal("audit")
	}
	var lane, actor, client string
	db.sql.QueryRow(`SELECT lane, actor, client FROM audit_log WHERE event='proposal_delivering'`).Scan(&lane, &actor, &client)
	if lane != "public" || actor != "device:1" || client != "device:1" {
		t.Fatalf("audit = %s %s %s", lane, actor, client)
	}
	// refetch while delivering: no new row
	if _, err := db.RecordFetch(ctx, "a", dev, dt0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if propAuditCount(t, db, "proposal_delivering", "a") != 1 {
		t.Fatal("refetch audited")
	}
	insertProp(t, db, mkProp("d", "2026-10-25", dt0, ProposalDelivered))
	p, err = db.RecordFetch(ctx, "d", dev, dt0)
	if err != nil || p.Status != ProposalDelivered || propAuditCount(t, db, "proposal_delivering", "d") != 0 {
		t.Fatalf("delivered: %+v %v", p, err)
	}
	insertProp(t, db, mkProp("pend", "2026-10-25", dt0, ProposalPending))
	_, err = db.RecordFetch(ctx, "pend", dev, dt0)
	var sc *StatusConflict
	if !errors.Is(err, ErrConflict) || !errors.As(err, &sc) || sc.Status != ProposalPending {
		t.Fatalf("pending: %v", err)
	}
	if _, err := db.RecordFetch(ctx, "nope", dev, dt0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent: %v", err)
	}
}

func TestAckDelivery(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	dev := mkDevice(t, db)
	insertProp(t, db, mkProp("a", "2026-10-25", dt0, ProposalApproved))
	if _, err := db.AckDelivery(ctx, "a", dev, dt0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no fetch: %v", err)
	}
	db.RecordFetch(ctx, "a", dev, dt0)
	p, err := db.AckDelivery(ctx, "a", dev, dt0.Add(time.Minute))
	if err != nil || p.Status != ProposalDelivered {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err = db.AckDelivery(ctx, "a", dev, dt0.Add(2*time.Minute)); err != nil || p.Status != ProposalDelivered {
		t.Fatalf("second: %+v %v", p, err)
	}
	if propAuditCount(t, db, "proposal_delivered", "a") != 1 {
		t.Fatal("delivered audit count")
	}
	insertProp(t, db, mkProp("w", "2026-10-25", dt0, ProposalApproved))
	db.RecordFetch(ctx, "w", dev, dt0)
	db.sql.Exec(`UPDATE proposals SET status='withdrawn' WHERE id='w'`)
	_, err = db.AckDelivery(ctx, "w", dev, dt0)
	var sc *StatusConflict
	if !errors.Is(err, ErrConflict) || !errors.As(err, &sc) || sc.Status != ProposalWithdrawn || sc.Error() != "workout is withdrawn" {
		t.Fatalf("withdrawn: %v", err)
	}
}

func TestStaleDeliveryFails(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	dev := mkDevice(t, db)
	for _, id := range []string{"old", "fresh", "acked"} {
		insertProp(t, db, mkProp(id, "2026-10-25", dt0, ProposalApproved))
	}
	now := dt0.Add(time.Hour)
	db.RecordFetch(ctx, "old", dev, now.Add(-16*time.Minute))
	db.RecordFetch(ctx, "fresh", dev, now.Add(-14*time.Minute))
	db.RecordFetch(ctx, "acked", dev, now.Add(-16*time.Minute))
	db.sql.Exec(`UPDATE deliveries SET acked_at=fetched_at WHERE proposal_id='acked'`)
	n, err := db.FailStaleDeliveries(ctx, now.Add(-15*time.Minute), now)
	if err != nil || n != 1 {
		t.Fatalf("n=%d %v", n, err)
	}
	if status(t, db, "old") != ProposalDeliveryFailed || status(t, db, "fresh") != ProposalDelivering || status(t, db, "acked") != ProposalDelivering {
		t.Fatal("statuses wrong")
	}
	var lane, actor, client string
	db.sql.QueryRow(`SELECT lane, actor, client FROM audit_log WHERE event='proposal_delivery_failed'`).Scan(&lane, &actor, &client)
	if lane != "system" || actor != "system" || client != "" || propAuditCount(t, db, "proposal_delivery_failed", "old") != 1 {
		t.Fatalf("audit = %s %s %s", lane, actor, client)
	}
	if n, _ := db.FailStaleDeliveries(ctx, now.Add(-15*time.Minute), now); n != 0 {
		t.Fatalf("again n=%d", n)
	}
}

func failedProp(t *testing.T, db *DB, dev int64) {
	t.Helper()
	insertProp(t, db, mkProp("a", "2026-10-25", dt0, ProposalApproved))
	db.RecordFetch(context.Background(), "a", dev, dt0)
	if n, _ := db.FailStaleDeliveries(context.Background(), dt0.Add(time.Minute), dt0.Add(20*time.Minute)); n != 1 {
		t.Fatal("not failed")
	}
}

func TestRefetchAfterFailure(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	dev := mkDevice(t, db)
	failedProp(t, db, dev)
	p, err := db.RecordFetch(ctx, "a", dev, dt0.Add(30*time.Minute))
	if err != nil || p.Status != ProposalDelivering {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestLateAckDelivers(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	dev := mkDevice(t, db)
	failedProp(t, db, dev)
	p, err := db.AckDelivery(ctx, "a", dev, dt0.Add(30*time.Minute))
	if err != nil || p.Status != ProposalDelivered {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestExpireDeliveringAndFailed(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	for _, s := range []ProposalStatus{ProposalDelivering, ProposalDeliveryFailed, ProposalDelivered} {
		insertProp(t, db, mkProp(string(s), "2026-10-23", dt0.Add(-48*time.Hour), s))
	}
	n, err := db.ExpireProposals(ctx, mustDate("2026-10-24"), dt0)
	if err != nil || n != 2 {
		t.Fatalf("n=%d %v", n, err)
	}
	if status(t, db, "delivering") != ProposalExpired || status(t, db, "delivery_failed") != ProposalExpired || status(t, db, "delivered") != ProposalDelivered {
		t.Fatal("statuses wrong")
	}
}
