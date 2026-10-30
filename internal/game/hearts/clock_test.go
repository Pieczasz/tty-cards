package hearts

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/gametest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The seat that plays the fourth card and takes the trick leads the next one: that
// lead is a new turn and gets the whole clock, not what was left of the last one.
func TestClock_TrickWinnerLeadsOnAFreshClock(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const turn = 30 * time.Second
		engine := game.NewEngine(&Rules{}, fourPlayers(), game.WithTurnTimeout(turn))
		t.Cleanup(engine.Close)
		require.NoError(t, engine.Start())

		// Past the pass and the opening trick; p4 follows clubs last with the king.
		king := deck.Card{Rank: deck.King, Suit: deck.Clubs}
		hands := [][]deck.Card{
			{{Rank: deck.Five, Suit: deck.Clubs}},
			{{Rank: deck.Three, Suit: deck.Clubs}},
			{{Rank: deck.Two, Suit: deck.Clubs}},
			{king, {Rank: deck.Nine, Suit: deck.Diamonds}},
		}
		engine.WithState(func(s *game.State) {
			extra := gametest.Extra[*State](t, s)
			extra.Phase = PhaseTrickPlay
			extra.PendingPasses = nil
			extra.TricksPlayed = 1
			for i, p := range s.Players {
				p.Cards = hands[i]
			}
			s.CurrentTurn = 0
		})
		for i, id := range []string{"p1", "p2", "p3"} {
			require.NoError(t, engine.SubmitAction(id, ActionPlayCard{Card: hands[i][0]}), id)
		}

		time.Sleep(turn - 5*time.Second)
		require.NoError(t, engine.SubmitAction("p4", ActionPlayCard{Card: king}))

		snap, _, remaining := engine.Frame("p4", nil)
		require.Equal(t, "p4", snap.CurrentPlayerID, "the king took the trick")
		assert.Equal(t, turn, remaining)
	})
}
