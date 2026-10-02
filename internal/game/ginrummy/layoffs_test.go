package ginrummy

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/deck"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyLayoffs_ExtendRunEnds(t *testing.T) {
	t.Parallel()
	melds := [][]deck.Card{{
		c(deck.Four, deck.Hearts), c(deck.Five, deck.Hearts), c(deck.Six, deck.Hearts),
	}}
	t.Run("low end", func(t *testing.T) {
		t.Parallel()
		rem, laid := applyLayoffs([]deck.Card{c(deck.Three, deck.Hearts)}, melds)
		assert.Empty(t, rem)
		assert.Equal(t, []deck.Card{c(deck.Three, deck.Hearts)}, laid)
	})
	t.Run("high end", func(t *testing.T) {
		t.Parallel()
		rem, laid := applyLayoffs([]deck.Card{c(deck.Seven, deck.Hearts)}, melds)
		assert.Empty(t, rem)
		assert.Equal(t, []deck.Card{c(deck.Seven, deck.Hearts)}, laid)
	})
}

func TestApplyLayoffs_ExtendSet(t *testing.T) {
	t.Parallel()
	melds := [][]deck.Card{{
		c(deck.King, deck.Spades), c(deck.King, deck.Hearts), c(deck.King, deck.Diamonds),
	}}
	rem, laid := applyLayoffs([]deck.Card{c(deck.King, deck.Clubs)}, melds)
	assert.Empty(t, rem)
	assert.Equal(t, []deck.Card{c(deck.King, deck.Clubs)}, laid)
}

func TestApplyLayoffs_SetAtFourBlocks(t *testing.T) {
	t.Parallel()
	melds := [][]deck.Card{{
		c(deck.Ten, deck.Spades), c(deck.Ten, deck.Hearts),
		c(deck.Ten, deck.Diamonds), c(deck.Ten, deck.Clubs),
	}}
	rem, laid := applyLayoffs([]deck.Card{c(deck.Nine, deck.Spades)}, melds)
	assert.Len(t, rem, 1)
	assert.Empty(t, laid)
}

func TestApplyLayoffs_MultiPass(t *testing.T) {
	t.Parallel()
	// 5♥ attaches first; then 4♥ becomes legal on the extended run.
	melds := [][]deck.Card{{
		c(deck.Six, deck.Hearts), c(deck.Seven, deck.Hearts), c(deck.Eight, deck.Hearts),
	}}
	dead := []deck.Card{c(deck.Four, deck.Hearts), c(deck.Five, deck.Hearts)}
	rem, laid := applyLayoffs(dead, melds)
	assert.Empty(t, rem)
	assert.ElementsMatch(t, dead, laid, "both cards moved, and the loop knows which")
}

func TestApplyLayoffs_NoneEligible(t *testing.T) {
	t.Parallel()
	melds := [][]deck.Card{{
		c(deck.Two, deck.Clubs), c(deck.Three, deck.Clubs), c(deck.Four, deck.Clubs),
	}}
	dead := []deck.Card{c(deck.Ace, deck.Hearts), c(deck.King, deck.Spades)}
	rem, laid := applyLayoffs(dead, melds)
	assert.Equal(t, dead, rem)
	assert.Empty(t, laid)
}

// Runs before sets, and the fixture has to make the two choices score differently or
// it proves nothing. The knocker holds a set of eights and a spade run: 8♠ fits both,
// but only the run leaves 9♠ somewhere to go.
func TestApplyLayoffs_RunsBeforeSets(t *testing.T) {
	t.Parallel()
	melds := [][]deck.Card{
		{c(deck.Eight, deck.Hearts), c(deck.Eight, deck.Diamonds), c(deck.Eight, deck.Clubs)},
		{c(deck.Five, deck.Spades), c(deck.Six, deck.Spades), c(deck.Seven, deck.Spades)},
	}
	dead := []deck.Card{c(deck.Eight, deck.Spades), c(deck.Nine, deck.Spades)}

	rem, laid := applyLayoffs(dead, melds)

	assert.Empty(t, rem, "spending 8♠ on the set strands 9♠")
	assert.ElementsMatch(t, dead, laid)
}

// The knocker's melds are what the hand is scored on. Laying off grows a working copy,
// and appending to a meld with spare capacity would otherwise reach the caller's slice.
func TestApplyLayoffs_LeavesTheKnockerMeldsAlone(t *testing.T) {
	t.Parallel()
	// Capacity beyond the three cards is what makes an in-place append visible.
	run := make([]deck.Card, 3, 5)
	copy(run, []deck.Card{c(deck.Four, deck.Hearts), c(deck.Five, deck.Hearts), c(deck.Six, deck.Hearts)})
	melds := [][]deck.Card{run}
	want := [][]deck.Card{{
		c(deck.Four, deck.Hearts), c(deck.Five, deck.Hearts), c(deck.Six, deck.Hearts),
	}}

	rem, laid := applyLayoffs([]deck.Card{c(deck.Seven, deck.Hearts), c(deck.Three, deck.Hearts)}, melds)

	require.Empty(t, rem)
	require.Len(t, laid, 2)
	assert.Equal(t, want, melds, "the caller's melds are read-only to a layoff")
	assert.Equal(t, deck.Card{}, run[:cap(run)][3], "nothing was written past the meld")
}
