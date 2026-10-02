package poker

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/deck"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func card(rank deck.Rank, suit deck.Suit) deck.Card {
	return deck.Card{Rank: rank, Suit: suit}
}

func rankOf(score int) handRank {
	return handRank(score >> handRankShift)
}

func TestEvaluateHand_Categories(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cards []deck.Card
		want  handRank
	}{
		{
			name: "high card",
			cards: []deck.Card{
				card(deck.Ace, deck.Spades),
				card(deck.King, deck.Hearts),
				card(deck.Nine, deck.Diamonds),
				card(deck.Seven, deck.Clubs),
				card(deck.Two, deck.Spades),
			},
			want: rankHighCard,
		},
		{
			name: "pair",
			cards: []deck.Card{
				card(deck.Ace, deck.Spades),
				card(deck.Ace, deck.Hearts),
				card(deck.King, deck.Diamonds),
				card(deck.Seven, deck.Clubs),
				card(deck.Two, deck.Spades),
			},
			want: rankPair,
		},
		{
			name: "two pair",
			cards: []deck.Card{
				card(deck.Ace, deck.Spades),
				card(deck.Ace, deck.Hearts),
				card(deck.King, deck.Diamonds),
				card(deck.King, deck.Clubs),
				card(deck.Two, deck.Spades),
			},
			want: rankTwoPair,
		},
		{
			name: "three of a kind",
			cards: []deck.Card{
				card(deck.Nine, deck.Spades),
				card(deck.Nine, deck.Hearts),
				card(deck.Nine, deck.Diamonds),
				card(deck.King, deck.Clubs),
				card(deck.Two, deck.Spades),
			},
			want: rankThreeOfAKind,
		},
		{
			name: "straight",
			cards: []deck.Card{
				card(deck.Nine, deck.Spades),
				card(deck.Eight, deck.Hearts),
				card(deck.Seven, deck.Diamonds),
				card(deck.Six, deck.Clubs),
				card(deck.Five, deck.Spades),
			},
			want: rankStraight,
		},
		{
			name: "wheel straight A-2-3-4-5",
			cards: []deck.Card{
				card(deck.Ace, deck.Spades),
				card(deck.Two, deck.Hearts),
				card(deck.Three, deck.Diamonds),
				card(deck.Four, deck.Clubs),
				card(deck.Five, deck.Spades),
			},
			want: rankStraight,
		},
		{
			name: "flush",
			cards: []deck.Card{
				card(deck.Ace, deck.Hearts),
				card(deck.Jack, deck.Hearts),
				card(deck.Nine, deck.Hearts),
				card(deck.Four, deck.Hearts),
				card(deck.Two, deck.Hearts),
			},
			want: rankFlush,
		},
		{
			name: "full house",
			cards: []deck.Card{
				card(deck.King, deck.Spades),
				card(deck.King, deck.Hearts),
				card(deck.King, deck.Diamonds),
				card(deck.Two, deck.Clubs),
				card(deck.Two, deck.Spades),
			},
			want: rankFullHouse,
		},
		{
			name: "four of a kind",
			cards: []deck.Card{
				card(deck.Seven, deck.Spades),
				card(deck.Seven, deck.Hearts),
				card(deck.Seven, deck.Diamonds),
				card(deck.Seven, deck.Clubs),
				card(deck.Ace, deck.Spades),
			},
			want: rankFourOfAKind,
		},
		{
			name: "straight flush",
			cards: []deck.Card{
				card(deck.Nine, deck.Clubs),
				card(deck.Eight, deck.Clubs),
				card(deck.Seven, deck.Clubs),
				card(deck.Six, deck.Clubs),
				card(deck.Five, deck.Clubs),
			},
			want: rankStraightFlush,
		},
		{
			name: "royal flush",
			cards: []deck.Card{
				card(deck.Ace, deck.Spades),
				card(deck.King, deck.Spades),
				card(deck.Queen, deck.Spades),
				card(deck.Jack, deck.Spades),
				card(deck.Ten, deck.Spades),
			},
			want: rankStraightFlush,
		},
		{
			name: "steel wheel straight flush A-2-3-4-5",
			cards: []deck.Card{
				card(deck.Ace, deck.Diamonds),
				card(deck.Two, deck.Diamonds),
				card(deck.Three, deck.Diamonds),
				card(deck.Four, deck.Diamonds),
				card(deck.Five, deck.Diamonds),
			},
			want: rankStraightFlush,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := evaluateHand(tt.cards)
			assert.Equal(t, tt.want, rankOf(got), "score=%#x", got)
		})
	}
}

