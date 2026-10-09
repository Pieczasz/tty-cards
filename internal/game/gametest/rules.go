//nolint:thelper // the case functions are subtest bodies, not helpers: a failure belongs on their own line
package gametest

import (
	"fmt"
	"log/slog"
	"slices"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/game"

	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// Game describes one game to the shared Rules contract: its catalog Module, and the
// moves worth trying for the seat on turn.
type Game struct {
	game.Module
	// Moves is every move worth trying for the seat on turn, legal or not: the suite
	// keeps the ones ValidateAction accepts. It may draw from t, as hearts draws which
	// three cards to pass.
	Moves func(t *rapid.T, state *game.State) []game.Action
}

const (
	// clockOnlySteps bounds a match played by TimeoutAction alone. Gin rummy is the
	// longest: its auto-player knocks only on gin, so most hands run to the wall, and
	// the longest of a hundred such matches took about 1700 moves (hearts, 850).
	clockOnlySteps = 5000
	// soakSteps is how many moves a SoakAmongRandomMoves table plays.
	soakSteps = 200
	// maxSoakSeats keeps a ten-seat uno table from making every check ten times slower
	// than it needs to be: six seats already reach every turn-order case.
	maxSoakSeats = 6
	// leaveOdds is one leave in that many steps while the table can lose a seat and
	// play on; leaveEndsOdds is the rarer leave that ends it, so a game that cannot
	// lose a seat still plays out most of its random moves.
	leaveOdds     = 20
	leaveEndsOdds = 200
)

// RunRules runs the contract every game's Rules owes the engine, each case its own
// parallel subtest: a match the clock plays alone ends, with standings that rank
// everyone who sat down, and another game's move is refused.
func RunRules(t *testing.T, g Game) {
	t.Helper()
	cases := []struct {
		name string
		run  func(t *testing.T, g Game)
	}{
		{"a match played by the clock alone ends", clockOnlyMatchEnds},
		{"another game's move is refused", foreignMoveIsRefused},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(t, g)
		})
	}
}

// The engine plays TimeoutAction for a seat that has gone quiet, so a table nobody
// touches is played by it alone, and has to end rather than loop: every seat could
// walk away at once.
func clockOnlyMatchEnds(t *testing.T, g Game) {
	rapid.Check(t, func(rt *rapid.T) {
		engine := g.table(rt, g.drawSeats(rt))
		defer engine.Close()

		for step := range clockOnlySteps {
			if engine.IsFinished() {
				requireStandingsShape(rt, engine)
				return
			}
			id := engine.CurrentPlayerID()
			var act game.Action
			engine.WithState(func(s *game.State) { act = timeoutMove(rt, s, step) })
			require.NoError(rt, engine.SubmitAction(id, act), "step %d: %s", step, act.Name())
		}
		rt.Fatalf("a table played by the clock alone was still going after %d moves", clockOnlySteps)
	})
}

// ApplyAction refuses one too: a move added to ValidateAction and forgotten there
// must not be accepted as a silent no-op.
func foreignMoveIsRefused(t *testing.T, g Game) {
	engine := g.table(t, g.MinPlayers)
	t.Cleanup(engine.Close)

	engine.WithState(func(s *game.State) {
		require.ErrorIs(t, s.Rules.ValidateAction(s, ForeignAction{}), game.ErrUnknownAction)
		require.ErrorIs(t, s.Rules.ApplyAction(s, ForeignAction{}), game.ErrUnknownAction)
	})
}

// SoakAmongRandomMoves plays tables of random legal moves, with a seat walking out now
// and then, and checks on every turn that TimeoutAction is a move ValidateAction
// accepts. A refused one is not a skipped turn: the clock re-arms and the seat is taken
// on the next expiry, so a player is removed for a mistake the rules made. A table the
// clock plays alone reaches few of the states real play does - a taken upcard, a short
// all-in, a wild's colour - and random moves put TimeoutAction in front of the rest. It
// is its own entry point, not a RunRules case, so it reads as its own test.
func SoakAmongRandomMoves(t *testing.T, g Game) {
	t.Helper()
	rapid.Check(t, func(rt *rapid.T) {
		engine := g.table(rt, g.drawSeats(rt))
		defer engine.Close()

		for step := 0; step < soakSteps && !engine.IsFinished(); step++ {
			if leaver, ok := g.drawLeaver(rt, engine, step); ok {
				engine.RemovePlayer(leaver)
				continue
			}
			id := engine.CurrentPlayerID()
			var legal []game.Action
			engine.WithState(func(s *game.State) { legal = g.legalMoves(rt, s, step) })
			act := rapid.SampledFrom(legal).Draw(rt, fmt.Sprintf("move%d", step))
			require.NoError(rt, engine.SubmitAction(id, act), "step %d: %s", step, act.Name())
		}
		requireStandingsShape(rt, engine)
	})
}

