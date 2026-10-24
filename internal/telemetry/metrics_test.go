package telemetry

import (
	"bytes"
	"context"
	"math"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestMetricViewDropsUnknownLabel(t *testing.T) {
	r := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append(meterProviderOptions(), sdkmetric.WithReader(r))...)
	c, _ := mp.Meter("t").Int64Counter("c")
	c.Add(context.Background(), 1, metric.WithAttributes(attribute.String("tool", "x"), attribute.Int("hrv", 5)))
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	sum := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64])
	set := sum.DataPoints[0].Attributes
	if set.Len() != 1 {
		t.Fatalf("attrs: %v", set)
	}
	if v, ok := set.Value("tool"); !ok || v.AsString() != "x" {
		t.Errorf("tool missing")
	}
}

type fakeSrc struct{}

func (fakeSrc) SyncAges(context.Context) map[string]float64 {
	return map[string]float64{"sleep": 10, "hrv": 20}
}
func (fakeSrc) DataFreshness(context.Context) map[string]float64 {
	return map[string]float64{"sleep": 1}
}
func (fakeSrc) KeyDaysLeft() float64      { return 30 }
func (fakeSrc) DBSizeBytes() float64      { return 1024 }
func (fakeSrc) DiskUsedRatio() float64    { return 0.5 }
func (fakeSrc) BackupAgeSeconds() float64 { return 3600 }
func (fakeSrc) DeviceLastSeenAges(context.Context) map[string]float64 {
	return map[string]float64{"1": 90}
}
func (fakeSrc) ProposalCounts(context.Context) map[string]int {
	return map[string]int{"pending": 2, "approved": 1, "delivered": 0}
}

func TestRegisterGauges(t *testing.T) {
	r := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append(meterProviderOptions(), sdkmetric.WithReader(r))...)
	if err := RegisterGauges(mp, fakeSrc{}); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	points := map[string]int{}
	for _, m := range rm.ScopeMetrics[0].Metrics {
		points[m.Name] = len(m.Data.(metricdata.Gauge[float64]).DataPoints)
	}
	want := map[string]int{
		"coachd.garmin.sync.last_success_age": 2, "coachd.garmin.data.freshness": 1,
		"coachd.garmin.key.days_left": 1, "coachd.db.size": 1, "coachd.data.disk.used_ratio": 1,
		"coachd.proposals": 3, "coachd.backup.age": 1, "coachd.device.last_seen_age": 1,
	}
	for k, v := range want {
		if points[k] != v {
			t.Errorf("%s: got %d want %d (%v)", k, points[k], v, points)
		}
	}
}

func TestNewMetrics(t *testing.T) {
	m, err := NewMetrics(noop.NewMeterProvider())
	if err != nil || m.MCPCalls == nil || m.AuthFailures == nil {
		t.Fatalf("%v %+v", err, m)
	}
}

func TestSetupDisabled(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	var buf bytes.Buffer
	tel, err := Setup(context.Background(), "v0", &buf)
	if err != nil {
		t.Fatal(err)
	}
	tel.Logger.Info("hello", "outcome", "ok", "hrv_ms", 5)
	_, sp := tel.Tracer.Start(context.Background(), "x")
	sp.End()
	if !strings.Contains(buf.String(), `"outcome":"ok"`) || strings.Contains(buf.String(), "hrv_ms") {
		t.Errorf("log: %s", buf.String())
	}
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMetricsNoExemplarsInSampledSpan(t *testing.T) {
	r := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append(meterProviderOptions(), sdkmetric.WithReader(r))...)
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	ctx, sp := tp.Tracer("t").Start(context.Background(), "op")
	defer sp.End()
	m, err := NewMetrics(mp)
	if err != nil {
		t.Fatal(err)
	}
	attrs := metric.WithAttributes(attribute.String("tool", "x"), attribute.Int("hrv_ms", 42))
	m.MCPCalls.Add(ctx, 1, attrs)
	m.MCPDuration.Record(ctx, 0.02, attrs)
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, mt := range rm.ScopeMetrics[0].Metrics {
		switch d := mt.Data.(type) {
		case metricdata.Sum[int64]:
			for _, p := range d.DataPoints {
				seen++
				if len(p.Exemplars) != 0 {
					t.Errorf("%s exemplars: %v", mt.Name, p.Exemplars)
				}
			}
		case metricdata.Histogram[float64]:
			for _, p := range d.DataPoints {
				seen++
				if len(p.Exemplars) != 0 {
					t.Errorf("%s exemplars: %v", mt.Name, p.Exemplars)
				}
				// 0.02s must land in the (0.01, 0.025] bucket (index 2).
				if p.Bounds[2] != 0.025 || p.BucketCounts[2] != 1 {
					t.Errorf("bucket: bounds=%v counts=%v", p.Bounds, p.BucketCounts)
				}
			}
		}
	}
	if seen != 2 {
		t.Errorf("seen %d points", seen)
	}
}

