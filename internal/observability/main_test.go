package observability

import (
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/goleak"
)

// testReader is installed before any test runs because the otel global wires its
// already-created instruments to the *first* provider it is given and no other
// (delegateMeterOnce). metrics.go builds its instruments at package init, so this
// has to happen here or they would record into whichever provider a test happened
// to install first.
var testReader = sdkmetric.NewManualReader()

func TestMain(m *testing.M) {
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(testReader)))
	// Setup starts the runtime metrics collector on a goroutine of its own, so a
	// shutdown that forgets one is a leak that only shows up as a slow drift in
	// production.
	goleak.VerifyTestMain(m)
}
