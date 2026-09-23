package uno

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/game/gametest"

	"github.com/stretchr/testify/require"
)

// shed is uno described to the shared shedding-game suite.
var shed = gametest.Shed{
	Rules:     &Rules{},
	NewExtra:  func(top deck.Card) any { return &State{CurrentColor: top.Suit, Direction: 1} },
	ShedState: func(e any) *game.ShedState { return &e.(*State).ShedState },
	Opens:     func(c deck.Card) bool { return !isWild(c.Rank) },
	Play:      func(c deck.Card) game.Action { return ActionPlayCard{Card: c} },
	Draw:      ActionDrawCard{},
}

func TestShedContract(t *testing.T) {
	t.Parallel()
	gametest.RunShed(t, shed)
}

// extra is the uno state of a table, failing the test on anything else.
func extra(t *testing.T, s *game.State) *State {
	t.Helper()
	e, ok := s.Extra.(*State)
	require.True(t, ok, "state.Extra is %T, not *uno.State", s.Extra)
	return e
}

func TestSoak_TimeoutActionIsAlwaysLegal(t *testing.T) {
	t.Parallel()
	gametest.SoakTimeoutIsAlwaysLegal(t, shed)
}