func TestEvaluateHand_Comparisons(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		winner []deck.Card
		loser  []deck.Card
	}{
		{
			name: "pair beats high card",
			winner: []deck.Card{
				card(deck.Two, deck.Spades), card(deck.Two, deck.Hearts),
				card(deck.Three, deck.Diamonds), card(deck.Four, deck.Clubs), card(deck.Five, deck.Spades),
			},
			loser: []deck.Card{
				card(deck.Ace, deck.Spades), card(deck.King, deck.Hearts),
				card(deck.Queen, deck.Diamonds), card(deck.Jack, deck.Clubs), card(deck.Nine, deck.Spades),
			},
		},
		{
			name: "higher pair wins",
			winner: []deck.Card{
				card(deck.Ace, deck.Spades), card(deck.Ace, deck.Hearts),
				card(deck.Three, deck.Diamonds), card(deck.Four, deck.Clubs), card(deck.Five, deck.Spades),
			},
			loser: []deck.Card{
				card(deck.King, deck.Spades), card(deck.King, deck.Hearts),
				card(deck.Ace, deck.Diamonds), card(deck.Queen, deck.Clubs), card(deck.Jack, deck.Spades),
			},
		},
		{
			name: "same pair higher kicker wins",
			winner: []deck.Card{
				card(deck.Ten, deck.Spades), card(deck.Ten, deck.Hearts),
				card(deck.Ace, deck.Diamonds), card(deck.King, deck.Clubs), card(deck.Two, deck.Spades),
			},
			loser: []deck.Card{
				card(deck.Ten, deck.Clubs), card(deck.Ten, deck.Diamonds),
				card(deck.Ace, deck.Spades), card(deck.Queen, deck.Hearts), card(deck.Two, deck.Hearts),
			},
		},
		{
			name: "flush beats straight",
			winner: []deck.Card{
				card(deck.Two, deck.Hearts), card(deck.Four, deck.Hearts),
				card(deck.Six, deck.Hearts), card(deck.Eight, deck.Hearts), card(deck.Nine, deck.Hearts),
			},
			loser: []deck.Card{
				card(deck.Ace, deck.Spades), card(deck.King, deck.Hearts),
				card(deck.Queen, deck.Diamonds), card(deck.Jack, deck.Clubs), card(deck.Ten, deck.Spades),
			},
		},
		{
			name: "broadway straight beats wheel",
			winner: []deck.Card{
				card(deck.Ace, deck.Spades), card(deck.King, deck.Hearts),
				card(deck.Queen, deck.Diamonds), card(deck.Jack, deck.Clubs), card(deck.Ten, deck.Spades),
			},
			loser: []deck.Card{
				card(deck.Ace, deck.Hearts), card(deck.Two, deck.Spades),
				card(deck.Three, deck.Diamonds), card(deck.Four, deck.Clubs), card(deck.Five, deck.Hearts),
			},
		},
		{
			name: "full house beats flush",
			winner: []deck.Card{
				card(deck.Three, deck.Spades), card(deck.Three, deck.Hearts), card(deck.Three, deck.Diamonds),
				card(deck.Two, deck.Clubs), card(deck.Two, deck.Spades),
			},
			loser: []deck.Card{
				card(deck.Ace, deck.Clubs), card(deck.King, deck.Clubs),
				card(deck.Queen, deck.Clubs), card(deck.Jack, deck.Clubs), card(deck.Nine, deck.Clubs),
			},
		},
		{
			name: "four of a kind beats full house",
			winner: []deck.Card{
				card(deck.Five, deck.Spades), card(deck.Five, deck.Hearts),
				card(deck.Five, deck.Diamonds), card(deck.Five, deck.Clubs), card(deck.Two, deck.Spades),
			},
			loser: []deck.Card{
				card(deck.Ace, deck.Spades), card(deck.Ace, deck.Hearts), card(deck.Ace, deck.Diamonds),
				card(deck.King, deck.Clubs), card(deck.King, deck.Spades),
			},
		},
		{
			name: "royal flush beats lower straight flush",
			winner: []deck.Card{
				card(deck.Ace, deck.Spades), card(deck.King, deck.Spades),
				card(deck.Queen, deck.Spades), card(deck.Jack, deck.Spades), card(deck.Ten, deck.Spades),
			},
			loser: []deck.Card{
				card(deck.Nine, deck.Hearts), card(deck.Eight, deck.Hearts),
				card(deck.Seven, deck.Hearts), card(deck.Six, deck.Hearts), card(deck.Five, deck.Hearts),
			},
		},
		{
			name: "higher two pair wins",
			winner: []deck.Card{
				card(deck.Ace, deck.Spades), card(deck.Ace, deck.Hearts),
				card(deck.Two, deck.Diamonds), card(deck.Two, deck.Clubs), card(deck.Three, deck.Spades),
			},
			loser: []deck.Card{
				card(deck.King, deck.Spades), card(deck.King, deck.Hearts),
				card(deck.Queen, deck.Diamonds), card(deck.Queen, deck.Clubs), card(deck.Ace, deck.Clubs),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			winScore := evaluateHand(tt.winner)
			loseScore := evaluateHand(tt.loser)
			assert.Greater(t, winScore, loseScore,
				"winner=%#x (%v) loser=%#x (%v)", winScore, rankOf(winScore), loseScore, rankOf(loseScore))
		})
	}
}

