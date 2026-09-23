package ginrummy

import (
	"slices"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

func c(rank deck.Rank, suit deck.Suit) deck.Card {
	return deck.Card{Rank: rank, Suit: suit}
}

func TestBestMeldSplit_PureRun(t *testing.T) {
	t.Parallel()
	hand := []deck.Card{
		c(deck.Five, deck.Hearts), c(deck.Six, deck.Hearts), c(deck.Seven, deck.Hearts),
		c(deck.Eight, deck.Hearts),
	}
	melds, dw, pts := bestMeldSplit(hand)
	assert.Empty(t, dw)
	assert.Equal(t, 0, pts)
	require.Len(t, melds, 1)
	assert.True(t, isRun(melds[0]))
}

func TestBestMeldSplit_PureSet(t *testing.T) {
	t.Parallel()
	for _, size := range []int{3, 4} {
		t.Run(string(rune('0'+size)), func(t *testing.T) {
			t.Parallel()
			suits := []deck.Suit{deck.Spades, deck.Hearts, deck.Diamonds, deck.Clubs}
			hand := make([]deck.Card, size)
			for i := range size {
				hand[i] = c(deck.King, suits[i])
			}
			_, dw, pts := bestMeldSplit(hand)
			assert.Empty(t, dw)
			assert.Equal(t, 0, pts)
		})
	}
}

func TestBestMeldSplit_MixedWithDeadwood(t *testing.T) {
	t.Parallel()
	hand := []deck.Card{
		c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts), c(deck.Ace, deck.Diamonds), // set
		c(deck.Four, deck.Clubs), c(deck.Five, deck.Clubs), c(deck.Six, deck.Clubs), // run
		c(deck.King, deck.Hearts), c(deck.Nine, deck.Spades), // deadwood 10+9
	}
	_, dw, pts := bestMeldSplit(hand)
	assert.Equal(t, 19, pts)
	assert.Len(t, dw, 2)
}

func TestBestMeldSplit_FullyDeadwood(t *testing.T) {
	t.Parallel()
	hand := []deck.Card{
		c(deck.Ace, deck.Spades), c(deck.Three, deck.Hearts), c(deck.Five, deck.Diamonds),
		c(deck.Seven, deck.Clubs), c(deck.Nine, deck.Spades),
	}
	melds, dw, pts := bestMeldSplit(hand)
	assert.Empty(t, melds)
	assert.Len(t, dw, 5)
	assert.Equal(t, 1+3+5+7+9, pts)
}

func TestBestMeldSplit_ExactGin(t *testing.T) {
	t.Parallel()
	hand := []deck.Card{
		c(deck.Two, deck.Hearts), c(deck.Three, deck.Hearts), c(deck.Four, deck.Hearts), c(deck.Five, deck.Hearts),
		c(deck.Jack, deck.Spades), c(deck.Jack, deck.Hearts), c(deck.Jack, deck.Diamonds),
		c(deck.Ace, deck.Clubs), c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts),
	}
	_, dw, pts := bestMeldSplit(hand)
	assert.Empty(t, dw)
	assert.Equal(t, 0, pts)
}

func TestBestMeldSplit_AmbiguousFourAces(t *testing.T) {
	t.Parallel()
	// Four aces + 2♠ + 3♠: set of 3 aces + A♠-2♠-3♠ run covers everything.
	hand := []deck.Card{
		c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts), c(deck.Ace, deck.Diamonds), c(deck.Ace, deck.Clubs),
		c(deck.Two, deck.Spades), c(deck.Three, deck.Spades),
	}
	_, dw, pts := bestMeldSplit(hand)
	assert.Empty(t, dw)
	assert.Equal(t, 0, pts)
}

func TestBestMeldSplit_AceLowRun_NoWraparound(t *testing.T) {
	t.Parallel()
	t.Run("A-2-3 valid", func(t *testing.T) {
		t.Parallel()
		hand := []deck.Card{
			c(deck.Ace, deck.Clubs), c(deck.Two, deck.Clubs), c(deck.Three, deck.Clubs),
		}
		_, dw, pts := bestMeldSplit(hand)
		assert.Empty(t, dw)
		assert.Equal(t, 0, pts)
	})
	t.Run("Q-K-A invalid", func(t *testing.T) {
		t.Parallel()
		hand := []deck.Card{
			c(deck.Queen, deck.Clubs), c(deck.King, deck.Clubs), c(deck.Ace, deck.Clubs),
		}
		melds, dw, pts := bestMeldSplit(hand)
		assert.Empty(t, melds)
		assert.Len(t, dw, 3)
		assert.Equal(t, 21, pts)
	})
}

