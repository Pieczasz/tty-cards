package ginrummy

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandResult_Clone(t *testing.T) {
	t.Parallel()

	t.Run("nil clones to nil", func(t *testing.T) {
		t.Parallel()
		var result *HandResult
		assert.Nil(t, result.Clone())
	})

	t.Run("copies every field", func(t *testing.T) {
		t.Parallel()
		result := &HandResult{
			KnockerMelds:           [][]deck.Card{{c(deck.Two, deck.Hearts)}},
			KnockerDeadwood:        []deck.Card{c(deck.King, deck.Clubs)},
			KnockerDeadwoodPoints:  10,
			OpponentMelds:          [][]deck.Card{{c(deck.Ace, deck.Spades)}},
			OpponentDeadwood:       []deck.Card{c(deck.Nine, deck.Hearts)},
			OpponentDeadwoodPoints: 9,
			LaidOffCards:           []deck.Card{c(deck.Ten, deck.Spades)},
			Outcome:                OutcomeUndercut,
			ScoreDelta:             34,
			Winner:                 "p2",
		}
		assert.Equal(t, result, result.Clone())
	})

	// The view keeps this pointer and renders from it after releasing the engine lock.
	t.Run("the clone shares no backing array", func(t *testing.T) {
		t.Parallel()
		original := &HandResult{
			KnockerMelds:     [][]deck.Card{{c(deck.Two, deck.Hearts), c(deck.Three, deck.Hearts)}},
			OpponentMelds:    [][]deck.Card{{c(deck.Ace, deck.Spades)}},
			KnockerDeadwood:  []deck.Card{c(deck.King, deck.Clubs)},
			OpponentDeadwood: []deck.Card{c(deck.Nine, deck.Hearts)},
			LaidOffCards:     []deck.Card{c(deck.Ten, deck.Spades)},
		}
		clone := original.Clone()
		require.NotSame(t, original, clone)

		swap := c(deck.Four, deck.Diamonds)
		clone.KnockerMelds[0][0] = swap
		clone.OpponentMelds[0][0] = swap
		clone.KnockerDeadwood[0] = swap
		clone.OpponentDeadwood[0] = swap
		clone.LaidOffCards[0] = swap

		assert.Equal(t, c(deck.Two, deck.Hearts), original.KnockerMelds[0][0])
		assert.Equal(t, c(deck.Ace, deck.Spades), original.OpponentMelds[0][0])
		assert.Equal(t, c(deck.King, deck.Clubs), original.KnockerDeadwood[0])
		assert.Equal(t, c(deck.Nine, deck.Hearts), original.OpponentDeadwood[0])
		assert.Equal(t, c(deck.Ten, deck.Spades), original.LaidOffCards[0])
	})
}

// The label is what the knock log carries, so every settled outcome needs its own.
func TestOutcome_StringNamesEverySettledOutcome(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, o := range []Outcome{OutcomeKnock, OutcomeGin, OutcomeUndercut, OutcomeWall} {
		label := o.String()
		assert.NotEqual(t, OutcomeUnknown.String(), label, "outcome %d", o)
		assert.False(t, seen[label], "two outcomes share %q", label)
		seen[label] = true
	}
}
