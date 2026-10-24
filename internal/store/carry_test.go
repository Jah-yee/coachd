package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestCarryForwardMatchesCredential: a revocation applies only to the row
// with the same id and the same credential, never to a reused id.
func TestCarryForwardMatchesCredential(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 24, 3, 15, 0, 0, time.UTC)
	open := func(name string, hash string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := db.CreateToken(ctx, TokenRecord{ID: "tok", Name: "n", Hash: []byte(hash), CreatedAt: at, ExpiresAt: at}); err != nil {
			t.Fatal(err)
		}
		if name == "live.db" {
			if err := db.RevokeToken(ctx, "tok", at); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	live, other := open("live.db", "hash-a"), open("other.db", "hash-b")
	c, err := ReadCarryForward(ctx, live)
	if err != nil {
		t.Fatal(err)
	}
	if revs, _, err := ApplyCarryForward(ctx, other, c, AuditEvent{At: at, Event: "backup_restored"}); err != nil || revs != 0 {
		t.Fatalf("revocations %d err=%v, want 0", revs, err)
	}
	db, err := Open(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if toks, err := db.ListTokens(ctx); err != nil || len(toks) != 1 || toks[0].RevokedAt != nil {
		t.Fatalf("tokens %+v err=%v, want tok still active", toks, err)
	}
}

// TestRelativePaths: VerifySnapshot, ReadCarryForward and ApplyCarryForward
// accept a path relative to the working directory (COACHD_DATA_DIR may be
// relative). A relative path must not become a file: URI authority.
func TestRelativePaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := Open(ctx, filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SnapshotTo(ctx, filepath.Join(dir, "snap.db")); err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Chdir(dir)
	if _, err := VerifySnapshot(ctx, "snap.db"); err != nil {
		t.Fatalf("VerifySnapshot(relative): %v", err)
	}
	c, err := ReadCarryForward(ctx, "live.db")
	if err != nil {
		t.Fatalf("ReadCarryForward(relative): %v", err)
	}
	at := time.Date(2026, 10, 24, 3, 15, 0, 0, time.UTC)
	if _, _, err := ApplyCarryForward(ctx, "snap.db", c, AuditEvent{At: at, Event: "backup_restored"}); err != nil {
		t.Fatalf("ApplyCarryForward(relative): %v", err)
	}
}

// TestApplyCarryForwardClearsOAuth: a restored snapshot keeps OAuth clients
// but loses every grant, code and token.
func TestApplyCarryForwardClearsOAuth(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 24, 3, 15, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "snap.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	cid, err := db.PutOAuthClient(ctx, testClient("https://a.example/c"))
	if err != nil {
		t.Fatal(err)
	}
	pk := oauthPasskey(t, db)
	consent(t, db, pk, cid, consentOpts{nonce: "n", code: "c", at: at})
	if _, err := db.RedeemCode(ctx, []byte("c"), at, accept, nt("a", "r", at)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := ReadCarryForward(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ApplyCarryForward(ctx, path, c, AuditEvent{At: at, Event: "backup_restored"}); err != nil {
		t.Fatal(err)
	}
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tbl := range []string{"oauth_grants", "oauth_codes", "oauth_tokens"} {
		var n int
		db.sql.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n)
		if n != 0 {
			t.Errorf("%s has %d rows after restore", tbl, n)
		}
	}
	if _, err := db.OAuthClientByClientID(ctx, "https://a.example/c"); err != nil {
		t.Errorf("client must stay: %v", err)
	}
}
