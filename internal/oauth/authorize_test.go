package oauth

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/passkey"
	"github.com/stefiix92/coachd/internal/store"
)

const (
	claudeAI         = "https://claude.ai/oauth/mcp-oauth-client-metadata"
	claudeAIRedirect = "https://claude.ai/api/mcp/auth_callback"
	claudeCode       = "https://claude.ai/oauth/claude-code-client-metadata"
	evilID           = "https://evil.example/c.json"
	evilRedirect     = "https://claude.ai.attacker.net/cb"
	aID              = "https://a.com/c.json"
	aRedirect        = "https://a.com/cb?x=1"
	twoID            = "https://two.example/c.json"
	loopID           = "https://loop.example/c.json"
	idnID            = "https://idn.example/c.json"
	idnRedirect      = "https://b\u00fccher.example/cb"
)

// docStub serves fixed metadata documents in place of the network fetch,
// cached for ttl (1 h when zero).
type docStub struct {
	docs  map[string]document
	calls int
	ttl   time.Duration
}

func (d *docStub) fetch(_ context.Context, clientID, _ string) (document, time.Duration, error) {
	d.calls++
	doc, ok := d.docs[clientID]
	if !ok {
		return document{}, 0, errFetch
	}
	if d.ttl == 0 {
		return doc, time.Hour, nil
	}
	return doc, d.ttl, nil
}

func fixtureDoc(t *testing.T, file, clientID string) document {
	t.Helper()
	body, err := os.ReadFile("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := parseDocument(clientID, body, testSelf)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// authEnv is a test server whose CIMD fetch serves the claude.ai and Claude
// Code fixtures and three made-up clients, with a passkey enrolled.
type authEnv struct {
	*testEnv
	docs *docStub
}

func newAuthEnv(t *testing.T, dcr bool) *authEnv {
	t.Helper()
	e := newTestEnv(t, testSelf, dcr)
	docs := &docStub{docs: map[string]document{
		claudeAI:   fixtureDoc(t, "claude-ai.json", claudeAI),
		claudeCode: fixtureDoc(t, "claude-code.json", claudeCode),
		evilID:     {ClientID: evilID, Name: "Claude", RedirectURIs: []string{evilRedirect}, Raw: []byte(`{}`)},
		aID:        {ClientID: aID, Name: "A", RedirectURIs: []string{aRedirect}, Raw: []byte(`{}`)},
		twoID:      {ClientID: twoID, Name: "Two", RedirectURIs: []string{"https://two.example/a", "https://two.example/b"}, Raw: []byte(`{}`)},
		loopID:     {ClientID: loopID, Name: "Loop", RedirectURIs: []string{"http://127.0.0.1/cb"}, Raw: []byte(`{}`)},
		idnID:      {ClientID: idnID, Name: "IDN", RedirectURIs: []string{idnRedirect}, Raw: []byte(`{}`)},
	}}
	e.s.res.fetchDoc = docs.fetch
	e.ensurePasskey(t)
	return &authEnv{testEnv: e, docs: docs}
}

// testChallenge is a well-formed S256 challenge.
var testChallenge = base64.RawURLEncoding.EncodeToString(sha("verifier"))

// authQuery is a valid authorization request; an empty redirect is omitted.
func authQuery(clientID, redirect string) url.Values {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"code_challenge":        {testChallenge},
		"code_challenge_method": {"S256"},
		"state":                 {"st-1"},
	}
	if redirect != "" {
		q.Set("redirect_uri", redirect)
	}
	return q
}

func pageError(err error) bool {
	var pe *PageError
	return errors.As(err, &pe) && pe.Message != ""
}

func redirectURL(t *testing.T, err error) *url.URL {
	t.Helper()
	var re *RedirectError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RedirectError", err)
	}
	u, perr := url.Parse(re.URL)
	if perr != nil {
		t.Fatalf("redirect %q: %v", re.URL, perr)
	}
	return u
}

func (e *authEnv) registerDCR(t *testing.T, name string, redirects ...string) string {
	t.Helper()
	id, _, err := registerDCR(e.s.res.dcrKey, registration{RedirectURIs: redirects, ClientName: name}, e.s.self, *e.now)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *authEnv) authorize(t *testing.T, q url.Values) Consent {
	t.Helper()
	c, err := e.s.Authorize(context.Background(), q)
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	return c
}

