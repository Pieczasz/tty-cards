package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/config"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/httpapi"

	charmssh "charm.land/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeServer struct {
	serveErr    chan error
	shutdownErr error
	closeErr    error
	closed      bool
	closeOnce   sync.Once
}

func (f *fakeServer) Serve(net.Listener) error       { return <-f.serveErr }
func (f *fakeServer) Shutdown(context.Context) error { return f.shutdownErr }

func (f *fakeServer) Close() error {
	f.closed = true
	if f.serveErr != nil {
		f.closeOnce.Do(func() { close(f.serveErr) })
	}
	return f.closeErr
}

// errOnce is a Serve that returns the given error immediately, once.
func errOnce(err error) chan error {
	ch := make(chan error, 1)
	ch <- err
	return ch
}

func testConfig() *config.Config {
	return &config.Config{ServerHost: "127.0.0.1", ServerPort: 0, MaxConnections: 4}
}

func runServe(t *testing.T, server sshServer) error {
	t.Helper()
	errCh := make(chan error, 1)
	go func() {
		errCh <- serve(t.Context(), serveDeps{config: testConfig(), sshServer: server})
	}()

	select {
	case err := <-errCh:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("serve blocked after the accept loop ended instead of returning")
		return nil
	}
}

func TestServe_AcceptLoopFailureIsReturned(t *testing.T) {
	t.Parallel()
	boom := errors.New("listener exploded")
	err := runServe(t, &fakeServer{serveErr: errOnce(boom)})

	require.Error(t, err)
	require.ErrorIs(t, err, boom, "the cause must survive so operators can see it")
	assert.Contains(t, err.Error(), "accept loop failed")
}

// Sessions outliving the drain window is what a card game does, so it must not stop
// the shutdown: the listener and the connections have to be let go either way.
func TestStopServer_ClosesOnEveryPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		server *fakeServer
	}{
		{name: "drained in time", server: &fakeServer{}},
		{name: "drain deadline passed", server: &fakeServer{shutdownErr: context.DeadlineExceeded}},
		{name: "already closed", server: &fakeServer{shutdownErr: context.DeadlineExceeded, closeErr: net.ErrClosed}},
		{name: "close itself fails", server: &fakeServer{closeErr: errors.New("close failed")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stopServer(tt.server)

			assert.True(t, tt.server.closed, "the server was never closed")
		})
	}
}

func TestServe_UnexpectedCleanStopIsReturned(t *testing.T) {
	t.Parallel()
	err := runServe(t, &fakeServer{serveErr: errOnce(charmssh.ErrServerClosed)})

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "accept loop failed")
	assert.Contains(t, err.Error(), "unexpectedly")
}

// A stats api that cannot bind used to be a log line nobody reads and a website whose
// numbers quietly stopped moving.
func TestServe_StatsAPIFailureStopsTheServer(t *testing.T) {
	t.Parallel()
	boom := errors.New("bind: address already in use")
	apiErr := make(chan error, 1)
	apiErr <- boom

	server := &fakeServer{serveErr: make(chan error)}
	errCh := make(chan error, 1)
	go func() {
		errCh <- serve(t.Context(), serveDeps{
			config:    testConfig(),
			sshServer: server,
			apiErr:    apiErr,
		})
	}()

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, boom)
		assert.True(t, server.closed, "the ssh server was left running")
	case <-time.After(5 * time.Second):
		t.Fatal("a stats api failure never reached the error path")
	}
}

// The signal channel belongs to run, which keeps it armed for the whole shutdown
// drain; serve only reads it. Handing serve a nil channel (the other tests) must not
// change that, and a signal on it must end the accept loop cleanly.
func TestServe_SignalDrainsAndReturnsCleanly(t *testing.T) {
	t.Parallel()
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM

	server := &fakeServer{serveErr: make(chan error)}
	var shutdownCalled bool

	errCh := make(chan error, 1)
	go func() {
		errCh <- serve(t.Context(), serveDeps{
			config:     testConfig(),
			sshServer:  server,
			signals:    signals,
			onShutdown: func() { shutdownCalled = true },
		})
	}()

	select {
	case err := <-errCh:
		require.NoError(t, err, "a requested shutdown is not a failure")
		assert.True(t, shutdownCalled, "live matches were never told the server is going away")
		assert.True(t, server.closed, "the ssh server was left running")
	case <-time.After(5 * time.Second):
		t.Fatal("serve ignored the signal")
	}
}

// "%s:%d" turned SERVER_HOST=:: into ":::6969", which no listener accepts, so an
// IPv6 literal host could never bind.
func TestServe_ListensOnAnIPv6LiteralHost(t *testing.T) {
	t.Parallel()
	probe, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback here:", err)
	}
	require.NoError(t, probe.Close())

	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	cfg := &config.Config{ServerHost: "::1", MaxConnections: 4}

	err = serve(t.Context(), serveDeps{config: cfg, sshServer: &fakeServer{serveErr: make(chan error)}, signals: signals})
	require.NoError(t, err, "the listener never bound")
}

func TestServe_ListenFailureIsReturned(t *testing.T) {
	t.Parallel()
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })
	cfg := testConfig()
	cfg.ServerPort = taken.Addr().(*net.TCPAddr).Port

	err = serve(t.Context(), serveDeps{config: cfg, sshServer: &fakeServer{}})
	require.ErrorContains(t, err, "create tcp listener")
}

