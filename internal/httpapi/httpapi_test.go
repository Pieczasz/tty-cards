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
	rankings []db.Ranking
	err      error
	gotLimit int
	gotGame  string
}

func (s *stubUsers) BestPlayers(_ context.Context, gameName string, limit int) ([]db.Ranking, error) {
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

// handler builds the API over deps, filling whatever a test leaves unset with an
// empty stub and config.Load's defaults.
func handler(t *testing.T, deps Deps) http.Handler {
	t.Helper()
	if deps.Sessions == nil {
		deps.Sessions = fakeSessions(0)
	}
	if deps.Lobbies == nil {
		deps.Lobbies = fakeLobbies{}
	}
	if deps.Users == nil {
		deps.Users = &stubUsers{}
	}
	if deps.RequestsPerMinute == 0 {
		deps.RequestsPerMinute = 120
	}
	if deps.AllowOrigin == "" {
		deps.AllowOrigin = "*"
	}
	h, err := newHandler(deps)
	require.NoError(t, err)
	return h
}

// send serves one request from peer, with an X-Forwarded-For when xff is set.
func send(t *testing.T, h http.Handler, method, target, peer, xff string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = peer
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	return send(t, h, http.MethodGet, target, "203.0.113.7:5555", "")
}

// A nil counter or repository used to be skipped, so a miswired server reported
// nobody online, or an empty leaderboard, for as long as it ran.
func TestNewServer_RequiresEveryDependency(t *testing.T) {
	t.Parallel()

	full := Deps{Sessions: fakeSessions(0), Lobbies: fakeLobbies{}, Users: &stubUsers{}, RequestsPerMinute: 1, AllowOrigin: "*"}
	tests := []struct {
		name  string
		unset func(*Deps)
	}{
		{name: "no sessions", unset: func(d *Deps) { d.Sessions = nil }},
		{name: "no lobbies", unset: func(d *Deps) { d.Lobbies = nil }},
		{name: "no users", unset: func(d *Deps) { d.Users = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deps := full
			tt.unset(&deps)
			srv, err := NewServer("127.0.0.1:0", deps)
			require.ErrorIs(t, err, ErrMissingDeps)
			assert.Nil(t, srv)
		})
	}

	srv, err := NewServer("127.0.0.1:0", full)
	require.NoError(t, err)
	assert.Equal(t, readTimeout, srv.ReadHeaderTimeout, "a slowloris client must be cut off")
}

func TestStats_ReportsLiveCounts(t *testing.T) {
	t.Parallel()
	h := handler(t, Deps{Sessions: fakeSessions(4), Lobbies: fakeLobbies{inGame: 2, waiting: 3}})

	rec := get(t, h, "/v1/stats")

	require.Equal(t, http.StatusOK, rec.Code)
	var got statsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, statsResponse{PlayersOnline: 4, HandsInPlay: 2, TablesOpen: 3}, got)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
}

// A HEAD is how a monitor checks the feed is alive; it must get the headers and
// nothing else, or a client that trusts Content-Length reads a truncated body.
func TestStats_HeadReturnsHeadersWithoutABody(t *testing.T) {
	t.Parallel()
	h := handler(t, Deps{Sessions: fakeSessions(3), Lobbies: fakeLobbies{inGame: 1, waiting: 2}})

	rec := send(t, h, http.MethodHead, "/v1/stats", "203.0.113.9:5555", "")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Empty(t, rec.Body.String(), "a HEAD must not carry a body")
}

func TestLeaderboard_ShapesRanks(t *testing.T) {
	t.Parallel()
	users := &stubUsers{rankings: []db.Ranking{
		ranking("alice", "Poker", 1800),
		ranking("bob", "Poker", 1700),
	}}

	rec := get(t, handler(t, Deps{Users: users}), "/v1/leaderboard")

	require.Equal(t, http.StatusOK, rec.Code)
	var got []leaderboardEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 2)
	assert.Equal(t, leaderboardEntry{Rank: 1, Username: "alice", Game: "Poker", Elo: 1800}, got[0])
	assert.Equal(t, 2, got[1].Rank, "rank is the position, not anything from the row")
}

// Every limit the repository sees is one the cache can serve, and anything that is not
// a positive integer is the caller's mistake. A repeated parameter takes the first
// value, so junk after a good one neither fails the request nor sneaks past the parse.
func TestLeaderboard_Limit(t *testing.T) {
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
		{name: "past an int is refused", query: "?limit=99999999999999999999", wantCode: http.StatusBadRequest},
		{name: "good then junk", query: "?limit=5&limit=abc", wantCode: http.StatusOK, wantLimit: 5},
		{name: "junk then good", query: "?limit=abc&limit=5", wantCode: http.StatusBadRequest},
		{name: "empty then good", query: "?limit=&limit=7", wantCode: http.StatusOK, wantLimit: defaultLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			users := &stubUsers{}
			rec := get(t, handler(t, Deps{Users: users}), "/v1/leaderboard"+tt.query)

			require.Equal(t, tt.wantCode, rec.Code)
			assert.Equal(t, tt.wantLimit, users.gotLimit, "a refused limit must never reach the repository")
		})
	}
}

