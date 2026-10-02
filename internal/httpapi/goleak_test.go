package httpapi

import (
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/goleak"
)

// Installed before any handler is built: otelhttp resolves its tracer and meter from
// the globals when the handler is constructed, and the otel globals forward only to
// the first provider they are given.
var (
	testSpans   = tracetest.NewSpanRecorder()
	testMetrics = sdkmetric.NewManualReader()
)

func TestMain(m *testing.M) {
	// What observability.Setup installs in production: without it a test could not
	// tell a handler that ignores a visitor's traceparent from one that never saw it.
	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(testSpans)))
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(testMetrics)))
	goleak.VerifyTestMain(m)
}
