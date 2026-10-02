package ssh

import (
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/goleak"
)

// testMetrics is installed before any test records: the otel global forwards only to
// the first provider it is given.
var testMetrics = sdkmetric.NewManualReader()

// metricTotal is the count recorded so far on the named counter or histogram for the
// data points whose key attribute is value. Other tests record too, so callers compare
// before and after, in tests that do not run in parallel.
func metricTotal(t *testing.T, name, key, value string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := testMetrics.Collect(t.Context(), &rm); err != nil {
		t.Fatal(err)
	}
	matches := func(set attribute.Set) bool {
		v, ok := set.Value(attribute.Key(key))
		return ok && v.AsString() == value
	}
	var n int64
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name != name {
				continue
			}
			switch data := mt.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					if matches(dp.Attributes) {
						n += dp.Value
					}
				}
			case metricdata.Histogram[float64]:
				for _, dp := range data.DataPoints {
					if matches(dp.Attributes) {
						n += int64(dp.Count)
					}
				}
			}
		}
	}
	return n
}

func TestMain(m *testing.M) {
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(testMetrics)))
	// Wish runs a real bubbletea Program per session. Closing the SSH channel
	// cancels the program, but in-flight Batch/Tick work can still sit in
	// WaitGroup.Wait when TestMain verifies leaks - especially under -race with
	// two sessions (displace). Top of stack is sync.runtime_SemacquireWaitGroup and
	// the only repo frames are below bubbletea's, so these have to match any frame:
	// IgnoreTopFunction here fails with bubbletea's own execBatchMsg parked.
	//
	// Program.eventLoop is deliberately NOT ignored. It runs synchronously inside
	// Program.Run, so a goroutine still in it means Run never returned - the session
	// never tore down, its tracker slot and lobby seat still held. That is the leak
	// this package exists to catch, and the suite passes without ignoring it.
	goleak.VerifyTestMain(m,
		goleak.IgnoreAnyFunction("charm.land/bubbletea/v2.Tick.func1"),
		goleak.IgnoreAnyFunction("charm.land/bubbletea/v2.(*Program).execBatchMsg"),
		goleak.IgnoreAnyFunction("charm.land/bubbletea/v2.(*Program).execBatchMsg.func2"),
	)
}
