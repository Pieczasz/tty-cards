package game_test

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestState_Deal(t *testing.T) {
	t.Parallel()

	t.Run("every seat gets n from a fresh stock", func(t *testing.T) {
		t.Parallel()
		held := []deck.Card{{Rank: deck.Ace, Suit: deck.Spades}}
		state := game.NewState(nil, []*game.Player{{ID: "p1", Cards: held}, {ID: "p2"}}, nil)

		require.NoError(t, state.Deal(deck.Standard(), 7))

		for _, p := range state.Players {
			assert.Len(t, p.Cards, 7, "%s replaces whatever it held", p.ID)
		}
		assert.Equal(t, 52-14, state.Deck.Size())
	})

	t.Run("a deck too small for the table is refused", func(t *testing.T) {
		t.Parallel()
		state := game.NewState(nil, []*game.Player{{ID: "p1"}}, nil)

		require.ErrorContains(t, state.Deal(deck.Standard()[:3], 7), "not enough cards")
	})
}
