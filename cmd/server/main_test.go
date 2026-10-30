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

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/config"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/httpapi"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	charmssh "charm.land/ssh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeServer struct {
	serveErr    chan error
	shutdownErr error
	closeErr    error
	waitErr     error
	endErr      error
	closed      bool
	closeOnce   sync.Once
	// stops is the order the drain called Shutdown, EndSessions, Close and
	// WaitSessions in.
	stops []string
	// duringShutdown runs inside Shutdown: what a live session does in the window.
	duringShutdown func()
}

func (f *fakeServer) Serve(net.Listener) error { return <-f.serveErr }

func (f *fakeServer) Shutdown(context.Context) error {
	f.stops = append(f.stops, "shutdown")
	if f.duringShutdown != nil {
		f.duringShutdown()
	}
	return f.shutdownErr
}

func (f *fakeServer) Close() error {
	f.stops = append(f.stops, "close")
	f.closed = true
	if f.serveErr != nil {
		f.closeOnce.Do(func() { close(f.serveErr) })
	}
	return f.closeErr
}

// EndSessions records an unbounded call apart: a wedged session would hold it, and
// with it the whole drain, forever.
func (f *fakeServer) EndSessions(ctx context.Context) error {
	if _, ok := ctx.Deadline(); !ok {
		f.stops = append(f.stops, "end sessions without a deadline")
		return f.endErr
	}
	f.stops = append(f.stops, "end sessions")
	return f.endErr
}

func (f *fakeServer) WaitSessions(context.Context) error {
	f.stops = append(f.stops, "wait sessions")
	return f.waitErr
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
// the shutdown: the listener and the connections have to be let go either way. The
// sessions left are ended before Close, while their connections can still carry
// the restart notice and the terminal restore, and a wedged one is not waited on. Close
// does not wait for the session handlers it ends, so the wait for their teardown -
// which the finalizer drain after this depends on - comes after it, every time. New
// games stop before the graceful window, but the lobby hears of the shutdown only
// after it: a ranked match that ends by play inside it is still rated, and only what
// Close tears down is not.
func TestDrainServer_ClosesOnEveryPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		server *fakeServer
	}{
		{name: "drained in time", server: &fakeServer{}},
		{name: "drain deadline passed", server: &fakeServer{shutdownErr: context.DeadlineExceeded}},
		{name: "already closed", server: &fakeServer{shutdownErr: context.DeadlineExceeded, closeErr: net.ErrClosed}},
		{name: "close itself fails", server: &fakeServer{closeErr: errors.New("close failed")}},
		{name: "teardowns outlast their window", server: &fakeServer{waitErr: context.DeadlineExceeded}},
		{name: "a session will not quit", server: &fakeServer{endErr: context.DeadlineExceeded}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			drainServer(
				func() { tt.server.stops = append(tt.server.stops, "stop new games") },
				func() { tt.server.stops = append(tt.server.stops, "lobby shutdown") },
				tt.server,
			)

			assert.True(t, tt.server.closed, "the server was never closed")
			assert.Equal(t, []string{
				"stop new games", "shutdown", "lobby shutdown", "end sessions", "close", "wait sessions",
			}, tt.server.stops)
		})
	}
}

// The drain window lets tables already in play finish, but a table that readies up
// inside it would only be torn down by Close and recorded unrated, so no new game
// may start once the drain has begun.
func TestDrainServer_NoGameStartsInTheWindow(t *testing.T) {
	t.Parallel()
	manager := lobby.NewManager(t.Context(), nil, catalog.NewRegistry())
	leader := &game.Player{ID: "leader", UserID: testutil.UID(1)}
	guest := &game.Player{ID: "guest", UserID: testutil.UID(2)}
	table, err := manager.CreateLobby(leader, lobby.WithCardGame("gin_rummy"), lobby.WithMaxPlayers(2))
	require.NoError(t, err)
	t.Cleanup(func() { manager.RemoveLobby(table.Code()) })
	_, err = manager.JoinLobbyByCode(t.Context(), table.Code(), guest)
	require.NoError(t, err)
	require.NoError(t, manager.ToggleReady(leader))

	server := &fakeServer{duringShutdown: func() {
		_ = manager.ToggleReady(guest)
		assert.Nil(t, table.ActiveGame(), "a game started inside the drain window")
	}}
	drainServer(manager.StopNewGames, manager.BeginShutdown, server)
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
	var stopCalled, shutdownCalled bool

	errCh := make(chan error, 1)
	go func() {
		errCh <- serve(t.Context(), serveDeps{
			config:       testConfig(),
			sshServer:    server,
			signals:      signals,
			stopNewGames: func() { stopCalled = true },
			onShutdown:   func() { shutdownCalled = true },
		})
	}()

	select {
	case err := <-errCh:
		require.NoError(t, err, "a requested shutdown is not a failure")
		assert.True(t, stopCalled, "new games were never stopped")
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

// A port already in use must refuse the boot before the ssh listener opens, not come
// back later on the error channel after players could already connect.
func TestStartStatsAPI_BindFailureIsReturned(t *testing.T) {
	t.Parallel()
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = taken.Close() })

	cfg := &config.Config{ServerHost: "127.0.0.1", APIPort: taken.Addr().(*net.TCPAddr).Port, APIRequestsPerMinute: 1}
	stop, _, err := startStatsAPI(t.Context(), cfg, onlineCount(0), lobbyCounts{}, emptyUsers{}, func(context.Context) error { return nil })
	if stop != nil {
		t.Cleanup(stop)
	}
	require.ErrorContains(t, err, "stats api listen")
}

// TestDrainBudgetFitsStopGracePeriod pins the drain arithmetic: the windows run one
// after another, so raising any one of them, or adding one, must still leave room
// under compose's SIGKILL for the db close and the last log writes.
func TestDrainBudgetFitsStopGracePeriod(t *testing.T) {
	t.Parallel()
	// stopGracePeriod mirrors the backend's stop_grace_period in compose.yaml.
	const stopGracePeriod = 80 * time.Second
	const headroom = 3 * time.Second
	drain := sshDrainTimeout + sessionDrainTimeout + apiDrainTimeout + 2*finalizeDrainTimeout + otelDrainTimeout
	assert.LessOrEqual(t, drain+headroom, stopGracePeriod)
}
