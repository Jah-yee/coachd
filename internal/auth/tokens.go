package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

const (
	tokenPrefix   = "cdt_"
	touchInterval = time.Minute
)

// GenerateToken returns a new plaintext token and its sha256 hash.
func GenerateToken() (plain string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	plain = tokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(plain))
	return plain, h[:], nil
}

// HashToken returns the storage hash of a plaintext token.
func HashToken(plain string) []byte {
	h := sha256.Sum256([]byte(plain))
	return h[:]
}

type TokenVerifier struct {
	db  *store.DB
	now func() time.Time
}

func NewTokenVerifier(db *store.DB, now func() time.Time) *TokenVerifier {
	return &TokenVerifier{db: db, now: now}
}

func (v *TokenVerifier) Verify(r *http.Request) (Identity, error) {
	h := r.Header.Get("Authorization")
	if strings.TrimSpace(h) == "" {
		return Identity{}, ErrMissing
	}
	// RFC 7235: the auth scheme is case-insensitive.
	if len(h) <= 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return Identity{}, ErrInvalid
	}
	plain := strings.TrimSpace(h[7:])
	if !strings.HasPrefix(plain, tokenPrefix) {
		return Identity{}, ErrInvalid
	}
	rec, err := v.db.TokenByHash(r.Context(), HashToken(plain))
	if errors.Is(err, store.ErrNotFound) {
		return Identity{}, ErrInvalid
	}
	if err != nil {
		return Identity{}, errors.New("auth: token lookup failed")
	}
	now := v.now()
	if rec.RevokedAt != nil {
		return Identity{}, ErrRevoked
	}
	if !now.Before(rec.ExpiresAt) {
		return Identity{}, ErrExpired
	}
	if rec.LastUsedAt == nil || now.Sub(*rec.LastUsedAt) > touchInterval {
		_ = v.db.TouchToken(r.Context(), rec.ID, now) // best effort; must not affect the auth decision
	}
	return Identity{Lane: LanePrivate, Subject: "token:" + rec.ID, TokenID: rec.ID, Scopes: rec.Scopes}, nil
}
