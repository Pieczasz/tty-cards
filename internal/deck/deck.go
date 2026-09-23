package deck

import (
	"crypto/rand"
	mrand "math/rand/v2"
	"slices"
)

// Pile is an ordered stack of cards - a stock or a discard - drawn from the top, the
// end of the slice.
type Pile struct {
	cards []Card
}

// New is a pile of a copy of cards, the last one on top.
func New(cards []Card) *Pile {
	return &Pile{
		cards: slices.Clone(cards),
	}
}

// Shuffle is a uniform permutation from a stdlib shuffle seeded once per call from
// crypto/rand, so the order is unpredictable to a player who has seen every previous
// deal. It cannot fail: since Go 1.24 crypto/rand.Read never returns an error (it
// aborts the process on an OS failure), so the error every caller used to plumb
// through was an unreachable branch dressed as resilience.
func (p *Pile) Shuffle() {
	var seed [32]byte
	_, _ = rand.Read(seed[:])
	mrand.New(mrand.NewChaCha8(seed)).Shuffle(len(p.cards), func(i, j int) { //nolint:gosec // G404: seeded from crypto/rand just above
		p.cards[i], p.cards[j] = p.cards[j], p.cards[i]
	})
}

// Peek is the top card without drawing it; false on an empty pile.
func (p *Pile) Peek() (Card, bool) {
	if len(p.cards) < 1 {
		return Card{}, false
	}
	return p.cards[len(p.cards)-1], true
}

// Draw takes the top card; false on an empty pile.
func (p *Pile) Draw() (Card, bool) {
	if len(p.cards) < 1 {
		return Card{}, false
	}
	lastIdx := len(p.cards) - 1
	topCard := p.cards[lastIdx]
	p.cards = p.cards[:lastIdx]
	return topCard, true
}

// DrawNCards takes the top cardsToDraw cards, topmost first. Asking for more than the
// pile holds draws nothing and answers false.
func (p *Pile) DrawNCards(cardsToDraw int) ([]Card, bool) {
	if cardsToDraw < 0 || cardsToDraw > len(p.cards) {
		return nil, false
	}

	splitIdx := len(p.cards) - cardsToDraw
	nCards := make([]Card, cardsToDraw)
	copy(nCards, p.cards[splitIdx:])
	p.cards = p.cards[:splitIdx]

	slices.Reverse(nCards)
	return nCards, true
}

// AddCard puts cards on top, the last one uppermost.
func (p *Pile) AddCard(cards ...Card) {
	p.cards = append(p.cards, cards...)
}

// Size is how many cards the pile holds.
func (p *Pile) Size() int {
	return len(p.cards)
}

// IsEmpty reports whether the pile holds no cards.
func (p *Pile) IsEmpty() bool {
	return len(p.cards) < 1
}

// Cards is a copy of the pile, bottom first.
func (p *Pile) Cards() []Card {
	return slices.Clone(p.cards)
}
