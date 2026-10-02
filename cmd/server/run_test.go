package main

import (
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// serverEnv points config.Load at a local plaintext server on free ports, with no
// PROXY header expected and the OTLP exporters aimed at collector.
func serverEnv(t *testing.T, collector string) {
	t.Helper()
	for key, value := range map[string]string{
		"ENV":                         "development",
		"SERVER_HOST":                 "127.0.0.1",
		"SERVER_PORT":                 freePort(t),
		"API_PORT":                    freePort(t),
		"PROXY_PROTOCOL":              "false",
		"SSH_KEY_PATH":                filepath.Join(t.TempDir(), "host_key"),
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://" + collector,
		"LOG_LEVEL":                   "ERROR",
	} {
		t.Setenv(key, value)
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return strconv.Itoa(port)
}

// fakeCollector is an OTLP endpoint that answers every export with Unimplemented,
// which the exporters do not retry, so a shutdown flush ends at once instead of
// backing off against a closed port for the whole drain window.
func fakeCollector(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Stop)
	return listener.Addr().String()
}

// Every way run can fail before it serves has to come back as an error: that is
// what main turns into exit status 1.
func TestRun_BootFailuresAreReturned(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{name: "invalid configuration", env: map[string]string{"ENV": "wat"}, wantErr: "load config"},
		{name: "otel endpoint without a scheme", env: map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "alloy:4317"}, wantErr: "load config"},
		{
			name:    "otel endpoint grpc cannot dial",
			env:     map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://[fe80::1%25en0]:4317"},
			wantErr: "setup otel",
		},
		{
			name:    "database unreachable",
			env:     map[string]string{"DB_HOST": "127.0.0.1", "DB_PORT": freePort(t)},
			wantErr: "setup database",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serverEnv(t, fakeCollector(t))
			for key, value := range tt.env {
				t.Setenv(key, value)
			}
			captureLogs(t)

			require.ErrorContains(t, run(), tt.wantErr)
		})
	}
}

// A server that dies has to say why while the log pipeline can still carry it.
//
//nolint:paralleltest // captures the slog default
func TestLogExit(t *testing.T) {
	logs := captureLogs(t)
	require.NoError(t, logExit(t.Context(), nil))
	assert.Empty(t, logs.String(), "a clean stop is not an error")

	boom := errors.New("boom")
	require.ErrorIs(t, logExit(t.Context(), boom), boom)
	assert.Contains(t, logs.String(), `"msg":"server exited with error","error":"boom"`)
}
