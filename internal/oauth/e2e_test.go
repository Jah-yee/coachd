package oauth_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"golang.org/x/oauth2"

	"github.com/stefiix92/coachd/internal/auth"
	"github.com/stefiix92/coachd/internal/mcpserver"
	"github.com/stefiix92/coachd/internal/oauth"
	"github.com/stefiix92/coachd/internal/passkey"
	"github.com/stefiix92/coachd/internal/passkey/passkeytest"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/telemetry"
	"github.com/stefiix92/coachd/internal/web"
)

const (
	// e2eDocHost serves the client's metadata document. httptest's
	// certificate covers *.example.com; the fetcher's resolver maps the host
	// to the document server.
	e2eDocHost  = "client.example.com"
	e2eClientID = "https://" + e2eDocHost + "/oauth/client.json"
	// e2eRedirect is where the client says it listens. Its CIMD document
	// registers http://localhost/callback without a port, as Claude Code's
	// does: a loopback redirect matches whatever its port (RFC 8252 §7.3).
	e2eRedirect = "http://localhost:54321/callback"
	// e2eSession is the page's session cookie.
	e2eSession = "__Host-coachd_session"
)

// oauthPaths mirrors cmd/coachd's: the OAuth server's routes on the public
// listener, outside the page.
var oauthPaths = []string{"/.well-known/", "/oauth/token", "/oauth/register", "/oauth/revoke",
	"/authorize", "/token", "/register"}

// TestBuiltinEndToEnd runs the MCP Go SDK's client-side OAuth (the
// authorization code handler with the streamable transport) against the
// builtin public lane on a TLS server: discovery from the 401, client
// identification (a CIMD document on a second TLS server, or DCR), the
// runner's login and consent with a software passkey, the token exchange,
// tools/list, a refresh once the access token is past its hour, and a 401
// once the refresh token is revoked.
func TestBuiltinEndToEnd(t *testing.T) {
	t.Run("cimd", func(t *testing.T) { runEndToEnd(t, false) })
	t.Run("dcr", func(t *testing.T) { runEndToEnd(t, true) })
}

