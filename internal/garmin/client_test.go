package garmin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/telemetry"
)

const (
	testKey = "SECRET-API-KEY-123"
	testTok = "SECRET-BEARER-456"
)

var (
	d1 = civil.Date{Year: 2026, Month: 10, Day: 1}
	d2 = civil.Date{Year: 2026, Month: 10, Day: 7}
)

func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	m, err := telemetry.NewMetrics(noop.NewMeterProvider())
	if err != nil {
		t.Fatal(err)
	}
	c := New(srv.URL, testKey, srv.Client(), m)
	c.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	return c, srv
}

func tokenJSON(w http.ResponseWriter) {
	w.Write([]byte(`{"access_token":"` + testTok + `","expires_in":86400}`))
}

func isToken(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/token") }

func TestTokenExchangedOnceAndCached(t *testing.T) {
	jwtBytes, err := os.ReadFile("testdata/token.jwt")
	if err != nil {
		t.Fatal(err)
	}
	wantJWT := strings.TrimSpace(string(jwtBytes))
	var tokens, data atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokens.Add(1)
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "ApiKey "+testKey {
				t.Errorf("bad token request: %s %q", r.Method, r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "text/plain")
			w.Write(jwtBytes)
			return
		}
		data.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+wantJWT {
			t.Errorf("bad auth %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`[{"activityId":7,"activityName":"Run","activityType":{"typeKey":"running"},"distance":1000.5}]`))
	}))
	for range 2 {
		items, err := c.SearchActivities(context.Background(), d1, d2, 0, 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].V.ActivityID != 7 || items[0].V.ActivityType.TypeKey != "running" || len(items[0].Raw) == 0 {
			t.Fatalf("items = %+v", items)
		}
	}
	if tokens.Load() != 1 || data.Load() != 2 {
		t.Fatalf("tokens=%d data=%d", tokens.Load(), data.Load())
	}
}

func TestSearchActivitiesQuery(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		q := r.URL.Query()
		if r.URL.Path != "/activitylist-service/activities/search/activities" ||
			q.Get("startDate") != "2026-10-01" || q.Get("endDate") != "2026-10-07" ||
			q.Get("start") != "5" || q.Get("limit") != "20" {
			t.Errorf("bad url %s", r.URL)
		}
		w.Write([]byte(`[]`))
	}))
	if _, err := c.SearchActivities(context.Background(), d1, d2, 5, 20); err != nil {
		t.Fatal(err)
	}
}

func TestDataEndpoint401ReexchangesOnce(t *testing.T) {
	var tokens, data atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokens.Add(1)
			tokenJSON(w)
			return
		}
		if data.Add(1) == 1 {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`[]`))
	}))
	if _, err := c.SleepRange(context.Background(), d1, d2); err != nil {
		t.Fatal(err)
	}
	if tokens.Load() != 2 || data.Load() != 2 {
		t.Fatalf("tokens=%d data=%d", tokens.Load(), data.Load())
	}
}

func TestDataEndpointStill401IsUnauthorized(t *testing.T) {
	var tokens atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokens.Add(1)
			tokenJSON(w)
			return
		}
		w.WriteHeader(401)
	}))
	_, err := c.SleepRange(context.Background(), d1, d2)
	if !errors.Is(err, ErrUnauthorized) || tokens.Load() != 2 {
		t.Fatalf("err=%v tokens=%d", err, tokens.Load())
	}
}

func TestDataEndpoint403IsUnauthorized(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		w.WriteHeader(403)
	}))
	if _, err := c.WeightRange(context.Background(), d1, d2); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err=%v", err)
	}
}

func TestTokenEndpoint401IsErrUnauthorized(t *testing.T) {
	for _, code := range []int{401, 403} {
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		if _, err := c.SleepRange(context.Background(), d1, d2); !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("code %d err=%v", code, err)
		}
	}
}

func Test429RetryAfterSeconds(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(429)
	}))
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	_, err := c.SleepRange(context.Background(), d1, d2)
	var rl *RateLimitedError
	if !errors.As(err, &rl) || !rl.RetryAt.Equal(now.Add(120*time.Second)) {
		t.Fatalf("err=%v", err)
	}
}

