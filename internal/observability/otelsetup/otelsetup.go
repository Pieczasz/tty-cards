// Package otelsetup installs the process's OTLP providers. It is apart from
// observability so the packages that only record metrics do not link the SDK, its
// exporters and gRPC: only the composition root imports this one.
package otelsetup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Pieczasz/tty-cards/internal/config"
	"github.com/Pieczasz/tty-cards/internal/observability"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// providerShutdownTimeout is each provider's own share of the shutdown window, so a
// collector stalled on one signal cannot spend the time the others needed.
const providerShutdownTimeout = 2 * time.Second

// Setup installs the OTLP log, trace and meter providers as the otel globals and
// starts the runtime metrics. The returned shutdown flushes all three; on error
// everything already started has been shut down and shutdown is nil.
func Setup(ctx context.Context, cfg *config.Config) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error
	// Reverse order: the meter provider, whose final flush is what the alerts read,
	// goes first, and the logger last, so the others can still log their failures.
	cleanup := func(ctx context.Context) error {
		var errs error
		for _, fn := range slices.Backward(shutdownFuncs) {
			providerCtx, cancel := context.WithTimeout(ctx, providerShutdownTimeout)
			errs = errors.Join(errs, fn(providerCtx))
			cancel()
		}
		shutdownFuncs = nil
		return errs
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, cleanup(ctx))
		}
	}()

	res := newResource(cfg)

	// Nothing here reads baggage, and nothing but trace context crosses a process
	// boundary: the stats API ignores incoming headers altogether.
	otel.SetTextMapPropagator(propagation.TraceContext{})

	loggerProvider, err := newLoggerProvider(ctx, cfg, res)
	if err != nil {
		return nil, fmt.Errorf("create logger provider: %w", err)
	}
	shutdownFuncs = append(shutdownFuncs, loggerProvider.Shutdown)
	global.SetLoggerProvider(loggerProvider)

	tracerProvider, err := newTracerProvider(ctx, cfg, res)
	if err != nil {
		return nil, fmt.Errorf("create tracer provider: %w", err)
	}
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	meterProvider, err := newMeterProvider(ctx, cfg, res)
	if err != nil {
		return nil, fmt.Errorf("create meter provider: %w", err)
	}
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)
	observability.SeedAlertedSeries(ctx)

	if err := runtime.Start(runtime.WithMeterProvider(meterProvider)); err != nil {
		return nil, fmt.Errorf("start runtime metrics: %w", err)
	}

	return cleanup, nil
}

// newResource is resource.Default with the service identity on top. A set keeps the
// last value of a repeated key, which is what resource.Merge would do, without
// Merge's schema-conflict error that the shared schema URL rules out anyway.
//
// service.name is fixed, so it is one Prometheus job and one Loki service_name in
// every environment. There is deliberately no service.instance.id: a fresh one per
// restart would become a Loki index label and a new set of series every deploy.
func newResource(cfg *config.Config) *resource.Resource {
	base := resource.Default()
	return resource.NewWithAttributes(base.SchemaURL(), append(base.Attributes(),
		semconv.ServiceName("tty-cards-server"),
		semconv.ServiceVersion(cfg.ServiceVersion),
		semconv.DeploymentEnvironmentNameKey.String(cfg.Env),
	)...)
}

func newLoggerProvider(ctx context.Context, cfg *config.Config, res *resource.Resource) (*sdklog.LoggerProvider, error) {
	exporter, err := otlploggrpc.New(ctx, otlploggrpc.WithEndpointURL(cfg.OTelEndpoint))
	if err != nil {
		return nil, fmt.Errorf("create otlp log exporter: %w", err)
	}
	return sdklog.NewLoggerProvider(
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exporter)),
		sdklog.WithResource(res),
	), nil
}

func newTracerProvider(ctx context.Context, cfg *config.Config, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(cfg.OTelEndpoint))
	if err != nil {
		return nil, fmt.Errorf("create otlp trace exporter: %w", err)
	}
	// Every span is kept. The volume is one session span and a handful of repository
	// spans per player action, with none from the stats API, which Tempo holds for
	// 48h; a ratio would drop exactly the rare slow or failed trace worth reading.
	// Explicit, too, so OTEL_TRACES_SAMPLER - a knob config.Load never sees - cannot
	// switch tracing off.
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithResource(res),
	), nil
}

func newMeterProvider(ctx context.Context, cfg *config.Config, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	exporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpointURL(cfg.OTelEndpoint))
	if err != nil {
		return nil, fmt.Errorf("create otlp metric exporter: %w", err)
	}
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter)),
		sdkmetric.WithResource(res),
	), nil
}