func runEndToEnd(t *testing.T, dcr bool) {
	e := newE2E(t, dcr)
	ctx := context.Background()
	e.enrol(t)

	var authorizations atomic.Int32
	var refuse atomic.Bool
	var src *clockSource
	cfg := &sdkauth.AuthorizationCodeHandlerConfig{
		RedirectURL:         e2eRedirect,
		RequestRefreshToken: true,
		Client:              e.client,
		AuthorizationCodeFetcher: func(ctx context.Context, args *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
			authorizations.Add(1)
			if refuse.Load() {
				return nil, errors.New("e2e: the runner does not approve again")
			}
			return e.browser(ctx, args.URL, dcr)
		},
		// The handler's own token source (oauth2.ReuseTokenSource) judges
		// expiry on the real clock, which never moves an hour in a test.
		// This one judges it on the server's fake clock and refreshes through
		// the oauth2.Config the handler built from the discovered metadata.
		NewTokenSource: func(ctx context.Context, c *oauth2.Config, tok *oauth2.Token) (oauth2.TokenSource, error) {
			src = &clockSource{ctx: ctx, cfg: c, tok: tok, issued: e.clock.Now(), now: e.clock.Now}
			return src, nil
		},
	}
	if dcr {
		cfg.DynamicClientRegistrationConfig = &sdkauth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{RedirectURIs: []string{e2eRedirect}, ClientName: "E2E DCR client",
				GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
				TokenEndpointAuthMethod: "none"},
		}
	} else {
		// x/oauth2 does not know a CIMD client's auth style, so its first
		// code exchange sends the client_id as HTTP Basic only; coachd
		// answers invalid_request without spending the code, and the retry
		// with client_id in the form succeeds.
		cfg.ClientIDMetadataDocumentConfig = &sdkauth.ClientIDMetadataDocumentConfig{URL: e2eClientID}
	}
	handler, err := sdkauth.NewAuthorizationCodeHandler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "1"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{Endpoint: e.self + "/mcp", HTTPClient: e.client, OAuthHandler: handler,
			DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()

	// 1. Connecting met the 401, discovered the server and got a token.
	first := e.rec.mcpAnswers()
	if len(first) == 0 || first[0].status != http.StatusUnauthorized ||
		!strings.Contains(first[0].challenge, `resource_metadata="`+e.self+`/.well-known/oauth-protected-resource/mcp"`) {
		t.Fatalf("first /mcp answer = %+v, want a 401 naming the resource metadata", first)
	}
	if n := authorizations.Load(); n != 1 {
		t.Fatalf("%d authorizations, want 1", n)
	}
	if !dcr && e.docFetches.Load() != 1 {
		t.Errorf("client document fetched %d times, want once (GET /oauth/authorize only)", e.docFetches.Load())
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if !slices.ContainsFunc(tools.Tools, func(tl *mcp.Tool) bool { return tl.Name == "get_status" }) {
		t.Fatalf("tools/list lacks get_status: %d tools", len(tools.Tools))
	}
	if res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_status"}); err != nil || res.IsError {
		t.Fatalf("get_status: %v %+v", err, res)
	}
	tok1 := src.current()
	if tok1.ExpiresIn != 3600 || !strings.HasPrefix(tok1.AccessToken, "cda_") || !strings.HasPrefix(tok1.RefreshToken, "cdr_") {
		t.Fatalf("token: expires_in %d, access %.4s…, refresh %.4s…", tok1.ExpiresIn, tok1.AccessToken, tok1.RefreshToken)
	}
	if scope, _ := tok1.Extra("scope").(string); scope != "coach:read coach:write" {
		t.Errorf("granted scope %q", scope)
	}

	// 2. Past the hour the access token is refused; the client refreshes
	// (no new consent) and the session goes on.
	e.clock.Add(61 * time.Minute)
	if code, _ := e.rawMCP(t, tok1.AccessToken); code != http.StatusUnauthorized {
		t.Fatalf("expired access token: status %d, want 401", code)
	}
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatalf("tools/list after the hour: %v", err)
	}
	tok2 := src.current()
	if src.refreshCount() != 1 || tok2.AccessToken == tok1.AccessToken || tok2.RefreshToken == tok1.RefreshToken {
		t.Fatalf("refreshes %d; access rotated %v, refresh rotated %v", src.refreshCount(),
			tok2.AccessToken != tok1.AccessToken, tok2.RefreshToken != tok1.RefreshToken)
	}
	if n := authorizations.Load(); n != 1 {
		t.Fatalf("%d authorizations after the refresh, want 1", n)
	}
	if code, _ := e.rawMCP(t, tok2.AccessToken); code != http.StatusOK {
		t.Fatalf("refreshed access token on a raw call: status %d", code)
	}

	// 3. Revoking the refresh token ends the grant: the next call answers
	// 401 invalid_token, and the client's attempt to authorize again is
	// refused here, as a runner who did not start it would.
	refuse.Store(true)
	form := url.Values{"token": {tok2.RefreshToken}, "client_id": {src.cfg.ClientID}}
	if code, body := e.postForm(t, "/oauth/revoke", form); code != http.StatusOK {
		t.Fatalf("revoke: %d %s", code, body)
	}
	before := len(e.rec.mcpAnswers())
	if _, err := session.ListTools(ctx, nil); err == nil {
		t.Fatal("tools/list after revoke succeeded")
	}
	after := e.rec.mcpAnswers()[before:]
	if len(after) == 0 || after[0].status != http.StatusUnauthorized || !strings.Contains(after[0].challenge, `error="invalid_token"`) {
		t.Fatalf("/mcp answers after revoke = %+v, want 401 invalid_token", after)
	}
	if n := authorizations.Load(); n != 2 {
		t.Errorf("%d authorizations, want 2 (the client tried again after the 401)", n)
	}
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok2.RefreshToken}, "client_id": {src.cfg.ClientID}}
	if code, body := e.postForm(t, "/oauth/token", refresh); code != http.StatusBadRequest || !strings.Contains(body, `"invalid_grant"`) {
		t.Fatalf("refresh after revoke: %d %s", code, body)
	}
}

// e2e is the builtin public lane composed as cmd/coachd's publicMux does,
// served over TLS at https://localhost:<port>, plus the client's view of it.
type e2e struct {
	self       string // the public URL
	db         *store.DB
	clock      *e2eClock
	authn      *passkeytest.Authenticator
	client     *http.Client // trusts the test certificate and dials the test listener
	rec        *recorder
	docFetches atomic.Int32
}

