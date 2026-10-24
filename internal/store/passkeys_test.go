package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)

func countAudit(t *testing.T, db *DB, event string) int {
	t.Helper()
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM audit_log WHERE event=?`, event).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func enrollParams(id int64, nonce string, cred string, at time.Time) EnrollParams {
	return EnrollParams{EnrollmentID: id, Nonce: nonce, NonceExpires: at.Add(10 * time.Minute),
		Passkey: Passkey{CredentialID: []byte(cred), CredentialJSON: []byte(`{"v":1}`), Label: "phone"},
		At:      at, Lane: "public"}
}

func TestEnrollmentSingleUse(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	id, err := db.CreateEnrollment(ctx, []byte("h1"), t0, t0.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := db.EnrollmentByHash(ctx, []byte("h1"), t0.Add(time.Minute)); err != nil || got != id {
		t.Fatalf("EnrollmentByHash = %d, %v", got, err)
	}
	at := t0.Add(time.Minute)
	pid, err := db.EnrollPasskey(ctx, enrollParams(id, "n1", "c1", at))
	if err != nil || pid == 0 {
		t.Fatalf("EnrollPasskey = %d, %v", pid, err)
	}
	if _, err := db.EnrollmentByHash(ctx, []byte("h1"), at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("used enrolment: %v", err)
	}
	if _, err := db.EnrollPasskey(ctx, enrollParams(id, "n2", "c2", at)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second enroll: %v", err)
	}
	if n := countAudit(t, db, "passkey_enrolled"); n != 1 {
		t.Fatalf("passkey_enrolled rows = %d", n)
	}
	// The new passkey is the client of its own enrolment.
	var client, target string
	if err := db.sql.QueryRow(`SELECT client, target FROM audit_log WHERE event='passkey_enrolled'`).Scan(&client, &target); err != nil {
		t.Fatal(err)
	}
	if want := "passkey:" + strconv.FormatInt(pid, 10); client != want || target != strconv.FormatInt(pid, 10) {
		t.Fatalf("passkey_enrolled client %q target %q, want client %q", client, target, want)
	}
	if n := countAudit(t, db, "enrollment_created"); n != 1 {
		t.Fatalf("enrollment_created rows = %d", n)
	}
}

func TestEnrollmentExpired(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	exp := t0.Add(10 * time.Minute)
	id, _ := db.CreateEnrollment(ctx, []byte("h1"), t0, exp)
	late := exp.Add(time.Second)
	if _, err := db.EnrollmentByHash(ctx, []byte("h1"), late); !errors.Is(err, ErrNotFound) {
		t.Fatalf("EnrollmentByHash: %v", err)
	}
	if _, err := db.EnrollPasskey(ctx, enrollParams(id, "n1", "c1", late)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("EnrollPasskey: %v", err)
	}
}

func TestEnrollNonceReuse(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	id1, _ := db.CreateEnrollment(ctx, []byte("h1"), t0, t0.Add(10*time.Minute))
	id2, _ := db.CreateEnrollment(ctx, []byte("h2"), t0, t0.Add(10*time.Minute))
	at := t0.Add(time.Minute)
	if _, err := db.EnrollPasskey(ctx, enrollParams(id1, "same", "c1", at)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnrollPasskey(ctx, enrollParams(id2, "same", "c2", at)); !errors.Is(err, ErrNonceUsed) {
		t.Fatalf("reuse: %v", err)
	}
	if got, err := db.EnrollmentByHash(ctx, []byte("h2"), at); err != nil || got != id2 {
		t.Fatalf("enrolment should stay unused: %d, %v", got, err)
	}
}

func TestEnrollDuplicateCredential(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	id1, _ := db.CreateEnrollment(ctx, []byte("h1"), t0, t0.Add(10*time.Minute))
	id2, _ := db.CreateEnrollment(ctx, []byte("h2"), t0, t0.Add(10*time.Minute))
	at := t0.Add(time.Minute)
	if _, err := db.EnrollPasskey(ctx, enrollParams(id1, "n1", "dup", at)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.EnrollPasskey(ctx, enrollParams(id2, "n2", "dup", at)); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := db.EnrollmentByHash(ctx, []byte("h2"), at); err != nil {
		t.Fatalf("enrolment should stay unused: %v", err)
	}
}

func enrolled(t *testing.T, db *DB, hash, nonce, cred string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := db.CreateEnrollment(ctx, []byte(hash), t0, t0.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := db.EnrollPasskey(ctx, enrollParams(id, nonce, cred, t0.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestRevokeKillsSessions(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	pid := enrolled(t, db, "h1", "n1", "c1")
	now := t0.Add(2 * time.Minute)
	if err := db.CreateSession(ctx, []byte("s1"), pid, now, now.Add(12*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s, err := db.SessionByHash(ctx, []byte("s1"), now); err != nil || s.PasskeyID != pid {
		t.Fatalf("SessionByHash = %+v, %v", s, err)
	}
	if err := db.RevokePasskey(ctx, pid, now, "public", "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SessionByHash(ctx, []byte("s1"), now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session after revoke: %v", err)
	}
	if _, err := db.PasskeyByCredentialID(ctx, []byte("c1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("by credential: %v", err)
	}
	if a, err := db.ActivePasskeys(ctx); err != nil || len(a) != 0 {
		t.Fatalf("active = %v, %v", a, err)
	}
	if l, err := db.ListPasskeys(ctx); err != nil || len(l) != 1 || l[0].RevokedAt == nil {
		t.Fatalf("list = %+v, %v", l, err)
	}
	if err := db.RevokePasskey(ctx, pid, now, "public", "web"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}
	if n := countAudit(t, db, "passkey_revoked"); n != 1 {
		t.Fatalf("passkey_revoked rows = %d", n)
	}
}

func TestSessionExpiry(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	pid := enrolled(t, db, "h1", "n1", "c1")
	exp := t0.Add(12 * time.Hour)
	if err := db.CreateSession(ctx, []byte("s1"), pid, t0, exp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SessionByHash(ctx, []byte("s1"), exp.Add(-time.Second)); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	if _, err := db.SessionByHash(ctx, []byte("s1"), exp); !errors.Is(err, ErrNotFound) {
		t.Fatalf("at expiry: %v", err)
	}
	if err := db.DeleteSession(ctx, []byte("s1")); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSession(ctx, []byte("s1")); err != nil {
		t.Fatalf("delete is idempotent: %v", err)
	}
}

func TestConsumeNonceTwice(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	if err := db.ConsumeNonce(ctx, "n", t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.ConsumeNonce(ctx, "n", t0, t0.Add(time.Hour)); !errors.Is(err, ErrNonceUsed) {
		t.Fatalf("second: %v", err)
	}
}

func TestNonceUsed(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	if used, err := db.NonceUsed(ctx, "n"); err != nil || used {
		t.Fatalf("before consume: used=%v err=%v", used, err)
	}
	if err := db.ConsumeNonce(ctx, "n", t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if used, err := db.NonceUsed(ctx, "n"); err != nil || !used {
		t.Fatalf("after consume: used=%v err=%v", used, err)
	}
	if used, err := db.NonceUsed(ctx, "other"); err != nil || used {
		t.Fatalf("other nonce: used=%v err=%v", used, err)
	}
}

func TestUsePasskeyUpdates(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	pid := enrolled(t, db, "h1", "n1", "c1")
	at := t0.Add(time.Hour)
	if err := db.UsePasskey(ctx, pid, []byte(`{"v":2}`), at); err != nil {
		t.Fatal(err)
	}
	p, err := db.PasskeyByCredentialID(ctx, []byte("c1"))
	if err != nil || string(p.CredentialJSON) != `{"v":2}` || p.LastUsedAt == nil || !p.LastUsedAt.Equal(at) {
		t.Fatalf("passkey = %+v, %v", p, err)
	}
	if err := db.RevokePasskey(ctx, pid, at, "public", "web"); err != nil {
		t.Fatal(err)
	}
	if err := db.UsePasskey(ctx, pid, []byte(`{"v":3}`), at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked: %v", err)
	}
}

func TestPurgeRunnerAuth(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	pid := enrolled(t, db, "h1", "n1", "c1") // used enrolment at t0+1m; nonce n1 expires t0+11m
	now := t0.Add(48 * time.Hour)
	// a fresh enrolment, session and nonce that must survive
	if _, err := db.CreateEnrollment(ctx, []byte("h2"), now, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, []byte("live"), pid, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.ConsumeNonce(ctx, "live", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// an old expired session and an expired, unused enrolment
	if err := db.CreateSession(ctx, []byte("old"), pid, t0, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateEnrollment(ctx, []byte("h3"), t0, t0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// removed: session old, nonce n1, enrolment h1 (used), enrolment h3 (expired)
	n, err := db.PurgeRunnerAuth(ctx, now)
	if err != nil || n != 4 {
		t.Fatalf("purged %d, %v; want 4", n, err)
	}
	if _, err := db.SessionByHash(ctx, []byte("live"), now); err != nil {
		t.Fatalf("live session gone: %v", err)
	}
	if err := db.ConsumeNonce(ctx, "live", now, now.Add(time.Hour)); !errors.Is(err, ErrNonceUsed) {
		t.Fatalf("live nonce gone: %v", err)
	}
	if _, err := db.EnrollmentByHash(ctx, []byte("h2"), now); err != nil {
		t.Fatalf("fresh enrolment gone: %v", err)
	}
	if n, err := db.PurgeRunnerAuth(ctx, now); err != nil || n != 0 {
		t.Fatalf("second purge = %d, %v", n, err)
	}
}

// TestMigration0005Upgrade builds a database at 0001-0004 holding a proposal,
// then opens it: 0005 applies and the proposal stays intact.
func TestMigration0005Upgrade(t *testing.T) {
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
	for _, v := range []string{"0001_init", "0002_activity_detail", "0003_activity_deleted_at", "0004_context_proposals"} {
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
	for _, tbl := range []string{"passkeys", "enrollments", "web_sessions", "used_nonces"} {
		var n int
		if err := db.sql.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil || n != 1 {
			t.Errorf("table %s missing (n=%d err=%v)", tbl, n, err)
		}
	}
	var n int
	if err := db.sql.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version='0005_runner_auth'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("0005 not recorded: n=%d err=%v", n, err)
	}
	if p, err := db.GetProposal(ctx, "p1"); err != nil || p.Name != "Easy" {
		t.Fatalf("proposal after upgrade = %+v, %v", p, err)
	}
}