func TestDeadwoodPoints(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1, deadwoodPoints(c(deck.Ace, deck.Spades)))
	assert.Equal(t, 5, deadwoodPoints(c(deck.Five, deck.Hearts)))
	assert.Equal(t, 10, deadwoodPoints(c(deck.Ten, deck.Clubs)))
	assert.Equal(t, 10, deadwoodPoints(c(deck.King, deck.Diamonds)))
}

// The classic greedy traps. Taking the highest-scoring meld first, or the longest run
// first, gets both of these wrong, and the difference is the whole knock.
func TestBestMeldSplit_BeatsGreedy(t *testing.T) {
	t.Parallel()

	t.Run("a set that steals a card the run needed", func(t *testing.T) {
		t.Parallel()
		hand := []deck.Card{
			c(deck.Seven, deck.Spades), c(deck.Eight, deck.Spades), c(deck.Nine, deck.Spades),
			c(deck.Seven, deck.Hearts), c(deck.Seven, deck.Diamonds),
		}
		melds, dw, pts := bestMeldSplit(hand)
		assert.Equal(t, 14, pts, "set-first leaves 8♠+9♠ = 17")
		require.Len(t, melds, 1)
		assert.True(t, isRun(melds[0]))
		assert.Len(t, dw, 2)
	})

	t.Run("a run that has to give a card back to a set", func(t *testing.T) {
		t.Parallel()
		hand := []deck.Card{
			c(deck.Four, deck.Spades), c(deck.Five, deck.Spades), c(deck.Six, deck.Spades),
			c(deck.Seven, deck.Spades),
			c(deck.Seven, deck.Hearts), c(deck.Seven, deck.Diamonds),
		}
		melds, dw, pts := bestMeldSplit(hand)
		assert.Equal(t, 0, pts, "the longest run leaves 7♥+7♦ = 14")
		assert.Empty(t, dw)
		assert.Len(t, melds, 2)
	})
}

// The candidate melds are uint16 index masks, so a hand past bit 15 cannot be
// searched. Unreachable in play (eleven cards is the most anyone holds) and
// TimeoutAction has no recover, so the oversized path must not panic.
func TestBestMeldSplit_OversizedHandIsAllDeadwood(t *testing.T) {
	t.Parallel()
	hand := deck.Standard()[:maskBits+1]

	var melds [][]deck.Card
	var dw []deck.Card
	var pts int
	assert.NotPanics(t, func() { melds, dw, pts = bestMeldSplit(hand) })
	assert.Empty(t, melds)
	assert.Equal(t, hand, dw)
	assert.Equal(t, sumDeadwood(hand), pts)

	assert.NotPanics(t, func() { bestMeldSplit(deck.Standard()[:maskBits]) },
		"the largest searchable hand is still searched")
}

// Every empty answer has to look the same, or a caller that reads len() on one and
// nil on the other is right by accident.
func TestBestMeldSplit_EmptyHandLooksLikeAnyOtherEmptyResult(t *testing.T) {
	t.Parallel()
	melds, dw, pts := bestMeldSplit(nil)

	assert.Empty(t, melds)
	assert.Empty(t, dw)
	assert.Equal(t, 0, pts)

	// A one-card hand is the nearest non-empty case: melds empty, deadwood the card.
	oneMelds, oneDW, onePts := bestMeldSplit([]deck.Card{c(deck.King, deck.Spades)})
	assert.Equal(t, melds, oneMelds, "no melds either way, and the same shape")
	assert.Len(t, oneDW, 1)
	assert.Equal(t, 10, onePts)
}

