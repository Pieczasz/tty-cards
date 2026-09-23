package crazyeight

import (
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
)

// State is the Crazy Eights state stored in game.State.Extra.
type State struct {
	// ShedState carries Passes: the deadlock counter every shedding game keeps.
	game.ShedState

	CurrentSuit deck.Suit
}
