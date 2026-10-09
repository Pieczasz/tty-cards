package uno

import (
	"slices"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"

	"github.com/Pieczasz/tty-cards/internal/game/shed"
)

// State holds Uno-specific game state stored in game.State.Extra.
type State struct {
	// shed.State carries Passes: the deadlock counter every shedding game keeps.
	shed.State

	CurrentColor deck.Suit // one of ColorRed/Yellow/Green/Blue once started
	Direction    int8      // +1 clockwise, -1 counterclockwise

	// leaverWasOnTurn is written by OnPlayerLeave and read by AfterPlayerRemoved.
	// Only the first sees whose turn it was, only the second sees the shifted seat
	// indices, and the direction-aware cursor needs both.
	leaverWasOnTurn bool
}

// IsWild reports whether c is a Wild or Wild Draw Four: the cards that name a colour
// rather than carry one.
func IsWild(c deck.Card) bool {
	return c.Rank == Wild || c.Rank == WildDrawFour
}

// hasColor reports whether the hand holds a playable card of the colour. Wilds sit
// on NoSuit, so they never count: holding one is not holding the colour.
func hasColor(hand []deck.Card, color deck.Suit) bool {
	return slices.ContainsFunc(hand, func(c deck.Card) bool { return !IsWild(c) && c.Suit == color })
}

// extraOf is the table's Extra, which OnGameStart put there. Anything else is a
// wiring bug, and the panic ends the one table as a rules error through the engine's
// recover (decision #37).
func extraOf(state *game.State) *State {
	return state.Extra.(*State) //nolint:forcetypeassert // a wiring bug; the engine's recover ends the table (decision #37)
}
