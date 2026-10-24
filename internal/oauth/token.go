package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

// Lifetimes pinned by spec §5.3 and §5.4.
const (
	codeTTL      = 5 * time.Minute
	accessTTL    = time.Hour
	refreshTTL   = 30 * 24 * time.Hour
	refreshGrace = 60 * time.Second
)

// errGrant is a code exchange whose client, redirect or PKCE verifier does not
// match the stored code. It never carries a presented value.
var errGrant = errors.New("oauth: code does not match the request")

var verifierRE = regexp.MustCompile(`^[A-Za-z0-9._~-]{43,128}$`)

// newToken returns prefix plus base64url of 32 random bytes, and the SHA-256
// of that text, the only form the store keeps.
func newToken(prefix string) (plain string, hash []byte) {
	b := make([]byte, 32)
	rand.Read(b) // never fails since Go 1.24
	plain = prefix + base64.RawURLEncoding.EncodeToString(b)
	return plain, hashToken(plain)
}

// hashToken is the stored form of a token or code: SHA-256 of its text.
func hashToken(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

// verifyPKCE checks an RFC 7636 S256 verifier against the stored challenge.
func verifyPKCE(challenge, verifier string) bool {
	if !verifierRE.MatchString(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(challenge)) == 1
}

// validResource reports whether v is an RFC 8707 resource naming this
// server's one protected resource: absent, <self>/mcp or <self>, with scheme
// and host compared case-insensitively and one trailing slash ignored. Every
// grant is for that resource (authorization accepts the same two forms), so a
// valid v always matches the grant's.
func (s *Server) validResource(v string) bool {
	if v == "" {
		return true
	}
	if strings.ContainsAny(v, "?#") {
		return false
	}
	u, err := url.Parse(v)
	if err != nil || u.Opaque != "" || u.User != nil || strings.ToLower(u.Scheme)+"://"+strings.ToLower(u.Host) != s.self {
		return false
	}
	p := strings.TrimSuffix(u.EscapedPath(), "/")
	return p == "" || p == "/mcp"
}

// tokenResponse is the RFC 6749 §5.1 answer.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

// noStore marks every answer of the token endpoint uncacheable (RFC 6749 §5.1).
func noStore(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Pragma", "no-cache")
		h(w, r)
	}
}

// tokenError answers an RFC 6749 §5.2 error and records its §9 outcome.
func tokenError(w http.ResponseWriter, code string) {
	outcome := code
	if code == "unsupported_grant_type" {
		outcome = "invalid_request"
	}
	setOutcome(w, outcome)
	writeError(w, http.StatusBadRequest, code)
}

// token is the token endpoint (spec §5.4). Parameters come from the form body
// only; an empty parameter counts as absent (RFC 6749 §3.1).
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	form, err := parseForm(r)
	if err != nil {
		if tooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request")
			return
		}
		tokenError(w, "invalid_request")
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		s.codeGrant(w, r, form)
	case "refresh_token":
		s.refreshGrant(w, r, form)
	case "":
		tokenError(w, "invalid_request")
	default:
		tokenError(w, "unsupported_grant_type")
	}
}

// codeGrant exchanges an authorization code. The client binding is enforced
// by the store: the code's client must equal client_id, so an unknown client
// is invalid_grant like any mismatch.
func (s *Server) codeGrant(w http.ResponseWriter, r *http.Request, form url.Values) {
	code, clientID, verifier := form.Get("code"), form.Get("client_id"), form.Get("code_verifier")
	if code == "" || clientID == "" || verifier == "" {
		tokenError(w, "invalid_request")
		return
	}
	if !s.validResource(form.Get("resource")) {
		tokenError(w, "invalid_target")
		return
	}
	redirect := form.Get("redirect_uri")
	check := func(c store.OAuthCode) error {
		if c.ClientID != clientID {
			return errGrant
		}
		if redirect != "" && redirect != c.RedirectURI {
			return errGrant
		}
		if !verifyPKCE(c.CodeChallenge, verifier) {
			return errGrant
		}
		return nil
	}
	at := s.d.Now()
	access, refresh, nt := mint(at)
	red, err := s.d.DB.RedeemCode(r.Context(), hashToken(code), at, check, nt)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrCodeReused):
		s.reused(w)
		return
	case errors.Is(err, errGrant), errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrExpired), errors.Is(err, store.ErrRevoked):
		tokenError(w, "invalid_grant")
		return
	default:
		s.logFail(r.Context(), "token", err)
		writeError(w, http.StatusInternalServerError, "server_error")
		return
	}
	if red.FirstUse && s.d.Notifier != nil {
		s.d.Notifier.AppConnected()
	}
	issue(w, access, refresh, red.Grant.Scopes)
}

