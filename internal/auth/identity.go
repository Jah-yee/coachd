// Package auth authenticates requests on the public (Cloudflare Access) and private (static token) lanes.
package auth

import (
	"context"
	"errors"
	"slices"
)

type Lane string

const (
	LanePublic  Lane = "public"
	LanePrivate Lane = "private"
)

const (
	ScopeRead  = "coach:read"
	ScopeWrite = "coach:write"
)

// Sentinel errors. Their text never carries credentials or e-mail addresses.
var (
	ErrMissing        = errors.New("auth: credentials missing")
	ErrInvalid        = errors.New("auth: credentials invalid")
	ErrExpired        = errors.New("auth: credentials expired")
	ErrRevoked        = errors.New("auth: token revoked")
	ErrForbiddenEmail = errors.New("auth: identity not allowed")
	// ErrUnavailable means the verifier could not decide (store outage); it says nothing about the credential.
	ErrUnavailable = errors.New("auth: verifier unavailable")
)

type Identity struct {
	Lane                     Lane
	Subject, TokenID, Client string
	Scopes                   []string
}

type ctxKey struct{}

func WithIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(ctxKey{}).(Identity)
	return id, ok
}

func (id Identity) Has(scope string) bool { return slices.Contains(id.Scopes, scope) }
