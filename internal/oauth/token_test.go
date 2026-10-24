package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/auth"
	"github.com/stefiix92/coachd/internal/store"
)

const (
	appID    = "https://app.example/client.json"
	otherID  = "https://other.example/client.json"
	loopback = "http://localhost:54321/callback"
	formType = "application/x-www-form-urlencoded"
)

type fakeNotifier struct{ connected, revoked atomic.Int32 }

func (f *fakeNotifier) AppConnected()      { f.connected.Add(1) }
func (f *fakeNotifier) ConnectionRevoked() { f.revoked.Add(1) }

func (e *testEnv) notifier() *fakeNotifier {
	n := &fakeNotifier{}
	e.s.d.Notifier = n
	return n
}

func sha(s string) []byte { x := sha256.Sum256([]byte(s)); return x[:] }

// pkcePair returns a fresh 43-character verifier and its S256 challenge,
// computed here rather than with the code under test.
func pkcePair() (verifier, challenge string) {
	b := make([]byte, 32)
	rand.Read(b)
	verifier = base64.RawURLEncoding.EncodeToString(b)
	return verifier, base64.RawURLEncoding.EncodeToString(sha(verifier))
}

type consent struct {
	clientID, redirect string
	scopes             []string
}

type issued struct {
	code, verifier, clientID string
	grant                    int64
}

// issueCode records a consent through store.GrantConsent, as the consent
// ceremony does, and returns the code text, its verifier and the grant id.
// Defaults: appID, the loopback redirect, both coach scopes.
func (e *testEnv) issueCode(t *testing.T, c consent) issued {
	t.Helper()
	if c.clientID == "" {
		c.clientID = appID
	}
	if c.redirect == "" {
		c.redirect = loopback
	}
	if c.scopes == nil {
		c.scopes = []string{"coach:read", "coach:write"}
	}
	ctx, at := context.Background(), *e.now
	e.ensurePasskey(t)
	row, err := e.db.PutOAuthClient(ctx, store.OAuthClient{ClientID: c.clientID, Kind: "cimd", Name: "App",
		RedirectURIs: []string{c.redirect}, Metadata: []byte(`{}`), FetchedAt: at, CacheUntil: at.Add(time.Hour), CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	verifier, challenge := pkcePair()
	code := "code-" + rand.Text()
	e.seq++
	gid, err := e.db.GrantConsent(ctx, store.ConsentGrant{Client: store.OAuthClient{ID: row}, PasskeyID: e.passkey,
		CredentialJSON: []byte(`{"v":2}`), Nonce: fmt.Sprintf("n-consent-%d", e.seq), NonceExpires: at.Add(10 * time.Minute),
		Scopes: c.scopes, Resource: e.s.self + "/mcp", CodeHash: sha(code), RedirectURI: c.redirect, RedirectExplicit: true,
		CodeChallenge: challenge, CodeExpires: at.Add(5 * time.Minute), At: at})
	if err != nil {
		t.Fatal(err)
	}
	return issued{code: code, verifier: verifier, clientID: c.clientID, grant: gid}
}

// form returns the code exchange for i, with kv pairs set on top.
func (i issued) form(kv ...string) url.Values {
	f := url.Values{"grant_type": {"authorization_code"}, "code": {i.code}, "client_id": {i.clientID}, "code_verifier": {i.verifier}}
	for j := 0; j+1 < len(kv); j += 2 {
		f.Set(kv[j], kv[j+1])
	}
	return f
}

func refreshForm(refresh string, kv ...string) url.Values {
	f := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {appID}}
	for j := 0; j+1 < len(kv); j += 2 {
		f.Set(kv[j], kv[j+1])
	}
	return f
}

func (e *testEnv) token(f url.Values) *httptest.ResponseRecorder {
	return e.do("POST", "/oauth/token", formType, f.Encode())
}

type tokenOK struct {
	access, refresh, typ, scope string
	expiresIn                   float64
}

func wantTokens(t *testing.T, rec *httptest.ResponseRecorder) tokenOK {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	m := jsonBody(t, rec.Body.String())
	str := func(k string) string { s, _ := m[k].(string); return s }
	n, _ := m["expires_in"].(float64)
	return tokenOK{access: str("access_token"), refresh: str("refresh_token"), typ: str("token_type"), scope: str("scope"), expiresIn: n}
}

func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	m := jsonBody(t, rec.Body.String())
	if rec.Code != status || m["error"] != code {
		t.Fatalf("got %d %s, want %d %s", rec.Code, rec.Body, status, code)
	}
	if len(m) != 1 {
		t.Fatalf("error body carries more than error: %s", rec.Body)
	}
}

