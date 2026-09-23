package crazyeight

import (
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game/shed"
)

// State is the Crazy Eights state stored in game.State.Extra.
type State struct {
	// shed.State carries Passes: the deadlock counter every shedding game keeps.
	shed.State

	CurrentSuit deck.Suit
}
