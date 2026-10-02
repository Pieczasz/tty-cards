package crazyeight

import (
	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game/shed"
)

// State is the Crazy Eights state stored in game.State.Extra.
type State struct {
	// shed.State carries Passes: the deadlock counter every shedding game keeps.
	shed.State

	CurrentSuit deck.Suit
}