// The split is a partition: every card is in exactly one meld or in the deadwood, the
// points match the cards, and every meld is a real one.
func TestBestMeldSplit_IsAPartition(t *testing.T) {
	t.Parallel()
	full := deck.Standard()

	rapid.Check(t, func(rt *rapid.T) {
		size := rapid.IntRange(1, 11).Draw(rt, "size")
		idxs := rapid.SliceOfNDistinct(rapid.IntRange(0, len(full)-1), size, size,
			func(i int) int { return i }).Draw(rt, "cards")
		hand := make([]deck.Card, 0, size)
		for _, i := range idxs {
			hand = append(hand, full[i])
		}

		melds, dw, pts := bestMeldSplit(hand)

		require.Equal(rt, sumDeadwood(dw), pts, "the points must count the deadwood reported")

		seen := make([]deck.Card, 0, size)
		for _, meld := range melds {
			require.True(rt, isSet(meld) || isRun(meld), "not a meld: %v", meld)
			seen = append(seen, meld...)
		}
		counts := map[deck.Card]int{}
		for _, card := range append(slices.Clone(seen), dw...) {
			counts[card]++
			require.Equal(rt, 1, counts[card], "%v is in two places", card)
		}
		require.ElementsMatch(rt, hand, append(slices.Clone(seen), dw...),
			"melds plus deadwood must be exactly the hand")
	})
}

// greedyDeadwood is the arrangement a person plays: take the meld that sheds the most
// points, then the best of what is left, and so on. It is the baseline bestMeldSplit
// has to match or beat on every hand, because a knock is scored on the difference.
func greedyDeadwood(hand []deck.Card) int {
	cards := slices.Clone(hand)
	masks := generateMeldMasks(cards)
	points := func(mask uint16) int {
		total := 0
		for i := range cards {
			if mask&(1<<i) != 0 {
				total += deadwoodPoints(cards[i])
			}
		}
		return total
	}

	var used uint16
	for {
		var best uint16
		bestGain := 0
		for _, m := range masks {
			if used&m != 0 {
				continue
			}
			if gain := points(m); gain > bestGain {
				best, bestGain = m, gain
			}
		}
		if bestGain == 0 {
			return points(^used)
		}
		used |= best
	}
}

// FuzzBestMeldSplit is the meld search's contract on a hand nobody designed: the split
// is a partition of the hand into real melds plus deadwood, the points count exactly
// the deadwood reported, and it is never worse than playing greedily.
func FuzzBestMeldSplit(f *testing.F) {
	f.Add([]byte{0, 1, 2, 13, 14, 15, 26, 27, 28, 39}) // three runs and a spare
	f.Add([]byte{0, 13, 26, 39, 1, 14, 27, 40, 2, 15}) // sets everywhere
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0})        // every byte collides
	f.Add([]byte{7, 8, 9, 10, 20, 33, 46, 3, 17, 51})  // a run plus scattered deadwood
	f.Add([]byte{255, 128, 64, 32, 16, 8, 4, 2, 1, 0}) // wide spread

	full := deck.Standard()
	f.Fuzz(func(t *testing.T, raw []byte) {
		// A hand is a set of distinct cards; duplicates in the input are dropped
		// rather than rejected, so the fuzzer is not fighting the encoding.
		var hand []deck.Card
		for _, b := range raw {
			card := full[int(b)%len(full)]
			if !slices.Contains(hand, card) {
				hand = append(hand, card)
			}
			if len(hand) == dealCount+1 {
				break
			}
		}
		if len(hand) == 0 {
			return
		}

		melds, dw, pts := bestMeldSplit(hand)

		require.Equal(t, sumDeadwood(dw), pts, "the points must count the deadwood reported")

		seen := make([]deck.Card, 0, len(hand))
		for _, meld := range melds {
			require.True(t, isSet(meld) || isRun(meld), "not a meld: %v", meld)
			seen = append(seen, meld...)
		}
		counts := make(map[deck.Card]int, len(hand))
		for _, card := range append(slices.Clone(seen), dw...) {
			counts[card]++
			require.Equal(t, 1, counts[card], "%v is melded and deadwood at once", card)
		}
		require.ElementsMatch(t, hand, append(slices.Clone(seen), dw...),
			"melds plus deadwood must be exactly the hand")
		require.LessOrEqual(t, pts, greedyDeadwood(hand),
			"the search must never lose to playing greedily")
	})
}
