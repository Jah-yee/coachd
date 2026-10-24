package web

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stefiix92/coachd/internal/store"
)

// seedClient stores a client row as the OAuth server would.
func (ts *testServer) seedClient(t *testing.T, clientID, kind, name string, redirects ...string) {
	t.Helper()
	now := ts.clock.Now()
	if _, err := ts.db.PutOAuthClient(context.Background(), store.OAuthClient{ClientID: clientID, Kind: kind, Name: name,
		RedirectURIs: redirects, Metadata: []byte("{}"), FetchedAt: now, CacheUntil: now.Add(time.Hour), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
}

// seedGrant stores a grant consented with the enrolled passkey for a stored
// client, and redeems its code (activating it) when activate is set.
func (ts *testServer) seedGrant(t *testing.T, clientID, code string, activate bool, scopes ...string) int64 {
	t.Helper()
	ctx := context.Background()
	c, err := ts.db.OAuthClientByClientID(ctx, clientID)
	if err != nil {
		t.Fatal(err)
	}
	pks, err := ts.db.ActivePasskeys(ctx)
	if err != nil || len(pks) == 0 {
		t.Fatalf("no passkey: %v", err)
	}
	now := ts.clock.Now()
	id, err := ts.db.GrantConsent(ctx, store.ConsentGrant{Client: store.OAuthClient{ID: c.ID}, PasskeyID: pks[0].ID,
		CredentialJSON: pks[0].CredentialJSON, Nonce: "nonce-" + code, NonceExpires: now.Add(10 * time.Minute), Scopes: scopes,
		Resource: cSelf + "/mcp", CodeHash: []byte(code), RedirectURI: c.RedirectURIs[0], RedirectExplicit: true,
		CodeChallenge: cChallenge, CodeExpires: now.Add(5 * time.Minute), At: now})
	if err != nil {
		t.Fatal(err)
	}
	if activate {
		if _, err := ts.db.RedeemCode(ctx, []byte(code), now, func(store.OAuthCode) error { return nil }, store.NewTokens{
			AccessHash: []byte("access-" + code), RefreshHash: []byte("refresh-" + code),
			AccessExpires: now.Add(time.Hour), RefreshExpires: now.Add(30 * 24 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func TestAppsPage(t *testing.T) {
	ts := builtinServer(t)
	cookie := ts.enroll(t)
	ts.seedClient(t, "https://idle.example/client.json", "cimd", "Idle App", "https://idle.example/cb")
	ts.seedClient(t, "dcr_seeded-client", "dcr", "Direct <App>", "https://direct.example/cb")
	live := ts.seedGrant(t, cClaudeAI, "code-live", true, "coach:read", "coach:write")
	ts.seedGrant(t, "https://idle.example/client.json", "code-idle", false, "coach:read")
	dcr := ts.seedGrant(t, "dcr_seeded-client", "code-dcr", true, "coach:read")

	rec := ts.do(t, "GET", "/apps", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	web := strings.Index(body, `<td class="web" data-label="Web address"><strong>claude.ai</strong></td>`)
	name := strings.Index(body, `Claude <span class="hint">(name given by the app)</span>`)
	if web < 0 || name < 0 || web > name {
		t.Errorf("the web address must be shown, before the name (%d, %d):\n%s", web, name, body)
	}
	for _, want := range []string{"coach:read", "coach:write", fmt.Sprintf(`action="/apps/%d/revoke"`, live),
		fmt.Sprintf(`action="/apps/%d/revoke"`, dcr), "registered directly with coachd", "Direct &lt;App&gt;",
		"direct.example", "never"} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"idle.example", "Idle App", "dcr_seeded-client", "Direct <App>"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("page shows %q:\n%s", unwanted, body)
		}
	}

	rec = ts.do(t, "POST", fmt.Sprintf("/apps/%d/revoke", live), nil, cookie)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/apps" {
		t.Fatalf("revoke: status %d, Location %q, body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	rows := ts.audit(t, "oauth_grant_revoked")
	if len(rows) != 1 || rows[0].Actor != "runner" || rows[0].Lane != "public" || rows[0].Target != fmt.Sprintf("g%d", live) {
		t.Fatalf("oauth_grant_revoked rows %+v", rows)
	}
	apps, err := ts.db.ConnectedApps(context.Background(), ts.clock.Now())
	if err != nil || len(apps) != 1 || apps[0].GrantID != dcr {
		t.Fatalf("connected apps after revoke: %+v %v", apps, err)
	}
	if rec := ts.do(t, "GET", "/apps", nil, cookie); strings.Contains(rec.Body.String(), "claude.ai") {
		t.Errorf("revoked app still listed:\n%s", rec.Body)
	}
	if rec := ts.do(t, "POST", fmt.Sprintf("/apps/%d/revoke", live), nil, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("second revoke: status %d", rec.Code)
	}
	if rec := ts.do(t, "POST", "/apps/x/revoke", nil, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("bad id: status %d", rec.Code)
	}
}

func TestAppsEmpty(t *testing.T) {
	ts := builtinServer(t)
	cookie := ts.enroll(t)
	rec := ts.do(t, "GET", "/apps", nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "No app is connected.") {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
}

func TestAppsNotInAccessMode(t *testing.T) {
	ts := consentServer(t)
	cookie := ts.enroll(t)
	if rec := ts.do(t, "GET", "/apps", nil, cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("GET /apps: status %d", rec.Code)
	}
	if rec := ts.do(t, "POST", "/apps/1/revoke", nil, cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("POST revoke: status %d", rec.Code)
	}
	if rec := ts.do(t, "GET", "/", nil, cookie); strings.Contains(rec.Body.String(), `href="/apps"`) {
		t.Fatal("home links /apps in Access mode")
	}
}

func TestHomeLinksApps(t *testing.T) {
	ts := builtinServer(t)
	cookie := ts.enroll(t)
	rec := ts.do(t, "GET", "/", nil, cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `href="/apps"`) {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
}
