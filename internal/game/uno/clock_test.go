package uno

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Heads-up, a Skip hands the turn straight back: that is a new turn and gets the
// whole clock, not what was left of the one the Skip was played on.
func TestClock_HeadsUpSkipIsAFreshTurn(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const turn = 30 * time.Second
		engine := game.NewEngine(&Rules{}, []*game.Player{{ID: "a"}, {ID: "b"}}, game.WithTurnTimeout(turn))
		t.Cleanup(engine.Close)
		require.NoError(t, engine.Start())

		var onTurn string
		var skip deck.Card
		engine.WithState(func(s *game.State) {
			p := s.Players[s.CurrentTurn]
			onTurn = p.ID
			skip = deck.Card{Rank: Skip, Suit: extraOf(s).CurrentColor}
			p.Cards = append(p.Cards, skip)
		})

		time.Sleep(turn - 5*time.Second)
		require.NoError(t, engine.SubmitAction(onTurn, ActionPlayCard{Card: skip}))

		snap, _, remaining := engine.Frame(onTurn, nil)
		require.Equal(t, onTurn, snap.CurrentPlayerID, "the Skip came back to the same seat")
		assert.Equal(t, turn, remaining)
	})
}