func TestLeaderboard_RepositoryErrorIsOpaque(t *testing.T) {
	t.Parallel()
	users := &stubUsers{err: errors.New("pq: relation \"rankings\" does not exist")}

	rec := get(t, handler(t, Deps{Users: users}), "/v1/leaderboard")

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
			rec := get(t, handler(t, Deps{Users: users}), "/v1/leaderboard"+query)

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Empty(t, users.gotGame, "the repository must never be handed a caller-supplied game name")
		})
	}
}

func TestRoutes_UnknownIs404(t *testing.T) {
	t.Parallel()
	h := handler(t, Deps{})
	assert.Equal(t, http.StatusNotFound, get(t, h, "/v1/secrets").Code)
	assert.Equal(t, http.StatusNotFound, get(t, h, "/").Code)
}

func TestRoutes_WriteMethodsAreRejected(t *testing.T) {
	t.Parallel()
	h := handler(t, Deps{Sessions: fakeSessions(1)})

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			rec := send(t, h, method, "/v1/stats", "203.0.113.9:1111", "")
			assert.NotEqual(t, http.StatusOK, rec.Code, "%s must not be served", method)
		})
	}
}

func TestCORS_PreflightIsAnswered(t *testing.T) {
	t.Parallel()
	rec := send(t, handler(t, Deps{}), http.MethodOptions, "/v1/stats", "203.0.113.11:2222", "")

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), "GET")
}

func TestCORS_AllowOriginCanBePinned(t *testing.T) {
	t.Parallel()
	rec := get(t, handler(t, Deps{AllowOrigin: "https://tty.cards"}), "/v1/stats")
	assert.Equal(t, "https://tty.cards", rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestErrorsAreJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		users    *stubUsers
		target   string
		wantCode int
	}{
		{name: "bad request", target: "/v1/leaderboard?limit=0", wantCode: http.StatusBadRequest},
		{name: "not found", target: "/v1/secrets", wantCode: http.StatusNotFound},
		{
			name:     "repository down",
			users:    &stubUsers{err: errors.New("boom")},
			target:   "/v1/leaderboard",
			wantCode: http.StatusServiceUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deps := Deps{}
			if tt.users != nil {
				deps.Users = tt.users
			}
			rec := get(t, handler(t, deps), tt.target)

			require.Equal(t, tt.wantCode, rec.Code)
			assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
			var got errorResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
			assert.NotEmpty(t, got.Error)
		})
	}
}

func TestRateLimit_RefusalIsJSON(t *testing.T) {
	t.Parallel()
	h := handler(t, Deps{RequestsPerMinute: 1})

	require.Equal(t, http.StatusOK, get(t, h, "/v1/stats").Code)
	rec := get(t, h, "/v1/stats")

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	assert.Equal(t, "60", rec.Header().Get("Retry-After"))
	var got errorResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "too many requests", got.Error)
}

func TestRateLimit_RejectsAFlood(t *testing.T) {
	t.Parallel()
	h := handler(t, Deps{RequestsPerMinute: 3})

	var lastCode int
	for range 6 {
		lastCode = get(t, h, "/v1/stats").Code
	}

	assert.Equal(t, http.StatusTooManyRequests, lastCode, "a flood from one network is throttled")
}

func TestRateLimit_PreflightIsCounted(t *testing.T) {
	t.Parallel()
	h := handler(t, Deps{RequestsPerMinute: 2})
	options := func() int {
		return send(t, h, http.MethodOptions, "/v1/stats", "203.0.113.11:2222", "").Code
	}

	require.Equal(t, http.StatusNoContent, options())
	require.Equal(t, http.StatusNoContent, options())
	assert.Equal(t, http.StatusTooManyRequests, options(),
		"a preflight flood must not be a free pass around the limiter")
}