func Test429RetryAfterHTTPDateAndDefault(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for hdr, want := range map[string]time.Time{
		now.Add(5 * time.Minute).Format(http.TimeFormat): now.Add(5 * time.Minute),
		"":        now.Add(15 * time.Minute),
		"garbage": now.Add(15 * time.Minute),
	} {
		var calls atomic.Int32
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isToken(r) {
				tokenJSON(w)
				return
			}
			calls.Add(1)
			if hdr != "" {
				w.Header().Set("Retry-After", hdr)
			}
			w.WriteHeader(429)
		}))
		c.now = func() time.Time { return now }
		_, err := c.SleepRange(context.Background(), d1, d2)
		var rl *RateLimitedError
		if !errors.As(err, &rl) || !rl.RetryAt.Equal(want) || calls.Load() != 1 {
			t.Fatalf("hdr %q err=%v calls=%d", hdr, err, calls.Load())
		}
	}
}

func Test5xxRetriesThenSucceeds(t *testing.T) {
	var data atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		if data.Add(1) <= 3 {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`[]`))
	}))
	var sleeps []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	if _, err := c.SleepRange(context.Background(), d1, d2); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if data.Load() != 4 || len(sleeps) != 3 || sleeps[0] != want[0] || sleeps[1] != want[1] || sleeps[2] != want[2] {
		t.Fatalf("data=%d sleeps=%v", data.Load(), sleeps)
	}
}

func Test5xxExhaustedError(t *testing.T) {
	var data atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		data.Add(1)
		w.WriteHeader(502)
		w.Write([]byte("body with health data"))
	}))
	_, err := c.HRVRange(context.Background(), d1, d2)
	if err == nil || data.Load() != 4 {
		t.Fatalf("err=%v data=%d", err, data.Load())
	}
	s := err.Error()
	if !strings.Contains(s, "hrv") || !strings.Contains(s, "502") || strings.Contains(s, testKey) || strings.Contains(s, testTok) || strings.Contains(s, "health") {
		t.Fatalf("error = %q", s)
	}
}

func TestContextCancelDuringBackoff(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		w.WriteHeader(500)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	c.sleep = sleepCtx // real sleep: would take 1 s+ without cancellation
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := c.SleepRange(ctx, d1, d2)
	if !errors.Is(err, context.Canceled) || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}

func fitZip(t *testing.T, entries map[string][]byte, order []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range order {
		w, _ := zw.Create(n)
		w.Write(entries[n])
	}
	zw.Close()
	return buf.Bytes()
}

func TestDownloadActivityFITUnzips(t *testing.T) {
	zb := fitZip(t, map[string][]byte{"readme.txt": []byte("x"), "99_ACTIVITY.FIT": []byte("FITDATA")}, []string{"readme.txt", "99_ACTIVITY.FIT"})
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		if r.URL.Path != "/download-service/files/activity/99" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Write(zb)
	}))
	got, err := c.DownloadActivityFIT(context.Background(), 99)
	if err != nil || string(got) != "FITDATA" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestDownloadActivityFITRawAndNoEntry(t *testing.T) {
	body := []byte("\x0e\x10rawfit")
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/1") {
			w.Write(body)
			return
		}
		w.Write(fitZip(t, map[string][]byte{"a.txt": []byte("x")}, []string{"a.txt"}))
	}))
	got, err := c.DownloadActivityFIT(context.Background(), 1)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("got %q err %v", got, err)
	}
	if _, err := c.DownloadActivityFIT(context.Background(), 2); err == nil {
		t.Fatal("want error for zip without .fit")
	}
}

func TestDownloadActivityFITOversizedEntry(t *testing.T) {
	zb := fitZip(t, map[string][]byte{"a.fit": make([]byte, maxFITBody+1)}, []string{"a.fit"})
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		w.Write(zb)
	}))
	if _, err := c.DownloadActivityFIT(context.Background(), 1); err == nil {
		t.Fatal("want error")
	}
}

