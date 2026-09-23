package observability

import (
	"context"
	"net"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/log/global"
	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
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
}

func (m *mockTraceService) Export(
	_ context.Context, req *coltracepb.ExportTraceServiceRequest,
) (*coltracepb.ExportTraceServiceResponse, error) {
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

// Setup installs process-global providers (otel.SetTracerProvider,
// global.SetLoggerProvider), so this test cannot share the process with a parallel
// one that also reads or writes them.
//
//nolint:paralleltest // mutates process-global OTel providers
func TestOTel_Integration(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	grpcServer := grpc.NewServer(grpc.Creds(insecure.NewCredentials()))
	mockSvc := &mockLogsService{}
	mockTraces := &mockTraceService{}
	mockMetrics := &mockMetricsService{}
	collogpb.RegisterLogsServiceServer(grpcServer, mockSvc)
	coltracepb.RegisterTraceServiceServer(grpcServer, mockTraces)
	colmetricpb.RegisterMetricsServiceServer(grpcServer, mockMetrics)

	go func() {
		_ = grpcServer.Serve(lis)
	}()
	defer grpcServer.Stop()

	addr := lis.Addr().String()
	cfg := &config.Config{
		OTelEndpoint: addr,
		OTelInsecure: true,
		Env:          "development",
	}

	ctx := t.Context()
	shutdown, err := Setup(ctx, cfg)
	require.NoError(t, err)

	logger := global.Logger("test-logger")

	var record otellog.Record
	record.SetBody(attribute.StringValue("hello from test"))

	logger.Emit(ctx, record)

	_, span := otel.Tracer("test-tracer").Start(ctx, "test-span")
	span.End()

	err = shutdown(ctx)
	require.NoError(t, err)

	require.NotEmpty(t, mockSvc.requests, "expected mock server to receive logs")
	req := mockSvc.requests[0]

	require.NotEmpty(t, req.ResourceLogs)
	assert.Equal(t, "development-terminal-card-server", req.ResourceLogs[0].Resource.Attributes[0].Value.GetStringValue())

	require.NotEmpty(t, mockTraces.requests, "shutdown did not flush the span batch")
	spans := mockTraces.requests[0].ResourceSpans
	require.NotEmpty(t, spans)
	require.NotEmpty(t, spans[0].ScopeSpans)
	assert.Equal(t, "test-span", spans[0].ScopeSpans[0].Spans[0].Name)

	require.NotEmpty(t, mockMetrics.requests, "shutdown did not flush the metric reader")
	names := metricNames(mockMetrics.requests)
	assert.Contains(t, names, "go.memory.used", "runtime.Start was not wired to this provider")
}

// The one failure Setup can actually hit at boot, and the reason main prints to
// stderr: nothing has a slog handler yet when it happens.
//
//nolint:paralleltest // Setup touches process-global providers
func TestOTel_UnusableEndpointIsAFatalError(t *testing.T) {
	shutdown, err := Setup(t.Context(), &config.Config{
		OTelEndpoint: "%%%",
		OTelInsecure: true,
		Env:          "production",
	})

	require.Error(t, err)
	assert.Nil(t, shutdown, "a failed setup must not hand back something the caller will defer")
	assert.Contains(t, err.Error(), "logger provider")
}
