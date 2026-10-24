package oauth

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/stefiix92/coachd/internal/auth"
	"github.com/stefiix92/coachd/internal/store"
)

// accessPrefix starts every access token this server issues.
const accessPrefix = "cda_"

// VerifyAccess authenticates a /mcp request by its OAuth access token. It
// reads only the Authorization header (never the query) and accepts only the
// Bearer scheme, matched case-insensitively, with exactly one token.
//
// Errors: no usable Authorization header is auth.ErrMissing; a wrong prefix
// (a cdt_ private-lane token included) or an unknown token is auth.ErrInvalid;
// an expired token or revoked grant is auth.ErrExpired or auth.ErrRevoked. A
// store failure returns auth.ErrUnavailable (the middleware answers 503, not 401).
func (s *Server) VerifyAccess(r *http.Request) (auth.Identity, error) {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	scheme, tok, ok := strings.Cut(h, " ")
	if h == "" || !ok || !strings.EqualFold(scheme, "Bearer") || tok == "" {
		return auth.Identity{}, auth.ErrMissing
	}
	if strings.ContainsAny(tok, " \t") {
		return auth.Identity{}, auth.ErrInvalid
	}
	if !strings.HasPrefix(tok, accessPrefix) {
		return auth.Identity{}, auth.ErrInvalid
	}
	g, scopes, err := s.d.DB.AccessGrant(r.Context(), hashToken(tok), s.d.Now())
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNotFound):
		return auth.Identity{}, auth.ErrInvalid
	case errors.Is(err, store.ErrExpired):
		return auth.Identity{}, auth.ErrExpired
	case errors.Is(err, store.ErrRevoked):
		return auth.Identity{}, auth.ErrRevoked
	default:
		return auth.Identity{}, fmt.Errorf("oauth: verify access: %w: %w", auth.ErrUnavailable, err)
	}
	// Ruling R-BA-F2: with DCR off, grants of dynamically registered clients
	// are refused (not revoked); turning DCR on again restores them.
	if !s.d.DCR && strings.HasPrefix(g.ClientID, dcrPrefix) {
		return auth.Identity{}, auth.ErrRevoked
	}
	id := strconv.FormatInt(g.ID, 10)
	return auth.Identity{Lane: auth.LanePublic, Subject: "grant:" + id, TokenID: "g" + id, Scopes: scopes}, nil
}

// Challenge is the WWW-Authenticate value for a failed /mcp request (RFC 9728
// resource_metadata, RFC 6750 error). It is built from the configured public
// URL only. A request with no credentials gets no error code.
func (s *Server) Challenge(err error) string {
	c := `Bearer resource_metadata="` + s.self + `/.well-known/oauth-protected-resource/mcp", scope="coach:read coach:write"`
	if errors.Is(err, auth.ErrMissing) {
		return c
	}
	return c + `, error="invalid_token"`
}
