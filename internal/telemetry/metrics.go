package telemetry

import (
	"context"
	"math"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/exemplar"
)

// meterProviderOptions returns the options every MeterProvider must use:
// a view dropping non-allowlisted attributes, and exemplars switched off
// (exemplars would otherwise carry the attributes the view filtered out).
// Setup and tests share these exact options.
func meterProviderOptions() []sdkmetric.Option {
	return []sdkmetric.Option{
		sdkmetric.WithView(sdkmetric.NewView(
			sdkmetric.Instrument{Name: "*"},
			sdkmetric.Stream{AttributeFilter: attribute.NewAllowKeysFilter(toKeys(AllowedKeys)...)},
		)),
		sdkmetric.WithExemplarFilter(exemplar.AlwaysOffFilter),
	}
}

func toKeys(ss []string) []attribute.Key {
	ks := make([]attribute.Key, len(ss))
	for i, s := range ss {
		ks[i] = attribute.Key(s)
	}
	return ks
}

// Bucket boundaries in seconds; SDK defaults are sized for milliseconds.
// Request buckets reach 60 s: request_sync runs up to 45 s and the Garmin
// client times out at 60 s.
var (
	requestBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10, 30, 60}
	syncBuckets    = []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300}
	// approvalBuckets run from 1 minute to 7 days.
	approvalBuckets = []float64{60, 300, 900, 1800, 3600, 7200, 14400, 28800, 86400, 172800, 604800}
)

// Metrics holds coachd's synchronous instruments.
type Metrics struct {
	SyncRuns          metric.Int64Counter
	SyncDuration      metric.Float64Histogram
	GarminRequests    metric.Int64Counter
	GarminDuration    metric.Float64Histogram
	GarminRateLimited metric.Int64Counter
	MCPCalls          metric.Int64Counter
	MCPDuration       metric.Float64Histogram
	AuthFailures      metric.Int64Counter
	NotifySends       metric.Int64Counter
	WebhookSends      metric.Int64Counter
	ProposalWarnings  metric.Int64Counter
	Deliveries        metric.Int64Counter
	ApprovalLatency   metric.Float64Histogram
	BackupRuns        metric.Int64Counter
	DeviceRequests    metric.Int64Counter
	OAuthRequests     metric.Int64Counter
}

