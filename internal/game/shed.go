package game

import (
	"errors"
	"slices"

	"github.com/Pieczasz/terminal-card/internal/deck"
)

// reshuffleDiscardIntoStock moves the discard pile, except the card in play, back into
// an empty stock and shuffles, conserving every card. A non-empty stock is left alone:
// merging two piles would lose the order of the one still in play.
func reshuffleDiscardIntoStock(state *State) {
	if !state.Deck.IsEmpty() {
		return
	}
	top, ok := state.Discard.Draw()
	if !ok {
		return
	}
	rest := state.Discard.Cards()
	state.Discard = deck.New([]deck.Card{top})
	state.Deck.Add(rest...)
	state.Deck.Shuffle()
}

// ValidateShedPlay is the half of a shedding game's play check both games share: a card
// in play to match against, and the played card in the hand of the seat on turn. match
// is the game's own rule against the top card. A draw is always legal and never gets
// here: gating it on the discard would freeze a seat on a board that has none.
func ValidateShedPlay(state *State, card deck.Card, match func(top deck.Card) error) error {
	top, ok := state.Discard.Peek()
	if !ok {
		return errors.New("no cards in discard pile")
	}
	if !slices.Contains(state.Players[state.CurrentTurn].Cards, card) {
		return errors.New("you don't have that card")
	}
	return match(top)
}

// DrawWithReshuffle is a shedding game's draw: off the stock, refilled from under the
// card in play when it runs out. false means both piles are spent, which the caller
// counts as a forced pass.
func DrawWithReshuffle(state *State) (deck.Card, bool) {
	reshuffleDiscardIntoStock(state)
	return state.Deck.Draw()
}

// returnHandToStock keeps the deck whole when a player leaves, reshuffling so the cards
// they were seen holding are not the next ones dealt.
func returnHandToStock(state *State, playerID string) {
	i := slices.IndexFunc(state.Players, func(p *Player) bool { return p.ID == playerID })
	if i < 0 {
		return
	}
	p := state.Players[i]
	state.Deck.Add(p.Cards...)
	p.Cards = nil
	state.Deck.Shuffle()
}

// HandEmptyOrAllPassed is the shedding-game win check: a hand is out, or every seat in
// succession could not draw, which is a board with no legal move left rather than a loop.
func HandEmptyOrAllPassed(state *State, passes int) bool {
	if slices.ContainsFunc(state.Players, func(p *Player) bool { return len(p.Cards) == 0 }) {
		return true
	}
	return len(state.Players) > 0 && passes >= len(state.Players)
}

// ShedState is the state every shedding game keeps identically. Embed it in the game's
// own Extra type rather than copying the field and its reasoning per game.
type ShedState struct {
	// Passes counts consecutive turns where a draw yielded nothing because both the
	// stock and the discard are exhausted. At one per seat the hand is deadlocked and
	// ends, scored by fewest cards held. A forced draw that comes up empty charges the
	// count too, even though the victim never had a turn: it is the board that is out
	// of cards, and the seat it happened to is beside the point.
	Passes int
}

// ShedScore ranks a shedding game by cards still held, fewest first, so two players
// left holding the same number are reported as the draw they are.
func ShedScore(p *Player) int { return len(p.Cards) }

// ShedStandings is Rules.Standings for a shedding game.
func ShedStandings(state *State) []*Player {
	return StandingsByScore(state.Players, ShedScore)
}

// LeaveShedGame is a shedding game's OnPlayerLeave: the hand goes back to the stock and
// the deadlock counter resets. Left alone the count would be stale - the returned cards
// refill the stock - and measured against a table one seat smaller, which reads as a
// deadlock that never happened.
func LeaveShedGame(state *State, shed *ShedState, playerID string) {
	shed.Passes = 0
	returnHandToStock(state, playerID)
}

// OpenDiscard starts the discard pile on the first card the game may legally open on,
// setting the rest aside and shuffling them back into the stock: uno cannot open on a
// Wild and crazy eights cannot open on an Eight, because neither card names the suit
// every player would then be matching against.
func OpenDiscard(state *State, playable func(deck.Card) bool) (deck.Card, error) {
	var setAside []deck.Card
	for {
		card, ok := state.Deck.Draw()
		if !ok {
			// Every card was set aside, so the stock has to come back whole before
			// the caller reports a table it cannot open.
			state.Deck.Add(setAside...)
			return deck.Card{}, errors.New("not enough cards to start")
		}
		if !playable(card) {
			setAside = append(setAside, card)
			continue
		}
		state.Discard = deck.New([]deck.Card{card})
		if len(setAside) == 0 {
			return card, nil
		}
		state.Deck.Add(setAside...)
		state.Deck.Shuffle()
		return card, nil
	}
}

// DrawInto deals up to n cards into p's hand, reshuffling the discard when the stock
// runs dry, and reports whether any card came at all.
func DrawInto(state *State, p *Player, n int) bool {
	drew := false
	for range n {
		card, ok := DrawWithReshuffle(state)
		if !ok {
			break
		}
		p.Cards = append(p.Cards, card)
		drew = true
	}
	return drew
}

// RecordDraw keeps the deadlock count: a draw that yielded nothing is a pass, any card
// resets it.
func (s *ShedState) RecordDraw(drew bool) {
	if drew {
		s.Passes = 0
		return
	}
	s.Passes++
}
