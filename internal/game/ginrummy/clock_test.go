package ginrummy

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/Pieczasz/tty-cards/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Draw then discard is one turn (State.ContinueTurn): the draw leaves the clock
// running, and only the discard starts the next seat's.
func TestClock_DrawThenDiscardKeepsTheClock(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const turn = 30 * time.Second
		engine := game.NewEngine(&Rules{}, twoPlayers(), game.WithTurnTimeout(turn))
		t.Cleanup(engine.Close)
		require.NoError(t, engine.Start())
		onTurn := engine.CurrentPlayerID()

		time.Sleep(15 * time.Second)
		require.NoError(t, engine.SubmitAction(onTurn, ActionDrawStock{}))
		_, hand, remaining := engine.Frame(onTurn, nil)
		assert.Equal(t, 15*time.Second, remaining, "the draw does not restart the turn")

		// The card just drawn from the stock is always a legal discard.
		require.NoError(t, engine.SubmitAction(onTurn, ActionDiscard{Card: hand[len(hand)-1]}))
		snap, _, remaining := engine.Frame(onTurn, nil)
		assert.NotEqual(t, onTurn, snap.CurrentPlayerID)
		assert.Equal(t, turn, remaining, "the discard hands the other seat a fresh clock")
	})
}