func TestEvaluateHand_BestOfSeven(t *testing.T) {
	t.Parallel()

	// Hole: junk + board makes a flush using 5 hearts from the 7 cards.
	cards := []deck.Card{
		card(deck.Two, deck.Clubs), // hole junk
		card(deck.Three, deck.Clubs),
		card(deck.Ace, deck.Hearts),
		card(deck.King, deck.Hearts),
		card(deck.Nine, deck.Hearts),
		card(deck.Four, deck.Hearts),
		card(deck.Two, deck.Hearts),
	}
	score := evaluateHand(cards)
	assert.Equal(t, rankFlush, rankOf(score))

	// Prefer full house over flush when both available in 7 cards.
	fullHouseBoard := []deck.Card{
		card(deck.Ace, deck.Spades),
		card(deck.Ace, deck.Hearts),
		card(deck.Ace, deck.Diamonds),
		card(deck.King, deck.Clubs),
		card(deck.King, deck.Spades),
		card(deck.Two, deck.Hearts),
		card(deck.Three, deck.Hearts),
	}
	assert.Equal(t, rankFullHouse, rankOf(evaluateHand(fullHouseBoard)))
}

func TestEvaluateHand_EdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("fewer than five cards scores zero", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, 0, evaluateHand(nil))
		assert.Equal(t, 0, evaluateHand([]deck.Card{
			card(deck.Ace, deck.Spades),
			card(deck.King, deck.Hearts),
			card(deck.Queen, deck.Diamonds),
			card(deck.Jack, deck.Clubs),
		}))
	})

	t.Run("identical hands tie", func(t *testing.T) {
		t.Parallel()
		a := []deck.Card{
			card(deck.Ace, deck.Spades), card(deck.Ace, deck.Hearts),
			card(deck.King, deck.Diamonds), card(deck.Queen, deck.Clubs), card(deck.Two, deck.Spades),
		}
		b := []deck.Card{
			card(deck.Ace, deck.Clubs), card(deck.Ace, deck.Diamonds),
			card(deck.King, deck.Spades), card(deck.Queen, deck.Hearts), card(deck.Two, deck.Hearts),
		}
		assert.Equal(t, evaluateHand(a), evaluateHand(b))
	})

	t.Run("trips plus trips becomes full house", func(t *testing.T) {
		t.Parallel()
		// 7 cards: AAA KKK 2 -> full house Aces full of Kings
		cards := []deck.Card{
			card(deck.Ace, deck.Spades), card(deck.Ace, deck.Hearts), card(deck.Ace, deck.Diamonds),
			card(deck.King, deck.Clubs), card(deck.King, deck.Spades), card(deck.King, deck.Hearts),
			card(deck.Two, deck.Clubs),
		}
		assert.Equal(t, rankFullHouse, rankOf(evaluateHand(cards)))
	})
}

