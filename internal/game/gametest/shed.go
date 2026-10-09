// Package gametest holds the rules tests more than one game has to pass, so a
// shedding game proves the shared contract once rather than carrying its own copy of
// every case. It is imported only by _test files.
//
//nolint:thelper // the case functions are subtest bodies, not helpers: a failure belongs on their own line
package gametest

import (
	"fmt"
	"slices"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/shed"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// ShedRules is what a shedding game's rules have to offer for the shared suite.
type ShedRules interface {
	game.Rules
	game.PlayerLeaveHandler
}

// Shed describes one shedding game to the shared suite.
type Shed struct {
	Rules ShedRules
	// MinPlayers and MaxPlayers are the game's seat range, as its catalog Module
	// declares it.
	MinPlayers, MaxPlayers int
	// Deck is the game's full deck, which Table deals from.
	Deck func() []deck.Card
	// NewExtra is the rules' Extra for a table whose card in play is top.
	NewExtra func(top deck.Card) any
	// ShedState reaches the deadlock counter inside an Extra NewExtra built.
	ShedState func(extra any) *shed.State
	// Opens reports whether a card may start the discard pile.
	Opens func(deck.Card) bool
	// Play is the game's action for playing card, naming suit as the one play
	// continues in: the Eight's or the Wild's choice, ignored for any other card.
	Play func(card deck.Card, suit deck.Suit) game.Action
	// Draw is the game's draw action, the one TimeoutAction has to play.
	Draw game.Action
}

// Table deals hands[i] cards to seat p<i+1> from a shuffled deck and opens the discard
// on a card the game may start on, bypassing OnGameStart: every multi-seat case needs
// a table whose hand sizes it chose.
func (s Shed) Table(t testing.TB, hands ...int) *game.State {
	t.Helper()
	stock := deck.New(s.Deck())
	stock.Shuffle()

	players := make([]*game.Player, 0, len(hands))
	for i, n := range hands {
		cards, ok := stock.DrawN(n)
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
	stock.Add(skipped...)

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

// Places is each standing's finishing place, in standings order.
func Places(standings []game.Standing) []int {
	out := make([]int, len(standings))
	for i, s := range standings {
		out[i] = s.Place
	}
	return out
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
		{"another game's move is refused", foreignActionIsRefused},
		{"another game's state is refused or ignored", foreignExtraIsRefused},
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
	assert.Equal(t, "p2", standings[0].Player.ID, "one card is the best position")
	assert.Equal(t, "p3", standings[1].Player.ID)
	assert.Equal(t, "p1", standings[2].Player.ID, "five cards is the worst")
	assert.Equal(t, []int{1, 2, 3}, Places(standings))
}

// Ties keep a stable order, so two players on the same count do not swap places
// between renders.
func standingsTiesAreStable(t *testing.T, s Shed) {
	state := s.Table(t, 2, 2, 2)

	first, second := s.Rules.Standings(state), s.Rules.Standings(state)

	require.Len(t, first, 3)
	for i := range first {
		assert.Equal(t, first[i].Player.ID, second[i].Player.ID, "position %d must not move between calls", i)
	}
}

// A genuine draw shares a place, or the seat that sorted first takes rating off the
// seat that did not.
func tiedSeatsShareAPlace(t *testing.T, s Shed) {
	players := []*game.Player{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}}
	engine := game.NewEngine(s.Rules, players)
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	engine.WithState(func(state *game.State) {
		state.Players[0].Cards = state.Players[0].Cards[:3]
		state.Players[1].Cards = state.Players[1].Cards[:3]
		state.Players[2].Cards = state.Players[2].Cards[:1]
	})

	standings := engine.Standings()
	require.Len(t, standings, 3)
	assert.Equal(t, "p3", standings[0].Player.ID, "one card is the best position")
	assert.Equal(t, []int{1, 2, 2}, Places(standings), "equal card counts are one place, not two")
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
	counter := s.ShedState(state.Extra)

	counter.Passes = 2
	s.Rules.OnPlayerLeave(state, "p3")
	state.Players = state.Players[:2] // the engine drops the seat after the hook

	assert.Zero(t, counter.Passes, "the count measured a table that no longer exists")
	assert.False(t, s.Rules.CheckWinCondition(state), "nobody passed, so nothing is deadlocked")
}

// With three seats the hand only ends once all three have passed in succession.
func deadlockNeedsEverySeat(t *testing.T, s Shed) {
	state := s.Table(t, 3, 5, 1)
	counter := s.ShedState(state.Extra)

	for passes := range len(state.Players) {
		counter.Passes = passes
		assert.False(t, s.Rules.CheckWinCondition(state),
			"%d of %d seats passed is not a deadlock", passes, len(state.Players))
	}

	counter.Passes = len(state.Players)
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
	assert.Error(t, s.Rules.ValidateAction(state, s.Play(state.Players[0].Cards[0], deck.NoSuit)),
		"a card still needs something to match against")
}

// Drawing is the only move ValidateAction accepts unconditionally, and on a dead board
// it degrades into the forced pass the turn loop already handles.
func timeoutDraws(t *testing.T, s Shed) {
	assert.Equal(t, s.Draw, s.Rules.TimeoutAction(nil))

	state := s.Table(t, 3, 3)
	assert.NoError(t, s.Rules.ValidateAction(state, s.Rules.TimeoutAction(state)))
}

// ApplyAction refuses one too: a move added to ValidateAction and forgotten here
// must not be accepted as a silent no-op.
func foreignActionIsRefused(t *testing.T, s Shed) {
	require.ErrorIs(t, s.Rules.ValidateAction(s.Table(t, 3, 3), ForeignAction{}), game.ErrUnknownAction)
	require.ErrorIs(t, s.Rules.ApplyAction(s.Table(t, 3, 3), ForeignAction{}), game.ErrUnknownAction)
}

// Extra holding something else is a wiring bug: every hook refuses or ignores it
// rather than acting on a table it cannot read.
func foreignExtraIsRefused(t *testing.T, s Shed) {
	state := s.Table(t, 3, 3)
	state.Extra = "not this game"
	hand := slices.Clone(state.Players[0].Cards)

	require.ErrorIs(t, s.Rules.ValidateAction(state, s.Draw), game.ErrInvalidState)
	require.ErrorIs(t, s.Rules.ApplyAction(state, s.Draw), game.ErrInvalidState)
	assert.False(t, s.Rules.CheckWinCondition(state))

	s.Rules.OnPlayerLeave(state, "p1")
	s.Rules.AfterPlayerRemoved(state, 0)
	assert.Equal(t, hand, state.Players[0].Cards, "a leave on a foreign state moves no cards")
	assert.Nil(t, state.OverrideNextTurn, "nor the turn")
}

// SoakTimeoutIsAlwaysLegal plays whole tables on TimeoutAction, with random legal
// plays for the first soakMixedSteps moves. The engine plays TimeoutAction for a seat
// that has gone quiet, and a move ValidateAction refuses is not a skipped turn: the
// clock re-arms and the seat is taken on the next expiry, so a player is removed for a
// mistake the rules made. The plays, the Eight's and the Wild's choices among them, put
// it in front of tables a clock-only game never reaches; the clock-only tail proves
// such a table still ends. It is its own entry point, not a RunShed case, so each game
// keeps a TestSoak_TimeoutActionIsAlwaysLegal of the usual name.
func SoakTimeoutIsAlwaysLegal(t *testing.T, s Shed) {
	t.Helper()

	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(s.MinPlayers, min(s.MaxPlayers, 6)).Draw(rt, "players")
		players := make([]*game.Player, n)
		for i := range players {
			players[i] = &game.Player{ID: fmt.Sprintf("p%d", i+1)}
		}
		engine := game.NewEngine(s.Rules, players)
		require.NoError(rt, engine.Start())
		defer engine.Close()

		for step := range soakMixedSteps + soakDrawSteps {
			if engine.IsFinished() {
				return
			}
			id := engine.CurrentPlayerID()
			var legal []game.Action
			engine.WithState(func(state *game.State) {
				act := s.Rules.TimeoutAction(state)
				require.NotNil(rt, act, "step %d: no move for %s", step, id)
				require.NoError(rt, s.Rules.ValidateAction(state, act),
					"step %d: %s is not a legal move", step, act.Name())
				legal = append(legal, act)
				if step < soakMixedSteps {
					legal = append(legal, s.legalPlays(state)...)
				}
			})
			act := rapid.SampledFrom(legal).Draw(rt, fmt.Sprintf("move%d", step))
			require.NoError(rt, engine.SubmitAction(id, act), "step %d: %s", step, act.Name())
		}
		rt.Fatalf("a table of %d that only draws after move %d never ran out of cards", n, soakMixedSteps)
	})
}

// A random play recycles the discard into the stock, so a table of them need not end;
// a table that only draws does, within a deck's worth of draws and a round of passes.
const (
	soakMixedSteps = 200
	soakDrawSteps  = 400
)

// legalPlays is every play ValidateAction accepts for the seat on turn: a card that
// needs no suit once, and a card that names one once per suit it may name.
func (s Shed) legalPlays(state *game.State) []game.Action {
	var out []game.Action
	for _, c := range state.Players[state.CurrentTurn].Cards {
		if play := s.Play(c, deck.NoSuit); s.Rules.ValidateAction(state, play) == nil {
			out = append(out, play)
			continue
		}
		for _, suit := range []deck.Suit{deck.Spades, deck.Hearts, deck.Diamonds, deck.Clubs} {
			if play := s.Play(c, suit); s.Rules.ValidateAction(state, play) == nil {
				out = append(out, play)
			}
		}
	}
	return out
}
