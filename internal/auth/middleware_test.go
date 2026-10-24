package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/telemetry"
)

type fakeAudit struct {
	mu      sync.Mutex
	ev      []store.AuditEvent
	ctxErrs []error
}

func (f *fakeAudit) AppendAudit(ctx context.Context, e store.AuditEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ctxErrs = append(f.ctxErrs, ctx.Err())
	f.ev = append(f.ev, e)
	return nil
}

func noopMetrics(t *testing.T) *telemetry.Metrics {
	t.Helper()
	m, err := telemetry.NewMetrics(noop.NewMeterProvider())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMiddlewareAuditsFailure(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	m, err := telemetry.NewMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	a := &fakeAudit{}
	verify := func(*http.Request) (Identity, error) { return Identity{}, ErrMissing }
	h := Middleware(LanePrivate, verify, NewLimiter(120, 30), a, m)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("next called")
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/mcp", nil)
	req.Header.Set("User-Agent", "claude-code/1.0")
	h.ServeHTTP(rec, req)
	if rec.Code != 401 || rec.Body.String() != `{"error":"unauthorized"}` || rec.Header().Get("WWW-Authenticate") != "Bearer" ||
		rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if len(a.ev) != 1 || a.ev[0].Event != "auth_failure" || a.ev[0].Outcome != "missing" || a.ev[0].Lane != "private" ||
		a.ev[0].Target != "/mcp" || a.ev[0].Actor != "" {
		t.Fatalf("%+v", a.ev)
	}
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name != "coachd.auth.failures" {
				continue
			}
			for _, dp := range mt.Data.(metricdata.Sum[int64]).DataPoints {
				total += dp.Value
				if v, _ := dp.Attributes.Value("reason"); v.AsString() != "missing" {
					t.Fatalf("reason %v", v)
				}
				if v, _ := dp.Attributes.Value("lane"); v.AsString() != "private" {
					t.Fatalf("lane %v", v)
				}
			}
		}
	}
	if total != 1 {
		t.Fatalf("counter %d", total)
	}
}

func TestMiddlewareReasonMapping(t *testing.T) {
	cases := map[string]error{"missing": ErrMissing, "invalid": ErrInvalid, "expired": ErrExpired, "revoked": ErrRevoked,
		"forbidden_email": ErrForbiddenEmail, "invalid ": errors.New("boom")}
	for want, e := range cases {
		a := &fakeAudit{}
		h := Middleware(LanePublic, func(*http.Request) (Identity, error) { return Identity{}, e }, NewLimiter(120, 30), a, noopMetrics(t))(http.NotFoundHandler())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if want == "invalid " {
			want = "invalid"
		}
		if rec.Code != 401 || a.ev[0].Outcome != want {
			t.Fatalf("%s: %d %+v", want, rec.Code, a.ev)
		}
	}
}

