package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/stefiix92/coachd/internal/store"
)

func jsonBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %v: %s", err, body)
	}
	return m
}

func TestMetadataDocuments(t *testing.T) {
	for _, dcr := range []bool{false, true} {
		e := newTestEnv(t, "https://Coachd.example.com/", dcr)
		a := e.do("GET", "/.well-known/oauth-protected-resource", "", "")
		b := e.do("GET", "/.well-known/oauth-protected-resource/mcp", "", "")
		if a.Code != 200 || a.Body.String() != b.Body.String() {
			t.Fatalf("PRM paths differ: %d %q vs %q", a.Code, a.Body, b.Body)
		}
		want := map[string]any{
			"resource": "https://coachd.example.com/mcp", "authorization_servers": []any{"https://coachd.example.com"},
			"scopes_supported": []any{"coach:read", "coach:write"}, "bearer_methods_supported": []any{"header"}}
		if got := jsonBody(t, a.Body.String()); !reflect.DeepEqual(got, want) {
			t.Fatalf("PRM = %v", got)
		}
		as := e.do("GET", "/.well-known/oauth-authorization-server", "", "")
		wantAS := map[string]any{
			"issuer": "https://coachd.example.com", "authorization_endpoint": "https://coachd.example.com/oauth/authorize",
			"token_endpoint": "https://coachd.example.com/oauth/token", "revocation_endpoint": "https://coachd.example.com/oauth/revoke",
			"response_types_supported": []any{"code"}, "grant_types_supported": []any{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported":               []any{"S256"},
			"token_endpoint_auth_methods_supported":          []any{"none"},
			"revocation_endpoint_auth_methods_supported":     []any{"none"},
			"scopes_supported":                               []any{"coach:read", "coach:write", "offline_access"},
			"client_id_metadata_document_supported":          true,
			"authorization_response_iss_parameter_supported": true}
		if dcr {
			wantAS["registration_endpoint"] = "https://coachd.example.com/oauth/register"
		}
		if got := jsonBody(t, as.Body.String()); !reflect.DeepEqual(got, wantAS) {
			t.Fatalf("dcr=%v AS = %v", dcr, got)
		}
		for _, rec := range []*httptest.ResponseRecorder{a, b, as} {
			h := rec.Header()
			if h.Get("Access-Control-Allow-Origin") != "*" || h.Get("Cache-Control") != "public, max-age=3600" ||
				h.Get("Content-Type") != "application/json" {
				t.Fatalf("headers = %v", h)
			}
		}
	}
}

func TestMetadataWithPort(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com:8443", true)
	as := jsonBody(t, e.do("GET", "/.well-known/oauth-authorization-server", "", "").Body.String())
	for _, k := range []string{"issuer", "authorization_endpoint", "token_endpoint", "revocation_endpoint", "registration_endpoint"} {
		if v, _ := as[k].(string); !strings.HasPrefix(v, "https://coachd.example.com:8443") {
			t.Errorf("%s = %q", k, v)
		}
	}
	prm := jsonBody(t, e.do("GET", "/.well-known/oauth-protected-resource", "", "").Body.String())
	if prm["resource"] != "https://coachd.example.com:8443/mcp" ||
		!reflect.DeepEqual(prm["authorization_servers"], []any{"https://coachd.example.com:8443"}) {
		t.Fatalf("PRM = %v", prm)
	}
}

func TestUnknownWellKnownIsJSON404(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	for _, p := range []string{"/.well-known/openid-configuration", "/.well-known/oauth-authorization-server/x", "/authorize", "/token", "/register"} {
		rec := e.do("GET", p, "", "")
		if rec.Code != 404 || rec.Header().Get("Content-Type") != "application/json" {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	rec := e.do("POST", "/oauth/register", "application/json", `{"redirect_uris":["http://localhost/cb"]}`)
	if rec.Code != 404 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("register with DCR off: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestCORSPreflight(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	for _, p := range []string{"/oauth/token", "/oauth/revoke", "/.well-known/oauth-authorization-server"} {
		rec := e.do("OPTIONS", p, "", "")
		h := rec.Header()
		if rec.Code != 204 || !strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") ||
			!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Content-Type") ||
			!strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") ||
			h.Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("%s: %d %v", p, rec.Code, h)
		}
		if _, ok := h["Access-Control-Allow-Credentials"]; ok {
			t.Fatalf("%s: credentials header set", p)
		}
	}
}

func TestRegisterEndpoint(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", true)
	rec := e.do("POST", "/oauth/register", "application/json", `{"redirect_uris":["http://localhost:1234/cb"],"client_name":"Claude"}`)
	if rec.Code != 201 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("valid: %d %v", rec.Code, rec.Header())
	}
	id, _ := jsonBody(t, rec.Body.String())["client_id"].(string)
	if !strings.HasPrefix(id, "dcr_") {
		t.Fatalf("client_id = %q", id)
	}
	if c, err := e.s.res.stored(context.Background(), id); err != nil || c.RowID != 0 {
		t.Fatalf("registration stored a row: %+v %v", c, err)
	}
	for body, want := range map[string]string{
		`{"redirect_uris":["https://coachd.example.com/x"]}`:                   "invalid_redirect_uri",
		`{"redirect_uris":["javascript:alert(1)"]}`:                            "invalid_redirect_uri",
		`{"redirect_uris":[]}`:                                                 "invalid_client_metadata",
		`{"redirect_uris":["http://localhost/cb"],"grant_types":["password"]}`: "invalid_client_metadata",
		`not json`: "invalid_client_metadata",
	} {
		rec := e.do("POST", "/oauth/register", "application/json", body)
		if rec.Code != 400 || jsonBody(t, rec.Body.String())["error"] != want {
			t.Errorf("%s: %d %s, want %s", body, rec.Code, rec.Body, want)
		}
	}
	big := `{"redirect_uris":["http://localhost/cb"],"client_name":"` + strings.Repeat("a", 65<<10) + `"}`
	if rec := e.do("POST", "/oauth/register", "application/json", big); rec.Code != 413 {
		t.Fatalf("big body: %d", rec.Code)
	}
}

func TestRevokeEndpoint(t *testing.T) {
	const cid = "https://app.example/c.json"
	post := func(e *testEnv, form url.Values) int {
		return e.do("POST", "/oauth/revoke", "application/x-www-form-urlencoded", form.Encode()).Code
	}
	for _, tok := range []string{"cda_access", "cdr_refresh"} {
		e := newTestEnv(t, "https://coachd.example.com", false)
		e.grantWithTokens(t, cid, "cda_access", "cdr_refresh")
		if code := post(e, url.Values{"token": {"cda_unknown"}}); code != 200 {
			t.Fatalf("unknown: %d", code)
		}
		if code := post(e, url.Values{"token": {tok}, "client_id": {"https://other.example/c.json"}}); code != 200 {
			t.Fatalf("mismatch: %d", code)
		}
		if err := e.accessErr("cda_access"); err != nil {
			t.Fatalf("must not be revoked yet: %v", err)
		}
		if code := post(e, url.Values{"token": {tok}, "client_id": {cid}}); code != 200 {
			t.Fatalf("revoke %s: %d", tok, code)
		}
		if err := e.accessErr("cda_access"); !errors.Is(err, store.ErrRevoked) {
			t.Fatalf("%s: access err = %v, want ErrRevoked", tok, err)
		}
		audit, err := e.db.ListAudit(context.Background(), 10)
		if err != nil || audit[0].Event != "oauth_grant_revoked" || audit[0].Actor != "client" || audit[0].Target != "g1" {
			t.Fatalf("audit = %+v %v", audit, err)
		}
	}
	e := newTestEnv(t, "https://coachd.example.com", false)
	if code := post(e, url.Values{}); code != 400 {
		t.Fatalf("no token: %d", code)
	}
}

func TestOAuthMetric(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	e.do("GET", "/.well-known/oauth-protected-resource", "", "")
	e.do("GET", "/.well-known/oauth-authorization-server", "", "")
	e.do("GET", "/nope", "", "")
	got := e.counter(t, "endpoint", "outcome")
	if got["metadata/ok"] != 2 || got["other/not_found"] != 1 {
		t.Fatalf("counter = %v", got)
	}
}

func TestNilMetricsAndNotifier(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	e.s.d.Metrics = nil
	if rec := e.do("GET", "/.well-known/oauth-protected-resource", "", ""); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
}

func TestNewValidates(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	good := e.s.d
	for name, mut := range map[string]func(*Deps){
		"no db": func(d *Deps) { d.DB = nil }, "short key": func(d *Deps) { d.NonceKey = []byte("x") },
		"bad url": func(d *Deps) { d.PublicURL = "ftp://x" }, "empty url": func(d *Deps) { d.PublicURL = "" },
	} {
		d := good
		mut(&d)
		if _, err := New(d); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestRevokeStoreFailureLogsAllowlistedKeys(t *testing.T) {
	e := newTestEnv(t, "https://coachd.example.com", false)
	e.db.Close()
	rec := e.do("POST", "/oauth/revoke", "application/x-www-form-urlencoded", "token=cda_secret&client_id=https%3A%2F%2Fapp.example%2Fc.json")
	if rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
	logs := e.logs.String()
	if !strings.Contains(logs, `"error_class"`) || strings.Contains(logs, "cda_secret") || strings.Contains(logs, "app.example") {
		t.Fatalf("logs = %s", logs)
	}
}

func TestOAuthMetricLabelsAreEnumerated(t *testing.T) {
	endpoints := []string{"metadata", "register", "authorize", "consent", "token", "revoke", "other"}
	outcomes := []string{"ok", "invalid_request", "invalid_client", "invalid_grant", "invalid_scope", "invalid_target",
		"access_denied", "reuse", "rate_limited", "fetch_failed", "login_required", "not_found", "error"}
	e := newTestEnv(t, "https://coachd.example.com", true)
	form, js := "application/x-www-form-urlencoded", "application/json"
	e.do("GET", "/.well-known/oauth-authorization-server", "", "")
	e.do("POST", "/.well-known/oauth-authorization-server", "", "")
	e.do("GET", "/nope", "", "")
	e.do("OPTIONS", "/oauth/token", "", "")
	e.do("GET", "/oauth/token", "", "")
	e.do("POST", "/oauth/register", js, `{"redirect_uris":["http://localhost/cb"]}`)
	e.do("POST", "/oauth/register", js, `bad`)
	e.do("POST", "/oauth/register", js, `{"client_name":"`+strings.Repeat("a", 65<<10)+`"}`)
	e.do("POST", "/oauth/revoke", form, "token=x")
	e.do("POST", "/oauth/revoke", form, "")
	e.do("POST", "/oauth/revoke", form, "token="+strings.Repeat("a", 65<<10))
	e.do("POST", "/oauth/token", form, "grant_type=password")
	e.do("POST", "/oauth/token", form, "grant_type=refresh_token&refresh_token=x&client_id=y")
	e.do("POST", "/oauth/token", form, "grant_type=refresh_token&refresh_token=x&client_id=y&resource=https%3A%2F%2Fother.example")
	e.do("POST", "/oauth/token", form, "grant_type=refresh_token&refresh_token=x&refresh_token=x&client_id=y")
	e.do("POST", "/oauth/token", form, "grant_type=refresh_token&refresh_token="+strings.Repeat("a", 65<<10))
	e.s.CheckSyntax(url.Values{})
	e.s.Begin(context.Background(), "x", true, false)
	e.s.CountConsent(context.Background(), "rate_limited")
	e.s.CountConsent(context.Background(), "not an outcome")
	e.db.Close()
	e.do("POST", "/oauth/revoke", form, "token=x")
	e.do("POST", "/oauth/token", form, "grant_type=refresh_token&refresh_token=x&client_id=y")
	got := e.counter(t, "endpoint", "outcome")
	if len(got) < 8 {
		t.Fatalf("too few label pairs driven: %v", got)
	}
	for k := range got {
		ep, oc, _ := strings.Cut(k, "/")
		if !slices.Contains(endpoints, ep) || !slices.Contains(outcomes, oc) {
			t.Errorf("label pair %q outside the enumerated sets", k)
		}
	}
	if got["authorize/invalid_client"] != 1 || got["consent/invalid_request"] != 1 || got["consent/rate_limited"] != 1 {
		t.Fatalf("authorize/consent counter = %v", got)
	}
	if got["revoke/error"] != 1 || got["revoke/invalid_request"] != 2 || got["metadata/not_found"] != 1 {
		t.Fatalf("counter = %v", got)
	}
	if got["token/invalid_request"] != 3 || got["token/invalid_grant"] != 1 || got["token/invalid_target"] != 1 ||
		got["token/error"] != 1 || got["token/not_found"] != 1 || got["token/ok"] != 1 {
		t.Fatalf("token counter = %v", got)
	}
}
