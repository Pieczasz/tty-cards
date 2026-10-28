package otelsetup

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type mockLogsService struct {
	collogpb.UnimplementedLogsServiceServer
	requests []*collogpb.ExportLogsServiceRequest
}

func (m *mockLogsService) Export(_ context.Context, req *collogpb.ExportLogsServiceRequest) (*collogpb.ExportLogsServiceResponse, error) {
	m.requests = append(m.requests, req)
	return &collogpb.ExportLogsServiceResponse{}, nil
}

// Setup exports traces and metrics as well as logs, and all three have to
// survive shutdown: a signal that only flushes one of them loses the other two on
// every deploy, which is exactly when the interesting telemetry is produced.
type mockTraceService struct {
	coltracepb.UnimplementedTraceServiceServer
	requests []*coltracepb.ExportTraceServiceRequest
	// stall holds each export past providerShutdownTimeout. It answers in the end
	// rather than never: the span processor's export outlives a shutdown that gave up
	// on it, and an export that never returned would be a leaked goroutine.
	stall time.Duration
}

func (m *mockTraceService) Export(
	_ context.Context, req *coltracepb.ExportTraceServiceRequest,
) (*coltracepb.ExportTraceServiceResponse, error) {
	time.Sleep(m.stall)
	m.requests = append(m.requests, req)
	return &coltracepb.ExportTraceServiceResponse{}, nil
}

type mockMetricsService struct {
	colmetricpb.UnimplementedMetricsServiceServer
	requests []*colmetricpb.ExportMetricsServiceRequest
}

func (m *mockMetricsService) Export(
	_ context.Context, req *colmetricpb.ExportMetricsServiceRequest,
) (*colmetricpb.ExportMetricsServiceResponse, error) {
	m.requests = append(m.requests, req)
	return &colmetricpb.ExportMetricsServiceResponse{}, nil
}

type collector struct {
	logs     *mockLogsService
	traces   *mockTraceService
	metrics  *mockMetricsService
	endpoint string
}

// startCollector serves the three OTLP services on a loopback port for this test.
func startCollector(t *testing.T, stallTraces time.Duration) *collector {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	c := &collector{
		logs:     &mockLogsService{},
		traces:   &mockTraceService{stall: stallTraces},
		metrics:  &mockMetricsService{},
		endpoint: "http://" + lis.Addr().String(),
	}
	grpcServer := grpc.NewServer(grpc.Creds(insecure.NewCredentials()))
	collogpb.RegisterLogsServiceServer(grpcServer, c.logs)
	coltracepb.RegisterTraceServiceServer(grpcServer, c.traces)
	colmetricpb.RegisterMetricsServiceServer(grpcServer, c.metrics)
	go func() { _ = grpcServer.Serve(lis) }()
	// Graceful: a stalled export still gets its answer, so the exporter is not left
	// retrying against a closed connection after the test.
	t.Cleanup(grpcServer.GracefulStop)
	return c
}

// emitOneOfEach gives every provider something to flush at shutdown.
func emitOneOfEach(ctx context.Context) {
	var record otellog.Record
	record.SetBody(attribute.StringValue("hello from test"))
	otel.Logger("test-logger").Emit(ctx, record)

	_, span := otel.Tracer("test-tracer").Start(ctx, "test-span")
	span.End()
}

// metricNames flattens an export request down to the instrument names in it.
func metricNames(reqs []*colmetricpb.ExportMetricsServiceRequest) []string {
	var out []string
	for _, req := range reqs {
		for _, rm := range req.ResourceMetrics {
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					out = append(out, m.Name)
				}
			}
		}
	}
	return out
}

func resourceAttrs(attrs []*commonpb.KeyValue) map[string]string {
	out := map[string]string{}
	for _, kv := range attrs {
		out[kv.Key] = kv.Value.GetStringValue()
	}
	return out
}

// Setup installs process-global providers (otel.SetTracerProvider,
// otel.SetLoggerProvider), so this test cannot share the process with a parallel
// one that also reads or writes them.
//
//nolint:paralleltest // mutates process-global OTel providers
func TestOTel_Integration(t *testing.T) {
	c := startCollector(t, 0)
	ctx := t.Context()
	shutdown, err := Setup(ctx, &config.Config{OTelEndpoint: c.endpoint, Env: "development"})
	require.NoError(t, err)

	emitOneOfEach(ctx)
	require.NoError(t, shutdown(ctx))

	require.NotEmpty(t, c.logs.requests, "expected mock server to receive logs")
	req := c.logs.requests[0]
	require.NotEmpty(t, req.ResourceLogs)
	// A fixed service.name is one Prometheus job and one Loki service_name for every
	// environment; the environment is its own attribute rather than a name prefix.
	res := resourceAttrs(req.ResourceLogs[0].Resource.Attributes)
	assert.Equal(t, "tty-cards-server", res["service.name"])
	assert.Equal(t, "development", res["deployment.environment.name"])
	assert.NotContains(t, res, "service.instance.id", "a per-restart id would become a Loki index label")

	require.NotEmpty(t, c.traces.requests, "shutdown did not flush the span batch")
	spans := c.traces.requests[0].ResourceSpans
	require.NotEmpty(t, spans)
	require.NotEmpty(t, spans[0].ScopeSpans)
	assert.Equal(t, "test-span", spans[0].ScopeSpans[0].Spans[0].Name)

	require.NotEmpty(t, c.metrics.requests, "shutdown did not flush the metric reader")
	names := metricNames(c.metrics.requests)
	assert.Contains(t, names, "go.memory.used", "runtime.Start was not wired to this provider")

	assertAlertedSeriesSeeded(t)

	// Nothing in the process reads baggage; a propagator for it only widened what a
	// caller could inject.
	assert.ElementsMatch(t, []string{"traceparent", "tracestate"}, otel.GetTextMapPropagator().Fields())
}

