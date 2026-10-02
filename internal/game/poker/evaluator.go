package poker

import (
	"cmp"
	"slices"

	"github.com/Pieczasz/terminal-card/internal/deck"
)

type handRank int

const (
	rankUnknown handRank = iota
	rankHighCard
	rankPair
	rankTwoPair
	rankThreeOfAKind
	rankStraight
	rankFlush
	rankFullHouse
	rankFourOfAKind
	rankStraightFlush
)

const (
	handRankShift = 20
	kickerShift0  = 16
	kickerShift1  = 12
	kickerShift2  = 8
	kickerShift3  = 4
)

// handValue is a classified poker hand with kickers for tiebreaking.
type handValue struct {
	Rank    handRank
	Kickers [5]int // descending importance; unused slots are 0
}

// score packs Rank and Kickers into a comparable integer.
// Encoding: (handRank << 20) | (k0 << 16) | (k1 << 12) | (k2 << 8) | (k3 << 4) | k4.
func (h handValue) score() int {
	return (int(h.Rank) << handRankShift) |
		(h.Kickers[0] << kickerShift0) |
		(h.Kickers[1] << kickerShift1) |
		(h.Kickers[2] << kickerShift2) |
		(h.Kickers[3] << kickerShift3) |
		h.Kickers[4]
}

func newHandValue(rank handRank, kickers ...int) handValue {
	hv := handValue{Rank: rank}
	copy(hv.Kickers[:], kickers)
	return hv
}

type rankedCard struct {
	rank int
	suit deck.Suit
}

// evaluateHand evaluates up to 7 cards and returns a score that can be directly compared.
func evaluateHand(cards []deck.Card) int {
	return classifyHand(cards).score()
}

// handSize is the five cards a poker hand is made of, whatever it is picked from.
const handSize = 5

// handShape is what the counting passes found in a hand, which is everything classify
// decides on besides the straight.
type handShape struct {
	quadRank, tripRank int
	pairs              []int
	// flushRanks is nil without a flush; straightFlushHigh is 0 without one.
	flushRanks        []int
	straightFlushHigh int
}

// classifyHand returns the best 5-card hand value from the given cards.
// Hands with fewer than 5 cards yield a zero handValue (score 0).
func classifyHand(cards []deck.Card) handValue {
	if len(cards) < handSize {
		return handValue{}
	}
	hand := normalizeCards(cards)
	var shape handShape
	shape.quadRank, shape.tripRank, shape.pairs = rankCounts(hand)
	shape.flushRanks, shape.straightFlushHigh = bestFlush(hand)
	return classify(hand, shape)
}

func normalizeCards(cards []deck.Card) []rankedCard {
	hand := make([]rankedCard, 0, len(cards))
	for _, c := range cards {
		hand = append(hand, rankedCard{rank: deck.RankValue(c.Rank), suit: c.Suit})
	}
	slices.SortFunc(hand, func(a, b rankedCard) int {
		return cmp.Compare(b.rank, a.rank)
	})
	return hand
}

// maxRankValue is the largest deck.RankValue a card can carry (ace, and joker with it),
// so a rank indexes an array rather than hashing into a map.
const maxRankValue = 14

func rankCounts(hand []rankedCard) (quadRank, tripRank int, pairs []int) {
	var counts [maxRankValue + 1]int
	for _, c := range hand {
		counts[c.rank]++
	}
	// Ranks descend, so the first quad and the first trip found are the highest ones;
	// a second trip is only ever worth a pair, and a second quad nothing at all.
	for r := maxRankValue; r >= 2; r-- {
		switch counts[r] {
		case 4:
			if quadRank == 0 {
				quadRank = r
			}
		case 3:
			if tripRank == 0 {
				tripRank = r
			} else {
				pairs = append(pairs, r)
			}
		case 2:
			pairs = append(pairs, r)
		}
	}
	return quadRank, tripRank, pairs
}

