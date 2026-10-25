package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	_ "time/tzdata" // the tests use Europe/Bratislava

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/stefiix92/coachd/internal/auth"
	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/passkey"
	"github.com/stefiix92/coachd/internal/passkey/passkeytest"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/telemetry"
	"github.com/stefiix92/coachd/internal/workout"
)

// testOrigin is both the passkey service's public URL and the authenticator's
// origin (RP ID localhost).
const testOrigin = "http://localhost:8080"

// t0 is 11:00 on Friday 23 Oct 2026 in Europe/Bratislava.
var t0 = time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
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

// testServer drives Handler() directly. In Access mode every request made
// through do or serve carries a public-lane identity, as the Access
// middleware would add; in builtin mode none does, as nothing sits in front
// of the page there.
type testServer struct {
	srv     *Server
	builtin bool
	h       http.Handler
	db      *store.DB
	dbPath  string
	authn   *passkeytest.Authenticator
	clock   *fakeClock
	reader  *sdkmetric.ManualReader
	logs    *lockedBuffer
	spans   *tracetest.SpanRecorder
}

// newTestServer returns the server with its store, a software authenticator
// for testOrigin and a fake clock at t0. On cleanup it checks that every log
// line and span attribute carries only allowlisted keys.
func newTestServer(t *testing.T) *testServer {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "coachd.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	tz, err := time.LoadLocation("Europe/Bratislava")
	if err != nil {
		t.Fatal(err)
	}
	clk := &fakeClock{t: t0}
	pk, err := passkey.New(testOrigin, passkey.NewSigner(bytes.Repeat([]byte{7}, 32)), db, clk.Now)
	if err != nil {
		t.Fatal(err)
	}
	reader := sdkmetric.NewManualReader()
	m, err := telemetry.NewMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	spans := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("web")
	srv, err := New(Deps{DB: db, Passkeys: pk, TZ: tz, Now: clk.Now,
		Log: slog.New(slog.NewJSONHandler(logs, nil)), Metrics: m, Tracer: tracer})
	if err != nil {
		t.Fatal(err)
	}
	ts := &testServer{srv: srv, h: srv.Handler(), db: db, dbPath: dbPath, authn: passkeytest.New(testOrigin), clock: clk, reader: reader, logs: logs, spans: spans}
	t.Cleanup(func() {
		checkLogKeys(t, logs.String())
		checkSpanKeys(t, spans)
	})
	return ts
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

// rawExec runs SQL on the test database over a second connection, to
// tamper with rows or break the store underneath the server.
func (ts *testServer) rawExec(t *testing.T, query string, args ...any) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+ts.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// serve runs req through the handler, with a public-lane identity in Access
// mode only.
func (ts *testServer) serve(req *http.Request) *httptest.ResponseRecorder {
	if !ts.builtin {
		req = req.WithContext(auth.WithIdentity(req.Context(),
			auth.Identity{Lane: auth.LanePublic, Subject: "runner@example.com", Client: "other"}))
	}
	rec := httptest.NewRecorder()
	ts.h.ServeHTTP(rec, req)
	return rec
}

// do sends a request. body: nil (none), url.Values (a form), string or
// []byte (raw JSON) or anything else (marshalled to JSON). The cookie is
// added explicitly: a cookie jar would never send a Secure cookie over http.
func (ts *testServer) do(t *testing.T, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
	case url.Values:
		rd, ctype = strings.NewReader(b.Encode()), "application/x-www-form-urlencoded"
	case string:
		rd, ctype = strings.NewReader(b), "application/json"
	case []byte:
		rd, ctype = bytes.NewReader(b), "application/json"
	default:
		j, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd, ctype = bytes.NewReader(j), "application/json"
	}
	req := httptest.NewRequest(method, testOrigin+path, rd)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return ts.serve(req)
}

// beginResponse is the body of /login/begin and /enroll/begin.
type beginResponse struct {
	Options  json.RawMessage `json:"options"`
	Ceremony string          `json:"ceremony"`
}