// An alert on increase() needs a sample before the first event, and a counter has no
// series until its first Add. observability's instruments record into testReader,
// which TestMain installed first, and nothing else here records them.
func assertAlertedSeriesSeeded(t *testing.T) {
	t.Helper()
	finalize, drops := map[string]bool{}, map[string]bool{}
	panics := false
	for _, scope := range collect(t).ScopeMetrics {
		for _, m := range scope.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				switch m.Name {
				case "ttycards.match.finalize":
					outcome, _ := dp.Attributes.Value("outcome")
					ranked, _ := dp.Attributes.Value("ranked")
					finalize[outcome.AsString()+"/"+ranked.String()] = true
				case "ttycards.broadcaster.events.dropped":
					stream, _ := dp.Attributes.Value("stream")
					drops[stream.AsString()] = true
				case "ttycards.ssh.session.panics":
					panics = true
				}
			}
		}
	}
	for _, key := range []string{"dropped/false", "dropped/true", "error/false", "error/true"} {
		assert.Truef(t, finalize[key], "no ttycards.match.finalize series for %s before the first event", key)
	}
	for _, stream := range []string{"game", "lobby"} {
		assert.Truef(t, drops[stream], "no ttycards.broadcaster.events.dropped series for %s", stream)
	}
	assert.True(t, panics, "no ttycards.ssh.session.panics series before the first panic")
}

// Setup used to shut the providers down in the order it built them, logs first, under
// one deadline: a collector that stalled on one signal spent the whole window and the
// rest - the final metric flush included - were never sent.
//
//nolint:paralleltest // mutates process-global OTel providers
func TestOTel_OneStalledExporterDoesNotCostTheOthers(t *testing.T) {
	c := startCollector(t, providerShutdownTimeout+time.Second)
	ctx := t.Context()
	shutdown, err := Setup(ctx, &config.Config{OTelEndpoint: c.endpoint, Env: "development"})
	require.NoError(t, err)
	emitOneOfEach(ctx)

	shutdownCtx, cancel := context.WithTimeout(ctx, 3*providerShutdownTimeout)
	defer cancel()
	require.Error(t, shutdown(shutdownCtx), "the stalled trace export must be reported")

	assert.NotEmpty(t, c.metrics.requests, "the final metric flush was lost to the stalled trace exporter")
	assert.NotEmpty(t, c.logs.requests, "the log flush was lost to the stalled trace exporter")
}

// The SDK's default sampler reads OTEL_TRACES_SAMPLER, a knob outside config.Load
// that could switch tracing off without a trace of its own.
//
// t.Setenv already rules out t.Parallel.
func TestOTel_SamplerIgnoresTheEnvironment(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER", "always_off")
	c := startCollector(t, 0)
	ctx := t.Context()
	shutdown, err := Setup(ctx, &config.Config{OTelEndpoint: c.endpoint, Env: "development"})
	require.NoError(t, err)

	_, span := otel.Tracer("test-tracer").Start(ctx, "sampled")
	span.End()
	assert.True(t, span.SpanContext().IsSampled())
	require.NoError(t, shutdown(ctx))
}

// A zoned IPv6 URL passes url.Parse, and so config, but not gRPC's target parser:
// the one endpoint Setup itself refuses at boot.
//
//nolint:paralleltest // Setup touches process-global providers
func TestOTel_UnusableEndpointIsAFatalError(t *testing.T) {
	shutdown, err := Setup(t.Context(), &config.Config{
		OTelEndpoint: "http://[fe80::1%25en0]:4317",
		Env:          "production",
	})

	require.Error(t, err)
	assert.Nil(t, shutdown, "a failed setup must not hand back something the caller will defer")
	assert.Contains(t, err.Error(), "logger provider")
}

// Setup stops at the logger, so the tracer and meter refusals are checked on their own.
func TestOTel_ProvidersRefuseAnUndialableEndpoint(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{OTelEndpoint: "http://[fe80::1%25en0]:4317", Env: "production"}
	res := newResource(cfg)

	_, err := newTracerProvider(t.Context(), cfg, res)
	require.ErrorContains(t, err, "trace exporter")
	_, err = newMeterProvider(t.Context(), cfg, res)
	require.ErrorContains(t, err, "metric exporter")
}
