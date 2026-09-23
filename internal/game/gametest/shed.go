// Package gametest holds the rules tests more than one game has to pass, so a
// shedding game proves the shared contract once rather than carrying its own copy of
// every case. It is imported only by _test files.
//
//nolint:thelper // the case functions are subtest bodies, not helpers: a failure belongs on their own line
package gametest

import (
	"fmt"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// ShedRules is what a shedding game's rules have to offer for the shared suite.
type ShedRules interface {
	game.Rules
	game.PlayerLeaveHandler
	game.TurnTimeoutHandler
}

// Shed describes one shedding game to the shared suite.
type Shed struct {
	Rules ShedRules
	// NewExtra is the rules' Extra for a table whose card in play is top.
	NewExtra func(top deck.Card) any
	// ShedState reaches the deadlock counter inside an Extra NewExtra built.
	ShedState func(extra any) *game.ShedState
	// Opens reports whether a card may start the discard pile.
	Opens func(deck.Card) bool
	// Play is the game's action for playing card with no colour or suit named.
	Play func(card deck.Card) game.Action
	// Draw is the game's draw action, the one TimeoutAction has to play.
	Draw game.Action
}

// Table deals hands[i] cards to seat p<i+1> from a shuffled deck and opens the discard
// on a card the game may start on, bypassing OnGameStart: every multi-seat case needs
// a table whose hand sizes it chose.
func (s Shed) Table(t testing.TB, hands ...int) *game.State {
	t.Helper()
	stock := deck.New(s.Rules.InitialDeck())
	stock.Shuffle()

	players := make([]*game.Player, 0, len(hands))
	for i, n := range hands {
		cards, ok := stock.DrawNCards(n)
		require.True(t, ok, "fixture deck must hold %d cards", n)
		players = append(players, &game.Player{ID: fmt.Sprintf("p%d", i+1), Cards: cards})
	}

	var skipped []deck.Card
	top, ok := stock.Draw()
	for ok && !s.Opens(top) {
		skipped = append(skipped, top)
		top, ok = stock.Draw()
	}
	require.True(t, ok, "fixture deck ran out before a card could open the discard")
	stock.AddCard(skipped...)

	state := game.NewState(s.Rules, players, nil)
	state.Deck = stock
	state.Discard = deck.New([]deck.Card{top})
	state.Extra = s.NewExtra(top)
	return state
}

// CardsInPlay is the shedding-game conservation invariant: every card is in exactly one
// of the seated hands, the stock or the discard.
func CardsInPlay(state *game.State) int {
	total := state.Deck.Size() + state.Discard.Size()
	for _, p := range state.Players {
		total += len(p.Cards)
	}
	return total
}

// RunShed runs every case the shedding games share, each as its own parallel subtest.
func RunShed(t *testing.T, s Shed) {
	t.Helper()
	cases := []struct {
		name string
		run  func(t *testing.T, s Shed)
	}{
		{"standings rank by fewest cards", standingsRankByFewestCards},
		{"standings ties are stable", standingsTiesAreStable},
		{"tied seats share a place", tiedSeatsShareAPlace},
		{"a leaver's cards go back to the stock", leaverCardsGoBack},
		{"an unknown leaver changes nothing", unknownLeaverChangesNothing},
		{"a leave clears stale passes", leaveClearsStalePasses},
		{"a deadlock needs every seat to pass", deadlockNeedsEverySeat},
		{"an emptied hand wins", emptyHandWins},
		{"an empty table is not a deadlock", emptyTableIsNotADeadlock},
		{"a draw needs no discard", drawNeedsNoDiscard},
		{"the timeout move draws", timeoutDraws},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			c.run(t, s)
		})
	}
}

func standingsRankByFewestCards(t *testing.T, s Shed) {
	standings := s.Rules.Standings(s.Table(t, 5, 1, 3))

	require.Len(t, standings, 3)
	assert.Equal(t, "p2", standings[0].ID, "one card is the best position")
	assert.Equal(t, "p3", standings[1].ID)
	assert.Equal(t, "p1", standings[2].ID, "five cards is the worst")
}

// Ties keep a stable order, so two players on the same count do not swap places
// between renders.
func standingsTiesAreStable(t *testing.T, s Shed) {
	state := s.Table(t, 2, 2, 2)

	first, second := s.Rules.Standings(state), s.Rules.Standings(state)

	require.Len(t, first, 3)
	for i := range first {
		assert.Equal(t, first[i].ID, second[i].ID, "position %d must not move between calls", i)
	}
}

// Standings and StandingScore have to agree, or the engine splits a genuine draw by
// slice position and the seat that sorted first takes rating off the seat that did not.
func tiedSeatsShareAPlace(t *testing.T, s Shed) {
	players := []*game.Player{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}}
	engine := game.NewEngine(s.Rules, players, s.Rules.InitialDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	engine.WithState(func(state *game.State) {
		state.Players[0].Cards = state.Players[0].Cards[:3]
		state.Players[1].Cards = state.Players[1].Cards[:3]
		state.Players[2].Cards = state.Players[2].Cards[:1]
	})

	standings, places := engine.StandingsWithPlaces()
	require.Len(t, standings, 3)
	assert.Equal(t, "p3", standings[0].ID, "one card is the best position")
	assert.Equal(t, []int{1, 2, 2}, places, "equal card counts are one place, not two")
}