func newE2E(t *testing.T, dcr bool) *e2e {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "coachd.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e := &e2e{db: db, clock: &e2eClock{t: time.Now().UTC().Truncate(time.Second)}}

	logs := &lockedBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	spans := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("e2e")
	m, err := telemetry.NewMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewManualReader())))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		checkLogKeys(t, logs.String())
		checkSpanKeys(t, spans)
	})

	// The client's metadata document, on its own TLS server.
	docSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/client.json" {
			http.NotFound(w, r)
			return
		}
		e.docFetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"client_id": e2eClientID, "client_name": "E2E CIMD client",
			"redirect_uris": []string{"http://localhost/callback"}, "grant_types": []string{"authorization_code", "refresh_token"},
			"response_types": []string{"code"}, "token_endpoint_auth_method": "none"})
	}))
	t.Cleanup(docSrv.Close)
	docRoots := x509.NewCertPool()
	docRoots.AddCert(docSrv.Certificate())
	loopback := netip.MustParseAddr("127.0.0.1")
	fetch := oauth.FetcherOpts(
		func(_ context.Context, host string) ([]netip.Addr, error) {
			if host != e2eDocHost {
				return nil, fmt.Errorf("unexpected CIMD host %q", host)
			}
			return []netip.Addr{loopback}, nil
		},
		func(a netip.Addr) bool { return a == loopback },
		&tls.Config{RootCAs: docRoots},
		// The production dialer resolved and checked the address; the
		// document server cannot listen on 443.
		func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != "127.0.0.1:443" {
				return nil, fmt.Errorf("dial to unchecked address %q", addr)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, docSrv.Listener.Addr().String())
		})

	// coachd on https://localhost:<port>: WebAuthn refuses an IP address as
	// the relying party, and the SDK's DNS-rebinding guard wants a loopback
	// Host on a loopback listener.
	srv := httptest.NewUnstartedServer(nil)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	e.self = fmt.Sprintf("https://localhost:%d", port)
	key := bytes.Repeat([]byte{7}, 32)
	oauthSrv, err := oauth.NewWithFetcher(oauth.Deps{DB: db, NonceKey: key, PublicURL: e.self, DCR: dcr,
		Now: e.clock.Now, Log: log, Metrics: m}, fetch)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := passkey.New(e.self, passkey.NewSigner(key), db, e.clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	sampler := auth.NewAuditSampler(60, e.clock.Now)
	ws, err := web.New(web.Deps{DB: db, Passkeys: pk, TZ: time.UTC, Now: e.clock.Now, Log: log, Metrics: m, Tracer: tracer,
		Builtin: true, OAuth: oauthSrv, Sampler: sampler})
	if err != nil {
		t.Fatal(err)
	}
	mcpH := auth.MiddlewareWith(auth.MiddlewareOptions{Lane: auth.LanePublic, Verify: oauthSrv.VerifyAccess,
		Limiter: auth.NewLimiter(120, 30), Auditor: db, Metrics: m, Challenge: oauthSrv.Challenge, Sampler: sampler,
		SkipAuditMissing: true})(mcpserver.Handler(mcpserver.New(mcpserver.Deps{DB: db, TZ: time.UTC, Now: e.clock.Now,
		DataDir: t.TempDir(), Metrics: m, Audit: db, Log: log, Tracer: tracer}, "e2e")))
	mux := http.NewServeMux()
	mux.Handle("/", ws.Handler())
	mux.Handle("/mcp", mcpH)
	for _, p := range oauthPaths {
		mux.Handle(p, oauthSrv.Handler())
	}
	cert, roots := localhostCert(t)
	srv.Config.Handler = mux
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	e.authn = passkeytest.New(e.self)
	e.rec = &recorder{base: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr != fmt.Sprintf("localhost:%d", port) {
				return nil, fmt.Errorf("unexpected dial %q", addr)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Listener.Addr().String())
		},
	}}
	t.Cleanup(e.rec.base.(*http.Transport).CloseIdleConnections)
	e.client = &http.Client{Transport: e.rec, Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return e
}

