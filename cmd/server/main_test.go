package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/config"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/httpapi"
	"github.com/Pieczasz/terminal-card/internal/lobby"

	charmssh "charm.land/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeServer struct {
	// serveErr blocks until it is written to, which is what a real accept loop does;
	// a nil channel blocks forever, so a test that never writes one keeps Serve parked.
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
	// A real Close unblocks the accept loop. Leaving Serve parked is a goroutine
	// leak in the fake, not in the code under test, and goleak is right to say so.
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
		{name: "close itself fails", server: &fakeServer{shutdownErr: context.DeadlineExceeded, closeErr: net.ErrClosed}},
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

func TestHealthcheck(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		noServe bool
		want    int
	}{
		{name: "healthy", status: http.StatusOK, want: 0},
		{name: "unhealthy", status: http.StatusServiceUnavailable, want: 1},
		{name: "nothing listening", noServe: true, want: 1},
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
			t.Setenv("API_PORT", port)

			assert.Equal(t, tt.want, healthcheck())
		})
	}
}

// The drain is what stands between a finished ranked match and losing it, so the
// happy path has to actually return rather than burn both windows on every shutdown.
func TestWaitForFinalizers_ReturnsWhenThereIsNothingToWaitFor(t *testing.T) {
	t.Parallel()
	manager := lobby.NewManager(t.Context(), nil)

	start := time.Now()
	waitForFinalizers(t.Context(), manager)

	assert.Less(t, time.Since(start), finalizeDrainTimeout,
		"an idle manager must not spend a drain window")
}

// installLogging replaces the process default, so it runs alone.
//
//nolint:paralleltest // mutates the slog default
func TestInstallLogging_LevelIsLiveAndGatesBothSinks(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	level := installLogging()
	require.NotNil(t, level)

	assert.False(t, slog.Default().Enabled(t.Context(), slog.LevelDebug),
		"debug must be off until configuration says otherwise")

	// config.Load is read after the handler is installed, so the level has to be
	// changeable afterwards or LOG_LEVEL=DEBUG would never take effect.
	level.Set(slog.LevelDebug)
	assert.True(t, slog.Default().Enabled(t.Context(), slog.LevelDebug))
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
