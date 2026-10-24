package oauth

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"

	"github.com/stefiix92/coachd/internal/passkey"
	"github.com/stefiix92/coachd/internal/store"
)

// requestTTL is how long a consent page's signed request token stays valid.
const requestTTL = 10 * time.Minute

const maxState = 1 << 10

var challengeRE = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// RedirectError sends the browser back to the client with an RFC 6749
// §4.1.2.1 error. It is returned only once the client and its redirect URI
// have been validated; URL carries the error, the request's state and iss.
type RedirectError struct{ URL string }

// Error never quotes the URL, which carries the client's state.
func (e *RedirectError) Error() string { return "oauth: authorization error, redirect to the client" }

// PageError is shown on coachd's own error page; the browser is never
// redirected. Message is fixed text for the runner, never request data.
type PageError struct{ Message string }

func (e *PageError) Error() string { return e.Message }

// ErrRequestExpired is a genuine request token past requestTTL: a consent
// page left open. A token that does not verify is errBadRequest instead.
var ErrRequestExpired = &PageError{Message: "This connection request has expired. Start the connection again from your app."}

var (
	errBadRequest = &PageError{Message: "This connection request is not valid. Start the connection again from your app."}
	errClient     = &PageError{Message: "coachd could not identify the app that asked to connect. Start the connection again from your app."}
	errRedirect   = &PageError{Message: "The app did not give a return address registered for it, so coachd will not send you back to it."}
	errNoPasskey  = &PageError{Message: "No passkey is enrolled, so nothing can approve this connection. Run coachd enroll on the server, then start the connection again from your app."}
	errRecognise  = &PageError{Message: "Confirm that you recognise where the app will send you, then approve again."}
	errNoScope    = &PageError{Message: "This app asked only for write access. Allow write access to approve, or deny."}
)

// Consent is what the consent page shows; every field comes from validated
// data, and Request carries it all to the ceremony.
type Consent struct {
	Request string // signed request token
	Kind    string // "cimd" or "dcr"
	Name    string // display-safe client_name, "unnamed" if none; the app's claim, not checked
	Host    string // the client_id's host in punycode; "" for DCR
	// HostDomain is Host's registrable domain (publicsuffix.EffectiveTLDPlusOne),
	// to emphasise; "" for DCR.
	HostDomain string
	// RedirectHost is where the browser goes next (punycode).
	RedirectHost string
	// DomainMismatch: RedirectHost's registrable domain is not HostDomain
	// (CIMD, non-loopback redirects only).
	DomainMismatch bool
	// LoopbackOnly: the code goes to a loopback redirect, where any program
	// on the runner's computer may be listening. Its warning stands in for
	// DomainMismatch, which a loopback host would always trip.
	LoopbackOnly bool
	Scopes       []string // the requested coach:* scopes, canonical order
	// WriteDefault is the coach:write checkbox's initial state (CIMD: on).
	WriteDefault bool
	// RequireRecognise asks for the "I recognise <redirect host>" checkbox (DCR).
	RequireRecognise bool
}

// Approval is a finished consent ceremony: the request and scopes are the
// ones the passkey assertion bound (passkey.Ceremony.Request and Scopes),
// never values posted next to it.
type Approval struct {
	Request        string
	Scopes         []string
	PasskeyID      int64
	CredentialJSON []byte // the credential after the assertion, for its sign count
	Nonce          string // the ceremony's nonce, consumed with the grant
	NonceExpires   time.Time
}

// authRequest is the validated authorization request the consent page
// carries, signed so that no field can come from the browser.
type authRequest struct {
	ClientID         string    `json:"c"`
	RedirectURI      string    `json:"r"` // as sent, or the one registered when omitted
	RedirectExplicit bool      `json:"x,omitempty"`
	CodeChallenge    string    `json:"cc"`
	Scopes           []string  `json:"sc"`
	Resource         string    `json:"rs"` // always <self>/mcp
	State            string    `json:"st,omitempty"`
	Expires          time.Time `json:"exp"`
}

// signRequest returns base64url(json) "." base64url(HMAC-SHA256(requestKey, json)).
func (s *Server) signRequest(r authRequest) (string, error) {
	payload, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("oauth: sign request: %w", err)
	}
	return b64.EncodeToString(payload) + "." + b64.EncodeToString(blobMAC(s.requestKey, payload)), nil
}

