package lobby

import (
	"log/slog"
	"testing"

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
		global.User = testUser(3, "carol") // never joined
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
		User: m.global.User, LobbyManager: m.global.LobbyManager, GameRegistry: m.global.GameRegistry,
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
