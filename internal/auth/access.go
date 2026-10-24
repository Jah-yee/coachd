package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

const accessHeader = "Cf-Access-Jwt-Assertion"

// AccessVerifier validates Cloudflare Access JWTs.
type AccessVerifier struct {
	verifier     *oidc.IDTokenVerifier
	allowedEmail string
}

func NewAccessVerifier(ctx context.Context, teamDomain, aud, allowedEmail string) *AccessVerifier {
	issuer := normalizeTeamDomain(teamDomain)
	// Bound JWKS fetches so a hung endpoint cannot stall requests indefinitely.
	ctx = oidc.ClientContext(ctx, &http.Client{Timeout: 10 * time.Second})
	keys := oidc.NewRemoteKeySet(ctx, issuer+"/cdn-cgi/access/certs")
	return &AccessVerifier{
		verifier:     oidc.NewVerifier(issuer, keys, &oidc.Config{ClientID: aud}),
		allowedEmail: strings.TrimSpace(allowedEmail),
	}
}

func (v *AccessVerifier) Verify(r *http.Request) (Identity, error) {
	raw := strings.TrimSpace(r.Header.Get(accessHeader))
	if raw == "" {
		return Identity{}, ErrMissing
	}
	tok, err := v.verifier.Verify(r.Context(), raw)
	if err != nil {
		var exp *oidc.TokenExpiredError
		if errors.As(err, &exp) {
			return Identity{}, ErrExpired
		}
		return Identity{}, ErrInvalid // deliberately drops err: it may echo token content
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := tok.Claims(&claims); err != nil {
		return Identity{}, ErrInvalid
	}
	email := strings.TrimSpace(claims.Email)
	if email == "" || !strings.EqualFold(email, v.allowedEmail) {
		return Identity{}, ErrForbiddenEmail
	}
	return Identity{Lane: LanePublic, Subject: strings.ToLower(email), Scopes: []string{ScopeRead, ScopeWrite}}, nil
}

// normalizeTeamDomain prepends https:// to a bare host and trims trailing slashes.
func normalizeTeamDomain(d string) string {
	d = strings.TrimRight(strings.TrimSpace(d), "/")
	if !strings.Contains(d, "://") {
		d = "https://" + d
	}
	return d
}