// enrol registers the authenticator's passkey the way `coachd enroll` and
// the /enroll page do, in a browser whose session is then thrown away.
func (e *e2e) enrol(t *testing.T) {
	t.Helper()
	raw := make([]byte, 32)
	rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	now := e.clock.Now()
	if _, err := e.db.CreateEnrollment(context.Background(), sum[:], now, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if code, _, _, err := e.do(ctx, "GET", "/enroll", nil, nil); err != nil || code != http.StatusOK {
		t.Fatalf("GET /enroll: %d %v", code, err)
	}
	opts, ceremony, err := e.begin(ctx, "/enroll/begin", map[string]any{"token": token}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := e.authn.Create(opts)
	if err != nil {
		t.Fatal(err)
	}
	code, body, _, err := e.do(ctx, "POST", "/enroll/finish", map[string]any{"ceremony": ceremony, "token": token,
		"label": "phone", "credential": json.RawMessage(cred)}, nil)
	if err != nil || code != http.StatusOK {
		t.Fatalf("enroll finish: %d %s %v", code, body, err)
	}
}

// browser plays the runner's browser for one authorization: the authorize
// URL without a session goes to the login page, which keeps it in next; the
// passkey login returns there; the consent page is approved with the
// passkey. It returns the code, state and iss of the client redirect.
func (e *e2e) browser(ctx context.Context, authorizeURL string, dcr bool) (*sdkauth.AuthorizationResult, error) {
	u, err := url.Parse(authorizeURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme+"://"+u.Host != e.self || u.Path != "/oauth/authorize" {
		return nil, fmt.Errorf("authorize URL %q is not on %s", authorizeURL, e.self)
	}
	code, _, h, err := e.do(ctx, "GET", u.RequestURI(), nil, nil)
	if err != nil {
		return nil, err
	}
	login := h.Get("Location")
	if code != http.StatusSeeOther || !strings.HasPrefix(login, "/login?next=") {
		return nil, fmt.Errorf("authorize without a session: %d to %q, want the login page", code, login)
	}
	code, page, _, err := e.do(ctx, "GET", login, nil, nil)
	if err != nil || code != http.StatusOK {
		return nil, fmt.Errorf("login page: %d %v", code, err)
	}
	next, err := attr(page, `data-action="login" data-next="`)
	if err != nil {
		return nil, err
	}
	if next != u.RequestURI() {
		return nil, fmt.Errorf("login next %q, want the authorize request %q", next, u.RequestURI())
	}
	opts, ceremony, err := e.begin(ctx, "/login/begin", map[string]any{}, nil)
	if err != nil {
		return nil, err
	}
	cred, err := e.authn.Get(opts)
	if err != nil {
		return nil, err
	}
	code, body, h, err := e.do(ctx, "POST", "/login/finish", map[string]any{"ceremony": ceremony, "credential": json.RawMessage(cred)}, nil)
	if err != nil || code != http.StatusOK {
		return nil, fmt.Errorf("login finish: %d %s %v", code, body, err)
	}
	var cookie *http.Cookie
	for _, c := range (&http.Response{Header: h}).Cookies() {
		if c.Name == e2eSession {
			cookie = c
		}
	}
	if cookie == nil {
		return nil, errors.New("login set no session cookie")
	}

	code, page, _, err = e.do(ctx, "GET", next, nil, cookie)
	if err != nil || code != http.StatusOK {
		return nil, fmt.Errorf("consent page: %d %v", code, err)
	}
	request, err := attr(page, `data-action="consent-approve" data-request="`)
	if err != nil {
		return nil, err
	}
	opts, ceremony, err = e.begin(ctx, "/oauth/authorize/begin", map[string]any{"request": request, "write": true, "recognise": dcr}, cookie)
	if err != nil {
		return nil, err
	}
	if cred, err = e.authn.Get(opts); err != nil {
		return nil, err
	}
	code, body, _, err = e.do(ctx, "POST", "/oauth/authorize/finish", map[string]any{"ceremony": ceremony, "credential": json.RawMessage(cred)}, cookie)
	if err != nil || code != http.StatusOK {
		return nil, fmt.Errorf("consent finish: %d %s %v", code, body, err)
	}
	var out struct {
		Redirect string `json:"redirect"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	back, err := url.Parse(out.Redirect)
	if err != nil {
		return nil, err
	}
	if back.Scheme+"://"+back.Host+back.Path != e2eRedirect {
		return nil, fmt.Errorf("redirect %q is not the client's %s", out.Redirect, e2eRedirect)
	}
	q := back.Query()
	return &sdkauth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
}

// do sends a request to the public lane. body: nil (none) or JSON-marshalled.
func (e *e2e) do(ctx context.Context, method, path string, body any, cookie *http.Cookie) (int, []byte, http.Header, error) {
	var rd io.Reader
	if body != nil {
		j, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
		rd = bytes.NewReader(j)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.self+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header, err
}

// begin posts a ceremony start and returns its options and token.
func (e *e2e) begin(ctx context.Context, path string, body any, cookie *http.Cookie) ([]byte, string, error) {
	code, b, _, err := e.do(ctx, "POST", path, body, cookie)
	if err != nil {
		return nil, "", err
	}
	var out struct {
		Options  json.RawMessage `json:"options"`
		Ceremony string          `json:"ceremony"`
	}
	if code != http.StatusOK || json.Unmarshal(b, &out) != nil || len(out.Options) == 0 || out.Ceremony == "" {
		return nil, "", fmt.Errorf("%s: %d %s", path, code, b)
	}
	return out.Options, out.Ceremony, nil
}

// postForm posts a form to an OAuth endpoint and returns the status and body.
func (e *e2e) postForm(t *testing.T, path string, form url.Values) (int, string) {
	t.Helper()
	resp, err := e.client.Post(e.self+path, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// rawMCP sends tools/list straight to /mcp with access, outside the SDK.
func (e *e2e) rawMCP(t *testing.T, access string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", e.self+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// attr returns the HTML-unescaped attribute value that follows prefix.
func attr(page []byte, prefix string) (string, error) {
	_, rest, ok := strings.Cut(string(page), prefix)
	if !ok {
		return "", fmt.Errorf("page lacks %s", prefix)
	}
	v, _, _ := strings.Cut(rest, `"`)
	return html.UnescapeString(v), nil
}

// clockSource is the client's token source, judged on the fake clock.
type clockSource struct {
	ctx context.Context
	cfg *oauth2.Config
	now func() time.Time

	mu        sync.Mutex
	tok       *oauth2.Token
	issued    time.Time
	refreshes int
}

// Token returns the token until a minute before expires_in runs out on the
// fake clock, then refreshes it through cfg as oauth2's own source would.
func (s *clockSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.now().Before(s.issued.Add(time.Duration(s.tok.ExpiresIn)*time.Second - time.Minute)) {
		return s.tok, nil
	}
	t, err := s.cfg.TokenSource(s.ctx, &oauth2.Token{RefreshToken: s.tok.RefreshToken}).Token()
	if err != nil {
		return nil, err
	}
	s.tok, s.issued = t, s.now()
	s.refreshes++
	return t, nil
}

func (s *clockSource) current() *oauth2.Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tok
}

func (s *clockSource) refreshCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshes
}

// recorder notes the status and challenge of every /mcp answer.
type recorder struct {
	base http.RoundTripper
	mu   sync.Mutex
	mcp  []mcpAnswer
}

type mcpAnswer struct {
	status    int
	challenge string
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.base.RoundTrip(req)
	if err == nil && req.URL.Path == "/mcp" {
		r.mu.Lock()
		r.mcp = append(r.mcp, mcpAnswer{resp.StatusCode, resp.Header.Get("WWW-Authenticate")})
		r.mu.Unlock()
	}
	return resp, err
}

func (r *recorder) mcpAnswers() []mcpAnswer {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.mcp)
}

type e2eClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *e2eClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *e2eClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// localhostCert is a self-signed certificate for localhost and a pool that
// trusts it.
func localhostCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k, Leaf: leaf}, pool
}

// lockedBuffer is a log sink safe for concurrent writes.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// checkLogKeys fails the test when a log line carries a key outside
// telemetry.AllowedKeys (besides slog's own time, level and msg).
func checkLogKeys(t *testing.T, logs string) {
	t.Helper()
	for line := range strings.SplitSeq(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Errorf("log line is not JSON: %v", err)
			continue
		}
		for k := range m {
			if k != "time" && k != "level" && k != "msg" && !slices.Contains(telemetry.AllowedKeys, k) {
				t.Errorf("log key %q is not allowlisted (line %s)", k, line)
			}
		}
	}
}

// checkSpanKeys fails the test when a span or link attribute key is outside
// telemetry.AllowedKeys.
func checkSpanKeys(t *testing.T, sr *tracetest.SpanRecorder) {
	t.Helper()
	for _, s := range sr.Ended() {
		kvs := slices.Clone(s.Attributes())
		for _, l := range s.Links() {
			kvs = append(kvs, l.Attributes...)
		}
		for _, kv := range kvs {
			if !slices.Contains(telemetry.AllowedKeys, string(kv.Key)) {
				t.Errorf("span %s: attribute key %q is not allowlisted", s.Name(), kv.Key)
			}
		}
	}
}