func wantNoStore(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	if h.Get("Cache-Control") != "no-store" || h.Get("Pragma") != "no-cache" || h.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("headers = %v", h)
	}
}

func TestTokenCodeExchange(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	n := e.notifier()
	i := e.issueCode(t, consent{})
	rec := e.token(i.form())
	wantNoStore(t, rec)
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content type %q", rec.Header().Get("Content-Type"))
	}
	tok := wantTokens(t, rec)
	if !strings.HasPrefix(tok.access, "cda_") || len(tok.access) != 4+43 || !strings.HasPrefix(tok.refresh, "cdr_") || len(tok.refresh) != 4+43 ||
		tok.typ != "Bearer" || tok.expiresIn != 3600 || tok.scope != "coach:read coach:write" {
		t.Fatalf("tokens = %+v", tok)
	}
	if len(jsonBody(t, rec.Body.String())) != 5 {
		t.Fatalf("unexpected fields: %s", rec.Body)
	}
	g, scopes, err := e.db.AccessGrant(context.Background(), sha(tok.access), *e.now)
	if err != nil || g.ID != i.grant || strings.Join(scopes, " ") != "coach:read coach:write" {
		t.Fatalf("access grant = %+v %v %v", g, scopes, err)
	}
	if n.connected.Load() != 1 {
		t.Fatalf("AppConnected = %d after the first exchange", n.connected.Load())
	}
	// Refreshes never announce a connection.
	r := wantTokens(t, e.token(refreshForm(tok.refresh)))
	wantTokens(t, e.token(refreshForm(r.refresh)))
	if n.connected.Load() != 1 {
		t.Fatalf("AppConnected = %d after refreshes", n.connected.Load())
	}
	// A second consent is a second grant: one more announcement.
	wantTokens(t, e.token(e.issueCode(t, consent{}).form()))
	if n.connected.Load() != 2 || n.revoked.Load() != 0 {
		t.Fatalf("notifier = %d connected, %d revoked", n.connected.Load(), n.revoked.Load())
	}
}

func TestTokenCodeOptionalRedirect(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	// Omitted: fine.
	wantTokens(t, e.token(e.issueCode(t, consent{redirect: "https://app.example/cb"}).form()))
	// Sent: must equal the authorization request's value exactly.
	i := e.issueCode(t, consent{redirect: "https://app.example/cb"})
	for _, bad := range []string{"https://app.example:443/cb", "https://app.example:8443/cb", "https://app.example/cb/", "https://APP.example/cb"} {
		wantError(t, e.token(i.form("redirect_uri", bad)), 400, "invalid_grant")
	}
	wantTokens(t, e.token(i.form("redirect_uri", "https://app.example/cb")))
	// A loopback code is bound to the port it was authorized with.
	l := e.issueCode(t, consent{redirect: loopback})
	wantError(t, e.token(l.form("redirect_uri", "http://localhost:1/callback")), 400, "invalid_grant")
	wantError(t, e.token(l.form("redirect_uri", "http://localhost/callback")), 400, "invalid_grant")
	wantTokens(t, e.token(l.form("redirect_uri", loopback)))
}

