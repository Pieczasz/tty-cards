package tuitest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func TestCount_SumsOnlyTheMatchingSeries(t *testing.T) {
	t.Parallel()
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
	counter, err := meter.Int64Counter("c")
	require.NoError(t, err)
	histogram, err := meter.Float64Histogram("h")
	require.NoError(t, err)

	counter.Add(t.Context(), 2, metric.WithAttributes(attribute.String("stream", "a")))
	counter.Add(t.Context(), 5, metric.WithAttributes(attribute.String("stream", "b")))
	histogram.Record(t.Context(), 1, metric.WithAttributes(attribute.String("stream", "a")))

	assert.Equal(t, int64(2), Count(t, reader, "c", "stream", "a"))
	assert.Equal(t, int64(0), Count(t, reader, "h", "stream", "a"), "only counters are summed")
	assert.Equal(t, int64(0), Count(t, reader, "c", "stream", "none"))
}
