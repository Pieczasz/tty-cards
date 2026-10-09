package gameview

import (
	"log/slog"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"
	"github.com/Pieczasz/tty-cards/internal/tui/views"

	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A table view that cannot subscribe never updates: a server-side failure, so it stays
// an ERROR, logged under the session's trace, and counted beside the lobby's.
//
//nolint:paralleltest // CaptureLogs swaps slog.Default; the counter is process-wide
func TestNewSession_SubscriptionFailureIsLoggedUnderTheSessionTraceAndCounted(t *testing.T) {
	logs := tuitest.CaptureLogs(t)
	engine, s := startedSession(t)
	s.Close()
	engine.Close()
	before := tuitest.Count(t, testMetrics, "ttycards.broadcaster.subscribe.failures", "stream", "game_view")

	global := router.GlobalContext{
		User:       &db.User{ID: testutil.UID(1), Username: "alice"},
		SessionCtx: tuitest.TracedContext(t),
	}
	broken, err := NewSession(global, game.Bind(engine, views.SessionPlayerID(global)), "crazy_eights")
	require.Error(t, err)
	assert.Nil(t, broken.events)

	errs := logs.AtLeast(slog.LevelError)
	require.Len(t, errs, 1)
	assert.True(t, errs[0].SameTrace(global.SessionCtx), "the record carries the session trace")
	assert.Equal(t, before+1,
		tuitest.Count(t, testMetrics, "ttycards.broadcaster.subscribe.failures", "stream", "game_view"))
}
