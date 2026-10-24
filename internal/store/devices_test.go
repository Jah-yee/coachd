package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func newPairing(id, code string, at time.Time) Pairing {
	return Pairing{ID: id, Code: code, Model: "m", CreatedAt: at, ExpiresAt: at.Add(10 * time.Minute)}
}

func TestPairingCapAndExpiry(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	for i := 0; i < 5; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		if err := db.CreatePairing(ctx, newPairing(fmt.Sprintf("id%032d", i), fmt.Sprintf("00000%d", i), at), 5); err != nil {
			t.Fatalf("pairing %d: %v", i, err)
		}
	}
	if err := db.CreatePairing(ctx, newPairing("idsix", "000009", t0.Add(6*time.Second)), 5); !errors.Is(err, ErrTooManyPairings) {
		t.Fatalf("sixth = %v, want ErrTooManyPairings", err)
	}
	// the first expires at t0+10m; a pairing created just after is accepted
	late := t0.Add(10*time.Minute + time.Second)
	if err := db.CreatePairing(ctx, newPairing("idlate", "000008", late), 5); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	// duplicate code among pending
	if err := db.CreatePairing(ctx, newPairing("iddup", "000008", late.Add(time.Second)), 5); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate code = %v, want ErrConflict", err)
	}
	if n := countAudit(t, db, "device_pairing_started"); n != 6 {
		t.Fatalf("audit rows = %d, want 6", n)
	}
	var lane, actor, client, target string
	if err := db.sql.QueryRow(`SELECT lane, actor, client, target FROM audit_log WHERE event='device_pairing_started' ORDER BY id LIMIT 1`).
		Scan(&lane, &actor, &client, &target); err != nil {
		t.Fatal(err)
	}
	if lane != "public" || actor != "device" || client != "device" || target != "id000000" {
		t.Fatalf("audit = %s %s %s %s", lane, actor, client, target)
	}
}

func TestPendingPairings(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	if ps, err := db.PendingPairings(ctx, t0); err != nil || len(ps) != 0 {
		t.Fatalf("none = %+v, %v", ps, err)
	}
	mk := func(id, code, model string, at time.Time) {
		t.Helper()
		p := newPairing(id, code, at)
		p.Model = model
		if err := db.CreatePairing(ctx, p, 5); err != nil {
			t.Fatal(err)
		}
	}
	mk("expiredaaaaaaaaaaaaaaaaaaaaaaaaa", "111111", "old", t0.Add(-11*time.Minute))
	mk("confirmedaaaaaaaaaaaaaaaaaaaaaaa", "222222", "conf", t0.Add(-3*time.Minute))
	mk("claimedaaaaaaaaaaaaaaaaaaaaaaaaa", "333333", "claim", t0.Add(-2*time.Minute))
	mk("firstaaaaaaaaaaaaaaaaaaaaaaaaaaa", "444444", "first", t0.Add(-5*time.Minute))
	mk("secondaaaaaaaaaaaaaaaaaaaaaaaaaa", "555555", "second", t0.Add(-1*time.Minute))
	if _, err := db.ConfirmPairing(ctx, "222222", "x", t0, "public", "runner", "passkey:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConfirmPairing(ctx, "333333", "y", t0, "public", "runner", "passkey:1"); err != nil {
		t.Fatal(err)
	}
	if st, _, err := db.ClaimPairing(ctx, "claimedaaaaaaaaaaaaaaaaaaaaaaaaa", []byte{1}, t0); err != nil || st != "paired" {
		t.Fatalf("claim = %q, %v", st, err)
	}
	ps, err := db.PendingPairings(ctx, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].Model != "second" || ps[1].Model != "first" {
		t.Fatalf("pending = %+v, want second then first", ps)
	}
	p := ps[1]
	if !p.CreatedAt.Equal(t0.Add(-5*time.Minute)) || !p.ExpiresAt.Equal(t0.Add(5*time.Minute)) || p.ConfirmedAt != nil || p.ClaimedAt != nil {
		t.Errorf("pairing = %+v", p)
	}
	// Neither the claim secret nor the code leaves the store this way.
	for _, p := range ps {
		if p.ID != "" || p.Code != "" {
			t.Errorf("pending pairing carries its id or code: %+v", p)
		}
	}
	// At its expiry a pairing is no longer pending.
	if ps, err := db.PendingPairings(ctx, t0.Add(5*time.Minute)); err != nil || len(ps) != 1 || ps[0].Model != "second" {
		t.Fatalf("at first's expiry = %+v, %v", ps, err)
	}
}