func TestTokenPKCE(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	i := e.issueCode(t, consent{})
	other, _ := pkcePair()
	wantError(t, e.token(i.form("code_verifier", other)), 400, "invalid_grant")
	wantError(t, e.token(i.form("code_verifier", i.verifier[:42])), 400, "invalid_grant")
	wantError(t, e.token(i.form("code_verifier", i.verifier+strings.Repeat("a", 86))), 400, "invalid_grant") // 129
	missing := i.form()
	missing.Del("code_verifier")
	wantError(t, e.token(missing), 400, "invalid_request")
	// None of the failures spent the code.
	wantTokens(t, e.token(i.form()))
}

func TestVerifyPKCE(t *testing.T) {
	// Vector computed independently: printf %s v | openssl dgst -sha256 -binary | base64url.
	const v, c = "coachd-pkce-test-verifier-0123456789_abcdefg~", "PulE4F1CiPFO3FwfC65oc2uwh2VZT19sbxfVv1fKH7M"
	if !verifyPKCE(c, v) {
		t.Fatal("vector refused")
	}
	long, longC := strings.Repeat("A", 128), base64.RawURLEncoding.EncodeToString(sha(strings.Repeat("A", 128)))
	if !verifyPKCE(longC, long) {
		t.Fatal("128-character verifier refused")
	}
	for name, tc := range map[string][2]string{
		"wrong":     {c, "coachd-pkce-test-verifier-0123456789_abcdefg."},
		"42 chars":  {base64.RawURLEncoding.EncodeToString(sha(strings.Repeat("A", 42))), strings.Repeat("A", 42)},
		"129 chars": {base64.RawURLEncoding.EncodeToString(sha(strings.Repeat("A", 129))), strings.Repeat("A", 129)},
		"bad char":  {base64.RawURLEncoding.EncodeToString(sha(strings.Repeat("A", 42) + "+")), strings.Repeat("A", 42) + "+"},
		"newline":   {base64.RawURLEncoding.EncodeToString(sha(v + "\n")), v + "\n"},
		"no chal":   {"", v},
		"plain":     {v, v},
	} {
		if verifyPKCE(tc[0], tc[1]) {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTokenCodeReuse(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	n := e.notifier()
	i := e.issueCode(t, consent{})
	first := wantTokens(t, e.token(i.form()))
	// A fully matching replay revokes the grant.
	wantError(t, e.token(i.form()), 400, "invalid_grant")
	if err := e.accessErr(first.access); !errors.Is(err, store.ErrRevoked) {
		t.Fatalf("access after reuse: %v, want ErrRevoked", err)
	}
	wantError(t, e.token(refreshForm(first.refresh)), 400, "invalid_grant")
	if n.revoked.Load() != 1 {
		t.Fatalf("ConnectionRevoked = %d", n.revoked.Load())
	}
	audit, err := e.db.ListAudit(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range audit {
		found = found || (a.Event == "oauth_code_reuse" && a.Target == fmt.Sprintf("g%d", i.grant) && a.Outcome == "revoked")
	}
	if !found {
		t.Fatalf("no oauth_code_reuse audit: %+v", audit)
	}
	// A replay that does not match (wrong verifier) is only refused.
	j := e.issueCode(t, consent{})
	tok := wantTokens(t, e.token(j.form()))
	other, _ := pkcePair()
	wantError(t, e.token(j.form("code_verifier", other)), 400, "invalid_grant")
	wantError(t, e.token(j.form("client_id", otherID)), 400, "invalid_grant")
	wantError(t, e.token(j.form("redirect_uri", "http://localhost:1/callback")), 400, "invalid_grant")
	if err := e.accessErr(tok.access); err != nil {
		t.Fatalf("mismatching replay revoked the grant: %v", err)
	}
	if n.revoked.Load() != 1 {
		t.Fatalf("ConnectionRevoked = %d after mismatching replays", n.revoked.Load())
	}
}

func TestTokenCodeExpired(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	i := e.issueCode(t, consent{})
	*e.now = e.now.Add(5*time.Minute + time.Second)
	wantError(t, e.token(i.form()), 400, "invalid_grant")
}

func TestTokenUnknownCode(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	i := e.issueCode(t, consent{})
	wantError(t, e.token(i.form("code", "code-unknown")), 400, "invalid_grant")
}

func TestTokenRefreshRotation(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	t0 := wantTokens(t, e.token(e.issueCode(t, consent{}).form()))
	t1 := wantTokens(t, e.token(refreshForm(t0.refresh)))
	if t1.refresh == t0.refresh || t1.access == t0.access || !strings.HasPrefix(t1.refresh, "cdr_") || t1.scope != "coach:read coach:write" ||
		t1.expiresIn != 3600 || t1.typ != "Bearer" {
		t.Fatalf("rotated = %+v", t1)
	}
	if err := e.accessErr(t1.access); err != nil {
		t.Fatalf("new access: %v", err)
	}
	// Narrowing carries forward to the new refresh token.
	t2 := wantTokens(t, e.token(refreshForm(t1.refresh, "scope", "coach:read")))
	if t2.scope != "coach:read" {
		t.Fatalf("narrowed scope = %q", t2.scope)
	}
	if _, s, err := e.db.AccessGrant(context.Background(), sha(t2.access), *e.now); err != nil || strings.Join(s, " ") != "coach:read" {
		t.Fatalf("narrowed access scopes = %v %v", s, err)
	}
	t3 := wantTokens(t, e.token(refreshForm(t2.refresh)))
	if t3.scope != "coach:read" {
		t.Fatalf("scope after narrowing = %q", t3.scope)
	}
	// It cannot widen again.
	wantError(t, e.token(refreshForm(t3.refresh, "scope", "coach:read coach:write")), 400, "invalid_scope")
	// The old token was spent: past the grace its reuse is detected.
	*e.now = e.now.Add(61 * time.Second)
	wantError(t, e.token(refreshForm(t0.refresh)), 400, "invalid_grant")
}

func TestTokenRefreshScopeOnReadGrant(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	t0 := wantTokens(t, e.token(e.issueCode(t, consent{scopes: []string{"coach:read"}}).form()))
	if t0.scope != "coach:read" {
		t.Fatalf("scope = %q", t0.scope)
	}
	wantError(t, e.token(refreshForm(t0.refresh, "scope", "coach:read coach:write")), 400, "invalid_scope")
	wantError(t, e.token(refreshForm(t0.refresh, "scope", "coach:read openid")), 400, "invalid_scope")
	// offline_access is accepted and ignored, as at authorization.
	t1 := wantTokens(t, e.token(refreshForm(t0.refresh, "scope", "coach:read offline_access")))
	if t1.scope != "coach:read" {
		t.Fatalf("scope = %q", t1.scope)
	}
	t2 := wantTokens(t, e.token(refreshForm(t1.refresh, "scope", "offline_access")))
	if t2.scope != "coach:read" {
		t.Fatalf("offline_access alone: scope = %q", t2.scope)
	}
}

func TestRefreshConcurrentSameToken(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	n := e.notifier()
	t0 := wantTokens(t, e.token(e.issueCode(t, consent{}).form()))
	var wg sync.WaitGroup
	start := make(chan struct{})
	recs := make([]*httptest.ResponseRecorder, 2)
	for k := range recs {
		wg.Go(func() {
			<-start
			recs[k] = e.token(refreshForm(t0.refresh))
		})
	}
	close(start)
	wg.Wait()
	a, b := wantTokens(t, recs[0]), wantTokens(t, recs[1])
	if a.refresh == b.refresh || a.access == b.access {
		t.Fatal("both refreshes got the same pair")
	}
	for _, tok := range []tokenOK{a, b} {
		if err := e.accessErr(tok.access); err != nil {
			t.Fatalf("grant not active: %v", err)
		}
		wantTokens(t, e.token(refreshForm(tok.refresh)))
	}
	if n.revoked.Load() != 0 {
		t.Fatalf("ConnectionRevoked = %d", n.revoked.Load())
	}
}

func TestRefreshReuseAfterGraceRevokes(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	n := e.notifier()
	t0 := wantTokens(t, e.token(e.issueCode(t, consent{}).form()))
	t1 := wantTokens(t, e.token(refreshForm(t0.refresh)))
	*e.now = e.now.Add(59 * time.Second)
	wantTokens(t, e.token(refreshForm(t0.refresh))) // within the grace: a sibling pair
	*e.now = e.now.Add(2 * time.Second)             // 61 s after the rotation
	wantError(t, e.token(refreshForm(t0.refresh)), 400, "invalid_grant")
	if err := e.accessErr(t1.access); !errors.Is(err, store.ErrRevoked) {
		t.Fatalf("access after reuse: %v, want ErrRevoked", err)
	}
	wantError(t, e.token(refreshForm(t1.refresh)), 400, "invalid_grant")
	if n.revoked.Load() != 1 {
		t.Fatalf("ConnectionRevoked = %d", n.revoked.Load())
	}
}

func TestTokenWrongClient(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	e.issueCode(t, consent{clientID: otherID}) // a known client, not the grant's
	i := e.issueCode(t, consent{})
	wantError(t, e.token(i.form("client_id", otherID)), 400, "invalid_grant")
	t0 := wantTokens(t, e.token(i.form()))
	for _, id := range []string{otherID, "https://unknown.example/c.json", "dcr_forged"} {
		wantError(t, e.token(refreshForm(t0.refresh, "client_id", id)), 400, "invalid_grant")
	}
	// The refused attempts neither spent the token nor revoked the grant.
	wantTokens(t, e.token(refreshForm(t0.refresh)))
}

func TestTokenNilNotifierAndMetrics(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	e.s.d.Metrics = nil
	i := e.issueCode(t, consent{})
	wantTokens(t, e.token(i.form()))
	wantError(t, e.token(i.form()), 400, "invalid_grant")
}

func TestResourceCaseAndSlash(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com:8443", false)
	good := []string{"https://COACHD.example.com:8443/mcp/", "https://coachd.example.com:8443", "https://coachd.example.com:8443/",
		"https://coachd.example.com:8443/mcp", "HTTPS://Coachd.Example.Com:8443/mcp"}
	bad := []string{"https://coachd.example.com/mcp", "https://other.example/mcp", "https://coachd.example.com:8443/mcp//",
		"https://coachd.example.com:8443/MCP", "https://coachd.example.com:8443/mcp?x=1", "https://coachd.example.com:8443/mcp#f",
		"https://coachd.example.com:8443/other", "http://coachd.example.com:8443/mcp", "https://u@coachd.example.com:8443/mcp",
		"https://coachd.example.com:8443/%6dcp", "https://coachd.example.com:443/mcp", "coachd.example.com:8443/mcp", "/mcp"}
	for _, v := range good {
		if !e.s.validResource(v) {
			t.Errorf("validResource(%q) = false", v)
		}
	}
	for _, v := range bad {
		if e.s.validResource(v) {
			t.Errorf("validResource(%q) = true", v)
		}
	}
	if !e.s.validResource("") {
		t.Error("absent resource refused")
	}
	var refresh string
	for _, v := range good[:2] {
		tok := wantTokens(t, e.token(e.issueCode(t, consent{}).form("resource", v)))
		refresh = wantTokens(t, e.token(refreshForm(tok.refresh, "resource", v))).refresh
	}
	for _, v := range bad[:2] {
		i := e.issueCode(t, consent{})
		wantError(t, e.token(i.form("resource", v)), 400, "invalid_target")
		wantError(t, e.token(refreshForm(refresh, "resource", v)), 400, "invalid_target")
		wantTokens(t, e.token(i.form())) // the refusal did not spend the code
	}
	wantTokens(t, e.token(refreshForm(refresh))) // nor the refresh token
}

func TestTokenRequestHygiene(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	i := e.issueCode(t, consent{})
	twice := i.form()
	twice.Add("code", i.code)
	rec := e.token(twice)
	wantError(t, rec, 400, "invalid_request")
	wantNoStore(t, rec)
	for _, gt := range []string{"password", "client_credentials", "implicit", "urn:ietf:params:oauth:grant-type:device_code"} {
		rec := e.token(url.Values{"grant_type": {gt}, "client_id": {appID}})
		wantError(t, rec, 400, "unsupported_grant_type")
		wantNoStore(t, rec)
	}
	rec = e.do("POST", "/oauth/token", "application/json",
		fmt.Sprintf(`{"grant_type":"authorization_code","code":%q,"client_id":%q,"code_verifier":%q}`, i.code, appID, i.verifier))
	wantError(t, rec, 400, "invalid_request")
	wantNoStore(t, rec)
	// The parameters come from the body only.
	rec = e.do("POST", "/oauth/token?"+i.form().Encode(), formType, "")
	wantError(t, rec, 400, "invalid_request")
	for name, f := range map[string]url.Values{
		"no grant_type":     {"code": {i.code}, "client_id": {appID}, "code_verifier": {i.verifier}},
		"no code":           {"grant_type": {"authorization_code"}, "client_id": {appID}, "code_verifier": {i.verifier}},
		"no client_id":      {"grant_type": {"authorization_code"}, "code": {i.code}, "code_verifier": {i.verifier}},
		"empty client_id":   {"grant_type": {"authorization_code"}, "code": {i.code}, "client_id": {""}, "code_verifier": {i.verifier}},
		"no refresh_token":  {"grant_type": {"refresh_token"}, "client_id": {appID}},
		"refresh no client": {"grant_type": {"refresh_token"}, "refresh_token": {"cdr_x"}},
		"double grant_type": {"grant_type": {"authorization_code", "authorization_code"}, "code": {i.code}, "client_id": {appID}, "code_verifier": {i.verifier}},
	} {
		rec := e.token(f)
		if rec.Code != 400 || jsonBody(t, rec.Body.String())["error"] != "invalid_request" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	rec = e.do("POST", "/oauth/token", formType, "grant_type=authorization_code&code="+strings.Repeat("a", 65<<10))
	wantError(t, rec, 413, "invalid_request")
	wantNoStore(t, rec)
	for _, m := range []string{"GET", "OPTIONS"} {
		wantNoStore(t, e.do(m, "/oauth/token", "", ""))
	}
	// None of it spent the code.
	wantTokens(t, e.token(i.form()))
}

func TestTokenStoreFailureLogsAllowlistedKeys(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	i := e.issueCode(t, consent{})
	e.db.Close()
	for _, f := range []url.Values{i.form(), refreshForm("cdr_secretrefresh")} {
		rec := e.token(f)
		wantError(t, rec, 500, "server_error")
		wantNoStore(t, rec)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, `"error_class"`) || !strings.Contains(logs, `"endpoint":"token"`) {
		t.Fatalf("logs = %s", logs)
	}
	for _, secret := range []string{i.code, i.verifier, "cdr_secretrefresh", "app.example", "localhost"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("logs carry %q: %s", secret, logs)
		}
	}
}

func TestTokenMetrics(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	i := e.issueCode(t, consent{})
	wantTokens(t, e.token(i.form()))
	wantError(t, e.token(i.form("code_verifier", strings.Repeat("A", 43))), 400, "invalid_grant")
	wantError(t, e.token(i.form()), 400, "invalid_grant") // reuse
	wantError(t, e.token(refreshForm("cdr_x", "resource", "https://other.example/mcp")), 400, "invalid_target")
	wantError(t, e.token(refreshForm("cdr_x", "scope", "coach:read")), 400, "invalid_grant")
	wantError(t, e.token(url.Values{"grant_type": {"password"}}), 400, "unsupported_grant_type")
	j := e.issueCode(t, consent{scopes: []string{"coach:read"}})
	tok := wantTokens(t, e.token(j.form()))
	wantError(t, e.token(refreshForm(tok.refresh, "scope", "coach:write")), 400, "invalid_scope")
	got := e.counter(t, "endpoint", "outcome")
	for k, want := range map[string]int64{"token/ok": 2, "token/invalid_grant": 2, "token/reuse": 1, "token/invalid_target": 1,
		"token/invalid_scope": 1, "token/invalid_request": 1} {
		if got[k] != want {
			t.Errorf("%s = %d, want %d (all %v)", k, got[k], want, got)
		}
	}
}

func TestNewToken(t *testing.T) {
	a, ha := newToken("cda_")
	b, hb := newToken("cda_")
	if !strings.HasPrefix(a, "cda_") || len(a) != 4+43 || a == b {
		t.Fatalf("tokens %q %q", a, b)
	}
	if raw, err := base64.RawURLEncoding.Strict().DecodeString(a[4:]); err != nil || len(raw) != 32 {
		t.Fatalf("body %q: %v", a[4:], err)
	}
	if string(ha) != string(sha(a)) || string(hb) != string(hashToken(b)) || string(hashToken(a)) != string(sha(a)) {
		t.Fatal("hash is not SHA-256 of the text")
	}
	if c, _ := newToken(""); len(c) != 43 {
		t.Fatalf("no prefix: %q", c)
	}
}

func TestParseFormRejectsRepeats(t *testing.T) {
	for body, ok := range map[string]bool{"a=1&b=2": true, "a=1&a=1": false, "a=1&a=2": false, "a=1;b=2": false, "a=%zz": false} {
		r := httptest.NewRequest("POST", "/x?a=9", strings.NewReader(body))
		r.Header.Set("Content-Type", formType+"; charset=UTF-8")
		f, err := parseForm(r)
		if (err == nil) != ok {
			t.Errorf("%q: err = %v", body, err)
		}
		if ok && (f.Get("a") != "1" || len(f["a"]) != 1) {
			t.Errorf("%q: form = %v (query must be ignored)", body, f)
		}
	}
	for _, ct := range []string{"", "application/json", "multipart/form-data; boundary=x", "text/plain"} {
		r := httptest.NewRequest("POST", "/x", strings.NewReader("a=1"))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		if _, err := parseForm(r); err == nil {
			t.Errorf("content type %q accepted", ct)
		}
	}
}

// TestDCRGrantsRefusedWhileDCROff (ruling R-BA-F2): with COACHD_OAUTH_DCR off
// a grant whose client_id starts with "dcr_" cannot refresh and its access
// token is refused on /mcp (revoked, 401 invalid_token); neither revokes the
// grant nor writes an audit row. Turning DCR on again restores both.
func TestDCRGrantsRefusedWhileDCROff(t *testing.T) {
	const dcrID = "dcr_example"
	e := newTestEnv(t, "https://coachd.example.com", true)
	iss := e.issueCode(t, consent{clientID: dcrID})
	tok := wantTokens(t, e.token(iss.form()))
	if err := e.verifyErr(tok.access); err != nil {
		t.Fatalf("DCR on, access: %v", err)
	}
	auditBefore, err := e.db.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}

	e.s.d.DCR = false
	if err := e.verifyErr(tok.access); !errors.Is(err, auth.ErrRevoked) {
		t.Fatalf("DCR off, access: %v, want ErrRevoked", err)
	}
	wantError(t, e.token(refreshForm(tok.refresh, "client_id", dcrID)), 400, "invalid_grant")
	auditAfter, err := e.db.ListAudit(context.Background(), 100)
	if err != nil || len(auditAfter) != len(auditBefore) {
		t.Fatalf("audit rows %d -> %d (%v): refusals must not audit", len(auditBefore), len(auditAfter), err)
	}
	// A CIMD grant is unaffected.
	cimd := wantTokens(t, e.token(e.issueCode(t, consent{}).form()))
	if err := e.verifyErr(cimd.access); err != nil {
		t.Fatalf("CIMD access with DCR off: %v", err)
	}
	wantTokens(t, e.token(refreshForm(cimd.refresh)))

	e.s.d.DCR = true
	if err := e.verifyErr(tok.access); err != nil {
		t.Fatalf("DCR back on, access: %v", err)
	}
	// The refresh token was not spent or revoked by the refusal.
	wantTokens(t, e.token(refreshForm(tok.refresh, "client_id", dcrID)))
}

func (e *testEnv) verifyErr(access string) error {
	_, err := e.s.VerifyAccess(bearerReq("Bearer " + access))
	return err
}
