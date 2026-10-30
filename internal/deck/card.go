// Package deck is the card mechanics every game shares: cards, suits and ranks, the
// three rank orderings, hand removal, and the Pile a stock or a discard is kept in.
package deck

import "slices"

// Card is one card. The zero Card is no card at all: standard ranks start at 1.
type Card struct {
	Rank Rank
	Suit Suit
}

// Suit is a card's suit; NoSuit is its zero value.
type Suit uint8

const (
	NoSuit Suit = iota
	Spades
	Hearts
	Diamonds
	Clubs
)

// Rank is a card's rank: Ace..King and Joker, then Uno's own block from 20.
type Rank uint8

// The standard ranks, 1-based so a zero Card is detectably empty, then the Joker.
const (
	Ace Rank = iota + 1
	Two
	Three
	Four
	Five
	Six
	Seven
	Eight
	Nine
	Ten
	Jack
	Queen
	King
	Joker
)

const (
	Zero Rank = iota + 20
	One
	Skip
	Reverse
	DrawTwo
	Wild
	WildDrawFour
)

// AllRanks is every Rank in ascending order: Ace..King, the Joker, then the Uno block.
// Tests in several packages walk it to cover every rank a card can carry, so it is
// shared and must be treated as read-only; nothing in production reads it.
var AllRanks = []Rank{
	Ace, Two, Three, Four, Five, Six, Seven, Eight, Nine, Ten, Jack, Queen, King, Joker,
	Zero, One, Skip, Reverse, DrawTwo, Wild, WildDrawFour,
}

// RankValue is a standard playing-card rank's comparison value, Ace high at 14.
// Anything outside Ace..King - the zero Rank, the Joker, the Uno block - has no
// comparison value and answers 0, the same as RunOrder and PipValue.
func RankValue(r Rank) int {
	switch {
	case r == Ace:
		return 14
	case r >= Two && r <= King:
		return int(r)
	default:
		return 0
	}
}

// RunOrder is a rank's place in a run, Ace low at 1 and every court distinct, for gin
// rummy melds. Anything outside Ace..King answers 0.
func RunOrder(r Rank) int {
	if r < Ace || r > King {
		return 0
	}
	return int(r)
}

// PipValue is a rank's deadwood count: courts count 10, and anything outside Ace..King
// answers 0.
func PipValue(r Rank) int {
	return min(RunOrder(r), 10)
}

// RemoveOne is hand without the first copy of card, as a new slice: the caller's hand
// is never aliased, whether card was in it.
func RemoveOne(hand []Card, card Card) []Card {
	i := slices.Index(hand, card)
	if i < 0 {
		return slices.Clone(hand)
	}
	return slices.Delete(slices.Clone(hand), i, i+1)
}

// RemoveEach is hand without one copy of each of cards, as a new slice.
func RemoveEach(hand []Card, cards []Card) []Card {
	out := slices.Clone(hand)
	for _, c := range cards {
		if i := slices.Index(out, c); i >= 0 {
			out = slices.Delete(out, i, i+1)
		}
	}
	return out
}

// IsSuit reports whether s is one of the four named suits: NoSuit and client garbage
// are not.
func IsSuit(s Suit) bool {
	switch s {
	case Spades, Hearts, Diamonds, Clubs:
		return true
	default:
		return false
	}
}
