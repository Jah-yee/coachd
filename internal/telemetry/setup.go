package telemetry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Telemetry bundles the logger, metrics and tracer.
type Telemetry struct {
	Logger   *slog.Logger
	Metrics  *Metrics
	Tracer   trace.Tracer
	Shutdown func(context.Context) error
}

// Setup builds providers. OTLP export is enabled only when
// OTEL_EXPORTER_OTLP_ENDPOINT is set; otherwise only stdout JSON logs run.
func Setup(ctx context.Context, version string, stdout io.Writer) (*Telemetry, error) {
	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceName("coachd"), semconv.ServiceVersion(version)))
	if err != nil {
		return nil, err
	}
	enabled := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""

	tpOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	mpOpts := append([]sdkmetric.Option{sdkmetric.WithResource(res)}, meterProviderOptions()...)
	handlers := []slog.Handler{slog.NewJSONHandler(stdout, nil)}
	var lp *sdklog.LoggerProvider

	if enabled {
		te, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		tpOpts = append(tpOpts, sdktrace.WithBatcher(NewRedactingExporter(te)))
		me, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, err
		}
		mpOpts = append(mpOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me)))
		le, err := otlploghttp.New(ctx)
		if err != nil {
			return nil, err
		}
		lp = sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(le)))
		handlers = append(handlers, otelslog.NewHandler("coachd", otelslog.WithLoggerProvider(lp)))
	}

	tp := sdktrace.NewTracerProvider(tpOpts...)
	mp := sdkmetric.NewMeterProvider(mpOpts...)
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	if err := runtime.Start(runtime.WithMeterProvider(mp)); err != nil {
		return nil, err
	}
	metrics, err := NewMetrics(mp)
	if err != nil {
		return nil, err
	}

	var h slog.Handler = handlers[0]
	if len(handlers) > 1 {
		h = slog.NewMultiHandler(handlers...)
	}
	logger := slog.New(NewRedactingHandler(h))

	return &Telemetry{
		Logger:  logger,
		Metrics: metrics,
		Tracer:  tp.Tracer("coachd"),
		Shutdown: func(ctx context.Context) error {
			errs := []error{tp.Shutdown(ctx), mp.Shutdown(ctx)}
			if lp != nil {
				errs = append(errs, lp.Shutdown(ctx))
			}
			return errors.Join(errs...)
		},
	}, nil
}
