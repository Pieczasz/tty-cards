// Command server runs tty-cards: the ssh game server on SERVER_PORT and the
// read-only stats API on API_PORT, sharing one database, lobby manager and session
// tracker.
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/config"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/httpapi"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/observability"
	"github.com/Pieczasz/tty-cards/internal/observability/otelsetup"
	"github.com/Pieczasz/tty-cards/internal/repository"
	"github.com/Pieczasz/tty-cards/internal/ssh"

	charmssh "charm.land/ssh"
	"github.com/pires/go-proxyproto"
	"golang.org/x/net/netutil"
)

// compose's stop_grace_period (80s) has to outlast the whole sequential drain:
// ssh, then the session teardowns its Close sets off, then the stats api, then two
// finalizer windows, then otel, whose three providers get 2s each
// (otelsetup.Setup): 30 + 5 + 5 + 2x15 + 6 = 76s. Two drain windows, no outbox.
// a match write gets windows rather than an outbox.
const (
	sshDrainTimeout = 30 * time.Second
	// sessionDrainTimeout bounds the wait for session handlers after Close. Their
	// teardown is in-memory work, but it is what ends the tables still in play, and
	// the finalizer drain that follows waits for each of those to be written.
	sessionDrainTimeout  = 5 * time.Second
	apiDrainTimeout      = 5 * time.Second
	finalizeDrainTimeout = 15 * time.Second
	otelDrainTimeout     = 6 * time.Second
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck())
	}
	if run() != nil {
		os.Exit(1)
	}
}

func healthcheck() int {
	port := cmp.Or(os.Getenv("API_PORT"), strconv.Itoa(config.DefaultAPIPort))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	target := "http://127.0.0.1:" + port + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}

// run keeps the signals registered until the drain is over, so a second SIGTERM
// cannot kill a shutdown in progress.
func run() error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		return logExit(ctx, fmt.Errorf("load config: %w", err))
	}
	installLogging(cfg.LogLevel)

	stopOTel, err := setupOTel(ctx, cfg)
	if err != nil {
		return logExit(ctx, err)
	}
	defer stopOTel()

	return logExit(ctx, runServers(ctx, cfg, signals))
}

// logExit records why the server stopped while the log pipeline is still up to
// carry it. Before installLogging that is the slog default, plain stderr.
func logExit(ctx context.Context, err error) error {
	if err != nil {
		slog.ErrorContext(ctx, "server exited with error", "error", err)
	}
	return err
}

func runServers(ctx context.Context, cfg *config.Config, signals <-chan os.Signal) error {
	database, err := repository.Connect(cfg.DSN(), repository.Pool{
		MaxOpenConns: cfg.DBMaxOpenConns,
		Verbose:      cfg.IsDevelopment(),
	})
	if err != nil {
		return fmt.Errorf("setup database: %w", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		return fmt.Errorf("get sql.DB: %w", err)
	}
	defer func() {
		if err := sqlDB.Close(); err != nil {
			slog.ErrorContext(ctx, "failed to close database", "error", err)
		}
	}()
	observability.RegisterDBStats(sqlDB)

	userRepo := repository.NewUserRepository(database)
	// One catalog for both: the registry the manager checks a new table against, and
	// the entries the menus offer and the routes render.
	lobbyManager := lobby.NewManager(ctx, repository.NewMatchRepository(database), catalog.NewRegistry())
	defer waitForFinalizers(ctx, lobbyManager.WaitForFinalizers)

	tracker := ssh.NewSessionTracker(cfg.MaxConnections)
	server, err := newSSHServer(cfg, userRepo, lobbyManager, catalog.Games, tracker)
	if err != nil {
		return err
	}
	observability.RegisterSessionGauge(tracker.Count)

	stopAPI, apiErr, err := startStatsAPI(ctx, cfg, tracker, lobbyManager, userRepo, sqlDB.PingContext)
	if err != nil {
		return err
	}
	defer stopAPI()

	return serve(ctx, serveDeps{
		config:     cfg,
		sshServer:  server,
		apiErr:     apiErr,
		signals:    signals,
		onShutdown: lobbyManager.BeginShutdown,
	})
}

func setupOTel(ctx context.Context, cfg *config.Config) (func(), error) {
	shutdown, err := otelsetup.Setup(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("setup otel: %w", err)
	}
	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), otelDrainTimeout)
		defer cancel()
		if err := shutdown(shutdownCtx); err != nil {
			slog.ErrorContext(shutdownCtx, "failed to shutdown OpenTelemetry", "error", err)
		}
	}, nil
}

// waitForFinalizers keeps the process alive while finished ranked matches are still
// being written, and says so at ERROR, which is alertable, when it gives up on them.
func waitForFinalizers(ctx context.Context, finalizersDone func(timeout time.Duration) bool) {
	if finalizersDone(finalizeDrainTimeout) {
		return
	}
	slog.WarnContext(ctx, "match finalizers exceeded their deadline; giving them one more window",
		"timeout", finalizeDrainTimeout)
	if !finalizersDone(finalizeDrainTimeout) {
		slog.ErrorContext(ctx, "abandoning match finalizers; a finished match may be missing from history",
			"timeout", finalizeDrainTimeout)
	}
}

