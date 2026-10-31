package lobbyview

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/crazyeight"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A refusal the player caused is already on their screen. Logging it at ERROR on the
// keypress path means a held key floods the error log with nothing an operator can fix.
//
//nolint:paralleltest // CaptureLogs swaps the process-wide slog.Default
func TestUserRefusals_AreShownNotLoggedAsErrors(t *testing.T) {
	t.Run("ready without a seat", func(t *testing.T) {
		logs := tuitest.CaptureLogs(t)
		m, l := leaderView(t)
		t.Cleanup(m.Close)
		global := m.global
		global.Player = lobby.NewPlayer(testUser(3, "carol")) // never joined
		outsider, ok := New(global, l).(*model)
		require.True(t, ok)
		t.Cleanup(outsider.Close)

		for range 20 {
			press(outsider, "r")
		}

		require.Error(t, outsider.actionErr)
		assert.Empty(t, logs.AtLeast(slog.LevelWarn))
	})

	t.Run("a kick the manager refuses", func(t *testing.T) {
		logs := tuitest.CaptureLogs(t)
		m, l := leaderView(t)
		addGuest(t, m, l, 2, "bob")
		m.cursor = cursorFirstGuest
		m.global.LobbyManager.RemoveLobby(l.Code())

		press(m, "enter")

		require.Error(t, m.actionErr, "the refusal is shown rather than dropped")
		assert.Contains(t, m.View().Content, m.actionErr.Error())
		assert.Empty(t, logs.AtLeast(slog.LevelWarn))
	})

	t.Run("a setting the lobby refuses", func(t *testing.T) {
		logs := tuitest.CaptureLogs(t)
		m, l := leaderView(t)
		t.Cleanup(m.Close)
		imposter := addGuest(t, m, l, 2, "bob")

		for _, row := range []int{cursorMaxPlayers, cursorVisibility, cursorMode} {
			m.actionErr = nil
			m.cursor = row
			m.adjustSetting(imposter, +1)
			require.Error(t, m.actionErr, "row %d: the refusal is shown rather than dropped", row)
		}
		assert.Empty(t, logs.AtLeast(slog.LevelWarn))
	})

	t.Run("an accepted setting clears the last refusal", func(t *testing.T) {
		m, _ := leaderView(t)
		t.Cleanup(m.Close)
		m.actionErr = assert.AnError
		m.cursor = cursorVisibility

		press(m, "l")

		require.NoError(t, m.actionErr)
		assert.True(t, m.snap.Private)
	})
}

// A lost feed is a server-side failure: it stays an ERROR, logged under the session's
// trace so it can be found from the session, and counted.
//
//nolint:paralleltest // CaptureLogs swaps slog.Default; the counter is process-wide
func TestNew_SubscriptionFailureIsLoggedUnderTheSessionTraceAndCounted(t *testing.T) {
	logs := tuitest.CaptureLogs(t)
	m, l := leaderView(t)
	m.Close()
	m.global.LobbyManager.RemoveLobby(l.Code())
	global := router.GlobalContext{
		Player: m.global.Player, LobbyManager: m.global.LobbyManager, Games: m.global.Games,
		SessionCtx: tuitest.TracedContext(t),
	}
	before := tuitest.Count(t, testMetrics, "ttycards.broadcaster.subscribe.failures", "stream", "lobby_view")

	broken, ok := New(global, l).(*model)
	require.True(t, ok)
	require.Error(t, broken.actionErr)

	errs := logs.AtLeast(slog.LevelError)
	require.Len(t, errs, 1)
	assert.True(t, errs[0].SameTrace(global.SessionCtx), "the record carries the session trace")
	assert.Equal(t, before+1,
		tuitest.Count(t, testMetrics, "ttycards.broadcaster.subscribe.failures", "stream", "lobby_view"))
}

// noDealRules is crazy eights whose deal fails, so the engine never starts: a defect
// in the rules, not something the players did.
type noDealRules struct{ crazyeight.Rules }

func (*noDealRules) OnGameStart(*game.State) error { return errors.New("no deal") }

// The lobby returns a failure unlogged and a refusal as the player should read it, so
// this is the one place each is told apart: a start the rules cannot deal is logged
// once at ERROR under the session's trace and shown as a generic line, while too few
// players is shown as it is and logged nowhere.
//
//nolint:paralleltest // CaptureLogs swaps the process-wide slog.Default
func TestReady_GenuineStartFailuresAreLoggedOnceRefusalsAreShown(t *testing.T) {
	tests := []struct {
		name      string
		factory   func() game.Rules
		withGuest bool
		wantShown string
		wantLog   bool
	}{
		{
			name: "the rules cannot deal", factory: func() game.Rules { return &noDealRules{} },
			withGuest: true, wantShown: "something went wrong", wantLog: true,
		},
		{
			name: "too few players is a refusal", factory: func() game.Rules { return &crazyeight.Rules{} },
			wantShown: "need at least 2 players",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logs := tuitest.CaptureLogs(t)
			mod := game.Module{
				Name: testGameName, Slug: testGameSlug,
				MinPlayers: crazyeight.MinPlayers, MaxPlayers: crazyeight.MaxPlayers, Factory: tt.factory,
			}
			manager := lobby.NewManager(t.Context(), nil, game.NewRegistry(mod))
			leaderUser := testUser(1, "alice")
			l, err := manager.CreateLobby(lobby.NewPlayer(leaderUser), lobby.WithCardGame(testGameSlug))
			require.NoError(t, err)
			global := router.GlobalContext{
				Player: lobby.NewPlayer(leaderUser), LobbyManager: manager, Games: []game.Module{mod},
				Width: 120, Height: 40, SessionCtx: tuitest.TracedContext(t),
			}
			m, ok := New(global, l).(*model)
			require.True(t, ok)
			t.Cleanup(m.Close)
			if tt.withGuest {
				guest := addGuest(t, m, l, 2, "bob")
				require.NoError(t, manager.ToggleReady(guest))
			}

			press(m, "r")

			require.Error(t, m.actionErr)
			assert.Contains(t, tuitest.StripANSI(m.View().Content), tt.wantShown)
			errs := logs.AtLeast(slog.LevelWarn)
			if !tt.wantLog {
				assert.Empty(t, errs, "a refusal is the player's business, not an error")
				return
			}
			require.Len(t, errs, 1)
			assert.Equal(t, slog.LevelError, errs[0].Level)
			assert.True(t, errs[0].SameTrace(global.SessionCtx), "the record carries the session trace")
			assert.NotContains(t, m.View().Content, "start game engine", "the detail is the log's, not the player's")
		})
	}
}