func TestEvaluateHand_OrderingMonotonicByCategory(t *testing.T) {
	t.Parallel()

	// One representative hand per category, weakest -> strongest category order.
	samples := [][]deck.Card{
		{ // HighCard
			card(deck.King, deck.Spades), card(deck.Queen, deck.Hearts),
			card(deck.Jack, deck.Diamonds), card(deck.Nine, deck.Clubs), card(deck.Two, deck.Spades),
		},
		{ // Pair
			card(deck.Two, deck.Spades), card(deck.Two, deck.Hearts),
			card(deck.Three, deck.Diamonds), card(deck.Four, deck.Clubs), card(deck.Five, deck.Spades),
		},
		{ // TwoPair
			card(deck.Three, deck.Spades), card(deck.Three, deck.Hearts),
			card(deck.Two, deck.Diamonds), card(deck.Two, deck.Clubs), card(deck.Four, deck.Spades),
		},
		{ // ThreeOfAKind
			card(deck.Four, deck.Spades), card(deck.Four, deck.Hearts), card(deck.Four, deck.Diamonds),
			card(deck.Two, deck.Clubs), card(deck.Three, deck.Spades),
		},
		{ // Straight
			card(deck.Five, deck.Spades), card(deck.Six, deck.Hearts), card(deck.Seven, deck.Diamonds),
			card(deck.Eight, deck.Clubs), card(deck.Nine, deck.Spades),
		},
		{ // Flush
			card(deck.Two, deck.Clubs), card(deck.Four, deck.Clubs), card(deck.Six, deck.Clubs),
			card(deck.Eight, deck.Clubs), card(deck.Nine, deck.Clubs),
		},
		{ // FullHouse
			card(deck.Five, deck.Spades), card(deck.Five, deck.Hearts), card(deck.Five, deck.Diamonds),
			card(deck.Two, deck.Clubs), card(deck.Two, deck.Spades),
		},
		{ // FourOfAKind
			card(deck.Six, deck.Spades), card(deck.Six, deck.Hearts),
			card(deck.Six, deck.Diamonds), card(deck.Six, deck.Clubs), card(deck.Two, deck.Spades),
		},
		{ // StraightFlush
			card(deck.Five, deck.Hearts), card(deck.Six, deck.Hearts), card(deck.Seven, deck.Hearts),
			card(deck.Eight, deck.Hearts), card(deck.Nine, deck.Hearts),
		},
	}

	prev := -1
	for i, cards := range samples {
		score := evaluateHand(cards)
		require.Equal(t, rankHighCard+handRank(i), rankOf(score), "sample %d category mismatch", i)
		assert.Greater(t, score, prev, "category %d should outrank previous", i)
		prev = score
	}
}

// Two quads in one hand can only happen at eight cards or more, which is beyond a
// hold'em showdown but reachable through the evaluator itself. rankCounts used to
// overwrite the quad rank as it counted down, keeping the lower of the two.
func TestClassifyHand_TwoQuadsKeepsTheHigher(t *testing.T) {
	t.Parallel()

	cards := []deck.Card{
		card(deck.Ace, deck.Spades), card(deck.Ace, deck.Hearts),
		card(deck.Ace, deck.Diamonds), card(deck.Ace, deck.Clubs),
		card(deck.Two, deck.Spades), card(deck.Two, deck.Hearts),
		card(deck.Two, deck.Diamonds), card(deck.Two, deck.Clubs),
	}

	hv := classifyHand(cards)

	assert.Equal(t, rankFourOfAKind, hv.Rank)
	assert.Equal(t, [5]int{14, 14, 14, 14, 2}, hv.Kickers, "quad aces with a deuce kicker")
	assert.Equal(t, bestOfFive(cards), hv.score())
}

// Two suits reach five only at ten cards or more. bestFlush picked the strongest
// flush and looked for a straight in that suit alone, so a straight flush sitting in
// the weaker suit was scored as a plain flush.
func TestClassifyHand_StraightFlushInTheWeakerSuit(t *testing.T) {
	t.Parallel()

	cards := []deck.Card{
		// Spades hold the higher flush, ace-high, but no straight.
		card(deck.Ace, deck.Spades), card(deck.King, deck.Spades),
		card(deck.Queen, deck.Spades), card(deck.Nine, deck.Spades),
		card(deck.Two, deck.Spades),
		// Hearts hold nine-high, and a straight flush.
		card(deck.Nine, deck.Hearts), card(deck.Eight, deck.Hearts),
		card(deck.Seven, deck.Hearts), card(deck.Six, deck.Hearts),
		card(deck.Five, deck.Hearts),
	}

	hv := classifyHand(cards)

	assert.Equal(t, rankStraightFlush, hv.Rank)
	assert.Equal(t, 9, hv.Kickers[0], "nine-high straight flush")
}

func FuzzEvaluateHand(f *testing.F) {
	f.Add(uint8(0), uint8(0), uint8(1), uint8(1), uint8(2), uint8(2), uint8(3), uint8(3), uint8(4), uint8(0))
	f.Add(uint8(0), uint8(0), uint8(12), uint8(0), uint8(11), uint8(0), uint8(10), uint8(0), uint8(9), uint8(0))

	f.Fuzz(func(t *testing.T, r0, s0, r1, s1, r2, s2, r3, s3, r4, s4 uint8) {
		mk := func(r, s uint8) deck.Card {
			return deck.Card{
				// Standard ranks are 1-based, so the offset matters: without it the
				// generator produced rank 0 - a card no deck holds - and never a King.
				Rank: deck.Rank(r%13) + deck.Ace, // Ace..King
				Suit: deck.Suit(s%4) + deck.Spades,
			}
		}
		cards := []deck.Card{mk(r0, s0), mk(r1, s1), mk(r2, s2), mk(r3, s3), mk(r4, s4)}
		score := evaluateHand(cards)
		if score < 0 {
			t.Fatalf("negative score: %d", score)
		}
		hr := rankOf(score)
		if hr < rankHighCard || hr > rankStraightFlush {
			t.Fatalf("hand rank out of range: %d score=%#x cards=%v", hr, score, cards)
		}
	})
}