func TestDownloadActivityFIT404IsErrNotFound(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		w.WriteHeader(404)
	}))
	if _, err := c.DownloadActivityFIT(context.Background(), 5); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestSleepAndWeightArrays(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case isToken(r):
			tokenJSON(w)
		case strings.HasPrefix(r.URL.Path, "/sleep-service/sleep/dailySleepSummariesByDate"):
			w.Write([]byte(`[{"calendarDate":"2026-10-02","sleepTimeSeconds":27000,"sleepScores":{"overall":{"value":81}},"averageSpO2Value":95.5}]`))
		case r.URL.Path == "/weight-service/weight/range/2026-10-01/2026-10-07":
			if r.URL.RawQuery != "" {
				t.Errorf("unexpected query %q", r.URL.RawQuery)
			}
			w.Write([]byte(`{"dailyWeightSummaries":[` +
				`{"summaryDate":"2026-10-02","numOfWeightEntries":1,"latestWeight":{"calendarDate":"2026-10-02","weight":75.5,"bmi":22.1}},` +
				`{"summaryDate":"2026-10-04","numOfWeightEntries":2,"latestWeight":{"calendarDate":"2026-10-04","weight":75.0}},` +
				`{"summaryDate":"2026-10-05","numOfWeightEntries":0,"latestWeight":null}],"totalAverage":{}}`))
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}))
	s, err := c.SleepRange(context.Background(), d1, d2)
	if err != nil || len(s) != 1 || *s[0].V.SleepScores["overall"].Value != 81 || *s[0].V.SleepTimeSeconds != 27000 {
		t.Fatalf("%+v %v", s, err)
	}
	w, err := c.WeightRange(context.Background(), d1, d2)
	if err != nil || len(w) != 3 {
		t.Fatalf("%+v %v", w, err)
	}
	if w[0].V.SummaryDate != "2026-10-02" || *w[0].V.LatestWeight.Weight != 75.5 || w[0].V.LatestWeight.CalendarDate != "2026-10-02" {
		t.Fatalf("%+v", w[0].V)
	}
	if w[2].V.LatestWeight != nil {
		t.Fatalf("empty summary: %+v", w[2].V)
	}
	for i, it := range w {
		var m map[string]any
		if err := json.Unmarshal(it.Raw, &m); err != nil || m["summaryDate"] != it.V.SummaryDate {
			t.Fatalf("raw %d is not its daily summary: %s", i, it.Raw)
		}
	}
}

func TestWeightRangeEmptyBodies(t *testing.T) {
	for _, body := range []string{``, `{}`, `{"dailyWeightSummaries":[]}`, `{"dailyWeightSummaries":null}`} {
		c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isToken(r) {
				tokenJSON(w)
				return
			}
			w.Write([]byte(body))
		}))
		w, err := c.WeightRange(context.Background(), d1, d2)
		if err != nil || len(w) != 0 {
			t.Fatalf("body %q: %+v %v", body, w, err)
		}
	}
}

func TestHRVRangeUnwrapsSummaries(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		if r.URL.Path != "/hrv-service/hrv/daily/2026-10-01/2026-10-07" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.Write([]byte(`{"hrvSummaries":[{"calendarDate":"2026-10-02","lastNightAvg":55,"weeklyAvg":58,"status":"BALANCED","baseline":{"balancedLow":48,"balancedUpper":65}}],"userProfilePk":1}`))
	}))
	items, err := c.HRVRange(context.Background(), d1, d2)
	if err != nil || len(items) != 1 {
		t.Fatalf("%+v %v", items, err)
	}
	v := items[0].V
	if *v.LastNightAvg != 55 || v.Status != "BALANCED" || *v.Baseline.BalancedUpper != 65 || !strings.Contains(string(items[0].Raw), "BALANCED") {
		t.Fatalf("%+v", v)
	}
}

func TestDailySummariesV2UnwrapsData(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		if r.URL.Path != "/usersummary-service/v2/dailysummaries" || r.URL.Query().Get("startDate") != "2026-10-01" {
			t.Errorf("url %s", r.URL)
		}
		w.Write([]byte(`{"data":[{"calendarDate":"2026-10-02","heartRate":{"restingValue":48},"stress":{"avgLevel":25},"bodyBattery":{"maxValue":90,"minValue":10,"chargedValue":70,"drainedValue":60},"pulseOx":{"avgValue":96}}]}`))
	}))
	items, err := c.DailySummariesV2(context.Background(), d1, d2)
	if err != nil || len(items) != 1 {
		t.Fatalf("%+v %v", items, err)
	}
	v := items[0].V
	if *v.HeartRate.RestingValue != 48 || *v.Stress.AvgLevel != 25 || *v.BodyBattery.DrainedValue != 60 || *v.PulseOx.AvgValue != 96 {
		t.Fatalf("%+v", v)
	}
}

func TestConcurrentCallsShareOneTokenExchange(t *testing.T) {
	var tokens atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokens.Add(1)
			time.Sleep(20 * time.Millisecond)
			tokenJSON(w)
			return
		}
		w.Write([]byte(`[]`))
	}))
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.SleepRange(context.Background(), d1, d2); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if tokens.Load() != 1 {
		t.Fatalf("tokens=%d", tokens.Load())
	}
}