// openRequest verifies a request token: the MAC (hmac.Equal) before anything
// in the payload is read, then the fields, then the expiry. A token that
// verifies but has expired is ErrRequestExpired; any other failure is
// errBadRequest, so a forgery never reads as a page left open.
func (s *Server) openRequest(token string) (authRequest, error) {
	p, m, ok := strings.Cut(token, ".")
	if !ok {
		return authRequest{}, errBadRequest
	}
	payload, err := b64.DecodeString(p)
	if err != nil {
		return authRequest{}, errBadRequest
	}
	sum, err := b64.DecodeString(m)
	if err != nil || !hmac.Equal(sum, blobMAC(s.requestKey, payload)) {
		return authRequest{}, errBadRequest
	}
	var r authRequest
	if err := json.Unmarshal(payload, &r); err != nil {
		return authRequest{}, errBadRequest
	}
	if r.ClientID == "" || r.RedirectURI == "" || r.CodeChallenge == "" || len(r.Scopes) == 0 || r.Resource == "" || r.Expires.IsZero() {
		return authRequest{}, errBadRequest
	}
	if !s.d.Now().Before(r.Expires) {
		return authRequest{}, ErrRequestExpired
	}
	return r, nil
}

// single returns the parameter's value, "" when absent or empty (RFC 6749
// §3.1: a parameter without a value is omitted); false when it is repeated.
func single(q url.Values, key string) (string, bool) {
	v := q[key]
	if len(v) > 1 {
		return "", false
	}
	if len(v) == 0 {
		return "", true
	}
	return v[0], true
}

// clientForm checks a client_id's form without the network or the store: a
// CIMD URL (spec §5.2) or, with DCR on, a DCR id whose signature verifies.
// It returns the §9 outcome of a refusal.
func (s *Server) clientForm(q url.Values) (string, string, bool) {
	id, ok := single(q, "client_id")
	switch {
	case !ok || id == "":
	case strings.HasPrefix(id, dcrPrefix):
		if _, err := verifyDCR(s.res.dcrKey, id); err == nil && s.d.DCR {
			return id, "", true
		}
	default:
		if _, ok := cimdHost(id); ok {
			return id, "", true
		}
	}
	return "", "invalid_client", false
}

// CheckSyntax checks an authorization request that arrives without a
// session: only the form of client_id and redirect_uri (spec §5.3, I3). It
// never fetches or stores anything, so the caller may then send the browser
// to the login page and nowhere else. Any failure is a *PageError. It counts
// the request (endpoint authorize, outcome login_required when it passes), so
// call it only when there is no session; Authorize counts its own.
func (s *Server) CheckSyntax(q url.Values) error {
	ctx := context.Background()
	if _, outcome, ok := s.clientForm(q); !ok {
		s.count(ctx, "authorize", outcome)
		return errClient
	}
	if r, ok := single(q, "redirect_uri"); !ok || (r != "" && !validRedirectURI(r, s.self)) {
		s.count(ctx, "authorize", "invalid_request")
		return errRedirect
	}
	s.count(ctx, "authorize", "login_required")
	return nil
}

// Authorize validates an authorization request from the signed-in runner, in
// the order of spec §5.3, and returns what the consent page shows:
//  1. the client resolves (fetching its metadata document if needed) and the
//     redirect URI is registered for it; otherwise a *PageError and never a
//     redirect;
//  2. the request itself; its errors are a *RedirectError to the client with
//     error, state and iss;
//  3. a passkey is enrolled; otherwise a *PageError.
//
// Any other error is coachd's own failure.
func (s *Server) Authorize(ctx context.Context, q url.Values) (Consent, error) {
	c, outcome, err := s.authorize(ctx, q)
	s.count(ctx, "authorize", outcome)
	return c, err
}