// NewMetrics creates the instruments on mp.
func NewMetrics(mp metric.MeterProvider) (*Metrics, error) {
	m := mp.Meter("coachd")
	var err error
	out := &Metrics{}
	counter := func(dst *metric.Int64Counter, name, desc string) {
		if err != nil {
			return
		}
		*dst, err = m.Int64Counter(name, metric.WithDescription(desc))
	}
	hist := func(dst *metric.Float64Histogram, name, desc string, bounds []float64) {
		if err != nil {
			return
		}
		*dst, err = m.Float64Histogram(name, metric.WithUnit("s"), metric.WithDescription(desc),
			metric.WithExplicitBucketBoundaries(bounds...))
	}
	counter(&out.SyncRuns, "coachd.garmin.sync.runs", "Garmin sync runs")
	hist(&out.SyncDuration, "coachd.garmin.sync.duration", "Garmin sync duration", syncBuckets)
	counter(&out.GarminRequests, "coachd.garmin.requests", "Garmin API requests")
	hist(&out.GarminDuration, "coachd.garmin.request.duration", "Garmin API request duration", requestBuckets)
	counter(&out.GarminRateLimited, "coachd.garmin.rate_limited", "Garmin 429 responses")
	counter(&out.MCPCalls, "coachd.mcp.calls", "MCP tool calls")
	hist(&out.MCPDuration, "coachd.mcp.call.duration", "MCP tool call duration", requestBuckets)
	counter(&out.AuthFailures, "coachd.auth.failures", "Authentication failures")
	counter(&out.NotifySends, "coachd.notify.sends", "ntfy notifications by outcome")
	counter(&out.WebhookSends, "coachd.webhook.sends", "post-run webhook events by outcome")
	counter(&out.ProposalWarnings, "coachd.proposals.warnings", "Proposal warnings by code")
	counter(&out.Deliveries, "coachd.proposals.deliveries", "Workout downloads by outcome; web downloads also by format")
	hist(&out.ApprovalLatency, "coachd.proposals.approval_latency", "Time from proposal to approval", approvalBuckets)
	counter(&out.BackupRuns, "coachd.backup.runs", "Backup runs by outcome")
	counter(&out.DeviceRequests, "coachd.device.requests", "Device API requests by endpoint and outcome")
	counter(&out.OAuthRequests, "coachd.oauth.requests", "OAuth requests by endpoint and outcome")
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GaugeSource supplies values for the observable gauges. The scalar methods
// return math.NaN() when the value cannot be read; RegisterGauges then skips
// that point rather than reporting a misleading number.
type GaugeSource interface {
	SyncAges(ctx context.Context) map[string]float64
	DataFreshness(ctx context.Context) map[string]float64
	KeyDaysLeft() float64
	DBSizeBytes() float64
	DiskUsedRatio() float64
	// BackupAgeSeconds is the age of the newest complete backup.
	BackupAgeSeconds() float64
	// DeviceLastSeenAges is seconds since each active device was last seen,
	// keyed by decimal device ID. Devices never seen are absent.
	DeviceLastSeenAges(ctx context.Context) map[string]float64
	// ProposalCounts is the number of proposals per status.
	ProposalCounts(ctx context.Context) map[string]int
}

// RegisterGauges registers observable gauges read from src by one callback.
func RegisterGauges(mp metric.MeterProvider, src GaugeSource) error {
	m := mp.Meter("coachd")
	age, err := m.Float64ObservableGauge("coachd.garmin.sync.last_success_age", metric.WithUnit("s"))
	if err != nil {
		return err
	}
	fresh, err := m.Float64ObservableGauge("coachd.garmin.data.freshness", metric.WithUnit("s"))
	if err != nil {
		return err
	}
	days, err := m.Float64ObservableGauge("coachd.garmin.key.days_left", metric.WithUnit("d"))
	if err != nil {
		return err
	}
	size, err := m.Float64ObservableGauge("coachd.db.size", metric.WithUnit("By"))
	if err != nil {
		return err
	}
	disk, err := m.Float64ObservableGauge("coachd.data.disk.used_ratio", metric.WithUnit("1"))
	if err != nil {
		return err
	}
	backupAge, err := m.Float64ObservableGauge("coachd.backup.age", metric.WithUnit("s"),
		metric.WithDescription("Age of the newest complete backup"))
	if err != nil {
		return err
	}
	deviceAge, err := m.Float64ObservableGauge("coachd.device.last_seen_age", metric.WithUnit("s"),
		metric.WithDescription("Seconds since each active watch last called the device API"))
	if err != nil {
		return err
	}
	props, err := m.Float64ObservableGauge("coachd.proposals", metric.WithDescription("Proposals by status"))
	if err != nil {
		return err
	}
	_, err = m.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		for k, v := range src.SyncAges(ctx) {
			o.ObserveFloat64(age, v, metric.WithAttributes(attribute.String("data_type", k)))
		}
		for k, v := range src.DataFreshness(ctx) {
			o.ObserveFloat64(fresh, v, metric.WithAttributes(attribute.String("data_type", k)))
		}
		for k, v := range src.DeviceLastSeenAges(ctx) {
			o.ObserveFloat64(deviceAge, v, metric.WithAttributes(attribute.String("device_id", k)))
		}
		for k, v := range src.ProposalCounts(ctx) {
			o.ObserveFloat64(props, float64(v), metric.WithAttributes(attribute.String("status", k)))
		}
		for _, g := range []struct {
			inst metric.Float64Observable
			v    float64
		}{{days, src.KeyDaysLeft()}, {size, src.DBSizeBytes()}, {disk, src.DiskUsedRatio()}, {backupAge, src.BackupAgeSeconds()}} {
			if !math.IsNaN(g.v) {
				o.ObserveFloat64(g.inst, g.v)
			}
		}
		return nil
	}, age, fresh, days, size, disk, backupAge, deviceAge, props)
	return err
}
