package httpapi

import (
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/goleak"
)

// Installed before any Handler is built: otelhttp resolves its tracer and meter from
// the globals when the handler is constructed, and the otel globals forward only to
// the first provider they are given.
var (
	testSpans   = tracetest.NewSpanRecorder()
	testMetrics = sdkmetric.NewManualReader()
)

func TestMain(m *testing.M) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(testSpans)))
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(testMetrics)))
	goleak.VerifyTestMain(m)
}