func TestMiddlewareRateLimit(t *testing.T) {
	ok := func(*http.Request) (Identity, error) { return Identity{Lane: LanePrivate, Subject: "token:t1"}, nil }
	h := Middleware(LanePrivate, ok, NewLimiter(120, 30), &fakeAudit{}, noopMetrics(t))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for i := 1; i <= 30; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != 200 {
			t.Fatalf("req %d: %d", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "60" || rec.Body.String() != `{"error":"rate_limited"}` {
		t.Fatalf("%d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
}

func TestLimiterPerSubject(t *testing.T) {
	l := NewLimiter(60, 1)
	if !l.Allow(LanePrivate, "a") || l.Allow(LanePrivate, "a") {
		t.Fatal("bucket a")
	}
	if !l.Allow(LanePrivate, "b") || !l.Allow(LanePublic, "a") {
		t.Fatal("buckets must be independent")
	}
}

func TestMiddlewareSetsIdentityWithClient(t *testing.T) {
	ok := func(*http.Request) (Identity, error) {
		return Identity{Lane: LanePrivate, Subject: "token:t1", TokenID: "t1", Scopes: []string{ScopeRead}}, nil
	}
	var got Identity
	var found bool
	h := Middleware(LanePrivate, ok, NewLimiter(120, 30), &fakeAudit{}, noopMetrics(t))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, found = IdentityFrom(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("User-Agent", "claude-code/1.0")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !found || got.Subject != "token:t1" || got.Client != telemetry.ClientClass("claude-code/1.0") || got.Client == "" || !got.Has(ScopeRead) {
		t.Fatalf("%+v %v", got, found)
	}
}

func TestMiddlewareAuditSurvivesClientDisconnect(t *testing.T) {
	a := &fakeAudit{}
	verify := func(*http.Request) (Identity, error) { return Identity{}, ErrInvalid }
	h := Middleware(LanePrivate, verify, NewLimiter(120, 30), a, noopMetrics(t))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/mcp", nil).WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if len(a.ctxErrs) != 1 || a.ctxErrs[0] != nil {
		t.Fatalf("audit ctx err = %v, want nil (context must be detached from the request)", a.ctxErrs)
	}
}

func failureTotal(t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name == "coachd.auth.failures" {
				for _, dp := range mt.Data.(metricdata.Sum[int64]).DataPoints {
					total += dp.Value
				}
			}
		}
	}
	return total
}

func readerMetrics(t *testing.T) (*sdkmetric.ManualReader, *telemetry.Metrics) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	m, err := telemetry.NewMetrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	if err != nil {
		t.Fatal(err)
	}
	return reader, m
}

func TestMiddlewareWithSkipAuditMissing(t *testing.T) {
	reader, m := readerMetrics(t)
	a := &fakeAudit{}
	h := MiddlewareWith(MiddlewareOptions{Lane: LanePublic, Auditor: a, Metrics: m, SkipAuditMissing: true,
		Verify:    func(*http.Request) (Identity, error) { return Identity{}, ErrMissing },
		Challenge: func(error) string { return "Bearer x" }})(http.NotFoundHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", nil))
	if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer x" {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	if len(a.ev) != 0 || failureTotal(t, reader) != 1 {
		t.Fatalf("audit %+v counter %d", a.ev, failureTotal(t, reader))
	}
	// A non-missing failure is still audited.
	h2 := MiddlewareWith(MiddlewareOptions{Lane: LanePublic, Auditor: a, Metrics: m, SkipAuditMissing: true,
		Verify: func(*http.Request) (Identity, error) { return Identity{}, ErrInvalid }})(http.NotFoundHandler())
	h2.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/mcp", nil))
	if len(a.ev) != 1 {
		t.Fatalf("%+v", a.ev)
	}
}

func TestMiddlewareWithSampler(t *testing.T) {
	reader, m := readerMetrics(t)
	a := &fakeAudit{}
	h := MiddlewareWith(MiddlewareOptions{Lane: LanePublic, Auditor: a, Metrics: m, Sampler: NewAuditSampler(1, nil),
		Verify: func(*http.Request) (Identity, error) { return Identity{}, ErrInvalid }})(http.NotFoundHandler())
	for range 3 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", nil))
		if rec.Code != 401 {
			t.Fatal(rec.Code)
		}
	}
	if len(a.ev) != 1 || a.ev[0].Event != "auth_failure" || failureTotal(t, reader) != 3 {
		t.Fatalf("audit %+v counter %d", a.ev, failureTotal(t, reader))
	}
}

func TestMiddlewareWithPerGrantLimit(t *testing.T) {
	verify := func(r *http.Request) (Identity, error) {
		return Identity{Lane: LanePublic, Subject: r.Header.Get("X-Grant")}, nil
	}
	h := MiddlewareWith(MiddlewareOptions{Lane: LanePublic, Verify: verify, Limiter: NewLimiter(1, 1), Auditor: &fakeAudit{}})(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	call := func(g string) int {
		req := httptest.NewRequest("POST", "/mcp", nil)
		req.Header.Set("X-Grant", g)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if call("grant:1") != 200 || call("grant:2") != 200 || call("grant:1") != 429 {
		t.Fatal("per-grant buckets")
	}
}

func TestMiddlewareWithUnavailable(t *testing.T) {
	reader, m := readerMetrics(t)
	a := &fakeAudit{}
	h := MiddlewareWith(MiddlewareOptions{Lane: LanePublic, Auditor: a, Metrics: m, Sampler: NewAuditSampler(1, nil),
		Verify:    func(*http.Request) (Identity, error) { return Identity{}, fmt.Errorf("db: %w", ErrUnavailable) },
		Challenge: func(error) string { return "Bearer x" }})(http.NotFoundHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/mcp", nil))
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "5" || rec.Body.String() != `{"error":"unavailable"}` ||
		rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("%d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	if len(a.ev) != 0 || failureTotal(t, reader) != 1 {
		t.Fatalf("audit %+v counter %d", a.ev, failureTotal(t, reader))
	}
	var rm metricdata.ResourceMetrics
	reader.Collect(context.Background(), &rm)
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name == "coachd.auth.failures" {
				for _, dp := range mt.Data.(metricdata.Sum[int64]).DataPoints {
					if v, _ := dp.Attributes.Value("reason"); v.AsString() != "unavailable" {
						t.Fatalf("reason %v", v)
					}
				}
			}
		}
	}
}