// consentAudits returns the oauth_consent audit rows, newest first.
func (e *authEnv) consentAudits(t *testing.T) []store.AuditEvent {
	t.Helper()
	all, err := e.db.ListAudit(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEvent
	for _, ev := range all {
		if ev.Event == "oauth_consent" {
			out = append(out, ev)
		}
	}
	return out
}

func (e *authEnv) rows(t *testing.T, table string) int {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+e.path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCheckSyntax(t *testing.T) {
	e := newAuthEnv(t, true)
	ctx := context.Background()
	dcrID := e.registerDCR(t, "Tool", "https://tool.example/cb")
	for name, q := range map[string]url.Values{
		"cimd":              authQuery("https://unseeded.example/c.json", "https://unseeded.example/cb"),
		"redirect omitted":  authQuery(claudeAI, ""),
		"loopback redirect": authQuery(claudeCode, "http://localhost:54321/callback"),
		"dcr":               authQuery(dcrID, "https://tool.example/cb"),
		// The rest of the request is checked only with a session.
		"response_type ignored": func() url.Values { q := authQuery(claudeAI, ""); q.Set("response_type", "token"); return q }(),
	} {
		if err := e.s.CheckSyntax(q); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if e.docs.calls != 0 {
		t.Fatalf("fetched %d times", e.docs.calls)
	}
	for _, id := range []string{"https://unseeded.example/c.json", claudeAI, dcrID} {
		if _, err := e.db.OAuthClientByClientID(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("client row stored: %v", err)
		}
	}

	bad := map[string]url.Values{}
	for _, id := range []string{"", "http://claude.ai/x", "https://claude.ai/", "https://claude.ai/x?y=1",
		"https://claude.ai:8443/x", "not a url", "dcr_garbage", dcrID + "x"} {
		bad["client_id "+id] = authQuery(id, claudeAIRedirect)
	}
	dup := authQuery(claudeAI, claudeAIRedirect)
	dup.Add("client_id", claudeCode)
	bad["duplicate client_id"] = dup
	missing := authQuery(claudeAI, claudeAIRedirect)
	missing.Del("client_id")
	bad["no client_id"] = missing
	for _, r := range []string{"cursor://cb", "http://example.com/cb", "https://x.com/cb#f", "https://u@x.com/cb", testSelf + "/cb", "javascript:alert(1)"} {
		bad["redirect "+r] = authQuery(claudeAI, r)
	}
	dupR := authQuery(claudeAI, claudeAIRedirect)
	dupR.Add("redirect_uri", claudeAIRedirect)
	bad["duplicate redirect_uri"] = dupR
	for name, q := range bad {
		if err := e.s.CheckSyntax(q); !pageError(err) {
			t.Errorf("%s: err = %v, want *PageError", name, err)
		}
	}
	if e.docs.calls != 0 {
		t.Fatalf("fetched %d times", e.docs.calls)
	}

	off := newAuthEnv(t, false)
	if err := off.s.CheckSyntax(authQuery(off.registerDCR(t, "Tool", "https://tool.example/cb"), "")); !pageError(err) {
		t.Fatalf("DCR client with DCR off: err = %v", err)
	}
}

func TestAuthorizeClientErrorsNeverRedirect(t *testing.T) {
	e := newAuthEnv(t, true)
	dcrID := e.registerDCR(t, "Tool", "https://tool.example/cb")
	wrongType := authQuery(claudeAI, "https://claude.ai/other")
	wrongType.Set("response_type", "token")
	for name, q := range map[string]url.Values{
		"unknown client":                   authQuery("https://unknown.example/c.json", "https://unknown.example/cb"),
		"non-matching redirect":            authQuery(claudeAI, "https://claude.ai/other"),
		"redirect with another port":       authQuery(claudeAI, "https://claude.ai:8443/api/mcp/auth_callback"),
		"non-matching and a request error": wrongType,
		"redirect not registered by dcr":   authQuery(dcrID, "https://other.example/cb"),
		"tampered dcr id":                  authQuery(dcrID[:len(dcrID)-2]+"AA", "https://tool.example/cb"),
		"bad client_id form":               authQuery("http://claude.ai/x", claudeAIRedirect),
	} {
		_, err := e.s.Authorize(context.Background(), q)
		var re *RedirectError
		if errors.As(err, &re) || !pageError(err) {
			t.Errorf("%s: err = %v, want *PageError", name, err)
		}
	}

	off := newAuthEnv(t, false)
	if _, err := off.s.Authorize(context.Background(), authQuery(off.registerDCR(t, "Tool", "https://tool.example/cb"), "")); !pageError(err) {
		t.Fatalf("DCR client with DCR off: err = %v", err)
	}
}

// A loopback COACHD_PUBLIC_URL in development: a loopback redirect that
// matches the registered one but lands on coachd's own origin is refused.
func TestAuthorizeRefusesOwnOrigin(t *testing.T) {
	e := newTestEnv(t, "http://localhost:8080", false)
	docs := &docStub{docs: map[string]document{claudeCode: {ClientID: claudeCode, Name: "Claude Code",
		RedirectURIs: []string{"http://localhost/callback"}, Raw: []byte(`{}`)}}}
	e.s.res.fetchDoc = docs.fetch
	e.ensurePasskey(t)
	if _, err := e.s.Authorize(context.Background(), authQuery(claudeCode, "http://localhost:8080/callback")); !pageError(err) {
		t.Fatalf("own origin: err = %v", err)
	}
	if err := e.s.CheckSyntax(authQuery(claudeCode, "http://localhost:8080/callback")); !pageError(err) {
		t.Fatalf("own origin syntax: err = %v", err)
	}
	if _, err := e.s.Authorize(context.Background(), authQuery(claudeCode, "http://localhost:54321/callback")); err != nil {
		t.Fatalf("another port: %v", err)
	}
}

func TestAuthorizeRequestErrorsRedirect(t *testing.T) {
	e := newAuthEnv(t, false)
	long := strings.Repeat("s", 1025)
	for name, tc := range map[string]struct {
		edit  func(url.Values)
		code  string
		state string
	}{
		"response_type token":      {func(q url.Values) { q.Set("response_type", "token") }, "unsupported_response_type", "st-1"},
		"no response_type":         {func(q url.Values) { q.Del("response_type") }, "invalid_request", "st-1"},
		"no challenge":             {func(q url.Values) { q.Del("code_challenge") }, "invalid_request", "st-1"},
		"plain":                    {func(q url.Values) { q.Set("code_challenge_method", "plain") }, "invalid_request", "st-1"},
		"no challenge method":      {func(q url.Values) { q.Del("code_challenge_method") }, "invalid_request", "st-1"},
		"44-character challenge":   {func(q url.Values) { q.Set("code_challenge", testChallenge+"A") }, "invalid_request", "st-1"},
		"42-character challenge":   {func(q url.Values) { q.Set("code_challenge", testChallenge[:42]) }, "invalid_request", "st-1"},
		"challenge not base64url":  {func(q url.Values) { q.Set("code_challenge", testChallenge[:42]+"=") }, "invalid_request", "st-1"},
		"scope admin":              {func(q url.Values) { q.Set("scope", "admin") }, "invalid_scope", "st-1"},
		"scope admin and read":     {func(q url.Values) { q.Set("scope", "coach:read admin") }, "invalid_scope", "st-1"},
		"other resource":           {func(q url.Values) { q.Set("resource", "https://other/mcp") }, "invalid_target", "st-1"},
		"resource on another path": {func(q url.Values) { q.Set("resource", testSelf+"/api") }, "invalid_target", "st-1"},
		"duplicate state":          {func(q url.Values) { q.Add("state", "st-2") }, "invalid_request", "st-1"},
		"duplicate scope":          {func(q url.Values) { q["scope"] = []string{"coach:read", "coach:write"} }, "invalid_request", "st-1"},
		"state over 1 KiB":         {func(q url.Values) { q.Set("state", long) }, "invalid_request", long},
		"no state":                 {func(q url.Values) { q.Del("state"); q.Set("scope", "admin") }, "invalid_scope", ""},
	} {
		q := authQuery(aID, aRedirect)
		tc.edit(q)
		_, err := e.s.Authorize(context.Background(), q)
		u := redirectURL(t, err)
		var re *RedirectError
		errors.As(err, &re)
		if !strings.HasPrefix(re.URL, aRedirect+"&error=") {
			t.Errorf("%s: redirect %q does not append to the registered query", name, re.URL)
		}
		got := u.Query()
		if got.Get("error") != tc.code || got.Get("iss") != testSelf || got.Get("x") != "1" || got.Has("code") {
			t.Errorf("%s: query = %v", name, got)
		}
		if tc.state == "" {
			if got.Has("state") {
				t.Errorf("%s: state = %q, want none", name, got.Get("state"))
			}
		} else if got["state"] == nil || len(got["state"]) != 1 || got.Get("state") != tc.state {
			t.Errorf("%s: state = %v, want %q", name, got["state"], tc.state)
		}
		if strings.Contains(re.Error(), "a.com") || strings.Contains(re.Error(), "st-1") {
			t.Errorf("%s: Error() quotes the URL: %q", name, re.Error())
		}
	}
	if e.rows(t, "oauth_grants") != 0 {
		t.Fatal("a grant was created")
	}
}

func TestAuthorizeOmittedRedirect(t *testing.T) {
	e := newAuthEnv(t, false)
	c := e.authorize(t, authQuery(claudeAI, ""))
	req, err := e.s.openRequest(c.Request)
	if err != nil {
		t.Fatal(err)
	}
	if req.RedirectURI != claudeAIRedirect || req.RedirectExplicit || c.RedirectHost != "claude.ai" {
		t.Fatalf("request = %+v, consent = %+v", req, c)
	}
	explicit := e.authorize(t, authQuery(claudeAI, claudeAIRedirect))
	if req, _ := e.s.openRequest(explicit.Request); req.RedirectURI != claudeAIRedirect || !req.RedirectExplicit {
		t.Fatalf("explicit request = %+v", req)
	}
	for name, id := range map[string]string{"two loopback redirects": claudeCode, "two https redirects": twoID,
		"one loopback redirect": loopID} {
		if _, err := e.s.Authorize(context.Background(), authQuery(id, "")); !pageError(err) {
			t.Errorf("%s: err = %v, want *PageError", name, err)
		}
	}
	empty := authQuery(claudeAI, "")
	empty.Set("redirect_uri", "")
	if _, err := e.s.Authorize(context.Background(), empty); err != nil {
		t.Fatalf("an empty redirect_uri counts as omitted: %v", err)
	}
}

func TestAuthorizeConsentFields(t *testing.T) {
	e := newAuthEnv(t, true)
	c := e.authorize(t, authQuery(claudeAI, claudeAIRedirect))
	want := Consent{Request: c.Request, Kind: "cimd", Name: "Claude", Host: "claude.ai", HostDomain: "claude.ai",
		RedirectHost: "claude.ai", Scopes: []string{"coach:read", "coach:write"}, WriteDefault: true}
	if !reflect.DeepEqual(c, want) {
		t.Fatalf("consent =\n %+v\nwant\n %+v", c, want)
	}
	req, err := e.s.openRequest(c.Request)
	if err != nil {
		t.Fatal(err)
	}
	wantReq := authRequest{ClientID: claudeAI, RedirectURI: claudeAIRedirect, RedirectExplicit: true, CodeChallenge: testChallenge,
		Scopes: []string{"coach:read", "coach:write"}, Resource: testSelf + "/mcp", State: "st-1", Expires: tNow.Add(requestTTL)}
	if !reflect.DeepEqual(req, wantReq) {
		t.Fatalf("request =\n %+v\nwant\n %+v", req, wantReq)
	}

	code := e.authorize(t, authQuery(claudeCode, "http://localhost:54321/callback"))
	if !code.LoopbackOnly || code.DomainMismatch || code.RedirectHost != "localhost" || code.Host != "claude.ai" || code.Name != "Claude Code" {
		t.Fatalf("Claude Code consent = %+v", code)
	}
	if v4 := e.authorize(t, authQuery(claudeCode, "http://127.0.0.1:9/callback")); !v4.LoopbackOnly || v4.RedirectHost != "127.0.0.1" {
		t.Fatalf("127.0.0.1 consent = %+v", v4)
	}

	evil := e.authorize(t, authQuery(evilID, evilRedirect))
	if !evil.DomainMismatch || evil.LoopbackOnly || evil.Host != "evil.example" || evil.HostDomain != "evil.example" ||
		evil.RedirectHost != "claude.ai.attacker.net" {
		t.Fatalf("evil consent = %+v", evil)
	}

	if idn := e.authorize(t, authQuery(idnID, idnRedirect)); idn.RedirectHost != "xn--bcher-kva.example" || !idn.DomainMismatch {
		t.Fatalf("IDN redirect consent = %+v", idn)
	}

	dcrID := e.registerDCR(t, "Tool", "https://tool.example/cb")
	d := e.authorize(t, authQuery(dcrID, "https://tool.example/cb"))
	if d.Kind != "dcr" || d.Host != "" || d.HostDomain != "" || d.Name != "Tool" || d.RedirectHost != "tool.example" ||
		d.WriteDefault || !d.RequireRecognise || d.DomainMismatch || d.LoopbackOnly {
		t.Fatalf("DCR consent = %+v", d)
	}

	for scope, want := range map[string][]string{
		"coach:read offline_access":             {"coach:read"},
		"offline_access coach:write coach:read": {"coach:read", "coach:write"},
		"coach:write":                           {"coach:write"},
		"coach:read coach:read":                 {"coach:read"},
		"":                                      {"coach:read", "coach:write"},
		"offline_access":                        {"coach:read", "coach:write"},
	} {
		q := authQuery(claudeAI, claudeAIRedirect)
		q.Set("scope", scope)
		c := e.authorize(t, q)
		req, _ := e.s.openRequest(c.Request)
		if !reflect.DeepEqual(c.Scopes, want) || !reflect.DeepEqual(req.Scopes, want) {
			t.Errorf("scope %q: consent %v, request %v, want %v", scope, c.Scopes, req.Scopes, want)
		}
	}

	for _, r := range []string{testSelf + "/mcp", "https://COACHD.example.com/mcp/", testSelf, testSelf + "/"} {
		q := authQuery(claudeAI, claudeAIRedirect)
		q.Set("resource", r)
		req, _ := e.s.openRequest(e.authorize(t, q).Request)
		if req.Resource != testSelf+"/mcp" {
			t.Errorf("resource %q stored as %q", r, req.Resource)
		}
	}
}

func TestNoPasskeyPage(t *testing.T) {
	e := newTestEnv(t, testSelf, false)
	docs := &docStub{docs: map[string]document{claudeAI: fixtureDoc(t, "claude-ai.json", claudeAI)}}
	e.s.res.fetchDoc = docs.fetch
	_, err := e.s.Authorize(context.Background(), authQuery(claudeAI, claudeAIRedirect))
	var pe *PageError
	if !errors.As(err, &pe) || !strings.Contains(pe.Message, "coachd enroll") {
		t.Fatalf("err = %v", err)
	}
	// The request is checked first: its errors still go to the client.
	q := authQuery(claudeAI, claudeAIRedirect)
	q.Set("scope", "admin")
	_, err = e.s.Authorize(context.Background(), q)
	if redirectURL(t, err).Query().Get("error") != "invalid_scope" {
		t.Fatalf("err = %v", err)
	}
}

// tamper flips one bit of the request token's payload.
func tamper(t *testing.T, tok string) string {
	t.Helper()
	p, m, ok := strings.Cut(tok, ".")
	if !ok {
		t.Fatalf("token %q", tok)
	}
	b, err := base64.RawURLEncoding.DecodeString(p)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 1
	return base64.RawURLEncoding.EncodeToString(b) + "." + m
}

func TestBeginScopes(t *testing.T) {
	e := newAuthEnv(t, true)
	ctx := context.Background()
	full := e.authorize(t, authQuery(claudeAI, claudeAIRedirect)).Request
	for _, tc := range []struct {
		write bool
		want  []string
	}{{false, []string{"coach:read"}}, {true, []string{"coach:read", "coach:write"}}} {
		got, exp, err := e.s.Begin(ctx, full, tc.write, false)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("write=%v: %v, %v; want %v", tc.write, got, err, tc.want)
		}
		if want := e.s.d.Now().Add(requestTTL); !exp.Equal(want) {
			t.Errorf("expiry = %v, want the request token's %v", exp, want)
		}
	}
	readQ := authQuery(claudeAI, claudeAIRedirect)
	readQ.Set("scope", "coach:read")
	if got, _, err := e.s.Begin(ctx, e.authorize(t, readQ).Request, true, false); err != nil || !reflect.DeepEqual(got, []string{"coach:read"}) {
		t.Errorf("write=true on a read-only request: %v, %v", got, err)
	}
	writeQ := authQuery(claudeAI, claudeAIRedirect)
	writeQ.Set("scope", "coach:write")
	if _, _, err := e.s.Begin(ctx, e.authorize(t, writeQ).Request, false, false); !pageError(err) {
		t.Errorf("nothing left to grant: err = %v", err)
	}

	dcr := e.authorize(t, authQuery(e.registerDCR(t, "Tool", "https://tool.example/cb"), "https://tool.example/cb")).Request
	if _, _, err := e.s.Begin(ctx, dcr, true, false); !pageError(err) {
		t.Errorf("DCR without recognise: err = %v", err)
	}
	if got, _, err := e.s.Begin(ctx, dcr, true, true); err != nil || !reflect.DeepEqual(got, []string{"coach:read", "coach:write"}) {
		t.Errorf("DCR with recognise: %v, %v", got, err)
	}
	// DCR switched off since the page opened: its requests stop there.
	e.s.d.DCR = false
	if _, _, err := e.s.Begin(ctx, dcr, true, true); !pageError(err) {
		t.Errorf("DCR off: err = %v", err)
	}
	e.s.d.DCR = true

	other := newAuthEnv(t, true)
	other.s.requestKey = deriveKey([]byte(strings.Repeat("k", 32)), labelRequest)
	foreign := other.authorize(t, authQuery(claudeAI, claudeAIRedirect)).Request
	for name, tok := range map[string]string{"tampered": tamper(t, full), "garbage": "x", "empty": "",
		"no mac": strings.Split(full, ".")[0], "other key": foreign} {
		_, _, err := e.s.Begin(ctx, tok, true, false)
		if !pageError(err) || errors.Is(err, ErrRequestExpired) {
			t.Errorf("%s: err = %v, want a *PageError other than ErrRequestExpired", name, err)
		}
	}

	// Begin re-validates the client: a redirect it no longer registers fails.
	row, err := e.db.OAuthClientByClientID(ctx, claudeAI)
	if err != nil {
		t.Fatal(err)
	}
	row.RedirectURIs = []string{"https://claude.ai/new_callback"}
	if _, err := e.db.PutOAuthClient(ctx, row); err != nil {
		t.Fatal(err)
	}
	var re *RedirectError
	if _, _, err := e.s.Begin(ctx, full, true, false); !pageError(err) || errors.As(err, &re) {
		t.Errorf("redirect dropped since: err = %v", err)
	}

	fresh := newAuthEnv(t, false)
	tok := fresh.authorize(t, authQuery(claudeAI, claudeAIRedirect)).Request
	*fresh.now = fresh.now.Add(requestTTL - time.Second)
	if _, _, err := fresh.s.Begin(ctx, tok, true, false); err != nil {
		t.Fatalf("just before expiry: %v", err)
	}
	*fresh.now = fresh.now.Add(time.Second)
	if _, _, err := fresh.s.Begin(ctx, tok, true, false); err != ErrRequestExpired {
		t.Fatalf("expired: err = %v, want ErrRequestExpired", err)
	}
	// A forged token never reads as expired.
	if _, _, err := fresh.s.Begin(ctx, tamper(t, tok), true, false); err == ErrRequestExpired || !pageError(err) {
		t.Fatalf("expired and tampered: err = %v", err)
	}
}

func (e *authEnv) approval(t *testing.T, request string, scopes []string) Approval {
	t.Helper()
	e.seq++
	return Approval{Request: request, Scopes: scopes, PasskeyID: e.passkey, CredentialJSON: []byte(`{"v":3}`),
		Nonce: "consent-nonce-" + strconv.Itoa(e.seq), NonceExpires: e.now.Add(requestTTL)}
}

func TestApprove(t *testing.T) {
	e := newAuthEnv(t, true)
	ctx := context.Background()
	verifier, challenge := pkcePair()
	q := authQuery(claudeAI, claudeAIRedirect)
	q.Set("code_challenge", challenge)
	q.Set("state", "st-approve")
	c := e.authorize(t, q)
	scopes, _, err := e.s.Begin(ctx, c.Request, true, false)
	if err != nil {
		t.Fatal(err)
	}
	a := e.approval(t, c.Request, scopes)
	redirect, err := e.s.Approve(ctx, a)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if !strings.HasPrefix(redirect, claudeAIRedirect+"?code=") {
		t.Fatalf("redirect = %q", redirect)
	}
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	got := u.Query()
	code := got.Get("code")
	if len(code) != 43 || got.Get("state") != "st-approve" || got.Get("iss") != testSelf || got.Has("error") {
		t.Fatalf("redirect query = %v", got)
	}
	if used, err := e.db.NonceUsed(ctx, a.Nonce); err != nil || !used {
		t.Fatalf("nonce used = %v, %v", used, err)
	}
	if e.rows(t, "oauth_grants") != 1 || e.rows(t, "oauth_codes") != 1 {
		t.Fatalf("grants %d, codes %d", e.rows(t, "oauth_grants"), e.rows(t, "oauth_codes"))
	}

	// A replay of the same assertion creates nothing.
	if _, err := e.s.Approve(ctx, a); !errors.Is(err, store.ErrNonceUsed) {
		t.Fatalf("replay: err = %v", err)
	}
	if e.rows(t, "oauth_grants") != 1 {
		t.Fatalf("replay created a grant")
	}

	// The code is bound to the challenge, the client and the redirect.
	exchange := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "client_id": {claudeAI},
		"redirect_uri": {claudeAIRedirect}}
	bad := url.Values{}
	for k, v := range exchange {
		bad[k] = v
	}
	bad.Set("code_verifier", strings.Repeat("v", 43))
	wantError(t, e.token(bad), 400, "invalid_grant")
	exchange.Set("code_verifier", verifier)
	tok := wantTokens(t, e.token(exchange))
	if tok.scope != "coach:read coach:write" {
		t.Fatalf("scope = %q", tok.scope)
	}
	g, held, err := e.db.AccessGrant(ctx, sha(tok.access), *e.now)
	if err != nil {
		t.Fatal(err)
	}
	if g.PasskeyID != e.passkey || g.ClientID != claudeAI || g.Resource != testSelf+"/mcp" || !reflect.DeepEqual(held, scopes) {
		t.Fatalf("grant = %+v, scopes %v", g, held)
	}
	if ev := e.consentAudits(t); len(ev) != 1 || ev[0].Outcome != "approved" || ev[0].Target != "g"+strconv.FormatInt(g.ID, 10) {
		t.Fatalf("audit = %+v", ev)
	}
	if e.rows(t, "oauth_clients") != 1 {
		t.Fatalf("clients = %d", e.rows(t, "oauth_clients"))
	}
}

func TestApproveLoopbackAndOmittedRedirect(t *testing.T) {
	e := newAuthEnv(t, false)
	ctx := context.Background()
	for _, tc := range []struct{ clientID, redirect, sent string }{
		{claudeCode, "http://localhost:54321/callback", "http://localhost:54321/callback"},
		{claudeAI, "", claudeAIRedirect},
	} {
		verifier, challenge := pkcePair()
		q := authQuery(tc.clientID, tc.redirect)
		q.Set("code_challenge", challenge)
		c := e.authorize(t, q)
		redirect, err := e.s.Approve(ctx, e.approval(t, c.Request, []string{"coach:read"}))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(redirect, tc.sent+"?code=") {
			t.Fatalf("redirect = %q", redirect)
		}
		u, _ := url.Parse(redirect)
		form := url.Values{"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")}, "client_id": {tc.clientID},
			"code_verifier": {verifier}, "redirect_uri": {tc.sent}}
		if tok := wantTokens(t, e.token(form)); tok.scope != "coach:read" {
			t.Fatalf("scope = %q", tok.scope)
		}
	}
}

