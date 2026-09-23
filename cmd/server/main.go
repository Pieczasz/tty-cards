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

	"github.com/Pieczasz/terminal-card/internal/catalog"
	"github.com/Pieczasz/terminal-card/internal/config"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/httpapi"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/observability"
	"github.com/Pieczasz/terminal-card/internal/repository"
	"github.com/Pieczasz/terminal-card/internal/ssh"

	charmssh "charm.land/ssh"
	"github.com/pires/go-proxyproto"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"golang.org/x/net/netutil"
)

const (
	sshDrainTimeout      = 30 * time.Second
	apiDrainTimeout      = 5 * time.Second
	finalizeDrainTimeout = 15 * time.Second
	otelDrainTimeout     = 5 * time.Second
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		os.Exit(1)
	}
}

// fatal reports a failure from before OTel is up. slog's stderr copy of a record is
// dropped by the log pipeline as a duplicate of its OTLP copy, and there is no OTLP
// copy yet, so a plain line is the only one that reaches Loki.
func fatal(err error) error {
	fmt.Fprintln(os.Stderr, "fatal:", err)
	return err
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

func run() (err error) {
	logLevel := installLogging()

	// Installed here, not in serve, and released only when run returns. Everything
	// below this line is deferred shutdown work - draining sessions, finalizing
	// matches, closing the database, flushing telemetry - and it can take the better
	// part of a minute. Releasing the handler when serve returns restored the default
	// disposition mid-drain, so a second Ctrl-C during a redeploy killed the process
	// while a ranked match was still being written.
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(done)

	cfg, err := config.Load()
	if err != nil {
		return fatal(fmt.Errorf("load configuration: %w", err))
	}
	logLevel.Set(cfg.LogLevel)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// This defer chain is LIFO and load-bearing; do not reorder it.
	otelCleanup, err := setupOTel(ctx, cfg)
	if err != nil {
		return fatal(err)
	}
	defer otelCleanup()
	// Every later failure is reported here, once, and before the flush above.
	defer func() {
		if err != nil {
			slog.ErrorContext(ctx, "server exited with error", "error", err)
		}
	}()

	database, err := db.Connect(cfg)
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
	if err := observability.RegisterDBStats(sqlDB); err != nil {
		slog.ErrorContext(ctx, "failed to register database pool metrics", "error", err)
	}

	userRepo := repository.NewUserRepository(database)
	matchRepo := repository.NewMatchRepository(database)
	lobbyManager := lobby.NewManager(ctx, matchRepo)

	defer waitForFinalizers(lobbyManager)

	// MaxConnections is the player-visible session cap: the tracker refuses the
	// overflow with a message, while the TCP LimitListener in serve only backstops
	// handshake floods at twice that, so a full server says so instead of hanging.
	// The stats api shares it to count who is online.
	tracker := ssh.NewSessionTracker(cfg.MaxConnections)
	server, err := newSSHServer(cfg, userRepo, lobbyManager, tracker)
	if err != nil {
		return err
	}
	if err := observability.RegisterSessionGauge(tracker.Count); err != nil {
		slog.ErrorContext(ctx, "failed to register the session gauge", "error", err)
	}

	stopAPI, apiErr, err := startStatsAPI(cfg, tracker, lobbyManager, userRepo, sqlDB.PingContext)
	if err != nil {
		return err
	}
	defer stopAPI()

	return serve(ctx, serveDeps{
		config:     cfg,
		sshServer:  server,
		apiErr:     apiErr,
		signals:    done,
		onShutdown: lobbyManager.BeginShutdown,
	})
}

func installLogging() *slog.LevelVar {
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	slog.SetDefault(slog.New(slog.NewMultiHandler(
		slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}),
		levelGate{Handler: otelslog.NewHandler("terminal-card"), level: level},
	)))
	return level
}

type levelGate struct {
	slog.Handler
	level slog.Leveler
}

func (g levelGate) Enabled(_ context.Context, l slog.Level) bool { return l >= g.level.Level() }

func (g levelGate) WithAttrs(attrs []slog.Attr) slog.Handler {
	return levelGate{Handler: g.Handler.WithAttrs(attrs), level: g.level}
}

func (g levelGate) WithGroup(name string) slog.Handler {
	return levelGate{Handler: g.Handler.WithGroup(name), level: g.level}
}

