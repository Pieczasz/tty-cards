package game

import (
	"errors"

	"github.com/Pieczasz/tty-cards/internal/deck"
)

// Deal is every game's opening deal: a fresh stock of cards, shuffled, and n to every
// seat, replacing whatever each seat held. Hearts, gin rummy and the shedding games
// all deal this way, so a change to how a deal works lands in one place.
func (s *State) Deal(cards []deck.Card, n int) error {
	s.Deck = deck.New(cards)
	s.Deck.Shuffle()
	for _, p := range s.Players {
		hand, ok := s.Deck.DrawN(n)
		if !ok {
			return errors.New("not enough cards to deal")
		}
		p.Cards = hand
	}
	return nil
}