func TestTokenRefreshedAfterExpiry(t *testing.T) {
	var tokens atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokens.Add(1)
			tokenJSON(w)
			return
		}
		w.Write([]byte(`[]`))
	}))
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	c.SleepRange(context.Background(), d1, d2)
	now = now.Add(22 * time.Hour) // before expiry-1h = 23h
	c.SleepRange(context.Background(), d1, d2)
	if tokens.Load() != 1 {
		t.Fatalf("tokens=%d", tokens.Load())
	}
	now = now.Add(2 * time.Hour)
	c.SleepRange(context.Background(), d1, d2)
	if tokens.Load() != 2 {
		t.Fatalf("tokens=%d", tokens.Load())
	}
}

func TestAPIKeyNotInErrors(t *testing.T) {
	type mode struct {
		name string
		h    http.HandlerFunc
	}
	body := []byte("echo " + testKey + " " + testTok)
	modes := []mode{
		{"token500", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500); w.Write(body) }},
		{"token400", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400); w.Write(body) }},
		{"token401", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401); w.Write(body) }},
		{"data500", func(w http.ResponseWriter, r *http.Request) {
			if isToken(r) {
				tokenJSON(w)
				return
			}
			w.WriteHeader(500)
			w.Write(body)
		}},
		{"data429", func(w http.ResponseWriter, r *http.Request) {
			if isToken(r) {
				tokenJSON(w)
				return
			}
			w.WriteHeader(429)
			w.Write(body)
		}},
		{"data404", func(w http.ResponseWriter, r *http.Request) {
			if isToken(r) {
				tokenJSON(w)
				return
			}
			w.WriteHeader(404)
			w.Write(body)
		}},
		{"data400", func(w http.ResponseWriter, r *http.Request) {
			if isToken(r) {
				tokenJSON(w)
				return
			}
			w.WriteHeader(400)
			w.Write(body)
		}},
		{"data401", func(w http.ResponseWriter, r *http.Request) {
			if isToken(r) {
				tokenJSON(w)
				return
			}
			w.WriteHeader(401)
			w.Write(body)
		}},
		{"badjson", func(w http.ResponseWriter, r *http.Request) {
			if isToken(r) {
				tokenJSON(w)
				return
			}
			w.Write([]byte(`{"x": ` + testKey))
		}},
	}
	for _, m := range modes {
		c, srv := newTestClient(t, m.h)
		calls := []func() error{
			func() error { _, err := c.SleepRange(context.Background(), d1, d2); return err },
			func() error { _, err := c.DownloadActivityFIT(context.Background(), 1); return err },
			func() error { _, err := c.HRVRange(context.Background(), d1, d2); return err },
		}
		for i, call := range calls {
			err := call()
			if err == nil && m.name == "badjson" && i == 1 {
				continue // FIT download returns raw bytes; not JSON
			}
			if err == nil {
				t.Errorf("%s/%d: want error", m.name, i)
				continue
			}
			if s := err.Error(); strings.Contains(s, testKey) || strings.Contains(s, testTok) || strings.Contains(s, srv.URL) || strings.Contains(s, "echo") {
				t.Errorf("%s/%d leaked: %q", m.name, i, s)
			}
		}
	}
	// transport error
	c, srv := newTestClient(t, http.NotFoundHandler())
	srv.Close()
	_, err := c.SleepRange(context.Background(), d1, d2)
	if err == nil || strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("transport err = %v", err)
	}
}

func TestHRVRange204IsEmpty(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		w.WriteHeader(204)
	}))
	items, err := c.HRVRange(context.Background(), d1, d2)
	if err != nil || len(items) != 0 {
		t.Fatalf("items=%v err=%v", items, err)
	}
}

func TestEmpty200BodyIsEmptyList(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isToken(r) {
			tokenJSON(w)
			return
		}
		w.WriteHeader(200)
	}))
	for name, call := range map[string]func() (int, error){
		"sleep": func() (int, error) { i, err := c.SleepRange(context.Background(), d1, d2); return len(i), err },
		"hrv":   func() (int, error) { i, err := c.HRVRange(context.Background(), d1, d2); return len(i), err },
		"v2":    func() (int, error) { i, err := c.DailySummariesV2(context.Background(), d1, d2); return len(i), err },
	} {
		if n, err := call(); err != nil || n != 0 {
			t.Fatalf("%s: n=%d err=%v", name, n, err)
		}
	}
}

