package oauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/telemetry"
)

// testEnv is a Server with its store, fake clock, metric reader and log buffer.
type testEnv struct {
	s      *Server
	db     *store.DB
	now    *time.Time
	reader *sdkmetric.ManualReader
	logs   *bytes.Buffer

	path    string // the store's database file
	passkey int64  // the passkey consents approve with, enrolled by ensurePasskey
	seq     int    // consent nonce counter
}

func newTestEnv(t *testing.T, publicURL string, dcr bool) *testEnv {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "c.db")
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reader := sdkmetric.NewManualReader()
	m, err := telemetry.NewMetrics(sdkmetric.NewMeterProvider(append(
		[]sdkmetric.Option{sdkmetric.WithReader(reader)}, telemetryViewOpts()...)...))
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	now := tNow
	s, err := New(Deps{DB: db, NonceKey: bytes.Repeat([]byte{7}, 32), PublicURL: publicURL, DCR: dcr,
		Now: func() time.Time { return now }, Log: slog.New(slog.NewJSONHandler(logs, nil)), Metrics: m})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { checkLogKeys(t, logs.String()) })
	return &testEnv{s: s, db: db, path: path, now: &now, reader: reader, logs: logs}
}

// ensurePasskey enrols the passkey consents approve with, once.
func (e *testEnv) ensurePasskey(t *testing.T) {
	t.Helper()
	if e.passkey != 0 {
		return
	}
	ctx, at := context.Background(), *e.now
	eid, err := e.db.CreateEnrollment(ctx, sha("enrol-token-test"), at, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	e.passkey, err = e.db.EnrollPasskey(ctx, store.EnrollParams{EnrollmentID: eid, Nonce: "n-enrol-token-test", NonceExpires: at.Add(time.Hour),
		Passkey: store.Passkey{CredentialID: []byte("cred-token-test"), CredentialJSON: []byte(`{"v":1}`), Label: "phone"}, At: at, Lane: "public"})
	if err != nil {
		t.Fatal(err)
	}
}

// telemetryViewOpts is empty: counters here are read with their own attributes.
func telemetryViewOpts() []sdkmetric.Option { return nil }

// newTestOAuth returns the server, its store and the fake clock.
func newTestOAuth(t *testing.T, publicURL string, dcr bool) (*Server, *store.DB, *time.Time) {
	t.Helper()
	e := newTestEnv(t, publicURL, dcr)
	return e.s, e.db, e.now
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

func (e *testEnv) do(method, path, ctype, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	return rec
}

// counter returns coachd.oauth.requests values keyed by the joined attribute values.
func (e *testEnv) counter(t *testing.T, keys ...string) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := e.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "coachd.oauth.requests" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				var parts []string
				for _, k := range keys {
					v, _ := dp.Attributes.Value(attribute.Key(k))
					parts = append(parts, v.AsString())
				}
				out[strings.Join(parts, "/")] += dp.Value
			}
		}
	}
	return out
}

// grantWithTokens stores a consented grant for clientID whose code was
// redeemed for the tokens access and refresh; it returns the grant id.
func (e *testEnv) grantWithTokens(t *testing.T, clientID, access, refresh string) int64 {
	t.Helper()
	ctx, at := context.Background(), *e.now
	h := func(s string) []byte { x := sha256.Sum256([]byte(s)); return x[:] }
	eid, err := e.db.CreateEnrollment(ctx, h("enrol"), at, at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	pk, err := e.db.EnrollPasskey(ctx, store.EnrollParams{EnrollmentID: eid, Nonce: "n-enrol", NonceExpires: at.Add(time.Hour),
		Passkey: store.Passkey{CredentialID: []byte("cred"), CredentialJSON: []byte(`{"v":1}`), Label: "phone"}, At: at, Lane: "public"})
	if err != nil {
		t.Fatal(err)
	}
	row, err := e.db.PutOAuthClient(ctx, store.OAuthClient{ClientID: clientID, Kind: "cimd", Name: "App",
		RedirectURIs: []string{"http://localhost/cb"}, Metadata: []byte(`{}`), FetchedAt: at, CacheUntil: at.Add(time.Hour), CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	gid, err := e.db.GrantConsent(ctx, store.ConsentGrant{Client: store.OAuthClient{ID: row}, PasskeyID: pk, CredentialJSON: []byte(`{"v":2}`),
		Nonce: "n-consent", NonceExpires: at.Add(10 * time.Minute), Scopes: []string{"coach:read"}, Resource: e.s.self + "/mcp",
		CodeHash: h("code"), RedirectURI: "http://localhost/cb", CodeChallenge: "chal", CodeExpires: at.Add(5 * time.Minute), At: at})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.db.RedeemCode(ctx, h("code"), at, func(store.OAuthCode) error { return nil }, store.NewTokens{
		AccessHash: h(access), RefreshHash: h(refresh), AccessExpires: at.Add(time.Hour), RefreshExpires: at.Add(30 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return gid
}

func (e *testEnv) accessErr(access string) error {
	x := sha256.Sum256([]byte(access))
	_, _, err := e.db.AccessGrant(context.Background(), x[:], *e.now)
	return err
}
