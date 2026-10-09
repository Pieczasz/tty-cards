package crazyeight

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/gametest"
	"github.com/Pieczasz/tty-cards/internal/game/shed"
)

// suite is crazy eights described to the shared shedding-game suite.
var suite = gametest.Shed{
	Rules:      &Rules{},
	MinPlayers: MinPlayers,
	MaxPlayers: MaxPlayers,
	NewExtra:   func(top deck.Card) any { return &State{CurrentSuit: top.Suit} },
	ShedState:  func(e any) *shed.State { return &e.(*State).State },
	Opens:      func(c deck.Card) bool { return c.Rank != deck.Eight },
	Play:       func(c deck.Card, suit deck.Suit) game.Action { return ActionPlayCard{Card: c, ChosenSuit: suit} },
	Draw:       ActionDrawCard{},
}

func TestShedContract(t *testing.T) {
	t.Parallel()
	gametest.RunShed(t, suite)
}

func TestSoak_TimeoutActionIsAlwaysLegal(t *testing.T) {
	t.Parallel()
	gametest.SoakTimeoutIsAlwaysLegal(t, suite)
}
