package catalog

import (
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.uber.org/goleak"
)

// testMetrics is installed before any test records: the otel global forwards only to
// the first provider it is given.
var testMetrics = sdkmetric.NewManualReader()

func TestMain(m *testing.M) {
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(testMetrics)))
	goleak.VerifyTestMain(m)
}