// table is a started table of n seats, p1 to pn, with no clock - the suite plays the
// timeouts itself - and no log: thousands of matches would bury a failure in hands.
func (g Game) table(t TB, n int) *game.Engine {
	t.Helper()
	players := make([]*game.Player, n)
	for i := range players {
		players[i] = &game.Player{ID: fmt.Sprintf("p%d", i+1)}
	}
	engine := game.NewEngine(g.Factory(), players, game.WithTurnTimeout(0), game.WithLogger(slog.New(slog.DiscardHandler)))
	require.NoError(t, engine.Start())
	return engine
}

func (g Game) drawSeats(t *rapid.T) int {
	return rapid.IntRange(g.MinPlayers, min(g.MaxPlayers, maxSoakSeats)).Draw(t, "players")
}

// drawLeaver picks a seat to walk out, or reports none this step.
func (g Game) drawLeaver(t *rapid.T, engine *game.Engine, step int) (string, bool) {
	seats := engine.Snapshot().Players
	odds := leaveOdds
	if len(seats) <= g.MinPlayers {
		odds = leaveEndsOdds
	}
	if rapid.IntRange(1, odds).Draw(t, fmt.Sprintf("leave%d", step)) != 1 {
		return "", false
	}
	return rapid.SampledFrom(seats).Draw(t, fmt.Sprintf("who%d", step)).ID, true
}

// legalMoves is the timeout move, checked, and every move from Moves the rules accept,
// for the seat on turn. Another game's move is checked against the same table, so it
// meets every phase the soak reaches.
func (g Game) legalMoves(t *rapid.T, s *game.State, step int) []game.Action {
	legal := []game.Action{timeoutMove(t, s, step)}
	require.ErrorIs(t, s.Rules.ValidateAction(s, ForeignAction{}), game.ErrUnknownAction, "step %d", step)
	for _, m := range g.Moves(t, s) {
		if s.Rules.ValidateAction(s, m) == nil {
			legal = append(legal, m)
		}
	}
	return legal
}

// timeoutMove is the rules' TimeoutAction for the seat on turn, failing the test unless
// there is one and ValidateAction accepts it: the table is live, so nil would take a
// seat that did nothing wrong.
func timeoutMove(t TB, s *game.State, step int) game.Action {
	t.Helper()
	act := s.Rules.TimeoutAction(s)
	require.NotNil(t, act, "step %d: no timeout move for the seat on turn", step)
	require.NoError(t, s.Rules.ValidateAction(s, act), "step %d: the timeout move %s is not legal", step, act.Name())
	return act
}

func requireStandingsShape(t TB, engine *game.Engine) {
	t.Helper()
	engine.WithState(func(s *game.State) { checkStandings(t, s, s.Rules.Standings(s)) })
}

// checkStandings checks the shape Rules.Standings promises: everyone who sat
// down ranked once, the leavers included, places from 1 that players who drew share,
// and every leaver below every seated player, never sharing a place with one.
func checkStandings(t TB, s *game.State, standings []game.Standing) {
	t.Helper()
	everyone := slices.Concat(s.Players, s.LeftPlayers)
	require.Len(t, standings, len(everyone), "standings rank everyone who sat down, the leavers included")

	left := make(map[string]bool, len(s.LeftPlayers))
	for _, p := range s.LeftPlayers {
		left[p.ID] = true
	}
	ranked := make(map[string]bool, len(standings))
	for i, st := range standings {
		require.NotNil(t, st.Player, "standing %d names nobody", i)
		require.False(t, ranked[st.Player.ID], "%s is ranked twice", st.Player.ID)
		ranked[st.Player.ID] = true
		if i == 0 {
			require.Equal(t, 1, st.Place, "places start at 1")
			continue
		}
		prev := standings[i-1]
		require.True(t, st.Place == prev.Place || st.Place == i+1,
			"%s is placed %d after %d at position %d: a place is shared or is its position", st.Player.ID, st.Place, prev.Place, i+1)
		require.False(t, left[prev.Player.ID] && !left[st.Player.ID], "leaver %s ranks above seated %s", prev.Player.ID, st.Player.ID)
		require.False(t, left[st.Player.ID] && !left[prev.Player.ID] && st.Place == prev.Place,
			"leaver %s shares a place with seated %s", st.Player.ID, prev.Player.ID)
	}
	for _, p := range everyone {
		require.True(t, ranked[p.ID], "%s sat down and is missing from the standings", p.ID)
	}
}
