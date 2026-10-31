package lobby

import (
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// gamesFinished is the ttycards.games.finished total recorded for gameType so far.
// Each test uses its own game, so parallel tests do not count each other's tables.
func gamesFinished(t *testing.T, gameType string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, testMetrics.Collect(t.Context(), &rm))
	var n int64
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			sum, ok := mt.Data.(metricdata.Sum[int64])
			if mt.Name != "ttycards.games.finished" || !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key("game_type")); ok && v.AsString() == gameType {
					n += dp.Value
				}
			}
		}
	}
	return n
}

// playFinishedTable seats two players at a table of slug's game and plays the hand out,
// returning once the table has reopened: by then the watcher has handed the match on.
func playFinishedTable(t *testing.T, m *Manager, slug string) {
	t.Helper()
	leader, guest := mockPlayer("leader", testutil.UID(1)), mockPlayer("guest", testutil.UID(2))
	l, err := m.CreateLobby(leader, WithMaxPlayers(2), WithCardGame(slug), WithRanked(true))
	require.NoError(t, err)
	require.NoError(t, joinErr(m.JoinLobbyByCode(t.Context(), l.Code(), guest)))
	require.NoError(t, m.ToggleReady(leader))
	require.NoError(t, m.ToggleReady(guest))

	endHand(l.ActiveGame(), stubWin)
	require.Eventually(t, func() bool { return isWaiting(l) }, 2*time.Second, time.Millisecond)
}

// games started minus games finished is how a dashboard shows tables in play. Only
// the persisted path used to count a finish, so every table a deploy drained, and
// every table on a server with no match repository, stayed "in play" forever.
func TestFinalize_EveryFinishedGameIsCounted(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(t *testing.T, games *game.Registry) *Manager
	}{
		{
			name: "no match repository",
			setup: func(t *testing.T, games *game.Registry) *Manager {
				t.Helper()
				return newManagerFor(t, nil, games)
			},
		},
		{
			name: "persisted",
			setup: func(t *testing.T, games *game.Registry) *Manager {
				t.Helper()
				repo := new(MockMatchRepo)
				// Rated or not depends on whether the drain below began before the write.
				repo.On("FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)
				repo.On("RecordCasualMatch", mock.Anything, mock.Anything, mock.Anything).Return(nil)
				return newManagerFor(t, repo, games)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			name := "Finished " + tt.name
			slug := strings.ToLower(name)
			m := tt.setup(t, gameRegistry(name, stubRules{}))
			// The counter is process-wide and cumulative, so -count=N sees earlier runs.
			before := gamesFinished(t, slug)

			playFinishedTable(t, m, slug)
			require.True(t, m.WaitForFinalizers(2*time.Second))

			assert.Equal(t, before+1, gamesFinished(t, slug))
		})
	}
}

// The feed can end with no EventGameEnded on it: RemoveLobby closes the engine, and a
// leave already on its way can still end the game on the closed engine, whose
// broadcast is then a no-op. The engine still knows why it ended, so a forfeit is
// rated like one rather than guessed at.
func TestFeedEnd_UnobservedEndIsFinalizedWithTheEnginesReason(t *testing.T) {
	t.Parallel()
	seats := []*game.Player{mockPlayer("a", testutil.UID(1)), mockPlayer("b", testutil.UID(2))}
	repo := new(MockMatchRepo)
	repo.On("FinalizeRankedMatch", mock.Anything, gameRef("Mock"),
		[]uuid.UUID{testutil.UID(2), testutil.UID(1)}, []int{1, 2}).Return(nil).Once()
	m := newTestManager(t, repo)
	l, err := m.CreateLobby(seats[0], WithCardGame("mock"), WithRanked(true))
	require.NoError(t, err)

	engine := game.NewEngine(stubRules{}, seats)
	require.NoError(t, engine.Start())
	engine.Close()
	engine.RemovePlayer("a")
	require.Equal(t, game.EndReasonForfeit, engine.EndReason())

	closedFeed := make(chan game.Event)
	close(closedFeed)
	m.watchGame(l, gameStart{engine: engine, feed: closedFeed, req: finalizeRequest{
		lobbyCode: l.Code(), game: gameRef("Mock"), isRanked: true, startedAt: time.Now(),
	}})

	require.True(t, m.WaitForFinalizers(2*time.Second))
	repo.AssertExpectations(t)
	repo.AssertNotCalled(t, "RecordCasualMatch", mock.Anything, mock.Anything, mock.Anything)
}

// An engine line that says "hand 7" matches every table on the server unless it names
// its lobby and game; the lobby hands the engine a logger that does.
//
//nolint:paralleltest // slog.SetDefault is process-wide, so this cannot share the process
func TestLobby_EngineLogsNameTheTable(t *testing.T) {
	var logged syncBuffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })

	m, l := newTestLobby(t, 2)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(t.Context(), l.Code(), guest)))
	require.NoError(t, m.ToggleReady(l.Snapshot().Leader.Player))
	require.NoError(t, m.ToggleReady(guest))

	l.ActiveGame().WithState(func(s *game.State) { s.Logger().Info("probe") })

	var rec map[string]any
	for line := range strings.Lines(logged.String()) {
		require.NoError(t, json.Unmarshal([]byte(line), &rec))
		if rec["msg"] == "probe" {
			break
		}
	}
	require.Equal(t, "probe", rec["msg"])
	assert.Equal(t, l.Code(), rec["lobby"])
	assert.Equal(t, "mock", rec["game"], "the slug, as every game metric labels it")
}

func TestToggleReady_SubscribeFailureDoesNotStartTheGame(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 2)
	m.engineOpts = []game.EngineOption{game.WithNoSubscribers(), game.WithTurnTimeout(0)}
	leader := l.Snapshot().Leader.Player
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(t.Context(), l.Code(), guest)))
	require.NoError(t, m.ToggleReady(leader))

	err := m.ToggleReady(guest)

	require.Error(t, err)
	require.NotErrorIs(t, err, ErrRefused)
	assert.Nil(t, l.ActiveGame())
	assert.False(t, l.Snapshot().InGame)
	require.True(t, m.WaitForFinalizers(time.Second), "the watcher count was left held for a game that never started")
}

func TestWatchGame_PanicDoesNotEscape(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	engine := game.NewEngine(stubRules{}, []*game.Player{{ID: "p1"}}, game.WithTurnTimeout(0))
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)
	feed := make(chan game.Event, 1)
	feed <- game.Event{Type: game.EventGameEnded}
	close(feed)

	require.NotPanics(t, func() {
		m.watchGame(nil, gameStart{engine: engine, feed: feed, req: finalizeRequest{game: gameRef("TestGame")}})
	})
}

func TestExpireLeave_NilPlayerDoesNotPanic(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	require.NotPanics(t, func() { m.expireLeaveGen(nil, 0) })
}

func TestExpireLeave_StaleGenerationDoesNotDropTheNewGrace(t *testing.T) {
	t.Parallel()
	m, l, _, guest := startedGame(t)
	m.DisconnectPlayer(t.Context(), guest)
	m.mu.Lock()
	stale := m.grace.pending[guest.ID].gen
	m.mu.Unlock()

	m.DisconnectPlayer(t.Context(), guest)
	m.expireLeaveGen(guest, stale)

	assert.True(t, l.HasPlayer(guest), "a stale grace timer dropped the seat the later drop is holding")
	assert.True(t, pendingGrace(m, guest.ID))
}