func TestConfirmPairing(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	if _, err := db.ConfirmPairing(ctx, "123456", "x", t0, "public", "runner", "passkey:1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown = %v", err)
	}
	if err := db.CreatePairing(ctx, newPairing("p1aaaaaaaa", "123456", t0), 5); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConfirmPairing(ctx, "123456", "x", t0.Add(11*time.Minute), "public", "runner", "c"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired = %v", err)
	}
	p, err := db.ConfirmPairing(ctx, "123456", "my watch", t0.Add(time.Minute), "cli", "cli", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if p.Label != "my watch" || p.ConfirmedAt == nil || p.ID != "p1aaaaaaaa" {
		t.Fatalf("pairing = %+v", p)
	}
	if _, err := db.ConfirmPairing(ctx, "123456", "again", t0.Add(2*time.Minute), "cli", "cli", "cli"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second = %v", err)
	}
	var lane, actor, client, target string
	if err := db.sql.QueryRow(`SELECT lane, actor, client, target FROM audit_log WHERE event='device_pairing_confirmed'`).
		Scan(&lane, &actor, &client, &target); err != nil {
		t.Fatal(err)
	}
	if lane != "cli" || actor != "cli" || client != "cli" || target != "p1aaaaaa" {
		t.Fatalf("audit = %s %s %s %s", lane, actor, client, target)
	}
}

func TestClaimOnce(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	if err := db.CreatePairing(ctx, newPairing("p1aaaaaaaa", "123456", t0), 5); err != nil {
		t.Fatal(err)
	}
	if st, _, err := db.ClaimPairing(ctx, "p1aaaaaaaa", []byte("h"), t0.Add(time.Second)); err != nil || st != "pending" {
		t.Fatalf("unconfirmed = %q, %v", st, err)
	}
	if _, err := db.ConfirmPairing(ctx, "123456", "watch", t0.Add(2*time.Second), "public", "runner", "passkey:1"); err != nil {
		t.Fatal(err)
	}
	st, id, err := db.ClaimPairing(ctx, "p1aaaaaaaa", []byte("h"), t0.Add(3*time.Second))
	if err != nil || st != "paired" || id == 0 {
		t.Fatalf("claim = %q, %d, %v", st, id, err)
	}
	d, err := db.DeviceByTokenHash(ctx, []byte("h"))
	if err != nil || d.ID != id || d.Label != "watch" || d.Model != "m" {
		t.Fatalf("device = %+v, %v", d, err)
	}
	if st, _, err := db.ClaimPairing(ctx, "p1aaaaaaaa", []byte("h2"), t0.Add(4*time.Second)); err != nil || st != "expired" {
		t.Fatalf("second claim = %q, %v", st, err)
	}
	if st, _, err := db.ClaimPairing(ctx, "unknown", []byte("h3"), t0); err != nil || st != "expired" {
		t.Fatalf("unknown claim = %q, %v", st, err)
	}
	if n := countAudit(t, db, "device_paired"); n != 1 {
		t.Fatalf("device_paired rows = %d", n)
	}
	var lane, actor, client, target string
	if err := db.sql.QueryRow(`SELECT lane, actor, client, target FROM audit_log WHERE event='device_paired'`).
		Scan(&lane, &actor, &client, &target); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("device:%d", id)
	if lane != "public" || actor != want || client != want || target != fmt.Sprint(id) {
		t.Fatalf("audit = %s %s %s %s", lane, actor, client, target)
	}
	if devs, _ := db.ListDevices(ctx); len(devs) != 1 {
		t.Fatalf("devices = %d", len(devs))
	}
}

func TestClaimExpiredConfirmed(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	if err := db.CreatePairing(ctx, newPairing("p1aaaaaaaa", "123456", t0), 5); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConfirmPairing(ctx, "123456", "watch", t0.Add(time.Minute), "cli", "cli", "cli"); err != nil {
		t.Fatal(err)
	}
	if st, _, err := db.ClaimPairing(ctx, "p1aaaaaaaa", []byte("h"), t0.Add(11*time.Minute)); err != nil || st != "expired" {
		t.Fatalf("claim = %q, %v", st, err)
	}
	if devs, _ := db.ListDevices(ctx); len(devs) != 0 {
		t.Fatalf("devices = %d, want 0", len(devs))
	}
}

