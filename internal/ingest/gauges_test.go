package ingest

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/backup"
	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/store"
)

func TestGauges(t *testing.T) {
	e := newEnv(t, newFake(), nil)
	ctx := context.Background()
	succ := testNow.Add(-2 * time.Hour)
	fresh := testNow.Add(-26 * time.Hour)
	if err := e.db.PutSyncState(ctx, store.SyncState{DataType: TypeSleep, LastSuccessAt: &succ, FreshestDataAt: &fresh, LastOutcome: OutcomeOK}); err != nil {
		t.Fatal(err)
	}
	// No success yet: skipped.
	if err := e.db.PutSyncState(ctx, store.SyncState{DataType: TypeHRV, LastOutcome: OutcomeError}); err != nil {
		t.Fatal(err)
	}
	tz := bratislava(t)
	g := NewGauges(e.db, func() civil.Date { return civil.Of(testNow, tz).AddDays(30) }, tz, e.dir, e.clock.Now)

	ages := g.SyncAges(ctx)
	if len(ages) != 1 || ages[TypeSleep] != (2*time.Hour).Seconds() {
		t.Fatalf("ages %v", ages)
	}
	fr := g.DataFreshness(ctx)
	if len(fr) != 1 || fr[TypeSleep] != (26*time.Hour).Seconds() {
		t.Fatalf("freshness %v", fr)
	}
	if d := g.KeyDaysLeft(); d != 30 {
		t.Fatalf("key days %v", d)
	}
	if s := g.DBSizeBytes(); s <= 0 {
		t.Fatalf("size %v", s)
	}
	if r := g.DiskUsedRatio(); r <= 0 || r >= 1 {
		t.Fatalf("ratio %v", r)
	}
	pc := g.ProposalCounts(ctx)
	if len(pc) != len(store.ProposalStatuses) || pc["pending"] != 0 {
		t.Fatalf("proposal counts %v", pc)
	}
}

func TestGaugesDiskErrorIsNaN(t *testing.T) {
	e := newEnv(t, newFake(), nil)
	g := NewGauges(e.db, func() civil.Date { return civil.Date{} }, bratislava(t), e.dir+"/missing", e.clock.Now)
	if r := g.DiskUsedRatio(); !math.IsNaN(r) {
		t.Fatalf("ratio %v", r)
	}
}

func TestGaugesBackupAge(t *testing.T) {
	e := newEnv(t, newFake(), nil)
	g := NewGauges(e.db, func() civil.Date { return civil.Date{} }, bratislava(t), e.dir, e.clock.Now)
	if a := g.BackupAgeSeconds(); !math.IsNaN(a) {
		t.Fatalf("age %v without backups", a)
	}
	if _, err := backup.Run(context.Background(), e.db, e.dir, "test", testNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if a := g.BackupAgeSeconds(); math.Abs(a-3600) > 1 {
		t.Fatalf("age %v", a)
	}
	// A newest backup over 1 h in the future (the clock was wrong) is no age.
	if _, err := backup.Run(context.Background(), e.db, e.dir, "test", testNow.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if a := g.BackupAgeSeconds(); !math.IsNaN(a) {
		t.Fatalf("age %v with a future-dated newest backup, want NaN", a)
	}
}

func TestGaugesDeviceLastSeen(t *testing.T) {
	e := newEnv(t, newFake(), nil)
	ctx := context.Background()
	g := NewGauges(e.db, func() civil.Date { return civil.Date{} }, bratislava(t), e.dir, e.clock.Now)
	if got := g.DeviceLastSeenAges(ctx); len(got) != 0 {
		t.Fatalf("no devices: %v", got)
	}
	pair := func(code string) int64 {
		t.Helper()
		id := "pairing-" + code
		if err := e.db.CreatePairing(ctx, store.Pairing{ID: id, Code: code, CreatedAt: testNow, ExpiresAt: testNow.Add(10 * time.Minute)}, 5); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.ConfirmPairing(ctx, code, "watch", testNow, "cli", "cli", "cli"); err != nil {
			t.Fatal(err)
		}
		_, dev, err := e.db.ClaimPairing(ctx, id, []byte("hash-"+code), testNow)
		if err != nil {
			t.Fatal(err)
		}
		return dev
	}
	seen, revoked, never := pair("111111"), pair("222222"), pair("333333")
	for _, id := range []int64{seen, revoked} {
		if err := e.db.TouchDevice(ctx, id, testNow.Add(-90*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.db.RevokeDevice(ctx, revoked, testNow, "cli", "cli", "cli"); err != nil {
		t.Fatal(err)
	}
	got := g.DeviceLastSeenAges(ctx)
	if len(got) != 1 || got[strconv.FormatInt(seen, 10)] != 90 {
		t.Fatalf("ages %v (seen %d, revoked %d, never %d)", got, seen, revoked, never)
	}
}