// NetKey collapses IPv6 to its /64 because one customer is routinely delegated 2^64
// addresses; without it, a fresh address per request is a free pass.
func TestRateLimit_IPv6AddressesInOneNetworkShareABudget(t *testing.T) {
	t.Parallel()
	h := handler(t, Deps{RequestsPerMinute: 1})
	code := func(peer string) int { return send(t, h, http.MethodGet, "/v1/stats", peer, "").Code }

	require.Equal(t, http.StatusOK, code("[2001:db8:1:1::1]:4000"))
	assert.Equal(t, http.StatusTooManyRequests, code("[2001:db8:1:1:ffff::9]:4001"),
		"a second address in the same /64 is the same subscriber")
	assert.Equal(t, http.StatusOK, code("[2001:db8:1:2::1]:4002"),
		"a different /64 is a different subscriber")
}

func TestTrustedProxy_SeparatesClients(t *testing.T) {
	t.Parallel()
	h := handler(t, Deps{RequestsPerMinute: 2, TrustedProxy: true})
	code := func(client string) int { return send(t, h, http.MethodGet, "/v1/stats", "10.0.0.1:9999", client).Code }

	assert.Equal(t, http.StatusOK, code("198.51.100.1"))
	assert.Equal(t, http.StatusOK, code("198.51.100.1"))
	assert.Equal(t, http.StatusTooManyRequests, code("198.51.100.1"), "that client is spent")
	assert.Equal(t, http.StatusOK, code("203.0.113.200"), "a different client has its own budget")
}

func TestTrustedProxy_OffIgnoresTheHeader(t *testing.T) {
	t.Parallel()
	h := handler(t, Deps{RequestsPerMinute: 2})

	var last int
	for i := range 4 {
		last = send(t, h, http.MethodGet, "/v1/stats", "198.51.100.9:1234", fmt.Sprintf("203.0.113.%d", i+1)).Code
	}

	assert.Equal(t, http.StatusTooManyRequests, last, "forged headers must not reset the budget")
}

func TestTrustedProxy_UsesLeftmostForwardedAddress(t *testing.T) {
	t.Parallel()
	h := handler(t, Deps{RequestsPerMinute: 1, TrustedProxy: true})
	code := func(xff string) int { return send(t, h, http.MethodGet, "/v1/stats", "10.0.0.1:9999", xff).Code }

	require.Equal(t, http.StatusOK, code("198.51.100.5, 10.0.0.1"))
	assert.Equal(t, http.StatusTooManyRequests, code("198.51.100.5, 172.16.0.9"),
		"same client through a different hop is still the same client")
}

// A header is only as honest as whoever set it. The stats port is reachable from
// every container on the compose network, not only nginx, so with the proxy's
// networks named, a forged X-Forwarded-For from anywhere else must key on the socket.
func TestTrustedProxy_HeaderFromOutsideTheProxyNetworksIsIgnored(t *testing.T) {
	t.Parallel()
	h := handler(t, Deps{
		RequestsPerMinute: 2, TrustedProxy: true,
		TrustedProxyNetworks: []netip.Prefix{netip.MustParsePrefix("172.29.69.0/24")},
	})
	code := func(peer, xff string) int { return send(t, h, http.MethodGet, "/v1/stats", peer, xff).Code }

	var last int
	for i := range 4 {
		last = code("10.0.0.7:1234", fmt.Sprintf("203.0.113.%d", i+1))
	}
	assert.Equal(t, http.StatusTooManyRequests, last, "a peer outside the proxy networks cannot mint budgets")
	assert.Equal(t, http.StatusOK, code("172.29.69.2:4000", "198.51.100.1"), "nginx's header is still believed")
}

// A blank-ish forwarded header used to key every one of these callers to NetKey(""),
// so they all shared one budget: an attacker could spend it and lock the rest out, or
// ride someone else's. Falling back to the socket address is the only honest answer.
func TestTrustedProxy_BlankForwardedHeaderFallsBackToTheSocket(t *testing.T) {
	t.Parallel()

	for _, xff := range []string{",", " ", ", 10.0.0.1", "not-an-address", "10.0.0.256"} {
		t.Run("xff="+xff, func(t *testing.T) {
			t.Parallel()
			h := handler(t, Deps{RequestsPerMinute: 1, TrustedProxy: true})
			code := func(peer string) int { return send(t, h, http.MethodGet, "/v1/stats", peer, xff).Code }

			require.Equal(t, http.StatusOK, code("198.51.100.1:1111"))
			assert.Equal(t, http.StatusOK, code("203.0.113.2:2222"),
				"a second socket must have its own budget, not share one empty bucket")
		})
	}
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
			rec := get(t, handler(t, Deps{Health: tt.health}), "/healthz")

			require.Equal(t, tt.wantCode, rec.Code)
			assert.JSONEq(t, tt.wantBody, rec.Body.String())
			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"),
				"a cached health answer is a claim about a moment that has passed")
			assert.NotContains(t, rec.Body.String(), "connection refused", "the probe's error is ours, not the caller's")
		})
	}
}