// authorize is Authorize, returning the §9 outcome to count as well.
func (s *Server) authorize(ctx context.Context, q url.Values) (Consent, string, error) {
	id, outcome, ok := s.clientForm(q)
	if !ok {
		return Consent{}, outcome, errClient
	}
	c, err := s.res.resolve(ctx, id)
	if err != nil {
		if strings.HasPrefix(id, dcrPrefix) {
			return Consent{}, "invalid_client", errClient
		}
		return Consent{}, "fetch_failed", errClient // a CIMD client resolves from its document or not at all
	}
	sent, ok := single(q, "redirect_uri")
	if !ok {
		return Consent{}, "invalid_request", errRedirect
	}
	redirect, explicit := sent, sent != ""
	if !explicit {
		// Omitted: only when exactly one non-loopback URI is registered
		// (OAuth 2.1 §2.3.2).
		if len(c.RedirectURIs) != 1 || isLoopbackURI(c.RedirectURIs[0]) {
			return Consent{}, "invalid_request", errRedirect
		}
		redirect = c.RedirectURIs[0]
	}
	if !validRedirectURI(redirect, s.self) || !matchRedirect(c.RedirectURIs, redirect) {
		return Consent{}, "invalid_request", errRedirect
	}
	rhost, ok := redirectHost(redirect)
	if !ok {
		return Consent{}, "invalid_request", errRedirect
	}

	// The client and redirect are validated: from here on errors go back to it.
	req, code := s.parseRequest(q)
	if code != "" {
		outcome := code
		if code == "unsupported_response_type" {
			outcome = "invalid_request"
		}
		to, err := s.clientRedirect(redirect, url.Values{"error": {code}}, q.Get("state"))
		if err != nil {
			return Consent{}, "invalid_request", err
		}
		return Consent{}, outcome, &RedirectError{URL: to}
	}
	req.ClientID, req.RedirectURI, req.RedirectExplicit = c.ClientID, redirect, explicit
	req.Expires = s.d.Now().Add(requestTTL)

	pks, err := s.d.DB.ActivePasskeys(ctx)
	if err != nil {
		return Consent{}, "error", fmt.Errorf("oauth: authorize: %w", err)
	}
	if len(pks) == 0 {
		return Consent{}, "login_required", errNoPasskey
	}
	token, err := s.signRequest(req)
	if err != nil {
		return Consent{}, "error", err
	}
	loopback := isLoopbackURI(redirect)
	out := Consent{Request: token, Kind: c.Kind, Name: c.Name, RedirectHost: rhost, LoopbackOnly: loopback,
		Scopes: req.Scopes, WriteDefault: c.Kind == "cimd", RequireRecognise: c.Kind == "dcr"}
	if c.Kind == "cimd" {
		out.Host, out.HostDomain = c.Host, registrable(c.Host)
		out.DomainMismatch = !loopback && registrable(rhost) != out.HostDomain
	}
	return out, "ok", nil
}

// parseRequest checks everything but the client and redirect (spec §5.3 step
// 2) and returns the request's fields, or the RFC 6749 error code.
func (s *Server) parseRequest(q url.Values) (authRequest, string) {
	for _, v := range q {
		if len(v) > 1 {
			return authRequest{}, "invalid_request"
		}
	}
	switch q.Get("response_type") {
	case "code":
	case "":
		return authRequest{}, "invalid_request"
	default:
		return authRequest{}, "unsupported_response_type"
	}
	challenge := q.Get("code_challenge")
	if q.Get("code_challenge_method") != "S256" || !challengeRE.MatchString(challenge) {
		return authRequest{}, "invalid_request"
	}
	state := q.Get("state")
	if len(state) > maxState {
		return authRequest{}, "invalid_request"
	}
	scopes, ok := requestedScopes(q.Get("scope"))
	if !ok {
		return authRequest{}, "invalid_scope"
	}
	if !s.validResource(q.Get("resource")) {
		return authRequest{}, "invalid_target"
	}
	return authRequest{CodeChallenge: challenge, Scopes: scopes, Resource: s.self + "/mcp", State: state}, ""
}

// requestedScopes parses a scope parameter into the coach:* scopes in
// canonical order. offline_access is accepted and ignored; naming no coach
// scope means both. An unsupported scope is false.
func requestedScopes(v string) ([]string, bool) {
	fields := strings.Fields(v)
	for _, sc := range fields {
		if !slices.Contains(scopesAS, sc) {
			return nil, false
		}
	}
	var out []string
	for _, sc := range scopesProtected {
		if slices.Contains(fields, sc) {
			out = append(out, sc)
		}
	}
	if len(out) == 0 {
		out = slices.Clone(scopesProtected)
	}
	return out, true
}

