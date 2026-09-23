package uno

import "github.com/Pieczasz/terminal-card/internal/deck"

// Color aliases map Uno colors onto the four deck suits. Wilds use NoSuit.
const (
	ColorRed    = deck.Hearts
	ColorYellow = deck.Diamonds
	ColorGreen  = deck.Clubs
	ColorBlue   = deck.Spades
	ColorWild   = deck.NoSuit
)

// Rank aliases keep rules code readable without importing deck.Rank names that
// collide with everyday English (Zero/One/Skip/…).
const (
	Zero         = deck.Zero
	One          = deck.One
	Skip         = deck.Skip
	Reverse      = deck.Reverse
	DrawTwo      = deck.DrawTwo
	Wild         = deck.Wild
	WildDrawFour = deck.WildDrawFour
)

// initialDeck builds the standard 108-card Uno deck.
func initialDeck() []deck.Card {
	colors := []deck.Suit{ColorRed, ColorYellow, ColorGreen, ColorBlue}
	cards := make([]deck.Card, 0, 108)

	// Uno's Zero and One are its own ranks, not the standard block's, so the number
	// ranks are listed rather than ranged over.

	numbers := []deck.Rank{One, deck.Two, deck.Three, deck.Four, deck.Five, deck.Six, deck.Seven, deck.Eight, deck.Nine}
	for _, color := range colors {
		cards = append(cards, deck.Card{Rank: Zero, Suit: color})
		for _, r := range numbers {
			cards = append(cards, deck.Card{Rank: r, Suit: color}, deck.Card{Rank: r, Suit: color})
		}
		for _, r := range []deck.Rank{Skip, Reverse, DrawTwo} {
			cards = append(cards, deck.Card{Rank: r, Suit: color}, deck.Card{Rank: r, Suit: color})
		}
	}
	for range 4 {
		cards = append(cards,
			deck.Card{Rank: Wild, Suit: ColorWild},
			deck.Card{Rank: WildDrawFour, Suit: ColorWild},
		)
	}
	return cards
}
