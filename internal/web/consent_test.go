package web

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/oauth"
	"github.com/stefiix92/coachd/internal/store"
)

const (
	cSelf         = "https://coachd.example.com"
	cClaudeAI     = "https://claude.ai/oauth/mcp-oauth-client-metadata"
	cClaudeAIRed  = "https://claude.ai/api/mcp/auth_callback"
	cClaudeCode   = "https://claude.ai/oauth/claude-code-client-metadata"
	cChallenge    = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	cUnseeded     = "https://unseeded.example/c.json"
	cUnseededRedr = "https://unseeded.example/cb"
)

// consentServer is a test server with the OAuth server mounted and the
// claude.ai and Claude Code clients seeded (cache a day ahead, so nothing is
// fetched).
func consentServer(t *testing.T) *testServer {
	t.Helper()
	ts := newTestServer(t)
	d := ts.srv.d
	o, err := oauth.New(oauth.Deps{DB: ts.db, NonceKey: bytes.Repeat([]byte{9}, 32), PublicURL: cSelf, DCR: true,
		Now: ts.clock.Now, Log: d.Log, Metrics: d.Metrics})
	if err != nil {
		t.Fatal(err)
	}
	d.OAuth = o
	srv, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	ts.srv, ts.h = srv, srv.Handler()
	for _, c := range []struct{ file, id string }{{"claude-ai.json", cClaudeAI}, {"claude-code.json", cClaudeCode}} {
		raw, err := os.ReadFile("../oauth/testdata/" + c.file)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Name      string   `json:"client_name"`
			Redirects []string `json:"redirect_uris"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		now := ts.clock.Now()
		if _, err := ts.db.PutOAuthClient(context.Background(), store.OAuthClient{ClientID: c.id, Kind: "cimd", Name: doc.Name,
			RedirectURIs: doc.Redirects, Metadata: raw, FetchedAt: now, CacheUntil: now.Add(24 * time.Hour), CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	return ts
}

func authQuery(clientID, redirect string) string {
	q := url.Values{"client_id": {clientID}, "redirect_uri": {redirect}, "response_type": {"code"},
		"code_challenge": {cChallenge}, "code_challenge_method": {"S256"}, "state": {"st8"},
		"scope": {"coach:read coach:write"}}
	return "/oauth/authorize?" + q.Encode()
}

func (ts *testServer) consentPage(t *testing.T, cookie *http.Cookie, clientID, redirect string) *httptest.ResponseRecorder {
	t.Helper()
	return ts.do(t, "GET", authQuery(clientID, redirect), nil, cookie)
}

// requestToken pulls the data-request token out of the consent page.
func requestToken(t *testing.T, body string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, `data-action="consent-approve" data-request="`)
	if !ok {
		t.Fatalf("no approve button in %s", body)
	}
	tok, _, _ := strings.Cut(rest, `"`)
	return tok
}

func (ts *testServer) consentToken(t *testing.T, cookie *http.Cookie) string {
	t.Helper()
	rec := ts.consentPage(t, cookie, cClaudeAI, cClaudeAIRed)
	if rec.Code != http.StatusOK {
		t.Fatalf("consent page: %d %s", rec.Code, rec.Body)
	}
	return requestToken(t, rec.Body.String())
}

func (ts *testServer) grantCount(t *testing.T) int {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+ts.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var n int
	if err := raw.QueryRow("SELECT COUNT(*) FROM oauth_grants").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAuthorizeWithoutSessionRedirectsToLogin(t *testing.T) {
	ts := consentServer(t)
	path := authQuery(cUnseeded, cUnseededRedr)
	start := time.Now()
	rec := ts.do(t, "GET", path, nil, nil)
	if time.Since(start) > time.Second {
		t.Fatal("answer took over a second: a fetch was attempted")
	}
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if want := "/login?next=" + url.QueryEscape(path); rec.Header().Get("Location") != want {
		t.Fatalf("Location = %q, want %q", rec.Header().Get("Location"), want)
	}
	if _, err := ts.db.OAuthClientByClientID(context.Background(), cUnseeded); err == nil {
		t.Fatal("an oauth_clients row was created")
	}
}

func TestAuthorizeWithoutSessionBadClient(t *testing.T) {
	ts := consentServer(t)
	rec := ts.do(t, "GET", authQuery("not-a-client", cUnseededRedr), nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
}

func TestConsentPageRenders(t *testing.T) {
	ts := consentServer(t)
	cookie := ts.enroll(t)
	rec := ts.consentPage(t, cookie, cClaudeAI, cClaudeAIRed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{"claude.ai", "name given by the app", "Claude",
		"Workouts still need your passkey to approve.", `name="write" checked`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q:\n%s", want, body)
		}
	}
	web := strings.Index(body, "The app's web address: <strong>claude.ai</strong>")
	name := strings.Index(body, "Its name: <strong>Claude</strong>")
	if web < 0 || name < 0 || web > name {
		t.Errorf("web address must be the identity line and come before the name (%d, %d):\n%s", web, name, body)
	}
	if !strings.Contains(body, "A read-only grant cannot be widened later; to widen it, revoke and reconnect.") ||
		!strings.Contains(body, "your Garmin data, summaries, goal and notes") ||
		!strings.Contains(body, "set your goal and notes, and propose workouts") {
		t.Errorf("scope wording missing:\n%s", body)
	}
	if strings.Contains(strings.ToLower(body), "verified") {
		t.Error("page says verified")
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", got)
	}
}

func TestConsentLoopbackWarning(t *testing.T) {
	ts := consentServer(t)
	cookie := ts.enroll(t)
	rec := ts.consentPage(t, cookie, cClaudeCode, "http://localhost:54321/callback")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "Any program on this computer can claim to be this app. Approve only if you just started the connection yourself.") {
		t.Errorf("no loopback warning:\n%s", rec.Body)
	}
}

func TestConsentNameEscaped(t *testing.T) {
	ts := consentServer(t)
	cookie := ts.enroll(t)
	reg := httptest.NewRecorder()
	req := httptest.NewRequest("POST", cSelf+"/oauth/register", strings.NewReader(
		`{"redirect_uris":["https://dcr.example/cb"],"client_name":"<script>x</script>\u202e"}`))
	req.Header.Set("Content-Type", "application/json")
	ts.srv.d.OAuth.Handler().ServeHTTP(reg, req)
	var r struct {
		ClientID string `json:"client_id"`
	}
	if err := json.Unmarshal(reg.Body.Bytes(), &r); err != nil || r.ClientID == "" {
		t.Fatalf("register: %d %s", reg.Code, reg.Body)
	}
	rec := ts.consentPage(t, cookie, r.ClientID, "https://dcr.example/cb")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, "<script>x") || !strings.Contains(body, "&lt;script&gt;x&lt;/script&gt;") {
		t.Errorf("name not escaped:\n%s", body)
	}
	if strings.ContainsRune(body, '\u202e') {
		t.Error("U+202E in the page")
	}
	if !strings.Contains(body, "registered directly with coachd; its identity is not checked") ||
		strings.Contains(body, "web address") {
		t.Error("DCR client must show the unchecked notice and no web address")
	}
	if strings.Contains(body, `name="write" checked`) {
		t.Error("write is on by default for a DCR client")
	}
	if !strings.Contains(body, `name="recognise"`) {
		t.Error("no recognise checkbox for a DCR client")
	}
}

// consentFinish runs begin and the authenticator, returning the finish body.
func (ts *testServer) consentFinishBody(t *testing.T, cookie *http.Cookie, token string) []byte {
	t.Helper()
	b := decodeBegin(t, ts.do(t, "POST", "/oauth/authorize/begin",
		map[string]any{"request": token, "write": true, "recognise": false}, cookie))
	cred, err := ts.authn.Get(b.Options)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"ceremony": b.Ceremony, "credential": json.RawMessage(cred)})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestConsentApproveFlow(t *testing.T) {
	ts := consentServer(t)
	cookie := ts.enroll(t)
	token := ts.consentToken(t, cookie)
	body := ts.consentFinishBody(t, cookie, token)
	rec := ts.do(t, "POST", "/oauth/authorize/finish", body, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("finish: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Redirect string `json:"redirect"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Redirect, cClaudeAIRed+"?code=") {
		t.Fatalf("redirect = %q", out.Redirect)
	}
	if rec := ts.do(t, "POST", "/oauth/authorize/finish", body, cookie); rec.Code != http.StatusConflict {
		t.Fatalf("replay: %d %s", rec.Code, rec.Body)
	}
	if n := ts.grantCount(t); n != 1 {
		t.Fatalf("%d grants, want 1", n)
	}
}

func TestConsentFinishBadCeremony(t *testing.T) {
	ts := consentServer(t)
	cookie := ts.enroll(t)
	rec := ts.do(t, "POST", "/oauth/authorize/finish", map[string]any{"ceremony": "forged", "credential": map[string]any{}}, cookie)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("finish: %d %s", rec.Code, rec.Body)
	}
	if n := ts.grantCount(t); n != 0 {
		t.Fatalf("%d grants", n)
	}
	if rec := ts.do(t, "POST", "/oauth/authorize/finish", `{"ceremony":""}`, cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed: %d", rec.Code)
	}
}

