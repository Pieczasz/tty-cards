package crazyeight

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/game/gametest"
	"github.com/Pieczasz/terminal-card/internal/game/shed"

	"github.com/stretchr/testify/require"
)

// suite is crazy eights described to the shared shedding-game suite.
var suite = gametest.Shed{
	Rules:     &Rules{},
	NewExtra:  func(top deck.Card) any { return &State{CurrentSuit: top.Suit} },
	ShedState: func(e any) *shed.State { return &e.(*State).State },
	Opens:     func(c deck.Card) bool { return c.Rank != deck.Eight },
	Play:      func(c deck.Card) game.Action { return ActionPlayCard{Card: c} },
	Draw:      ActionDrawCard{},
}

func TestShedContract(t *testing.T) {
	t.Parallel()
	gametest.RunShed(t, suite)
}

// extra is the crazy eights state of a table, failing the test on anything else.
func extra(t *testing.T, s *game.State) *State {
	t.Helper()
	e, ok := s.Extra.(*State)
	require.True(t, ok, "state.Extra is %T, not *crazyeight.State", s.Extra)
	return e
}

func TestSoak_TimeoutActionIsAlwaysLegal(t *testing.T) {
	t.Parallel()
	gametest.SoakTimeoutIsAlwaysLegal(t, suite)
}
