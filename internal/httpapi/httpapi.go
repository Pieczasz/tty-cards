// Package httpapi serves the small read-only JSON feed the marketing site reads:
// how many people are connected, how many hands are in play, and the top of the
// leaderboard.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/observability"
	"github.com/Pieczasz/tty-cards/internal/ratelimit"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/propagation"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const (
	// 200 is also the repository's bestPlayersCacheSize, so every limit this endpoint
	// accepts is one the cache can serve. Raising it past that turns each request
	// into its own database query.
	maxLeaderboardLimit = 200
	defaultLimit        = 5

	cacheSeconds = 15

	readTimeout  = 5 * time.Second
	writeTimeout = 10 * time.Second
	idleTimeout  = 60 * time.Second

	healthTimeout = 2 * time.Second
)

// SessionCounter is how many players are connected: ssh.SessionTracker in production.
type SessionCounter interface {
	Count() int
}

// LobbyCounter is how many tables are playing and waiting: lobby.Manager in production.
type LobbyCounter interface {
	Stats() (inGame, waiting int)
}

// ErrMissingDeps refuses a server built without its counters, its repository or its
// health probe. A nil one used to be skipped, so a miswiring served zeros, an empty
// leaderboard or an unconditional healthy forever instead of failing the boot.
var ErrMissingDeps = errors.New("stats api needs Sessions, Lobbies, Users and Health")

// Deps are what the stats API reads from. Sessions, Lobbies, Users and Health are required.
type Deps struct {
	Sessions SessionCounter
	Lobbies  LobbyCounter
	Users    db.Leaderboard

	AllowOrigin          string
	RequestsPerMinute    int
	TrustedProxy         bool
	TrustedProxyNetworks []netip.Prefix

	Health func(ctx context.Context) error
}

type statsResponse struct {
	PlayersOnline int `json:"players_online"`
	HandsInPlay   int `json:"hands_in_play"`
	TablesOpen    int `json:"tables_open"`
}

type errorResponse struct {
	Error string `json:"error"`
}

type healthResponse struct {
	Status string `json:"status"`
}

type leaderboardEntry struct {
	Rank     int    `json:"rank"`
	Username string `json:"username"`
	Game     string `json:"game"`
	Elo      uint32 `json:"elo"`
}

// NewServer returns the stats API's http.Server for addr, with its timeouts set. The
// caller runs and shuts it down.
func NewServer(addr string, deps Deps) (*http.Server, error) {
	h, err := newHandler(deps)
	if err != nil {
		return nil, err
	}
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}, nil
}

func newHandler(deps Deps) (http.Handler, error) {
	if deps.Sessions == nil || deps.Lobbies == nil || deps.Users == nil || deps.Health == nil {
		return nil, ErrMissingDeps
	}
	limiter := ratelimit.New(deps.RequestsPerMinute, time.Minute)
	clientAddr := clientIPFunc(deps.TrustedProxy, deps.TrustedProxyNetworks)

	mux := http.NewServeMux()
	mux.Handle("GET /v1/stats", statsHandler(deps))
	mux.Handle("GET /healthz", healthHandler(deps))
	mux.Handle("GET /v1/leaderboard", leaderboardHandler(deps))

	mux.HandleFunc("OPTIONS /", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, http.StatusNotFound, "not found")
	})

	// No spans and no propagation: a noop tracer still carries an extracted
	// traceparent into the request context, where the repository's spans would take
	// the visitor's chosen trace id as their parent.
	return otelhttp.NewHandler(
		withCORS(deps.AllowOrigin, withRateLimit(limiter, clientAddr, mux)),
		"stats-api",
		otelhttp.WithTracerProvider(tracenoop.NewTracerProvider()),
		otelhttp.WithPropagators(propagation.NewCompositeTextMapPropagator()),
		otelhttp.WithServerName("stats-api:80"),
	), nil
}

func statsHandler(deps Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inGame, waiting := deps.Lobbies.Stats()
		writeJSON(w, r, statsResponse{
			PlayersOnline: deps.Sessions.Count(),
			HandsInPlay:   inGame,
			TablesOpen:    waiting,
		})
	})
}

func leaderboardHandler(deps Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := defaultLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				writeError(w, r, http.StatusBadRequest, "limit must be a positive integer")
				return
			}
			limit = min(n, maxLeaderboardLimit)
		}

		rankings, err := deps.Users.BestPlayers(r.Context(), "", limit)
		if err != nil {
			slog.ErrorContext(r.Context(), "leaderboard query failed", "error", err)
			writeError(w, r, http.StatusServiceUnavailable, "leaderboard unavailable")
			return
		}

		out := make([]leaderboardEntry, 0, len(rankings))
		for i, entry := range rankings {
			out = append(out, leaderboardEntry{
				Rank:     i + 1,
				Username: entry.User.Username,
				Game:     entry.Game.Name,
				Elo:      entry.Elo,
			})
		}
		writeJSON(w, r, out)
	})
}

func clientIPFunc(trustProxy bool, proxies []netip.Prefix) func(*http.Request) string {
	socketHost := func(r *http.Request) string {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr
		}
		return host
	}
	if !trustProxy {
		return socketHost
	}
	return func(r *http.Request) string {
		peer := socketHost(r)
		if !fromProxy(peer, proxies) {
			return peer
		}
		first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
		first = strings.TrimSpace(first)
		if _, err := netip.ParseAddr(first); err != nil {
			return peer
		}
		return first
	}
}

func fromProxy(host string, proxies []netip.Prefix) bool {
	if len(proxies) == 0 {
		return true
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return slices.ContainsFunc(proxies, func(p netip.Prefix) bool { return p.Contains(addr) })
}

func writeJSON(w http.ResponseWriter, r *http.Request, v any) {
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", cacheSeconds))
	w.Header().Add("Vary", "Origin")
	encodeJSON(w, r, http.StatusOK, v)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Add("Vary", "Origin")
	encodeJSON(w, r, status, errorResponse{Error: msg})
}

func encodeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	if r.Method == http.MethodHead {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.WarnContext(r.Context(), "failed to encode api response", "error", err, "path", r.URL.Path)
	}
}

func withCORS(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "86400")
		next.ServeHTTP(w, r)
	})
}

func withRateLimit(
	limiter *ratelimit.SlidingWindow,
	clientAddr func(*http.Request) string,
	next http.Handler,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Probes must not share the visitor budget: a scraper or page poll would
		// otherwise 429 /healthz and look like the process is down.
		if r.URL.Path != "/healthz" && !limiter.Allow(ratelimit.NetKey(clientAddr(r))) {
			observability.RateLimitReject(r.Context(), "http")
			w.Header().Set("Retry-After", "60")
			writeError(w, r, http.StatusTooManyRequests, "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func healthHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()
		if err := deps.Health(ctx); err != nil {
			slog.ErrorContext(ctx, "health check failed", "error", err)
			writeError(w, r, http.StatusServiceUnavailable, "unhealthy")
			return
		}
		// Not writeJSON: a cached health answer is a lie about a later moment.
		w.Header().Set("Cache-Control", "no-store")
		encodeJSON(w, r, http.StatusOK, healthResponse{Status: "ok"})
	}
}
