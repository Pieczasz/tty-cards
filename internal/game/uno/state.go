package uno

import (
	"slices"

	"github.com/Pieczasz/terminal-card/internal/deck"

	"github.com/Pieczasz/terminal-card/internal/game"
)

// State holds Uno-specific game state stored in game.State.Extra.
type State struct {
	// ShedState carries Passes: the deadlock counter every shedding game keeps.
	game.ShedState

	CurrentColor deck.Suit // one of ColorRed/Yellow/Green/Blue once started
	Direction    int8      // +1 clockwise, -1 counterclockwise

	// leaverWasOnTurn is written by OnPlayerLeave and read by AfterPlayerRemoved.
	// Only the first sees whose turn it was, only the second sees the shifted seat
	// indices, and the direction-aware cursor needs both.
	leaverWasOnTurn bool
}

func isWild(r deck.Rank) bool {
	return r == Wild || r == WildDrawFour
}

// hasColor reports whether the hand holds a playable card of the colour. Wilds sit
// on NoSuit, so they never count: holding one is not holding the colour.
func hasColor(hand []deck.Card, color deck.Suit) bool {
	return slices.ContainsFunc(hand, func(c deck.Card) bool { return !isWild(c.Rank) && c.Suit == color })
}
