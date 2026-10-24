package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "coachd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func newTok(t *testing.T, db *store.DB, id string, created, expires time.Time) string {
	t.Helper()
	plain, hash, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateToken(context.Background(), store.TokenRecord{ID: id, Name: id, Hash: hash,
		Scopes: []string{ScopeRead}, CreatedAt: created, ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	return plain
}

func authReq(tok string) *http.Request {
	r := httptest.NewRequest("GET", "/mcp", nil)
	if tok != "" {
		r.Header.Set("Authorization", tok)
	}
	return r
}

func TestGenerateToken(t *testing.T) {
	p, h, err := GenerateToken()
	if err != nil || !strings.HasPrefix(p, "cdt_") || len(p) != 4+43 {
		t.Fatalf("%q %v", p, err)
	}
	sum := sha256.Sum256([]byte(p))
	if !bytes.Equal(h, sum[:]) {
		t.Fatal("hash mismatch")
	}
	p2, _, _ := GenerateToken()
	if p == p2 {
		t.Fatal("not random")
	}
}

func TestTokenVerifierRoundTrip(t *testing.T) {
	db := openDB(t)
	now := time.Date(2026, 10, 23, 6, 0, 0, 0, time.UTC)
	plain := newTok(t, db, "t1", now, now.Add(24*time.Hour))
	v := NewTokenVerifier(db, func() time.Time { return now })
	id, err := v.Verify(authReq("Bearer " + plain))
	if err != nil {
		t.Fatal(err)
	}
	if id.Lane != LanePrivate || id.Subject != "token:t1" || id.TokenID != "t1" || !id.Has(ScopeRead) || id.Has(ScopeWrite) {
		t.Fatalf("%+v", id)
	}
	list, _ := db.ListTokens(context.Background())
	if list[0].LastUsedAt == nil || !list[0].LastUsedAt.Equal(now) {
		t.Fatalf("not touched: %+v", list[0])
	}
}

func TestTokenVerifierRevokedAndExpired(t *testing.T) {
	db := openDB(t)
	now := time.Date(2026, 10, 23, 6, 0, 0, 0, time.UTC)
	rev := newTok(t, db, "rev", now, now.Add(time.Hour))
	exp := newTok(t, db, "exp", now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err := db.RevokeToken(context.Background(), "rev", now); err != nil {
		t.Fatal(err)
	}
	v := NewTokenVerifier(db, func() time.Time { return now })
	if _, err := v.Verify(authReq("Bearer " + rev)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked: %v", err)
	}
	_, err := v.Verify(authReq("Bearer " + exp))
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	if strings.Contains(err.Error(), exp) {
		t.Fatal("error leaks token")
	}
	// exactly at expiry is expired
	if _, err := NewTokenVerifier(db, func() time.Time { return now.Add(-time.Hour) }).Verify(authReq("Bearer " + exp)); !errors.Is(err, ErrExpired) {
		t.Fatalf("at expiry: %v", err)
	}
}

func TestTokenVerifierMalformed(t *testing.T) {
	db := openDB(t)
	now := time.Now()
	v := NewTokenVerifier(db, func() time.Time { return now })
	if _, err := v.Verify(authReq("")); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing: %v", err)
	}
	for _, h := range []string{"Basic abc", "Bearer abc", "Bearer ", "cdt_abc", "Bearer cdt_unknown"} {
		_, err := v.Verify(authReq(h))
		if err != ErrInvalid { //nolint:errorlint // exact sentinel required
			t.Fatalf("%q: got %v, want exactly ErrInvalid", h, err)
		}
	}
}

func TestTokenVerifierSchemeCaseInsensitive(t *testing.T) {
	db := openDB(t)
	now := time.Date(2026, 10, 23, 6, 0, 0, 0, time.UTC)
	plain := newTok(t, db, "t1", now, now.Add(time.Hour))
	v := NewTokenVerifier(db, func() time.Time { return now })
	for _, h := range []string{"bearer " + plain, "BEARER " + plain, "Bearer  " + plain} {
		if _, err := v.Verify(authReq(h)); err != nil {
			t.Fatalf("%.12q: %v", h, err)
		}
	}
}

func TestTokenVerifierTouchThrottled(t *testing.T) {
	db := openDB(t)
	t0 := time.Date(2026, 10, 23, 6, 0, 0, 0, time.UTC)
	plain := newTok(t, db, "t1", t0, t0.Add(time.Hour))
	cur := t0
	v := NewTokenVerifier(db, func() time.Time { return cur })
	v.Verify(authReq("Bearer " + plain))
	cur = t0.Add(30 * time.Second)
	v.Verify(authReq("Bearer " + plain))
	list, _ := db.ListTokens(context.Background())
	if !list[0].LastUsedAt.Equal(t0) {
		t.Fatalf("touched again: %v", list[0].LastUsedAt)
	}
	cur = t0.Add(2 * time.Minute)
	v.Verify(authReq("Bearer " + plain))
	list, _ = db.ListTokens(context.Background())
	if !list[0].LastUsedAt.Equal(cur) {
		t.Fatalf("not touched after interval: %v", list[0].LastUsedAt)
	}
}