func newSSHServer(
	cfg *config.Config,
	userRepo db.UserRepository,
	lobbyManager *lobby.Manager,
	games []catalog.Entry,
	tracker *ssh.SessionTracker,
) (*ssh.Server, error) {
	server, err := ssh.NewServer(ssh.Deps{
		Config:       cfg,
		Auth:         userRepo,
		Profiles:     userRepo,
		Leaderboard:  userRepo,
		LobbyManager: lobbyManager,
		Games:        games,
		Tracker:      tracker,
	})
	if err != nil {
		return nil, fmt.Errorf("setup ssh server: %w", err)
	}
	return server, nil
}

func startStatsAPI(
	ctx context.Context,
	cfg *config.Config,
	sessions httpapi.SessionCounter,
	lobbies httpapi.LobbyCounter,
	users db.Leaderboard,
	health func(ctx context.Context) error,
) (func(), <-chan error, error) {
	addr := net.JoinHostPort(cfg.ServerHost, strconv.Itoa(cfg.APIPort))
	srv, err := httpapi.NewServer(addr, httpapi.Deps{
		Sessions:             sessions,
		Lobbies:              lobbies,
		Users:                users,
		AllowOrigin:          cfg.APIAllowOrigin,
		RequestsPerMinute:    cfg.APIRequestsPerMinute,
		TrustedProxy:         cfg.APITrustProxy,
		TrustedProxyNetworks: cfg.ProxyTrustedCIDRs,
		Health:               health,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("setup stats api: %w", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.InfoContext(ctx, "starting stats api", "address", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- fmt.Errorf("stats api stopped: %w", err)
		}
	}()

	return func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), apiDrainTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.WarnContext(shutdownCtx, "stats api shutdown was not clean", "error", err)
		}
	}, serveErr, nil
}

type sshServer interface {
	Serve(net.Listener) error
	Shutdown(context.Context) error
	Close() error
	WaitSessions(context.Context) error
}

type serveDeps struct {
	config     *config.Config
	sshServer  sshServer
	apiErr     <-chan error
	signals    <-chan os.Signal
	onShutdown func()
}

func serve(ctx context.Context, d serveDeps) error {
	cfg, server := d.config, d.sshServer
	addr := net.JoinHostPort(cfg.ServerHost, strconv.Itoa(cfg.ServerPort))
	lc := net.ListenConfig{}
	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("create tcp listener: %w", err)
	}
	acceptListener, err := proxyListener(netutil.LimitListener(listener, 2*cfg.MaxConnections), cfg)
	if err != nil {
		_ = listener.Close()
		return err
	}
	// Shutdown already closed it on the normal path; this covers an early return.
	defer func() { _ = acceptListener.Close() }()

	slog.InfoContext(ctx, "starting ssh server",
		"address", addr,
		"max_connections", cfg.MaxConnections,
		"version", cfg.ServiceVersion,
	)
	serveErr := make(chan error, 1)
	go func() {
		err := server.Serve(acceptListener)
		if errors.Is(err, charmssh.ErrServerClosed) {
			// Expected: our own Shutdown below unblocks Serve this way.
			err = nil
		}
		serveErr <- err
	}()

	defer drainServer(d.onShutdown, server)
	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("ssh accept loop failed: %w", err)
		}
		return errors.New("ssh server stopped accepting connections unexpectedly")
	case err := <-d.apiErr:
		return err
	case <-d.signals:
		return nil
	}
}

// proxyListener puts the PROXY protocol in front of the ssh listener. proxyproto
// defaults to REQUIRE: every connection must open with a PROXY header, which is right
// behind nginx. Without PROXY_TRUSTED_CIDRS any peer's header is honored, which is why
// 6969 must never be published; config refuses that outside development, so the empty
// branch runs only there. With the list, a connection from anywhere else is dropped
// before its header is read. PROXY_PROTOCOL=false is the local escape hatch for bare
// ssh.
func proxyListener(inner net.Listener, cfg *config.Config) (net.Listener, error) {
	if !cfg.ProxyProtocol {
		return inner, nil
	}
	listener := &proxyproto.Listener{Listener: inner, ReadHeaderTimeout: 10 * time.Second}
	if len(cfg.ProxyTrustedCIDRs) > 0 {
		ranges := make([]string, len(cfg.ProxyTrustedCIDRs))
		for i, prefix := range cfg.ProxyTrustedCIDRs {
			ranges[i] = prefix.String()
		}
		policy, err := proxyproto.TrustProxyHeaderFromRanges(ranges)
		if err != nil {
			return nil, fmt.Errorf("proxy trusted cidrs: %w", err)
		}
		listener.ConnPolicy = policy
	}
	return listener, nil
}

func drainServer(onShutdown func(), server sshServer) {
	if onShutdown != nil {
		onShutdown()
	}
	stopServer(server)
}

func stopServer(server sshServer) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), sshDrainTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.WarnContext(shutdownCtx, "sessions outlasted the drain window; closing them",
			"error", err, "timeout", sshDrainTimeout)
	}
	if err := server.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.WarnContext(shutdownCtx, "failed to close ssh server", "error", err)
	}

	// Close returns before the session handlers it ends have given up their seats;
	// the finalizer drain the caller runs next must not start until they have.
	waitCtx, cancelWait := context.WithTimeout(context.Background(), sessionDrainTimeout)
	defer cancelWait()
	if err := server.WaitSessions(waitCtx); err != nil {
		slog.WarnContext(waitCtx, "session teardowns outlasted their window; the finalizer drain may wait on their tables",
			"error", err, "timeout", sessionDrainTimeout)
	}
}