// A trusted-network policy that cannot be built must stop the boot: falling back to
// a listener without one would honor a PROXY header from any peer.
func TestServe_UnbuildableProxyPolicyRefusesToStart(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.ProxyProtocol = true
	cfg.ProxyTrustedCIDRs = []netip.Prefix{{}}

	err := serve(t.Context(), serveDeps{config: cfg, sshServer: &fakeServer{}})
	require.ErrorContains(t, err, "proxy trusted cidrs")
}

func TestHealthcheck(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		noServe bool
		port    string
		want    int
	}{
		{name: "healthy", status: http.StatusOK, want: 0},
		{name: "unhealthy", status: http.StatusServiceUnavailable, want: 1},
		{name: "nothing listening", noServe: true, want: 1},
		{name: "unparsable port", port: "not-a-port", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			t.Cleanup(srv.Close)

			_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
			require.NoError(t, err)
			if tt.noServe {
				srv.Close() // the port is now free, so the dial fails rather than hangs
			}
			t.Setenv("API_PORT", cmp.Or(tt.port, port))

			assert.Equal(t, tt.want, healthcheck())
		})
	}
}

// The drain is what stands between a finished ranked match and losing it: the happy
// path must not burn a window, a slow write gets exactly one more, and giving up has
// to be loud enough to alert on.
//
//nolint:paralleltest // captures the slog default
func TestWaitForFinalizers(t *testing.T) {
	tests := []struct {
		name      string
		doneAfter int
		wantCalls int
		wantLog   string
	}{
		{name: "nothing in flight", doneAfter: 1, wantCalls: 1},
		{name: "done in the second window", doneAfter: 2, wantCalls: 2, wantLog: "one more window"},
		{name: "never done", doneAfter: 3, wantCalls: 2, wantLog: `"level":"ERROR"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := captureLogs(t)
			calls := 0
			waitForFinalizers(t.Context(), func(timeout time.Duration) bool {
				calls++
				assert.Equal(t, finalizeDrainTimeout, timeout)
				return calls >= tt.doneAfter
			})

			assert.Equal(t, tt.wantCalls, calls)
			if tt.wantLog == "" {
				assert.Empty(t, logs.String())
				return
			}
			assert.Contains(t, logs.String(), tt.wantLog)
		})
	}
}

// captureLogs points the slog default at a JSON buffer for the rest of the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	return &buf
}

// The level comes from configuration and the OTLP sink has no level of its own, so
// the gate has to hold for it too - including on a logger derived with With or
// WithGroup, which is how most call sites get theirs.
//
//nolint:paralleltest // mutates the slog default
func TestInstallLogging_LevelGatesBothSinks(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	installLogging(slog.LevelWarn)
	ctx := t.Context()
	assert.False(t, slog.Default().Enabled(ctx, slog.LevelInfo))
	assert.True(t, slog.Default().Enabled(ctx, slog.LevelWarn))

	var gate slog.Handler = levelGate{Handler: slog.DiscardHandler, level: slog.LevelWarn}
	for name, h := range map[string]slog.Handler{
		"as installed": gate,
		"WithAttrs":    gate.WithAttrs([]slog.Attr{slog.String("k", "v")}),
		"WithGroup":    gate.WithGroup("g"),
	} {
		assert.Falsef(t, h.Enabled(ctx, slog.LevelInfo), "%s let an info record through", name)
		assert.Truef(t, h.Enabled(ctx, slog.LevelError), "%s dropped an error record", name)
	}
}

type onlineCount int

func (n onlineCount) Count() int { return int(n) }

type lobbyCounts struct{}

func (lobbyCounts) Stats() (int, int) { return 0, 0 }

type emptyUsers struct{ db.UserRepository }

// A miswired stats api used to serve zeros for as long as it ran; now it refuses to
// start, and run returns before anything binds.
func TestStartStatsAPI_RefusesAMissingDependency(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{ServerHost: "127.0.0.1", APIRequestsPerMinute: 1}

	_, _, err := startStatsAPI(t.Context(), cfg, nil, lobbyCounts{}, emptyUsers{}, nil)
	require.ErrorIs(t, err, httpapi.ErrMissingDeps)
}

// The stats api runs on its own goroutine, and a bind failure there used to be a log
// line nobody reads. This pins the whole small lifecycle: it binds, it answers, it stops.
func TestStartStatsAPI_ServesAndStops(t *testing.T) {
	t.Parallel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())

	cfg := &config.Config{ServerHost: "127.0.0.1", APIPort: port, APIRequestsPerMinute: 100}
	stop, serveErr, err := startStatsAPI(t.Context(), cfg, onlineCount(0), lobbyCounts{}, emptyUsers{}, func(context.Context) error { return nil })
	require.NoError(t, err)

	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	require.Eventually(t, func() bool {
		req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		if reqErr != nil {
			return false
		}
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond, "the stats api never came up")

	stop()

	select {
	case err := <-serveErr:
		t.Fatalf("a clean shutdown reported an error: %v", err)
	default:
	}
}

// Binding happens on the api's own goroutine, so a port already in use has to come
// back on the error channel that stops the server, not only as a log line.
func TestStartStatsAPI_BindFailureIsReported(t *testing.T) {
	t.Parallel()
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })

	cfg := &config.Config{ServerHost: "127.0.0.1", APIPort: taken.Addr().(*net.TCPAddr).Port, APIRequestsPerMinute: 1}
	stop, serveErr, err := startStatsAPI(t.Context(), cfg, onlineCount(0), lobbyCounts{}, emptyUsers{}, nil)
	require.NoError(t, err)
	t.Cleanup(stop)

	select {
	case err := <-serveErr:
		require.ErrorContains(t, err, "stats api stopped")
	case <-time.After(5 * time.Second):
		t.Fatal("a bind failure never reached the error channel")
	}
}