func pairedDevice(t *testing.T, db *DB, hash string) int64 {
	t.Helper()
	ctx := context.Background()
	if err := db.CreatePairing(ctx, newPairing("pid"+hash, "55"+hash, t0), 5); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ConfirmPairing(ctx, "55"+hash, "w", t0, "cli", "cli", "cli"); err != nil {
		t.Fatal(err)
	}
	_, id, err := db.ClaimPairing(ctx, "pid"+hash, []byte(hash), t0)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDeviceTokenAndRevoke(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	id := pairedDevice(t, db, "1111")
	if _, err := db.DeviceByTokenHash(ctx, []byte("1111")); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeDevice(ctx, id, t0.Add(time.Hour), "public", "runner", "passkey:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DeviceByTokenHash(ctx, []byte("1111")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after revoke = %v", err)
	}
	if err := db.RevokeDevice(ctx, id, t0.Add(time.Hour), "public", "runner", "passkey:1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke = %v", err)
	}
	if err := db.RevokeDevice(ctx, 999, t0, "cli", "cli", "cli"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent revoke = %v", err)
	}
	if n := countAudit(t, db, "device_revoked"); n != 1 {
		t.Fatalf("device_revoked rows = %d", n)
	}
	devs, err := db.ListDevices(ctx)
	if err != nil || len(devs) != 1 || devs[0].RevokedAt == nil {
		t.Fatalf("ListDevices = %+v, %v", devs, err)
	}
}

func TestTouchDeviceThrottled(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	id := pairedDevice(t, db, "2222")
	at := t0.Add(time.Hour)
	if err := db.TouchDevice(ctx, id, at); err != nil {
		t.Fatal(err)
	}
	if err := db.TouchDevice(ctx, id, at.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	d, _ := db.DeviceByTokenHash(ctx, []byte("2222"))
	if d.LastSeenAt == nil || !d.LastSeenAt.Equal(at) {
		t.Fatalf("last_seen = %v, want %v", d.LastSeenAt, at)
	}
	later := at.Add(2 * time.Minute)
	if err := db.TouchDevice(ctx, id, later); err != nil {
		t.Fatal(err)
	}
	d, _ = db.DeviceByTokenHash(ctx, []byte("2222"))
	if !d.LastSeenAt.Equal(later) {
		t.Fatalf("last_seen = %v, want %v", d.LastSeenAt, later)
	}
}

func TestPurgePairings(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	if err := db.CreatePairing(ctx, newPairing("old", "111111", t0), 5); err != nil {
		t.Fatal(err)
	}
	now := t0.Add(10*time.Minute + 24*time.Hour + time.Minute)
	if err := db.CreatePairing(ctx, newPairing("new", "222222", now.Add(-time.Minute)), 5); err != nil {
		t.Fatal(err)
	}
	n, err := db.PurgeRunnerAuth(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	var left int
	if err := db.sql.QueryRow(`SELECT count(*) FROM device_pairings`).Scan(&left); err != nil || left != 1 {
		t.Fatalf("left = %d, %v", left, err)
	}
}

// TestMigration0007Upgrade builds a database at 0001-0006 holding a proposal,
// then opens it: 0007 and any later migration apply and the data stays intact.
func TestMigration0007Upgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coachd.db")
	ctx := context.Background()
	old, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := old.Exec(q, args...); err != nil {
			t.Fatalf("%.40s: %v", q, err)
		}
	}
	exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY)`)
	for _, v := range []string{"0001_init", "0002_activity_detail", "0003_activity_deleted_at", "0004_context_proposals", "0005_runner_auth", "0006_proposal_trace"} {
		body, err := migrationFS.ReadFile("migrations/" + v + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		exec(string(body))
		exec(`INSERT INTO schema_migrations(version) VALUES (?)`, v)
	}
	exec(`INSERT INTO proposals (id, date, sport, name, content_json, content_hash, status, status_at, created_at, created_lane, created_by, created_client)
		VALUES ('p1','2026-10-25','running','Easy','{}','abc','pending','2026-10-23T09:00:00.000000000Z','2026-10-23T09:00:00.000000000Z','mcp','agent','c')`)
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tbl := range []string{"devices", "device_pairings"} {
		var n int
		if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (n=%d err=%v)", tbl, n, err)
		}
	}
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version='0007_devices'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("0007 not recorded: n=%d err=%v", n, err)
	}
	if p, err := db.GetProposal(ctx, "p1"); err != nil || p.Name != "Easy" {
		t.Fatalf("proposal after upgrade = %+v, %v", p, err)
	}
}