type nanSrc struct{ fakeSrc }

func (nanSrc) KeyDaysLeft() float64                          { return math.NaN() }
func (nanSrc) DBSizeBytes() float64                          { return math.NaN() }
func (nanSrc) DiskUsedRatio() float64                        { return math.NaN() }
func (nanSrc) BackupAgeSeconds() float64                     { return math.NaN() }
func (nanSrc) ProposalCounts(context.Context) map[string]int { return nil }

func TestRegisterGaugesSkipsNaN(t *testing.T) {
	r := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append(meterProviderOptions(), sdkmetric.WithReader(r))...)
	if err := RegisterGauges(mp, nanSrc{}); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, m := range rm.ScopeMetrics[0].Metrics {
		switch m.Name {
		case "coachd.garmin.key.days_left", "coachd.db.size", "coachd.data.disk.used_ratio", "coachd.backup.age":
			t.Errorf("%s observed despite NaN", m.Name)
		}
	}
}

// request_sync may run 45 s and the Garmin client times out at 60 s, so the
// request histograms need buckets up to 60 s.
func TestRequestBucketsCoverSlowCalls(t *testing.T) {
	r := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append(meterProviderOptions(), sdkmetric.WithReader(r))...)
	m, err := NewMetrics(mp)
	if err != nil {
		t.Fatal(err)
	}
	m.MCPDuration.Record(context.Background(), 45)
	m.GarminDuration.Record(context.Background(), 59)
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, mt := range rm.ScopeMetrics[0].Metrics {
		h, ok := mt.Data.(metricdata.Histogram[float64])
		if !ok || len(h.DataPoints) == 0 {
			continue
		}
		p := h.DataPoints[0]
		n := len(p.Bounds)
		if n < 2 || p.Bounds[n-2] != 30 || p.Bounds[n-1] != 60 || p.BucketCounts[n-1] != 1 {
			t.Errorf("%s: bounds %v counts %v, want the last bucket (30, 60] to hold the sample", mt.Name, p.Bounds, p.BucketCounts)
		}
	}
}

func TestWorkoutMetricsExist(t *testing.T) {
	r := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append(meterProviderOptions(), sdkmetric.WithReader(r))...)
	m, err := NewMetrics(mp)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m.NotifySends.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "sent")))
	m.ProposalWarnings.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", "x")))
	m.Deliveries.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "ok")))
	m.ApprovalLatency.Record(ctx, 90)
	var rm metricdata.ResourceMetrics
	if err := r.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, mm := range rm.ScopeMetrics[0].Metrics {
		names[mm.Name] = true
		if mm.Name == "coachd.proposals.approval_latency" {
			b := mm.Data.(metricdata.Histogram[float64]).DataPoints[0].Bounds
			want := []float64{60, 300, 900, 1800, 3600, 7200, 14400, 28800, 86400, 172800, 604800}
			if len(b) != len(want) {
				t.Fatalf("bounds %v", b)
			}
			for i := range want {
				if b[i] != want[i] {
					t.Fatalf("bounds %v", b)
				}
			}
		}
	}
	for _, n := range []string{"coachd.notify.sends", "coachd.proposals.warnings", "coachd.proposals.deliveries", "coachd.proposals.approval_latency"} {
		if !names[n] {
			t.Errorf("missing %s", n)
		}
	}
}

func TestProposalsGauge(t *testing.T) {
	r := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append(meterProviderOptions(), sdkmetric.WithReader(r))...)
	if err := RegisterGauges(mp, fakeSrc{}); err != nil {
		t.Fatal(err)
	}
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, m := range rm.ScopeMetrics[0].Metrics {
		if m.Name != "coachd.proposals" {
			continue
		}
		for _, dp := range m.Data.(metricdata.Gauge[float64]).DataPoints {
			if dp.Attributes.Len() != 1 {
				t.Fatalf("attrs %v", dp.Attributes)
			}
			v, _ := dp.Attributes.Value("status")
			got[v.AsString()] = dp.Value
		}
	}
	if len(got) != 3 || got["pending"] != 2 || got["approved"] != 1 || got["delivered"] != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestBackupRunsCounter(t *testing.T) {
	r := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(append(meterProviderOptions(), sdkmetric.WithReader(r))...)
	m, err := NewMetrics(mp)
	if err != nil {
		t.Fatal(err)
	}
	m.BackupRuns.Add(context.Background(), 1, metric.WithAttributes(attribute.String("outcome", "ok")))
	var rm metricdata.ResourceMetrics
	if err := r.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	for _, mm := range rm.ScopeMetrics[0].Metrics {
		if mm.Name == "coachd.backup.runs" {
			return
		}
	}
	t.Fatal("coachd.backup.runs not reported")
}
