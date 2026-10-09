package otelsetup

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/goleak"
)

// testReader is installed before any test runs because the otel global wires its
// already-created instruments to the *first* provider it is given and no other
// (delegateMeterOnce). observability builds its instruments at package init, so
// this has to happen here or the seeded series would land in whichever provider
// Setup installed first, out of reach of the test that looks for them.
var testReader = sdkmetric.NewManualReader()

func TestMain(m *testing.M) {
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(testReader)))
	// Setup starts the runtime metrics collector on a goroutine of its own, so a
	// shutdown that forgets one is a leak that only shows up as a slow drift in
	// production.
	goleak.VerifyTestMain(m)
}

func collect(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var got metricdata.ResourceMetrics
	require.NoError(t, testReader.Collect(t.Context(), &got))
	return got
}