func TestToken404IsNotErrNotFound(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	_, err := c.DownloadActivityFIT(context.Background(), 1)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
	s := err.Error()
	if !strings.Contains(s, "token") || !strings.Contains(s, "404") || strings.Contains(s, testKey) {
		t.Fatalf("err=%q", s)
	}
}

func TestActivityExists(t *testing.T) {
	tests := []struct {
		name      string
		data      func(w http.ResponseWriter) // activity endpoint response
		token     func(w http.ResponseWriter) // token endpoint response; nil = valid token
		want      bool
		wantErr   func(error) bool // nil = no error
		wantCalls int32            // activity endpoint requests
	}{
		{name: "200 exists", data: func(w http.ResponseWriter) { w.Write([]byte(`{"activityId":42,"activityName":"x"}`)) },
			want: true, wantCalls: 1},
		{name: "404 deleted", data: func(w http.ResponseWriter) { w.WriteHeader(404) }, want: false, wantCalls: 1},
		{name: "500 retried then error", data: func(w http.ResponseWriter) { w.WriteHeader(500) },
			wantErr: func(err error) bool {
				var se *StatusError
				return errors.As(err, &se) && se.Endpoint == "activity" && se.Status == 500
			}, wantCalls: 4},
		// Only an explicit 404 from the activity endpoint means deleted; everything else is an error.
		{name: "403 unauthorized", data: func(w http.ResponseWriter) { w.WriteHeader(403) },
			wantErr: func(err error) bool { return errors.Is(err, ErrUnauthorized) }, wantCalls: 1},
		{name: "401 after reexchange unauthorized", data: func(w http.ResponseWriter) { w.WriteHeader(401) },
			wantErr: func(err error) bool { return errors.Is(err, ErrUnauthorized) }, wantCalls: 2},
		{name: "429 rate limited", data: func(w http.ResponseWriter) { w.WriteHeader(429) },
			wantErr: func(err error) bool { var rl *RateLimitedError; return errors.As(err, &rl) }, wantCalls: 1},
		{name: "400 status error", data: func(w http.ResponseWriter) { w.WriteHeader(400) },
			wantErr: func(err error) bool { var se *StatusError; return errors.As(err, &se) && se.Status == 400 }, wantCalls: 1},
		{name: "token 404 is not deleted", token: func(w http.ResponseWriter) { w.WriteHeader(404) },
			data:    func(w http.ResponseWriter) { w.WriteHeader(404) },
			wantErr: func(err error) bool { return err != nil && !errors.Is(err, ErrNotFound) }, wantCalls: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if isToken(r) {
					if tc.token != nil {
						tc.token(w)
						return
					}
					tokenJSON(w)
					return
				}
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/activity-service/activity/42" || r.URL.RawQuery != "" {
					t.Errorf("bad request %s %s", r.Method, r.URL)
				}
				tc.data(w)
			}))
			got, err := c.ActivityExists(context.Background(), 42)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if tc.wantErr != nil && !tc.wantErr(err) {
				t.Fatalf("err = %v, want matching error", err)
			}
			if err != nil && got {
				t.Fatal("exists = true alongside an error")
			}
			if got != tc.want {
				t.Fatalf("exists = %v, want %v", got, tc.want)
			}
			if n := calls.Load(); n != tc.wantCalls {
				t.Fatalf("activity requests = %d, want %d", n, tc.wantCalls)
			}
			if err != nil && (strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), testTok)) {
				t.Fatalf("error leaks a secret: %q", err)
			}
		})
	}
}

func TestActivityExistsDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Int32
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenPath:
			tokenJSON(w)
		case "/activity-service/activity/42":
			http.Redirect(w, r, "/gone", http.StatusFound)
		case "/activitylist-service/activities/search/activities":
			http.Redirect(w, r, "/list", http.StatusFound)
		case "/list":
			w.Write([]byte(`[]`))
		default: // the redirect target: a 404 that must not read as "deleted"
			followed.Add(1)
			w.WriteHeader(404)
		}
	}))
	got, err := c.ActivityExists(context.Background(), 42)
	var se *StatusError
	if got || !errors.As(err, &se) || se.Endpoint != "activity" || se.Status != http.StatusFound {
		t.Fatalf("ActivityExists = %v, %v; want a 302 status error", got, err)
	}
	if n := followed.Load(); n != 0 {
		t.Fatalf("redirect target requested %d times, want 0", n)
	}
	// Other endpoints still follow redirects.
	if _, err := c.SearchActivities(context.Background(), d1, d2, 0, 20); err != nil {
		t.Fatalf("SearchActivities through a redirect: %v", err)
	}
}