// refreshGrant rotates a refresh token. The store matches the token to
// client_id, allows a reuse within refreshGrace and revokes after it.
func (s *Server) refreshGrant(w http.ResponseWriter, r *http.Request, form url.Values) {
	rt, clientID := form.Get("refresh_token"), form.Get("client_id")
	if rt == "" || clientID == "" {
		tokenError(w, "invalid_request")
		return
	}
	if !s.validResource(form.Get("resource")) {
		tokenError(w, "invalid_target")
		return
	}
	// Ruling R-BA-F2: with DCR off, a DCR client's grants do not refresh.
	// Refused before the store, so nothing is spent, revoked or audited.
	if !s.d.DCR && strings.HasPrefix(clientID, dcrPrefix) {
		tokenError(w, "invalid_grant")
		return
	}
	at := s.d.Now()
	access, refresh, nt := mint(at)
	rot, err := s.d.DB.RotateRefresh(r.Context(), hashToken(rt), clientID, at, refreshGrace, narrowScopes(form.Get("scope")), nt)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrTokenReused):
		s.reused(w)
		return
	case errors.Is(err, store.ErrScope):
		tokenError(w, "invalid_scope")
		return
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrExpired), errors.Is(err, store.ErrRevoked):
		tokenError(w, "invalid_grant")
		return
	default:
		s.logFail(r.Context(), "token", err)
		writeError(w, http.StatusInternalServerError, "server_error")
		return
	}
	issue(w, access, refresh, rot.Scopes)
}

// reused answers a code or refresh token replay that revoked its grant.
func (s *Server) reused(w http.ResponseWriter) {
	if s.d.Notifier != nil {
		s.d.Notifier.ConnectionRevoked()
	}
	setOutcome(w, "reuse")
	writeError(w, http.StatusBadRequest, "invalid_grant")
}

// mint creates a fresh access and refresh token issued at at.
func mint(at time.Time) (access, refresh string, t store.NewTokens) {
	access, t.AccessHash = newToken("cda_")
	refresh, t.RefreshHash = newToken("cdr_")
	t.AccessExpires, t.RefreshExpires = at.Add(accessTTL), at.Add(refreshTTL)
	return access, refresh, t
}

func issue(w http.ResponseWriter, access, refresh string, scopes []string) {
	writeJSON(w, http.StatusOK, tokenResponse{AccessToken: access, TokenType: "Bearer", ExpiresIn: int(accessTTL / time.Second),
		RefreshToken: refresh, Scope: grantedScope(scopes)})
}

// grantedScope is the response's scope: the coach:* scopes held, in canonical order.
func grantedScope(held []string) string {
	var out []string
	for _, sc := range scopesProtected {
		if slices.Contains(held, sc) {
			out = append(out, sc)
		}
	}
	return strings.Join(out, " ")
}

// narrowScopes reads a refresh request's scope parameter: nil when absent,
// which keeps the token's scopes, else the distinct requested scopes.
// offline_access is accepted and ignored, as at authorization, so a request
// naming only it keeps the token's scopes too.
func narrowScopes(v string) []string {
	var out []string
	for _, sc := range strings.Fields(v) {
		if sc != "offline_access" && !slices.Contains(out, sc) {
			out = append(out, sc)
		}
	}
	return out
}
