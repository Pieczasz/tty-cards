package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeSessions int

func (f fakeSessions) Count() int { return int(f) }

type fakeLobbies struct{ inGame, waiting int }

func (f fakeLobbies) Stats() (int, int) { return f.inGame, f.waiting }

type stubUsers struct {
	db.UserRepository
	rankings []db.Ranking
	err      error
	gotLimit int
	gotGame  string
}

func (s *stubUsers) BestPlayers(_ context.Context, limit int, gameName string) ([]db.Ranking, error) {
	s.gotLimit = limit
	s.gotGame = gameName
	return s.rankings, s.err
}

func ranking(name, game string, elo uint32) db.Ranking {
	return db.Ranking{
		Elo:  elo,
		User: db.User{Username: name},
		Game: db.Game{Name: game},
	}
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.RemoteAddr = "203.0.113.7:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestStats_ReportsLiveCounts(t *testing.T) {
	t.Parallel()
	h := Handler(Deps{
		Sessions:          fakeSessions(4),
		Lobbies:           fakeLobbies{inGame: 2, waiting: 3},
		RequestsPerMinute: 120,
		AllowOrigin:       "*",
	})

	rec := get(t, h, "/v1/stats")

	require.Equal(t, http.StatusOK, rec.Code)
	var got statsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, statsResponse{PlayersOnline: 4, HandsInPlay: 2, TablesOpen: 3}, got)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestStats_NilDepsDoNotPanic(t *testing.T) {
	t.Parallel()
	rec := get(t, Handler(Deps{RequestsPerMinute: 120, AllowOrigin: "*"}), "/v1/stats")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"players_online":0,"hands_in_play":0,"tables_open":0}`, rec.Body.String())
}

func TestLeaderboard_ShapesRanks(t *testing.T) {
	t.Parallel()
	users := &stubUsers{rankings: []db.Ranking{
		ranking("alice", "Poker", 1800),
		ranking("bob", "Poker", 1700),
	}}

	rec := get(t, Handler(Deps{Users: users, RequestsPerMinute: 120, AllowOrigin: "*"}), "/v1/leaderboard")

	require.Equal(t, http.StatusOK, rec.Code)
	var got []leaderboardEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 2)
	assert.Equal(t, leaderboardEntry{Rank: 1, Username: "alice", Game: "Poker", Elo: 1800}, got[0])
	assert.Equal(t, 2, got[1].Rank, "rank is the position, not anything from the row")
}

func TestLeaderboard_LimitIsClampedAndValidated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		wantCode  int
		wantLimit int
	}{
		{name: "absent uses the default", query: "", wantCode: http.StatusOK, wantLimit: defaultLimit},
		{name: "in range passes through", query: "?limit=10", wantCode: http.StatusOK, wantLimit: 10},
		{name: "over the cap is clamped", query: "?limit=1000", wantCode: http.StatusOK, wantLimit: maxLeaderboardLimit},
		{name: "zero is refused", query: "?limit=0", wantCode: http.StatusBadRequest},
		{name: "negative is refused", query: "?limit=-3", wantCode: http.StatusBadRequest},
		{name: "nonsense is refused", query: "?limit=all", wantCode: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			users := &stubUsers{}
			rec := get(t, Handler(Deps{Users: users, RequestsPerMinute: 120, AllowOrigin: "*"}), "/v1/leaderboard"+tt.query)

			require.Equal(t, tt.wantCode, rec.Code)
			if tt.wantCode == http.StatusOK {
				assert.Equal(t, tt.wantLimit, users.gotLimit)
			}
		})
	}
}

func TestLeaderboard_RepositoryErrorIsOpaque(t *testing.T) {
	t.Parallel()
	users := &stubUsers{err: errors.New("pq: relation \"rankings\" does not exist")}

	rec := get(t, Handler(Deps{Users: users, RequestsPerMinute: 120, AllowOrigin: "*"}), "/v1/leaderboard")

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.NotContains(t, rec.Body.String(), "relation")
	assert.Contains(t, rec.Body.String(), "unavailable")
}

// The board is always the whole board. A caller-supplied game name reached the
// repository unvalidated, and anything that is not a real game name misses the cache
// and costs a join - a free database query per request, from any visitor.
func TestLeaderboard_GameParamIsIgnored(t *testing.T) {
	t.Parallel()

	for _, query := range []string{"", "?game=Uno", "?game=" + strings.Repeat("x", 64), "?game=%27%20OR%201%3D1"} {
		t.Run("query="+query, func(t *testing.T) {
			t.Parallel()
			users := &stubUsers{}
			rec := get(t, Handler(Deps{Users: users, RequestsPerMinute: 120, AllowOrigin: "*"}), "/v1/leaderboard"+query)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Empty(t, users.gotGame, "the repository must never be handed a caller-supplied game name")
		})
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	t.Parallel()
	assert.Equal(t, http.StatusNotFound, get(t, Handler(Deps{RequestsPerMinute: 120, AllowOrigin: "*"}), "/v1/secrets").Code)
	assert.Equal(t, http.StatusNotFound, get(t, Handler(Deps{RequestsPerMinute: 120, AllowOrigin: "*"}), "/").Code)
}

func TestWriteMethodsAreRejected(t *testing.T) {
	t.Parallel()
	h := Handler(Deps{Sessions: fakeSessions(1), RequestsPerMinute: 120, AllowOrigin: "*"})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(method, "/v1/stats", nil)
			req.RemoteAddr = "203.0.113.9:1111"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.NotEqual(t, http.StatusOK, rec.Code, "%s must not be served", method)
		})
	}
}

func options(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodOptions, "/v1/stats", nil)
	req.RemoteAddr = "203.0.113.11:2222"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPreflightIsAnswered(t *testing.T) {
	t.Parallel()
	rec := options(t, Handler(Deps{RequestsPerMinute: 120, AllowOrigin: "*"}))

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), "GET")
}

func TestPreflightIsRateLimited(t *testing.T) {
	t.Parallel()
	h := Handler(Deps{RequestsPerMinute: 2, AllowOrigin: "*"})

	require.Equal(t, http.StatusNoContent, options(t, h).Code)
	require.Equal(t, http.StatusNoContent, options(t, h).Code)
	assert.Equal(t, http.StatusTooManyRequests, options(t, h).Code,
		"a preflight flood must not be a free pass around the limiter")
}

func TestErrorsAreJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		handler  http.Handler
		target   string
		wantCode int
	}{
		{
			name:     "bad request",
			handler:  Handler(Deps{Users: &stubUsers{}, RequestsPerMinute: 120, AllowOrigin: "*"}),
			target:   "/v1/leaderboard?limit=0",
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "not found",
			handler:  Handler(Deps{RequestsPerMinute: 120, AllowOrigin: "*"}),
			target:   "/v1/secrets",
			wantCode: http.StatusNotFound,
		},
		{
			name:     "repository down",
			handler:  Handler(Deps{Users: &stubUsers{err: errors.New("boom")}, RequestsPerMinute: 120, AllowOrigin: "*"}),
			target:   "/v1/leaderboard",
			wantCode: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := get(t, tt.handler, tt.target)

			require.Equal(t, tt.wantCode, rec.Code)
			assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
			var got errorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.NotEmpty(t, got.Error)
		})
	}
}

func TestRateLimitErrorIsJSON(t *testing.T) {
	t.Parallel()
	h := Handler(Deps{Sessions: fakeSessions(1), RequestsPerMinute: 1, AllowOrigin: "*"})

	require.Equal(t, http.StatusOK, get(t, h, "/v1/stats").Code)
	rec := get(t, h, "/v1/stats")

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	assert.Equal(t, "60", rec.Header().Get("Retry-After"))
	var got errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "too many requests", got.Error)
}

func TestRateLimitRejectsAFlood(t *testing.T) {
	t.Parallel()
	h := Handler(Deps{Sessions: fakeSessions(1), RequestsPerMinute: 3, AllowOrigin: "*"})

	var lastCode int
	for range 6 {
		lastCode = get(t, h, "/v1/stats").Code
	}

	assert.Equal(t, http.StatusTooManyRequests, lastCode, "a flood from one network is throttled")
}

func TestAllowOriginCanBePinned(t *testing.T) {
	t.Parallel()
	rec := get(t, Handler(Deps{AllowOrigin: "https://tty.cards", RequestsPerMinute: 120}), "/v1/stats")
	assert.Equal(t, "https://tty.cards", rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestRateLimit_TrustedProxySeparatesClients(t *testing.T) {
	t.Parallel()
	h := Handler(Deps{Sessions: fakeSessions(1), RequestsPerMinute: 2, TrustedProxy: true, AllowOrigin: "*"})

	send := func(clientIP string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
		req.RemoteAddr = "10.0.0.1:9999"
		req.Header.Set("X-Forwarded-For", clientIP)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusOK, send("198.51.100.1"))
	assert.Equal(t, http.StatusOK, send("198.51.100.1"))
	assert.Equal(t, http.StatusTooManyRequests, send("198.51.100.1"), "that client is spent")
	assert.Equal(t, http.StatusOK, send("203.0.113.200"), "a different client has its own budget")
}

func TestRateLimit_UntrustedProxyHeaderIsIgnored(t *testing.T) {
	t.Parallel()
	h := Handler(Deps{Sessions: fakeSessions(1), RequestsPerMinute: 2, AllowOrigin: "*"})

	var last int
	for i := range 4 {
		req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
		req.RemoteAddr = "198.51.100.9:1234"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i+1))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		last = rec.Code
	}

	assert.Equal(t, http.StatusTooManyRequests, last, "forged headers must not reset the budget")
}

func TestTrustedProxy_UsesLeftmostForwardedAddress(t *testing.T) {
	t.Parallel()
	h := Handler(Deps{Sessions: fakeSessions(1), RequestsPerMinute: 1, TrustedProxy: true, AllowOrigin: "*"})

	send := func(xff string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
		req.RemoteAddr = "10.0.0.1:9999"
		req.Header.Set("X-Forwarded-For", xff)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusOK, send("198.51.100.5, 10.0.0.1"))
	assert.Equal(t, http.StatusTooManyRequests, send("198.51.100.5, 172.16.0.9"),
		"same client through a different hop is still the same client")
}

// A header is only as honest as whoever set it. The stats port is reachable from
// every container on the compose network, not only nginx, so with the proxy's
// networks named, a forged X-Forwarded-For from anywhere else must key on the socket.
func TestTrustedProxy_HeaderFromOutsideTheProxyNetworksIsIgnored(t *testing.T) {
	t.Parallel()
	h := Handler(Deps{
		Sessions: fakeSessions(1), RequestsPerMinute: 2, TrustedProxy: true, AllowOrigin: "*",
		TrustedProxyNetworks: []netip.Prefix{netip.MustParsePrefix("172.29.69.0/24")},
	})

	send := func(peer, xff string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
		req.RemoteAddr = peer
		req.Header.Set("X-Forwarded-For", xff)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	var last int
	for i := range 4 {
		last = send("10.0.0.7:1234", fmt.Sprintf("203.0.113.%d", i+1))
	}
	assert.Equal(t, http.StatusTooManyRequests, last, "a peer outside the proxy networks cannot mint budgets")
	assert.Equal(t, http.StatusOK, send("172.29.69.2:4000", "198.51.100.1"), "nginx's header is still believed")
}

// A HEAD is how a monitor checks the feed is alive; it must get the headers and
// nothing else, or a client that trusts Content-Length reads a truncated body.
func TestHeadReturnsHeadersWithoutABody(t *testing.T) {
	t.Parallel()
	h := Handler(Deps{Sessions: fakeSessions(3), Lobbies: fakeLobbies{inGame: 1, waiting: 2}, RequestsPerMinute: 120, AllowOrigin: "*"})

	req := httptest.NewRequest(http.MethodHead, "/v1/stats", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Empty(t, rec.Body.String(), "a HEAD must not carry a body")
}

// A limit that does not fit in an int is a malformed request, not a server fault.
func TestLeaderboard_LimitOverflowIsRejected(t *testing.T) {
	t.Parallel()
	users := &stubUsers{}
	h := Handler(Deps{Users: users, RequestsPerMinute: 120, AllowOrigin: "*"})

	rec := get(t, h, "/v1/leaderboard?limit=99999999999999999999")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, 0, users.gotLimit, "the repository must never be reached")
}

// A repeated parameter takes the first value; the junk after it must not turn a
// perfectly good request into a 400, nor sneak past the parse.
func TestLeaderboard_RepeatedLimitUsesTheFirstValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		query     string
		wantCode  int
		wantLimit int
	}{
		{name: "good then junk", query: "?limit=5&limit=abc", wantCode: http.StatusOK, wantLimit: 5},
		{name: "junk then good", query: "?limit=abc&limit=5", wantCode: http.StatusBadRequest},
		{name: "empty then good", query: "?limit=&limit=7", wantCode: http.StatusOK, wantLimit: defaultLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			users := &stubUsers{}
			rec := get(t, Handler(Deps{Users: users, RequestsPerMinute: 120, AllowOrigin: "*"}), "/v1/leaderboard"+tt.query)

			require.Equal(t, tt.wantCode, rec.Code)
			if tt.wantCode == http.StatusOK {
				assert.Equal(t, tt.wantLimit, users.gotLimit)
			}
		})
	}
}

// A blank-ish forwarded header used to key every one of these callers to NetKey(""),
// so they all shared one budget: an attacker could spend it and lock the rest out, or
// ride someone else's. Falling back to the socket address is the only honest answer.
func TestTrustedProxy_BlankForwardedHeaderFallsBackToTheSocket(t *testing.T) {
	t.Parallel()

	for _, xff := range []string{",", " ", ", 10.0.0.1", "not-an-address", "10.0.0.256"} {
		t.Run("xff="+xff, func(t *testing.T) {
			t.Parallel()
			h := Handler(Deps{Sessions: fakeSessions(1), RequestsPerMinute: 1, TrustedProxy: true, AllowOrigin: "*"})

			send := func(socket string) int {
				req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
				req.RemoteAddr = socket
				req.Header.Set("X-Forwarded-For", xff)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				return rec.Code
			}

			require.Equal(t, http.StatusOK, send("198.51.100.1:1111"))
			assert.Equal(t, http.StatusOK, send("203.0.113.2:2222"),
				"a second socket must have its own budget, not share one empty bucket")
		})
	}
}

// NetKey collapses IPv6 to its /64 because one customer is routinely delegated 2^64
// addresses; without it, a fresh address per request is a free pass.
func TestRateLimit_IPv6AddressesInOneNetworkShareABudget(t *testing.T) {
	t.Parallel()
	h := Handler(Deps{Sessions: fakeSessions(1), RequestsPerMinute: 1, AllowOrigin: "*"})

	send := func(addr string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	require.Equal(t, http.StatusOK, send("[2001:db8:1:1::1]:4000"))
	assert.Equal(t, http.StatusTooManyRequests, send("[2001:db8:1:1:ffff::9]:4001"),
		"a second address in the same /64 is the same subscriber")
	assert.Equal(t, http.StatusOK, send("[2001:db8:1:2::1]:4002"),
		"a different /64 is a different subscriber")
}

// /healthz backs the container healthcheck, so both of its answers are load-bearing:
// a 200 that is really unhealthy keeps a wedged process in rotation.
func TestHealthz(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		health   func(context.Context) error
		wantCode int
		wantBody string
	}{
		{name: "no probe means the process alone", wantCode: http.StatusOK, wantBody: `{"status":"ok"}`},
		{
			name:     "probe passes",
			health:   func(context.Context) error { return nil },
			wantCode: http.StatusOK, wantBody: `{"status":"ok"}`,
		},
		{
			name:     "probe fails",
			health:   func(context.Context) error { return errors.New("dial tcp: connection refused") },
			wantCode: http.StatusServiceUnavailable, wantBody: `{"error":"unhealthy"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := get(t, Handler(Deps{Health: tt.health, RequestsPerMinute: 120, AllowOrigin: "*"}), "/healthz")

			require.Equal(t, tt.wantCode, rec.Code)
			assert.JSONEq(t, tt.wantBody, rec.Body.String())
			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"),
				"a cached health answer is a claim about a moment that has passed")
			assert.NotContains(t, rec.Body.String(), "connection refused", "the probe's error is ours, not the caller's")
		})
	}
}