// Deliberately uneven hands: with equal ones, returning the wrong player's cards still
// balances the deck and the leak goes unnoticed.
func leaverCardsGoBack(t *testing.T, s Shed) {
	state := s.Table(t, 3, 6)
	before, stockBefore := CardsInPlay(state), state.Deck.Size()

	s.Rules.OnPlayerLeave(state, "p2")

	assert.Equal(t, before, CardsInPlay(state), "leaving must not create or destroy cards")
	assert.Equal(t, stockBefore+6, state.Deck.Size(), "the leaver's six cards go back")
	assert.Empty(t, state.Players[1].Cards, "the leaver keeps no cards")
	assert.Len(t, state.Players[0].Cards, 3, "everyone else keeps their hand")
}

func unknownLeaverChangesNothing(t *testing.T, s Shed) {
	state := s.Table(t, 3, 3)
	before, stockBefore := CardsInPlay(state), state.Deck.Size()

	s.Rules.OnPlayerLeave(state, "nobody")

	assert.Equal(t, before, CardsInPlay(state))
	assert.Equal(t, stockBefore, state.Deck.Size())
}

// Passes is counted against the number of seats, so a leaver who arrives with the
// count part-way up leaves a table that reads as deadlocked without a single seat
// having passed. Their returned cards also refill the stock the count was measuring.
func leaveClearsStalePasses(t *testing.T, s Shed) {
	state := s.Table(t, 3, 3, 3)
	shed := s.ShedState(state.Extra)

	shed.Passes = 2
	s.Rules.OnPlayerLeave(state, "p3")
	state.Players = state.Players[:2] // the engine drops the seat after the hook

	assert.Zero(t, shed.Passes, "the count measured a table that no longer exists")
	assert.False(t, s.Rules.CheckWinCondition(state), "nobody passed, so nothing is deadlocked")
}

// With three seats the hand only ends once all three have passed in succession.
func deadlockNeedsEverySeat(t *testing.T, s Shed) {
	state := s.Table(t, 3, 5, 1)
	shed := s.ShedState(state.Extra)

	for passes := range len(state.Players) {
		shed.Passes = passes
		assert.False(t, s.Rules.CheckWinCondition(state),
			"%d of %d seats passed is not a deadlock", passes, len(state.Players))
	}

	shed.Passes = len(state.Players)
	assert.True(t, s.Rules.CheckWinCondition(state), "every seat passing ends the hand")
}

func emptyHandWins(t *testing.T, s Shed) {
	assert.True(t, s.Rules.CheckWinCondition(s.Table(t, 0, 3, 3)))
}

// With nobody seated there is no hand to end, and treating it as won would finish a
// game that never started.
func emptyTableIsNotADeadlock(t *testing.T, s Shed) {
	state := s.Table(t)
	s.ShedState(state.Extra).Passes = 3

	assert.False(t, s.Rules.CheckWinCondition(state), "no seats means no hand to deadlock")
}

// A draw is unconditionally legal, and TimeoutAction plays one. Reading the top of the
// discard before the switch made the validator reject it on a pile that came up empty,
// which is the shape that turns a quiet seat into a kicked one.
func drawNeedsNoDiscard(t *testing.T, s Shed) {
	state := s.Table(t, 3, 3)
	state.Discard = deck.New(nil)

	require.NoError(t, s.Rules.ValidateAction(state, s.Draw))
	assert.Error(t, s.Rules.ValidateAction(state, s.Play(state.Players[0].Cards[0])),
		"a card still needs something to match against")
}

// Drawing is the only move ValidateAction accepts unconditionally, and on a dead board
// it degrades into the forced pass the turn loop already handles.
func timeoutDraws(t *testing.T, s Shed) {
	assert.Equal(t, s.Draw, s.Rules.TimeoutAction(nil))

	state := s.Table(t, 3, 3)
	assert.NoError(t, s.Rules.ValidateAction(state, s.Rules.TimeoutAction(state)))
}

// SoakTimeoutIsAlwaysLegal plays whole tables on nothing but TimeoutAction. The engine
// plays it for a seat that has gone quiet, and a move ValidateAction refuses is not a
// skipped turn: the clock re-arms and the seat is taken on the next expiry, so a player
// is removed for a mistake the rules made. It is its own entry point, not a RunShed
// case, so each game keeps a TestSoak_TimeoutActionIsAlwaysLegal of the usual name.
func SoakTimeoutIsAlwaysLegal(t *testing.T, s Shed) {
	t.Helper()

	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(s.Rules.MinPlayers(), min(s.Rules.MaxPlayers(), 6)).Draw(rt, "players")
		players := make([]*game.Player, n)
		for i := range players {
			players[i] = &game.Player{ID: fmt.Sprintf("p%d", i+1)}
		}
		engine := game.NewEngine(s.Rules, players, s.Rules.InitialDeck())
		require.NoError(rt, engine.Start())
		defer engine.Close()

		for step := range 400 {
			if engine.IsFinished() {
				return
			}
			id := engine.CurrentPlayerID()
			var act game.Action
			engine.WithState(func(state *game.State) {
				act = s.Rules.TimeoutAction(state)
				require.NotNil(rt, act, "step %d: no move for %s", step, id)
				require.NoError(rt, s.Rules.ValidateAction(state, act),
					"step %d: %s is not a legal move", step, act.Name())
			})
			require.NoError(rt, engine.SubmitAction(id, act))
		}
		rt.Fatalf("a table of %d that only ever draws never ran out of cards", n)
	})
}