func setupOTel(ctx context.Context, cfg *config.Config) (func(), error) {
	shutdown, err := observability.Setup(ctx, cfg)
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

// waitForFinalizers is the last thing between a finished ranked match and losing it:
// the finalizers write asynchronously, so shutdown has to outlive them.
//
// Two bounded windows rather than a durable queue, deliberately. The alternative is an
// outbox table written at game end and drained on boot, which buys nothing here that
// the grace period does not: the write it protects is a single short transaction
// against a database in the same compose project. What it costs is a schema, a drain
// path, and a second way for a match to be recorded.
//
// Two things make that trade honest, and both must hold:
//   - compose's stop_grace_period must exceed the whole sequential drain (30s ssh +
//     5s api + 2x15s here + 5s otel = 70s), or the runtime SIGKILLs us mid-write and
//     the windows below never get to expire. It is set to 80s.
//   - the give-up below is not silent: it logs at ERROR and the finalizer path counts
//     observability.MatchFinalize(outcome="dropped"), which is alertable.
//
// So the residual risk is stated rather than removed: a match that finishes in the
// last seconds of a deploy, whose write then blocks for 30s, is lost from history.
func waitForFinalizers(lobbyManager *lobby.Manager) {
	if lobbyManager.WaitForFinalizers(finalizeDrainTimeout) {
		return
	}
	slog.Warn("match finalizers exceeded their deadline; giving them one more window",
		"timeout", finalizeDrainTimeout)
	if !lobbyManager.WaitForFinalizers(finalizeDrainTimeout) {
		slog.Error("abandoning match finalizers; a finished match may be missing from history",
			"timeout", finalizeDrainTimeout)
	}
}

func newSSHServer(
	cfg *config.Config,
	userRepo db.UserRepository,
	lobbyManager *lobby.Manager,
	tracker *ssh.SessionTracker,
) (*charmssh.Server, error) {
	server, err := ssh.NewServer(ssh.Deps{
		Config:         cfg,
		UserRepository: userRepo,
		LobbyManager:   lobbyManager,
		GameRegistry:   catalog.NewRegistry(),
		Tracker:        tracker,
	})
	if err != nil {
		return nil, fmt.Errorf("setup ssh server: %w", err)
	}
	return server, nil
}

// startStatsAPI takes interfaces, not the concrete tracker and manager: a nil
// *SessionTracker in an interface is not nil, and would pass NewServer's check.
func startStatsAPI(
	cfg *config.Config,
	sessions httpapi.SessionCounter,
	lobbies httpapi.LobbyCounter,
	users db.UserRepository,
	health func(ctx context.Context) error,
) (func(), <-chan error, error) {
	addr := net.JoinHostPort(cfg.ServerHost, strconv.Itoa(cfg.APIPort))
	srv, err := httpapi.NewServer(addr, httpapi.Deps{
		Sessions:          sessions,
		Lobbies:           lobbies,
		Users:             users,
		AllowOrigin:       cfg.APIAllowOrigin,
		RequestsPerMinute: cfg.APIRequestsPerMinute,
		TrustedProxy:      cfg.APITrustProxy,
		// Same networks the PROXY header is trusted from: in compose, exactly nginx's.
		TrustedProxyNetworks: cfg.ProxyTrustedCIDRs,
		Health:               health,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("setup stats api: %w", err)
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("starting stats api", "address", addr)
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
}

type serveDeps struct {
	config    *config.Config
	sshServer sshServer
	apiErr    <-chan error
	// signals is owned by run, which keeps it armed across the whole shutdown drain.
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
	defer func() {
		if err := acceptListener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			slog.WarnContext(ctx, "failed to close listener", "error", err)
		}
	}()

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

	// Every way out drains, and a failure's teardown ends live matches the same way a
	// deploy does, so they must not be rated either.
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
// 6969 must never be published; with it, a connection from anywhere else is dropped
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

// stopServer drains in-flight sessions, then closes whatever is left.
//
// A table still playing when the deploy lands is the normal case, not a failed
// shutdown: treating the drain deadline as an error reported a healthy redeploy as a
// failure and, worse, returned without ever closing the server, so every session
// stayed open until the runtime killed the process.
func stopServer(server sshServer) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), sshDrainTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		slog.WarnContext(shutdownCtx, "sessions outlasted the drain window; closing them",
			"error", err, "timeout", sshDrainTimeout)
	}
	// Shutdown waits, Close is what lets go, so it runs on both paths.
	if err := server.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		slog.WarnContext(shutdownCtx, "failed to close ssh server", "error", err)
	}
}
