package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/stefiix92/coachd/internal/auth"
	"github.com/stefiix92/coachd/internal/civil"
	"github.com/stefiix92/coachd/internal/fitdata"
	"github.com/stefiix92/coachd/internal/fitdata/fittest"
	"github.com/stefiix92/coachd/internal/store"
	"github.com/stefiix92/coachd/internal/summaries"
	"github.com/stefiix92/coachd/internal/telemetry"
)

// benchHandler serves two years of synthetic data (the shape of
// cmd/coachd's TestPerfServe) plus one activity with a 36k-record FIT, behind
// an injected read identity. Server side only: no network, no client decode.
func benchHandler(b *testing.B) (http.Handler, civil.Date) {
	b.Helper()
	ctx := context.Background()
	dir := b.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "coachd.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	today := civil.Of(testNow, testTZ)
	hrvStatus := "BALANCED"
	for i := 0; i < 730; i++ {
		d := today.AddDays(-i)
		if err := db.UpsertDailyHealth(ctx, store.DailyHealth{Date: d, SleepS: ip(27000 + i%3000), HRVOvernightMs: ip(55 + i%15),
			HRVWeeklyAvgMs: ip(58), RestingHR: ip(48 + i%6), Garmin: store.DailyGarmin{SleepScore: ip(80), HRVStatus: &hrvStatus}}); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < 400; i++ {
		d := today.AddDays(-(i * 730 / 400))
		start := time.Date(d.Year, d.Month, d.Day, 7, 0, 0, 0, testTZ)
		if err := db.UpsertActivity(ctx, store.Activity{ID: int64(1000 + i), StartLocal: start, StartUTC: start.UTC(), Date: d,
			WeekStart: d.WeekStart(), TypeKey: "running", Sport: store.SportRunning, DistanceM: 8000 + float64(i%5000),
			DurationS: 2700, MovingS: 2650, ElevGainM: 60, FITStatus: store.FITUnavailable}, []byte(`{}`)); err != nil {
			b.Fatal(err)
		}
	}
	if err := summaries.Recompute(ctx, db, today, 730); err != nil {
		b.Fatal(err)
	}
	fit, err := fittest.Build(fittest.Options{Start: testNow.Add(-24 * time.Hour).UTC(), Laps: 10, Records: 36000, HR: 150, SpeedMS: 3})
	if err != nil {
		b.Fatal(err)
	}
	start := testNow.Add(-24 * time.Hour)
	if err := db.UpsertActivity(ctx, store.Activity{ID: 42, StartLocal: start, StartUTC: start.UTC(), Date: civil.Of(start, testTZ),
		WeekStart: civil.Of(start, testTZ).WeekStart(), TypeKey: "running", Sport: store.SportRunning, DistanceM: 108000,
		DurationS: 36000, MovingS: 36000}, nil); err != nil {
		b.Fatal(err)
	}
	laps, points, err := fitdata.Decode(bytes.NewReader(fit))
	if err != nil {
		b.Fatal(err)
	}
	detail := store.ActivityDetail{Laps: laps, Series: fitdata.Downsample(points, fitdata.MaxSeriesPoints)}
	if err := db.PutActivityDetail(ctx, 42, detail); err != nil {
		b.Fatal(err)
	}
	if err := db.SetFIT(ctx, 42, "fit/42.fit", store.FITStored); err != nil {
		b.Fatal(err)
	}
	m, err := telemetry.NewMetrics(noop.NewMeterProvider())
	if err != nil {
		b.Fatal(err)
	}
	h := Handler(New(Deps{DB: db, TZ: testTZ, Now: func() time.Time { return testNow }, DataDir: dir, Metrics: m}, "bench"))
	id := *readIdentity()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
	}), today
}

func benchCall(b *testing.B, h http.Handler, tool string, args map[string]any) {
	b.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{
		"name": tool, "arguments": args, "_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28",
			"io.modelcontextprotocol/clientCapabilities": map[string]any{}}}})
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "tools/call")
		req.Header.Set("Mcp-Name", tool)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || bytes.Contains(rec.Body.Bytes(), []byte(`"isError":true`)) {
			b.Fatalf("status %d: %.300s", rec.Code, rec.Body.Bytes())
		}
	}
}

// BenchmarkGetSummaries366d is the Pi's slowest read: a year of summaries.
func BenchmarkGetSummaries366d(b *testing.B) {
	h, today := benchHandler(b)
	benchCall(b, h, "get_summaries", map[string]any{"from": today.AddDays(-365).String(), "to": today.String()})
}

// BenchmarkGetActivitySeries is the 600-point series of a 10-hour activity.
func BenchmarkGetActivitySeries(b *testing.B) {
	h, _ := benchHandler(b)
	benchCall(b, h, "get_activity", map[string]any{"id": 42, "detail": "series"})
}
