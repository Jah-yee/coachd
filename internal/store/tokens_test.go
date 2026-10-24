package store

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestTokenRoundTripAndRevoke(t *testing.T) {
	db, ctx := openTest(t), context.Background()
	now := time.Date(2026, 10, 23, 6, 0, 0, 0, time.UTC)
	tok := TokenRecord{ID: "t1", Name: "laptop", Hash: []byte{1, 2, 3}, Scopes: []string{"read:activities", "read:health", "write:notes"}, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	if err := db.CreateToken(ctx, tok); err != nil {
		t.Fatal(err)
	}
	got, err := db.TokenByHash(ctx, []byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "t1" || got.Name != "laptop" || !bytes.Equal(got.Hash, tok.Hash) || len(got.Scopes) != 3 || got.Scopes[0] != "read:activities" || got.Scopes[2] != "write:notes" ||
		!got.CreatedAt.Equal(now) || !got.ExpiresAt.Equal(tok.ExpiresAt) || got.LastUsedAt != nil || got.RevokedAt != nil {
		t.Fatalf("%+v", got)
	}
	if _, err := db.TokenByHash(ctx, []byte{9}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	if err := db.TouchToken(ctx, "t1", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeToken(ctx, "t1", now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListTokens(ctx)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == nil || list[0].RevokedAt == nil || !list[0].RevokedAt.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("%+v %v", list, err)
	}
	if err := db.RevokeToken(ctx, "nope", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	// empty scopes round trip
	db.CreateToken(ctx, TokenRecord{ID: "t2", Name: "x", Hash: []byte{4}, CreatedAt: now, ExpiresAt: now})
	g2, _ := db.TokenByHash(ctx, []byte{4})
	if len(g2.Scopes) != 0 {
		t.Fatalf("scopes %v", g2.Scopes)
	}
}