func TestConsentDeny(t *testing.T) {
	ts := consentServer(t)
	cookie := ts.enroll(t)
	token := ts.consentToken(t, cookie)
	rec := ts.do(t, "POST", "/oauth/authorize/deny", map[string]any{"request": token}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("deny: %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Redirect string `json:"redirect"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Redirect, cClaudeAIRed) || !strings.Contains(out.Redirect, "error=access_denied") {
		t.Fatalf("redirect = %q", out.Redirect)
	}
}

func TestConsentExpiredRequest(t *testing.T) {
	ts := consentServer(t)
	cookie := ts.enroll(t)
	token := ts.consentToken(t, cookie)
	ts.clock.Add(9 * time.Minute)
	body := ts.consentFinishBody(t, cookie, token) // begun in time; the ceremony is cut to the request's expiry
	ts.clock.Add(time.Minute + time.Second)
	// The session is 12 h, so it is still valid.
	for _, c := range []struct {
		path string
		body any
	}{
		{"/oauth/authorize/begin", map[string]any{"request": token, "write": true}},
		{"/oauth/authorize/finish", body},
		{"/oauth/authorize/deny", map[string]any{"request": token}},
	} {
		rec := ts.do(t, "POST", c.path, c.body, cookie)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, body %s", c.path, rec.Code, rec.Body)
			continue
		}
		if got := jsonError(t, rec); got != oauth.ErrRequestExpired.Message {
			t.Errorf("%s: error %q", c.path, got)
		}
		if strings.Contains(rec.Body.String(), "redirect") {
			t.Errorf("%s: produced a redirect", c.path)
		}
	}
	if n := ts.grantCount(t); n != 0 {
		t.Fatalf("%d grants", n)
	}
	if rows := ts.audit(t, "passkey_failure"); len(rows) != 0 {
		t.Fatalf("%d passkey_failure rows for an expired page", len(rows))
	}
}

func TestConsentCeremonyEndsWithRequest(t *testing.T) {
	ts := consentServer(t)
	cookie := ts.enroll(t)
	token := ts.consentToken(t, cookie)
	ts.clock.Add(9 * time.Minute)
	body := ts.consentFinishBody(t, cookie, token)
	// At the request's 10 min the ceremony (to 14 min if uncut) must be over too.
	ts.clock.Add(time.Minute)
	rec := ts.do(t, "POST", "/oauth/authorize/finish", body, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
}

func TestConsentNeedsSession(t *testing.T) {
	ts := consentServer(t)
	for _, p := range []string{"begin", "finish", "deny"} {
		rec := ts.do(t, "POST", "/oauth/authorize/"+p, map[string]any{"request": "x"}, nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", p, rec.Code)
			continue
		}
		jsonError(t, rec)
	}
}

func TestConsentRoutesOnlyWithOAuth(t *testing.T) {
	ts := newTestServer(t)
	if rec := ts.do(t, "GET", authQuery(cClaudeAI, cClaudeAIRed), nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
}
