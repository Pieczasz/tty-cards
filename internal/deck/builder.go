package deck

// StandardDeck is the 52 cards of a standard deck, no jokers, in suit then rank order.
func StandardDeck() []Card {
	cards := make([]Card, 0, 52)
	for s := Spades; s <= Clubs; s++ {
		for r := Ace; r <= King; r++ {
			cards = append(cards, Card{
				Suit: s,
				Rank: r,
			})
		}
	}
	return cards
}