// redirectHost is the redirect URI's host as the consent page shows it:
// punycode, or the address of an IP literal.
func redirectHost(redirect string) (string, bool) {
	u, ok := parseRedirect(redirect)
	if !ok {
		return "", false
	}
	h := u.Hostname()
	if a, err := netip.ParseAddr(h); err == nil {
		return a.String(), true
	}
	h, err := idna.Lookup.ToASCII(h)
	return h, err == nil && h != ""
}

// registrable is host's registrable domain, or host itself when it has none
// (an IP address, localhost, a public suffix).
func registrable(host string) string {
	if _, err := netip.ParseAddr(host); err == nil {
		return host
	}
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	return host
}

// clientRedirect appends params, the state when there is one, and iss to
// redirect, keeping its own query as it is (RFC 6749 §3.1.2). redirect has
// passed validRedirectURI, so it parses; if not, the answer is errRedirect,
// never an empty URL.
func (s *Server) clientRedirect(redirect string, params url.Values, state string) (string, error) {
	u, err := url.Parse(redirect)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errRedirect
	}
	if state != "" {
		params.Set("state", state)
	}
	params.Set("iss", s.self)
	if u.RawQuery != "" {
		u.RawQuery += "&" + params.Encode()
	} else {
		u.RawQuery = params.Encode()
	}
	return u.String(), nil
}

// reopen verifies a request token and re-validates its client and redirect,
// as begin, finish and deny must (spec §5.3). The client is the stored one,
// never fetched: only GET /oauth/authorize fetches (spec §5.2), and it stored
// the row it validated at most requestTTL ago. Every failure is a *PageError.
func (s *Server) reopen(ctx context.Context, token string) (authRequest, client, error) {
	req, err := s.openRequest(token)
	if err != nil {
		return authRequest{}, client{}, err
	}
	if strings.HasPrefix(req.ClientID, dcrPrefix) && !s.d.DCR {
		return authRequest{}, client{}, errClient
	}
	c, err := s.res.stored(ctx, req.ClientID)
	if err != nil {
		return authRequest{}, client{}, errClient
	}
	if !validRedirectURI(req.RedirectURI, s.self) || !matchRedirect(c.RedirectURIs, req.RedirectURI) {
		return authRequest{}, client{}, errRedirect
	}
	return req, c, nil
}

// consentOutcome is the §9 outcome of a begin, finish or deny.
func consentOutcome(err error, ok string) string {
	var pe *PageError
	switch {
	case err == nil:
		return ok
	case errors.As(err, &pe), errors.Is(err, store.ErrNonceUsed), errors.Is(err, passkey.ErrUnknownCredential):
		return "invalid_request"
	}
	return "error"
}

// Begin re-validates the request and the stored client before the passkey
// ceremony starts and returns the scopes to grant: coach:read, plus
// coach:write when write is set, each only if requested. A DCR client needs
// recognise. Every refusal is a *PageError (ErrRequestExpired for a page left
// open); the caller then binds the scopes with passkey.Service.BeginConsent,
// for no longer than the returned expiry of the request token.
func (s *Server) Begin(ctx context.Context, request string, write, recognise bool) ([]string, time.Time, error) {
	scopes, exp, err := s.begin(ctx, request, write, recognise)
	s.count(ctx, "consent", consentOutcome(err, "ok"))
	return scopes, exp, err
}

func (s *Server) begin(ctx context.Context, request string, write, recognise bool) ([]string, time.Time, error) {
	req, c, err := s.reopen(ctx, request)
	if err != nil {
		return nil, time.Time{}, err
	}
	if c.Kind == "dcr" && !recognise {
		return nil, time.Time{}, errRecognise
	}
	var out []string
	for _, sc := range req.Scopes {
		if sc == "coach:read" || (sc == "coach:write" && write) {
			out = append(out, sc)
		}
	}
	if len(out) == 0 {
		return nil, time.Time{}, errNoScope
	}
	return out, req.Expires, nil
}

// Approve records a finished consent ceremony: it re-validates the request
// and the stored client, then in one store transaction consumes the nonce, records the
// passkey use, inserts a DCR client at its first consent, and creates the
// grant and its code. It returns the client redirect with code, state and iss.
//
// Errors: a *PageError (ErrRequestExpired for a page left open); one matching
// store.ErrNonceUsed for a replayed assertion; passkey.ErrUnknownCredential
// when the passkey was revoked in between; one matching passkey.ErrInternal
// for coachd's own failure.
func (s *Server) Approve(ctx context.Context, a Approval) (string, error) {
	redirect, err := s.approve(ctx, a)
	s.count(ctx, "consent", consentOutcome(err, "ok"))
	return redirect, err
}

