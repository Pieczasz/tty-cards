package deck

import (
	"crypto/rand"
	mrand "math/rand/v2"
	"slices"
)

type Pile struct {
	cards []Card
}

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

func (p *Pile) Peek() (Card, bool) {
	if len(p.cards) < 1 {
		return Card{}, false
	}
	return p.cards[len(p.cards)-1], true
}

func (p *Pile) Draw() (Card, bool) {
	if len(p.cards) < 1 {
		return Card{}, false
	}
	lastIdx := len(p.cards) - 1
	topCard := p.cards[lastIdx]
	p.cards = p.cards[:lastIdx]
	return topCard, true
}

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

func (p *Pile) AddCard(cards ...Card) {
	p.cards = append(p.cards, cards...)
}

func (p *Pile) Size() int {
	return len(p.cards)
}

func (p *Pile) IsEmpty() bool {
	return len(p.cards) < 1
}

func (p *Pile) Cards() []Card {
	return slices.Clone(p.cards)
}
