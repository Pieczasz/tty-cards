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

// playFinishedTable seats two players at a table of gameName and plays the hand out,
// returning once the table has reopened: by then the watcher has handed the match on.
func playFinishedTable(t *testing.T, m *Manager, gameName string) {
	t.Helper()
	leader, guest := mockPlayer("leader", testutil.UID(1)), mockPlayer("guest", testutil.UID(2))
	l, err := m.CreateLobby(leader, WithMaxPlayers(2), WithCardGame(gameName), WithRanked(true))
	require.NoError(t, err)
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
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
			name: "dropped by shutdown",
			setup: func(t *testing.T, games *game.Registry) *Manager {
				t.Helper()
				m := newManagerFor(t, new(MockMatchRepo), games)
				require.True(t, m.WaitForFinalizers(time.Second), "nothing is in flight yet")
				return m
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
			m := tt.setup(t, gameRegistry(name, stubRules{}))
			// The counter is process-wide and cumulative, so -count=N sees earlier runs.
			before := gamesFinished(t, strings.ToLower(name))

			playFinishedTable(t, m, name)
			require.True(t, m.WaitForFinalizers(2*time.Second))

			assert.Equal(t, before+1, gamesFinished(t, strings.ToLower(name)))
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
	l, err := m.CreateLobby(seats[0], WithCardGame("Mock"), WithRanked(true))
	require.NoError(t, err)

	engine := game.NewEngine(stubRules{}, seats)
	require.NoError(t, engine.Start())
	engine.Close()
	engine.RemovePlayer("a")
	require.Equal(t, game.EndReasonForfeit, engine.EndReason())

	closedFeed := make(chan game.Event)
	close(closedFeed)
	l.handleGameEvents(closedFeed, engine, finalizeRequest{
		lobbyCode: l.Code(), game: gameRef("Mock"), isRanked: true, startedAt: time.Now(),
	})

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
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	require.NoError(t, m.ToggleReady(l.Leader()))
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

// The lobby view logs what ToggleReady returns at Debug, because a refusal (too few
// players, game in progress) and a real failure look alike from there. Only the start
// itself knows a rules set that cannot deal is not a refusal, so it has to say so at
// Error, naming the table.
//
//nolint:paralleltest // slog.SetDefault is process-wide, so this cannot share the process
func TestToggleReady_GenuineStartFailuresAreLoggedAtTheSource(t *testing.T) {
	tests := []struct {
		name     string
		registry *game.Registry
		wantLog  bool
	}{
		{name: "engine cannot deal", registry: gameRegistry("Mock", refusedStartRules{}), wantLog: true},
		{name: "too few players is a refusal", registry: boundedRegistry("Mock", stubRules{}, 3, 9)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logged syncBuffer
			original := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
			t.Cleanup(func() { slog.SetDefault(original) })

			m := newManagerFor(t, nil, tt.registry)
			l, err := m.CreateLobby(mockPlayer("p1", testutil.UID(1)), WithCardGame("Mock"))
			require.NoError(t, err)
			guest := mockPlayer("p2", testutil.UID(2))
			require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
			require.NoError(t, m.ToggleReady(l.Leader()))
			require.Error(t, m.ToggleReady(guest))

			if !tt.wantLog {
				assert.Empty(t, logged.String(), "a refusal is the player's business, not an error")
				return
			}
			var rec map[string]any
			require.NoError(t, json.Unmarshal([]byte(logged.String()), &rec), logged.String())
			assert.Equal(t, "cannot start game", rec["msg"])
			assert.Equal(t, l.Code(), rec["lobby"])
			assert.Equal(t, "mock", rec["game"])
			assert.NotEmpty(t, rec["error"])
		})
	}
}