func decodeBegin(t *testing.T, rec *httptest.ResponseRecorder) beginResponse {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("begin: status %d, body %s", rec.Code, rec.Body)
	}
	var b beginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Options) == 0 || b.Ceremony == "" {
		t.Fatalf("begin: %s", rec.Body)
	}
	return b
}

// jsonError returns the "error" field of a JSON response.
func jsonError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, body %s", ct, rec.Body)
	}
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	return e.Error
}

// newEnrollment stores an enrolment, as `coachd enroll` does, and returns its token.
func (ts *testServer) newEnrollment(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 32)
	rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(token))
	now := ts.clock.Now()
	if _, err := ts.db.CreateEnrollment(context.Background(), h[:], now, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	return token
}

// enrollWith runs the enrolment flow for token and returns the response of
// /enroll/finish.
func (ts *testServer) enrollWith(t *testing.T, token, label string) *httptest.ResponseRecorder {
	t.Helper()
	b := decodeBegin(t, ts.do(t, "POST", "/enroll/begin", map[string]any{"token": token}, nil))
	cred, err := ts.authn.Create(b.Options)
	if err != nil {
		t.Fatal(err)
	}
	return ts.do(t, "POST", "/enroll/finish", map[string]any{
		"ceremony": b.Ceremony, "token": token, "label": label, "credential": json.RawMessage(cred)}, nil)
}

// enroll enrols ts.authn and returns the session cookie the enrolment started.
func (ts *testServer) enroll(t *testing.T) *http.Cookie {
	t.Helper()
	rec := ts.enrollWith(t, ts.newEnrollment(t), "phone")
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll finish: status %d, body %s", rec.Code, rec.Body)
	}
	return sessionCookie(t, rec)
}

// loginFinishBody runs /login/begin and the authenticator and returns the
// body for /login/finish.
func (ts *testServer) loginFinishBody(t *testing.T) []byte {
	t.Helper()
	b := decodeBegin(t, ts.do(t, "POST", "/login/begin", "{}", nil))
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

// login signs in with ts.authn and returns the session cookie.
func (ts *testServer) login(t *testing.T) *http.Cookie {
	t.Helper()
	rec := ts.do(t, "POST", "/login/finish", ts.loginFinishBody(t), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("login finish: status %d, body %s", rec.Code, rec.Body)
	}
	return sessionCookie(t, rec)
}

// sessionCookie returns the session cookie set by rec.
func sessionCookie(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "__Host-coachd_session" {
			return c
		}
	}
	t.Fatalf("no session cookie in %q", rec.Header().Values("Set-Cookie"))
	return nil
}

// audit returns the audit rows for event, newest first.
func (ts *testServer) audit(t *testing.T, event string) []store.AuditEvent {
	t.Helper()
	all, err := ts.db.ListAudit(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.AuditEvent
	for _, e := range all {
		if e.Event == event {
			out = append(out, e)
		}
	}
	return out
}

// authFailures returns coachd.auth.failures by "lane/reason".
func (ts *testServer) authFailures(t *testing.T) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := ts.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "coachd.auth.failures" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				lane, _ := dp.Attributes.Value(attribute.Key("lane"))
				reason, _ := dp.Attributes.Value(attribute.Key("reason"))
				out[lane.AsString()+"/"+reason.AsString()] += dp.Value
			}
		}
	}
	return out
}

// today is the fake clock's date in the server's timezone.
func (ts *testServer) today() civil.Date { return civil.Of(ts.clock.Now(), ts.srv.d.TZ) }

// addProposal stores a valid one-step running workout as a proposal written
// by an agent. warnings is the stored warnings JSON ("" means none).
func (ts *testServer) addProposal(t *testing.T, name string, date civil.Date, status store.ProposalStatus, warnings string) store.Proposal {
	t.Helper()
	tenMin := 600
	w := workout.Workout{Name: name, Sport: "running", Date: date.String(), Steps: []workout.Step{
		{StepBase: workout.StepBase{Kind: "warmup", Duration: &workout.Duration{Type: "time", TimeS: &tenMin}}}}}
	return ts.propose(t, w, proposeOpts{status: status, warnings: warnings})
}

func ip(v int) *int { return &v }