// bestFlush returns the ranks (desc) of the strongest flush and the high card of the
// strongest straight flush, or nil and 0 when there is none.
//
// Every flush suit is checked for a straight flush, not only the strongest flush: at
// ten or more cards two suits can both reach five, and the weaker flush is the one
// that may hold the straight.
//
// Suits are walked in a fixed order rather than taken from map iteration: on a hand
// large enough for two suits to reach five, ranging the map would score identical
// input differently between runs. NoSuit is absent because it marks a joker.
func bestFlush(hand []rankedCard) (best []int, straightFlushHigh int) {
	suits := make(map[deck.Suit][]int, 4)
	for _, c := range hand {
		suits[c.suit] = append(suits[c.suit], c.rank)
	}

	for _, suit := range []deck.Suit{deck.Spades, deck.Hearts, deck.Diamonds, deck.Clubs} {
		ranks := suits[suit]
		if len(ranks) < handSize {
			continue
		}
		if best == nil || slices.Compare(ranks[:handSize], best[:handSize]) > 0 {
			best = ranks
		}
		straightFlushHigh = max(straightFlushHigh, straightHigh(ranks))
	}
	return best, straightFlushHigh
}

func ranksOf(hand []rankedCard) []int {
	ranks := make([]int, len(hand))
	for i, c := range hand {
		ranks[i] = c.rank
	}
	return ranks
}

// wheelMask is A-5-4-3-2 as rank bits: the one straight whose ranks are not
// consecutive, because the ace plays low in it.
const wheelMask = 1<<14 | 1<<5 | 1<<4 | 1<<3 | 1<<2

// straightHigh returns the high card of the best straight in ranks, or 0.
// Wheel (A-2-3-4-5) returns 5. ranks must be sorted descending; duplicates are fine.
//
// It walks the descending run in place rather than deduplicating first, so the hottest
// check in the evaluator - every classify runs it at least once - allocates nothing.
// The first run of five it reaches is the highest straight, since the ranks descend.
func straightHigh(ranks []int) int {
	if len(ranks) == 0 {
		return 0
	}
	var seen uint16
	seen |= 1 << ranks[0]
	run, high := 1, ranks[0]
	for i := 1; i < len(ranks); i++ {
		seen |= 1 << ranks[i]
		switch ranks[i] {
		case ranks[i-1]: // a duplicate rank neither extends nor breaks the run
		case ranks[i-1] - 1:
			run++
			if run == handSize {
				return high
			}
		default:
			run, high = 1, ranks[i]
		}
	}
	if seen&wheelMask == wheelMask {
		return 5
	}
	return 0
}

func kickers(hand []rankedCard, exclude []int, count int) []int {
	k := make([]int, 0, count)
	for _, c := range hand {
		if slices.Contains(exclude, c.rank) {
			continue
		}
		k = append(k, c.rank)
		if len(k) == count {
			break
		}
	}
	for len(k) < count {
		k = append(k, 0)
	}
	return k
}

func classify(hand []rankedCard, s handShape) handValue {
	straight := straightHigh(ranksOf(hand))
	quad, trip, pairs, flush := s.quadRank, s.tripRank, s.pairs, s.flushRanks

	switch {
	case s.straightFlushHigh > 0:
		return newHandValue(rankStraightFlush, s.straightFlushHigh)
	case quad > 0:
		k := kickers(hand, []int{quad}, 1)
		return newHandValue(rankFourOfAKind, quad, quad, quad, quad, k[0])
	case trip > 0 && len(pairs) > 0:
		return newHandValue(rankFullHouse, trip, trip, trip, pairs[0], pairs[0])
	case flush != nil:
		return newHandValue(rankFlush, flush[:handSize]...)
	case straight > 0:
		return newHandValue(rankStraight, straight)
	case trip > 0:
		k := kickers(hand, []int{trip}, 2)
		return newHandValue(rankThreeOfAKind, trip, trip, trip, k[0], k[1])
	case len(pairs) >= 2:
		k := kickers(hand, []int{pairs[0], pairs[1]}, 1)
		return newHandValue(rankTwoPair, pairs[0], pairs[0], pairs[1], pairs[1], k[0])
	case len(pairs) == 1:
		k := kickers(hand, []int{pairs[0]}, 3)
		return newHandValue(rankPair, pairs[0], pairs[0], k[0], k[1], k[2])
	default:
		return newHandValue(rankHighCard, kickers(hand, nil, handSize)...)
	}
}