// evaluateHand runs once per player per showdown, over 7 cards, and allocates on
// every call - it is the hottest function in the rules layer.
func BenchmarkEvaluateHand(b *testing.B) {
	sevenCards := []deck.Card{
		card(deck.Ace, deck.Spades), card(deck.King, deck.Spades),
		card(deck.Queen, deck.Spades), card(deck.Jack, deck.Spades),
		card(deck.Ten, deck.Spades), card(deck.Two, deck.Hearts),
		card(deck.Three, deck.Diamonds),
	}

	b.Run("straight-flush-7", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = evaluateHand(sevenCards)
		}
	})

	b.Run("high-card-7", func(b *testing.B) {
		highCard := []deck.Card{
			card(deck.Two, deck.Spades), card(deck.Four, deck.Hearts),
			card(deck.Six, deck.Diamonds), card(deck.Eight, deck.Clubs),
			card(deck.Ten, deck.Spades), card(deck.Queen, deck.Hearts),
			card(deck.Ace, deck.Diamonds),
		}
		b.ReportAllocs()
		for b.Loop() {
			_ = evaluateHand(highCard)
		}
	})
}

// FuzzClassifyHand complements FuzzEvaluateHand above, which fixes the input at
// exactly five valid cards. This one varies the hand size and admits Joker and
// NoSuit, so it reaches the length-dependent index arithmetic: straightHigh reads
// unique[i+4], and bestFlush and kickers slice by count.
func FuzzClassifyHand(f *testing.F) {
	f.Add([]byte{0, 0, 1, 1, 2, 2, 3})       // pairs
	f.Add([]byte{0, 12, 11, 10, 9, 8, 7})    // broadway-ish
	f.Add([]byte{})                          // no cards
	f.Add([]byte{5})                         // fewer than five cards
	f.Add([]byte{13, 13, 13, 13, 13, 13, 4}) // jokers and a repeat

	f.Fuzz(func(t *testing.T, raw []byte) {
		cards := make([]deck.Card, 0, len(raw))
		for _, b := range raw {
			cards = append(cards, deck.Card{
				Rank: deck.Rank(b % 14),     // Ace..Joker
				Suit: deck.Suit(b / 14 % 5), // NoSuit..Clubs
			})
		}

		got := evaluateHand(cards)
		// Determinism is the property worth pinning: bestFlush once walked a map and
		// returned a different flush between runs for identical input.
		assert.Equal(t, got, evaluateHand(cards), "evaluateHand must be deterministic")

		if len(cards) < 5 {
			assert.Zero(t, got, "fewer than five cards has no hand value")
			return
		}
		assert.Positive(t, got, "five or more cards always classify to something")

		// The real oracle: the classifier claims the best five-card hand, so it has
		// to agree with actually trying all of them. Bounded because the subset count
		// explodes, and skipped on duplicate cards - a rank appearing five times is
		// not a hand any deck can deal, and there quads legitimately go undetected.
		if len(cards) <= 9 && !hasDuplicateCard(cards) {
			assert.Equal(t, bestOfFive(cards), got, "classifier disagrees with brute force")
		}
	})
}

func hasDuplicateCard(cards []deck.Card) bool {
	seen := make(map[deck.Card]bool, len(cards))
	for _, c := range cards {
		if seen[c] {
			return true
		}
		seen[c] = true
	}
	return false
}

// bestOfFive is the brute-force reference: the highest score over every five-card
// subset. Too slow for the evaluator itself, fast enough to check it.
func bestOfFive(cards []deck.Card) int {
	best := 0
	var pick func(start int, chosen []deck.Card)
	pick = func(start int, chosen []deck.Card) {
		if len(chosen) == 5 {
			best = max(best, evaluateHand(chosen))
			return
		}
		for i := start; i < len(cards); i++ {
			pick(i+1, append(chosen, cards[i]))
		}
	}
	pick(0, make([]deck.Card, 0, 5))
	return best
}