func (s *Server) approve(ctx context.Context, a Approval) (string, error) {
	req, c, err := s.reopen(ctx, a.Request)
	if err != nil {
		return "", err
	}
	scopes, ok := grantedScopes(req.Scopes, a.Scopes)
	if !ok {
		return "", errBadRequest
	}
	if a.PasskeyID <= 0 || a.Nonce == "" || a.NonceExpires.IsZero() {
		return "", fmt.Errorf("oauth: approve: %w: no passkey assertion", passkey.ErrInternal)
	}
	now := s.d.Now()
	cl := store.OAuthClient{ID: c.RowID}
	if c.RowID == 0 { // a DCR client's first consent stores it
		cl = store.OAuthClient{ClientID: c.ClientID, Kind: c.Kind, Name: c.Name, RedirectURIs: c.RedirectURIs,
			Metadata: []byte("{}"), FetchedAt: now, CacheUntil: now, CreatedAt: now}
	}
	code, codeHash := newToken("")
	to, err := s.clientRedirect(req.RedirectURI, url.Values{"code": {code}}, req.State)
	if err != nil {
		return "", err
	}
	_, err = s.d.DB.GrantConsent(ctx, store.ConsentGrant{Client: cl, PasskeyID: a.PasskeyID, CredentialJSON: a.CredentialJSON,
		Nonce: a.Nonce, NonceExpires: a.NonceExpires, Scopes: scopes, Resource: req.Resource, CodeHash: codeHash,
		RedirectURI: req.RedirectURI, RedirectExplicit: req.RedirectExplicit, CodeChallenge: req.CodeChallenge,
		CodeExpires: now.Add(codeTTL), At: now})
	switch {
	case err == nil:
	case errors.Is(err, store.ErrNonceUsed):
		return "", fmt.Errorf("oauth: approve: %w", err)
	case errors.Is(err, store.ErrNotFound):
		return "", fmt.Errorf("oauth: approve: %w", passkey.ErrUnknownCredential)
	default:
		return "", fmt.Errorf("oauth: approve: %w: %w", passkey.ErrInternal, err)
	}
	return to, nil
}

// grantedScopes checks the ceremony's scopes against the request's: distinct,
// non-empty and requested. It returns them in canonical order.
func grantedScopes(requested, chosen []string) ([]string, bool) {
	if len(chosen) == 0 {
		return nil, false
	}
	for i, sc := range chosen {
		if !slices.Contains(requested, sc) || slices.Contains(chosen[:i], sc) {
			return nil, false
		}
	}
	var out []string
	for _, sc := range scopesProtected {
		if slices.Contains(chosen, sc) {
			out = append(out, sc)
		}
	}
	return out, true
}

// Deny re-validates the request and the stored client, audits the refusal
// and returns the client redirect with error=access_denied, state and iss. A
// token that does not verify is a *PageError and is not audited. Any other
// error is coachd's own failure.
func (s *Server) Deny(ctx context.Context, request string) (string, error) {
	redirect, err := s.deny(ctx, request)
	s.count(ctx, "consent", consentOutcome(err, "access_denied"))
	return redirect, err
}

func (s *Server) deny(ctx context.Context, request string) (string, error) {
	req, c, err := s.reopen(ctx, request)
	if err != nil {
		return "", err
	}
	to, err := s.clientRedirect(req.RedirectURI, url.Values{"error": {"access_denied"}}, req.State)
	if err != nil {
		return "", err
	}
	target := "dcr" // a DCR client that never consented has no row
	if c.RowID != 0 {
		target = "c" + strconv.FormatInt(c.RowID, 10)
	}
	if err := s.d.DB.AppendAudit(ctx, store.AuditEvent{At: s.d.Now(), Lane: "public", Actor: "runner",
		Event: "oauth_consent", Target: target, Outcome: "denied"}); err != nil {
		return "", fmt.Errorf("oauth: deny: %w", err)
	}
	return to, nil
}