func TestApproveRefusals(t *testing.T) {
	e := newAuthEnv(t, false)
	ctx := context.Background()
	readQ := authQuery(claudeAI, claudeAIRedirect)
	readQ.Set("scope", "coach:read")
	read := e.authorize(t, readQ).Request
	for name, scopes := range map[string][]string{
		"wider than requested": {"coach:read", "coach:write"},
		"none":                 nil,
		"unknown":              {"admin"},
		"repeated":             {"coach:read", "coach:read"},
	} {
		if _, err := e.s.Approve(ctx, e.approval(t, read, scopes)); !pageError(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := e.s.Approve(ctx, e.approval(t, tamper(t, read), []string{"coach:read"})); !pageError(err) {
		t.Errorf("tampered: err = %v", err)
	}
	if err := e.db.RevokePasskey(ctx, e.passkey, *e.now, "public", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Approve(ctx, e.approval(t, read, []string{"coach:read"})); !errors.Is(err, passkey.ErrUnknownCredential) {
		t.Errorf("revoked passkey: err = %v", err)
	}
	*e.now = e.now.Add(requestTTL)
	if _, err := e.s.Approve(ctx, e.approval(t, read, []string{"coach:read"})); err != ErrRequestExpired {
		t.Errorf("expired: err = %v", err)
	}
	if e.rows(t, "oauth_grants") != 0 {
		t.Fatal("a grant was created")
	}
}

func TestApproveDCRInsertsClient(t *testing.T) {
	e := newAuthEnv(t, true)
	ctx := context.Background()
	dcrID := e.registerDCR(t, "Tool", "https://tool.example/cb", "http://127.0.0.1/cb")
	c := e.authorize(t, authQuery(dcrID, "https://tool.example/cb"))
	if _, err := e.db.OAuthClientByClientID(ctx, dcrID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row before consent: %v", err)
	}
	scopes, _, err := e.s.Begin(ctx, c.Request, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Approve(ctx, e.approval(t, c.Request, scopes)); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	row, err := e.db.OAuthClientByClientID(ctx, dcrID)
	if err != nil {
		t.Fatalf("row after consent: %v", err)
	}
	if row.Kind != "dcr" || row.Name != "Tool" || !reflect.DeepEqual(row.RedirectURIs, []string{"https://tool.example/cb", "http://127.0.0.1/cb"}) {
		t.Fatalf("row = %+v", row)
	}
	// A second consent reuses the row.
	again := e.authorize(t, authQuery(dcrID, "http://127.0.0.1:5000/cb"))
	if _, err := e.s.Approve(ctx, e.approval(t, again.Request, []string{"coach:read"})); err != nil {
		t.Fatalf("second Approve: %v", err)
	}
	if e.rows(t, "oauth_clients") != 1 || e.rows(t, "oauth_grants") != 2 {
		t.Fatalf("clients %d, grants %d", e.rows(t, "oauth_clients"), e.rows(t, "oauth_grants"))
	}
}

func TestDeny(t *testing.T) {
	e := newAuthEnv(t, true)
	ctx := context.Background()
	q := authQuery(aID, aRedirect)
	q.Set("state", "st-deny")
	c := e.authorize(t, q)
	redirect, err := e.s.Deny(ctx, c.Request)
	if err != nil {
		t.Fatalf("Deny: %v", err)
	}
	if !strings.HasPrefix(redirect, aRedirect+"&error=access_denied") {
		t.Fatalf("redirect = %q", redirect)
	}
	u, _ := url.Parse(redirect)
	if got := u.Query(); got.Get("state") != "st-deny" || got.Get("iss") != testSelf || got.Get("x") != "1" || got.Has("code") {
		t.Fatalf("query = %v", got)
	}
	row, err := e.db.OAuthClientByClientID(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	events := e.consentAudits(t)
	if len(events) != 1 {
		t.Fatalf("audit = %+v", events)
	}
	if ev := events[0]; !ev.At.Equal(tNow) || ev.Lane != "public" || ev.Actor != "runner" || ev.Client != "" ||
		ev.Target != "c"+strconv.FormatInt(row.ID, 10) || ev.Outcome != "denied" {
		t.Fatalf("audit = %+v", ev)
	}

	dcr := e.authorize(t, authQuery(e.registerDCR(t, "Tool", "https://tool.example/cb"), "")).Request
	if _, err := e.s.Deny(ctx, dcr); err != nil {
		t.Fatal(err)
	}
	if events := e.consentAudits(t); len(events) != 2 || events[0].Target != "dcr" || events[0].Outcome != "denied" {
		t.Fatalf("DCR audit = %+v", events)
	}

	// A token that does not verify is neither answered with a redirect nor audited.
	if _, err := e.s.Deny(ctx, tamper(t, c.Request)); !pageError(err) {
		t.Fatalf("tampered: err = %v", err)
	}
	*e.now = e.now.Add(requestTTL + time.Second)
	if r, err := e.s.Deny(ctx, c.Request); err != ErrRequestExpired || r != "" {
		t.Fatalf("expired: %q, %v", r, err)
	}
	if events := e.consentAudits(t); len(events) != 2 {
		t.Fatalf("audit rows = %d", len(events))
	}
	if e.rows(t, "oauth_grants") != 0 {
		t.Fatal("a grant was created")
	}
}

func TestConsentMetrics(t *testing.T) {
	e := newAuthEnv(t, false)
	ctx := context.Background()
	if err := e.s.CheckSyntax(authQuery(claudeAI, "")); err != nil {
		t.Fatal(err)
	}
	e.s.CheckSyntax(authQuery("http://claude.ai/x", ""))
	e.s.CheckSyntax(authQuery(claudeAI, "cursor://cb"))
	e.s.Authorize(ctx, authQuery("https://unknown.example/c.json", ""))
	e.s.Authorize(ctx, authQuery(claudeAI, "https://claude.ai/other"))
	bad := authQuery(claudeAI, "")
	bad.Set("scope", "admin")
	e.s.Authorize(ctx, bad)
	bad.Set("scope", "coach:read")
	bad.Set("response_type", "token")
	e.s.Authorize(ctx, bad)
	c := e.authorize(t, authQuery(claudeAI, ""))
	scopes, _, _ := e.s.Begin(ctx, c.Request, true, false)
	if _, err := e.s.Approve(ctx, e.approval(t, c.Request, scopes)); err != nil {
		t.Fatal(err)
	}
	e.s.Begin(ctx, "x", true, false)
	if _, err := e.s.Deny(ctx, c.Request); err != nil {
		t.Fatal(err)
	}
	got := e.counter(t, "endpoint", "outcome")
	want := map[string]int64{
		"authorize/login_required": 1, "authorize/invalid_client": 1, "authorize/invalid_request": 3,
		"authorize/fetch_failed": 1, "authorize/invalid_scope": 1, "authorize/ok": 1,
		"consent/ok": 2, "consent/invalid_request": 1, "consent/access_denied": 1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("counter = %v\nwant %v", got, want)
	}
}

func TestErrorTexts(t *testing.T) {
	if (&PageError{Message: "m"}).Error() != "m" {
		t.Fatal("PageError.Error")
	}
	if s := (&RedirectError{URL: "https://a.com/cb?state=secret"}).Error(); strings.Contains(s, "a.com") || strings.Contains(s, "secret") {
		t.Fatalf("RedirectError.Error = %q", s)
	}
	if !strings.Contains(ErrRequestExpired.Message, "Start the connection again from your app") {
		t.Fatalf("ErrRequestExpired = %q", ErrRequestExpired.Message)
	}
}

// Only GET /oauth/authorize fetches (spec §5.2): begin, finish and deny use
// the stored client, even once its cache has expired within the request's
// lifetime.
func TestConsentStepsNeverFetch(t *testing.T) {
	e := newAuthEnv(t, false)
	e.docs.ttl = minCache
	ctx := context.Background()
	approveReq := e.authorize(t, authQuery(claudeAI, claudeAIRedirect)).Request
	denyReq := e.authorize(t, authQuery(claudeCode, "http://localhost:54321/callback")).Request
	if e.docs.calls != 2 {
		t.Fatalf("authorize fetched %d times", e.docs.calls)
	}
	e.docs.docs = map[string]document{} // every further fetch would fail
	*e.now = e.now.Add(minCache + time.Minute)

	scopes, _, err := e.s.Begin(ctx, approveReq, true, false)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := e.s.Approve(ctx, e.approval(t, approveReq, scopes)); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if _, err := e.s.Deny(ctx, denyReq); err != nil {
		t.Fatalf("Deny: %v", err)
	}
	if e.docs.calls != 2 {
		t.Fatalf("consent steps fetched: %d calls", e.docs.calls)
	}
	// The cache really has expired: a new authorization refetches.
	if _, err := e.s.Authorize(ctx, authQuery(claudeCode, "http://localhost:54321/callback")); e.docs.calls != 3 || !pageError(err) {
		t.Fatalf("authorize after expiry: calls %d, err %v", e.docs.calls, err)
	}
}

func TestCountConsent(t *testing.T) {
	e := newTestEnv(t, testSelf, false)
	ctx := context.Background()
	for _, o := range []string{"rate_limited", "invalid_request", "rate_limited", "error", "bogus", "", "Rate_Limited"} {
		e.s.CountConsent(ctx, o)
	}
	want := map[string]int64{"consent/rate_limited": 2, "consent/invalid_request": 1, "consent/error": 1}
	if got := e.counter(t, "endpoint", "outcome"); !reflect.DeepEqual(got, want) {
		t.Fatalf("counter = %v, want %v", got, want)
	}
}
