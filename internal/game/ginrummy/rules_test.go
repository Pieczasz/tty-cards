package ginrummy

import (
	"slices"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

func twoPlayers(hands ...[]deck.Card) []*game.Player {
	out := []*game.Player{{ID: "p1"}, {ID: "p2"}}
	for i, h := range hands {
		out[i].Cards = h
	}
	return out
}

func startedState(t *testing.T) (*game.State, *State) {
	t.Helper()
	rules := &Rules{}
	players := twoPlayers()
	state := game.NewState(rules, players, nil)
	require.NoError(t, rules.OnGameStart(state))
	extra := extra(t, state)
	return state, extra
}

func TestRules_OnGameStart_DealsTen(t *testing.T) {
	t.Parallel()
	state, extra := startedState(t)
	assert.Equal(t, 1, extra.HandNumber)
	assert.Equal(t, PhaseAwaitingDraw, extra.Phase)
	assert.Len(t, state.Players[0].Cards, 10)
	assert.Len(t, state.Players[1].Cards, 10)
	assert.Equal(t, 31, state.Deck.Size())
	_, ok := state.Discard.Peek()
	assert.True(t, ok)
}

func TestRules_ValidateAction_PhaseGates(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	t.Run("discard rejected before draw", func(t *testing.T) {
		t.Parallel()
		state, _ := startedState(t)
		card := state.Players[0].Cards[0]
		err := rules.ValidateAction(state, ActionDiscard{Card: card})
		require.ErrorContains(t, err, "must draw first")
	})

	t.Run("draw stock accepted", func(t *testing.T) {
		t.Parallel()
		state, _ := startedState(t)
		err := rules.ValidateAction(state, ActionDrawStock{})
		require.NoError(t, err)
	})

	t.Run("draw rejected after draw", func(t *testing.T) {
		t.Parallel()
		state, extra := startedState(t)
		rules.ApplyAction(state, ActionDrawStock{})
		err := rules.ValidateAction(state, ActionDrawStock{})
		require.ErrorContains(t, err, "must discard first")
		assert.Equal(t, PhaseAwaitingDiscard, extra.Phase)
	})
}

func TestRules_ValidateAction_KnockBoundary(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	t.Run("deadwood 10 legal", func(t *testing.T) {
		t.Parallel()
		hand11 := []deck.Card{
			c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts), c(deck.Ace, deck.Diamonds),
			c(deck.Four, deck.Clubs), c(deck.Five, deck.Clubs), c(deck.Six, deck.Clubs),
			c(deck.Nine, deck.Hearts), c(deck.Ten, deck.Hearts), c(deck.Jack, deck.Hearts),
			c(deck.King, deck.Spades),
			c(deck.Two, deck.Diamonds),
		}
		state := game.NewState(rules, twoPlayers(hand11, nil), nil)
		state.Extra = &State{Phase: PhaseAwaitingDiscard, CumulativeScores: map[string]int{"p1": 0, "p2": 0}}
		state.CurrentTurn = 0
		err := rules.ValidateAction(state, ActionKnock{Discard: c(deck.Two, deck.Diamonds)})
		require.NoError(t, err)
	})

	t.Run("deadwood 11 illegal", func(t *testing.T) {
		t.Parallel()
		hand := []deck.Card{
			c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts), c(deck.Ace, deck.Diamonds),
			c(deck.Four, deck.Clubs), c(deck.Five, deck.Clubs), c(deck.Six, deck.Clubs),
			c(deck.Nine, deck.Hearts), c(deck.Ten, deck.Hearts), c(deck.Jack, deck.Hearts),
			c(deck.King, deck.Spades),
			c(deck.Queen, deck.Diamonds),
		}
		st := game.NewState(rules, twoPlayers(hand, nil), nil)
		st.Extra = &State{Phase: PhaseAwaitingDiscard, CumulativeScores: map[string]int{"p1": 0, "p2": 0}}
		st.CurrentTurn = 0
		err := rules.ValidateAction(st, ActionKnock{Discard: c(deck.Ace, deck.Spades)})
		require.ErrorContains(t, err, "deadwood")
	})
}

// takeUpcard draws the face-up discard for the seat on turn and returns the card the
// rules must now refuse to take straight back.
func takeUpcard(t *testing.T, rules *Rules) (*game.State, *State, deck.Card) {
	t.Helper()
	state, extra := startedState(t)
	up, ok := state.Discard.Peek()
	require.True(t, ok)
	rules.ApplyAction(state, ActionDrawDiscard{})
	require.NotNil(t, extra.TakenUpcard, "drawing the discard must record the taken upcard")
	require.Equal(t, up, *extra.TakenUpcard)
	return state, extra, up
}

func TestRules_ValidateAction_CannotLayBackTakenUpcard(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	t.Run("discarding it straight back is refused", func(t *testing.T) {
		t.Parallel()
		state, _, up := takeUpcard(t, rules)
		err := rules.ValidateAction(state, ActionDiscard{Card: up})
		require.ErrorContains(t, err, "just took from the discard pile")
	})

	t.Run("knocking with it is refused", func(t *testing.T) {
		t.Parallel()
		state, _, up := takeUpcard(t, rules)
		err := rules.ValidateAction(state, ActionKnock{Discard: up})
		require.ErrorContains(t, err, "just took from the discard pile")
	})

	t.Run("every other card in hand is still legal", func(t *testing.T) {
		t.Parallel()
		state, _, up := takeUpcard(t, rules)
		for _, card := range state.Players[state.CurrentTurn].Cards {
			if card == up {
				continue
			}
			require.NoError(t, rules.ValidateAction(state, ActionDiscard{Card: card}))
		}
	})

	t.Run("a stock draw carries no restriction", func(t *testing.T) {
		t.Parallel()
		state, extra := startedState(t)
		rules.ApplyAction(state, ActionDrawStock{})
		assert.Nil(t, extra.TakenUpcard)
		for _, card := range state.Players[state.CurrentTurn].Cards {
			require.NoError(t, rules.ValidateAction(state, ActionDiscard{Card: card}))
		}
	})

	t.Run("the restriction ends with the turn", func(t *testing.T) {
		t.Parallel()
		state, extra, up := takeUpcard(t, rules)
		var other deck.Card
		for _, card := range state.Players[state.CurrentTurn].Cards {
			if card != up {
				other = card
				break
			}
		}
		rules.ApplyAction(state, ActionDiscard{Card: other})
		assert.Nil(t, extra.TakenUpcard, "the next player may take what was just laid down")
	})
}

func TestRules_Knock_Gin(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	knocker := []deck.Card{
		c(deck.Two, deck.Hearts), c(deck.Three, deck.Hearts), c(deck.Four, deck.Hearts), c(deck.Five, deck.Hearts),
		c(deck.Jack, deck.Spades), c(deck.Jack, deck.Hearts), c(deck.Jack, deck.Diamonds),
		c(deck.Ace, deck.Clubs), c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts),
		c(deck.King, deck.Clubs), // discard
	}
	opponent := []deck.Card{
		c(deck.King, deck.Spades), c(deck.Queen, deck.Hearts), c(deck.Nine, deck.Diamonds),
		c(deck.Eight, deck.Clubs), c(deck.Seven, deck.Spades), c(deck.Six, deck.Hearts),
		c(deck.Three, deck.Clubs), c(deck.Two, deck.Diamonds), c(deck.Four, deck.Spades),
		c(deck.Five, deck.Diamonds),
	}
	state := game.NewState(rules, twoPlayers(knocker, opponent), nil)
	extra := &State{
		Phase:            PhaseAwaitingDiscard,
		FirstActor:       0,
		CumulativeScores: map[string]int{"p1": 0, "p2": 0},
	}
	state.Extra = extra
	state.CurrentTurn = 0
	state.Deck = deck.New(nil)
	state.Discard = deck.New(nil)

	require.NoError(t, rules.ValidateAction(state, ActionKnock{Discard: c(deck.King, deck.Clubs)}))
	rules.ApplyAction(state, ActionKnock{Discard: c(deck.King, deck.Clubs)})

	require.NotNil(t, extra.LastHandResult)
	assert.Equal(t, OutcomeGin, extra.LastHandResult.Outcome)
	assert.Equal(t, "p1", extra.LastHandResult.Winner)
	assert.Equal(t, sumDeadwood(opponent)+ginBonus, extra.LastHandResult.ScoreDelta)
	assert.Equal(t, extra.LastHandResult.ScoreDelta, extra.CumulativeScores["p1"])

	// 6♥ extends the knocker's 2♥-5♥ run, so it is deadwood only because gin blocks
	// layoffs outright. Without a card that would otherwise come off, "no layoffs"
	// and "nothing was layable" look the same.
	layable := c(deck.Six, deck.Hearts)
	require.Contains(t, opponent, layable)
	_, layableOnto := findAttach(layable, extra.LastHandResult.KnockerMelds)
	require.True(t, layableOnto, "the fixture must give the opponent a card that would lay off")

	assert.Empty(t, extra.LastHandResult.LaidOffCards, "gin blocks layoffs")
	assert.Contains(t, extra.LastHandResult.OpponentDeadwood, layable,
		"a layable card still scores as deadwood against gin")
}

func TestRules_Knock_Undercut(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	knocker := []deck.Card{
		c(deck.Two, deck.Hearts), c(deck.Three, deck.Hearts), c(deck.Four, deck.Hearts),
		c(deck.Jack, deck.Spades), c(deck.Jack, deck.Hearts), c(deck.Jack, deck.Diamonds),
		c(deck.Ace, deck.Clubs), c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts),
		c(deck.Five, deck.Diamonds), // deadwood 5
		c(deck.King, deck.Clubs),    // discard
	}
	opponent := []deck.Card{
		c(deck.Six, deck.Spades), c(deck.Seven, deck.Spades), c(deck.Eight, deck.Spades), c(deck.Nine, deck.Spades),
		c(deck.King, deck.Hearts), c(deck.King, deck.Diamonds), c(deck.King, deck.Spades),
		c(deck.Queen, deck.Clubs), c(deck.Queen, deck.Hearts), c(deck.Queen, deck.Diamonds),
	}
	state := game.NewState(rules, twoPlayers(knocker, opponent), nil)
	extra := &State{
		Phase:            PhaseAwaitingDiscard,
		FirstActor:       0,
		CumulativeScores: map[string]int{"p1": 0, "p2": 0},
	}
	state.Extra = extra
	state.CurrentTurn = 0
	state.Deck = deck.New(nil)
	state.Discard = deck.New(nil)

	rules.ApplyAction(state, ActionKnock{Discard: c(deck.King, deck.Clubs)})
	require.NotNil(t, extra.LastHandResult)
	assert.Equal(t, OutcomeUndercut, extra.LastHandResult.Outcome)
	assert.Equal(t, "p2", extra.LastHandResult.Winner)
	assert.Equal(t, 5+undercutBonus, extra.LastHandResult.ScoreDelta)
	assert.Equal(t, 5+undercutBonus, extra.CumulativeScores["p2"])
}

// The undercut boundary is `remPts <= knockerPts`, so a tie goes to the opponent.
// With `<` the knocker would win the hand instead, for zero points.
func TestRules_Knock_UndercutOnATie(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	// Three melds plus 5♦ deadwood; the knock discards K♣, leaving 5 points.
	knocker := []deck.Card{
		c(deck.Two, deck.Hearts), c(deck.Three, deck.Hearts), c(deck.Four, deck.Hearts),
		c(deck.Jack, deck.Spades), c(deck.Jack, deck.Hearts), c(deck.Jack, deck.Diamonds),
		c(deck.Ace, deck.Clubs), c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts),
		c(deck.Five, deck.Diamonds),
		c(deck.King, deck.Clubs),
	}
	// Three melds plus 5♣: also 5 points, and it lays off onto none of the knocker's
	// melds (wrong suit for the heart run, wrong rank for both sets).
	opponent := []deck.Card{
		c(deck.Six, deck.Spades), c(deck.Seven, deck.Spades), c(deck.Eight, deck.Spades),
		c(deck.King, deck.Hearts), c(deck.King, deck.Diamonds), c(deck.King, deck.Spades),
		c(deck.Queen, deck.Clubs), c(deck.Queen, deck.Hearts), c(deck.Queen, deck.Diamonds),
		c(deck.Five, deck.Clubs),
	}
	state := game.NewState(rules, twoPlayers(knocker, opponent), nil)
	extra := &State{
		Phase:            PhaseAwaitingDiscard,
		FirstActor:       0,
		CumulativeScores: map[string]int{"p1": 0, "p2": 0},
	}
	state.Extra = extra
	state.CurrentTurn = 0
	state.Deck = deck.New(nil)
	state.Discard = deck.New(nil)

	rules.ApplyAction(state, ActionKnock{Discard: c(deck.King, deck.Clubs)})

	result := extra.LastHandResult
	require.NotNil(t, result)
	require.Equal(t, 5, result.KnockerDeadwoodPoints)
	require.Equal(t, 5, result.OpponentDeadwoodPoints, "the tie is the point of this test")
	assert.Equal(t, OutcomeUndercut, result.Outcome, "equal deadwood undercuts the knocker")
	assert.Equal(t, "p2", result.Winner)
	assert.Equal(t, undercutBonus, result.ScoreDelta, "a tie scores the bonus alone")
	assert.Equal(t, undercutBonus, extra.CumulativeScores["p2"])
	assert.Zero(t, extra.CumulativeScores["p1"])
}

// wallStockSize cards stay undealt, so the boundary is drawn at exactly that size.
func TestRules_DrawStock_WallBoundary(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	tests := []struct {
		name    string
		stock   int
		wantErr bool
	}{
		{name: "one above the wall is drawable", stock: wallStockSize + 1},
		{name: "exactly at the wall is not", stock: wallStockSize, wantErr: true},
		{name: "below the wall is not", stock: wallStockSize - 1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state, _ := startedState(t)
			state.Deck = deck.New(deck.Standard()[:tt.stock])

			err := rules.ValidateAction(state, ActionDrawStock{})
			if tt.wantErr {
				require.ErrorContains(t, err, "stock is at the wall")
				return
			}
			require.NoError(t, err)
		})
	}
}

// An absent player must still be handed a legal move at the wall, where the stock is
// off limits and the discard pile is the only place left to draw from - and where the
// pile is empty too, no move at all rather than one the validator refuses. A refused
// auto-play costs the seat on the next expiry, which is the opposite of a defence.
func TestRules_TimeoutAction_AtTheWall(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	t.Run("draws the discard while there is one", func(t *testing.T) {
		t.Parallel()
		state, _ := startedState(t)
		state.Deck = deck.New(deck.Standard()[:wallStockSize])

		action := rules.TimeoutAction(state)
		assert.Equal(t, ActionDrawDiscard{}, action)
		require.NoError(t, rules.ValidateAction(state, action), "the safe move must be legal")
	})

	t.Run("an empty pile at the wall has no legal move", func(t *testing.T) {
		t.Parallel()
		state, _ := startedState(t)
		state.Deck = deck.New(deck.Standard()[:wallStockSize])
		state.Discard = deck.New(nil)

		assert.Nil(t, rules.TimeoutAction(state), "every draw here is one ValidateAction refuses")
	})
}

func TestRules_Wall_AfterDiscard(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra := startedState(t)
	state.CurrentTurn = 0
	extra.Phase = PhaseAwaitingDiscard
	// Stock already at wall size: discard ends the hand.
	state.Deck = deck.New([]deck.Card{
		c(deck.Two, deck.Clubs), c(deck.Three, deck.Clubs),
	})
	card := state.Players[0].Cards[0]
	state.Players[0].Cards = append(state.Players[0].Cards, c(deck.Ace, deck.Diamonds)) // 11 cards
	rules.ApplyAction(state, ActionDiscard{Card: card})
	require.NoError(t, rules.AfterAction(state, ActionDiscard{Card: card}))
	assert.True(t, extra.HandComplete())
	require.NotNil(t, extra.LastHandResult)
	assert.Equal(t, OutcomeWall, extra.LastHandResult.Outcome)
	assert.Equal(t, 0, extra.CumulativeScores["p1"])
	assert.Equal(t, 0, extra.CumulativeScores["p2"])
}

func TestRules_Wall_StockThreeDoesNotTrigger(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra := startedState(t)
	extra.Phase = PhaseAwaitingDiscard
	state.Deck = deck.New([]deck.Card{
		c(deck.Two, deck.Clubs), c(deck.Three, deck.Clubs), c(deck.Four, deck.Clubs),
	})
	card := state.Players[0].Cards[0]
	state.Players[0].Cards = append(state.Players[0].Cards, c(deck.Ace, deck.Diamonds))
	rules.ApplyAction(state, ActionDiscard{Card: card})
	require.NoError(t, rules.AfterAction(state, ActionDiscard{Card: card}))
	assert.False(t, extra.HandComplete())
	assert.Equal(t, PhaseAwaitingDraw, extra.Phase)
}

func TestRules_Standings_Descending(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra := startedState(t)
	extra.CumulativeScores["p1"] = 40
	extra.CumulativeScores["p2"] = 90
	standings := rules.Standings(state)
	require.Len(t, standings, 2)
	assert.Equal(t, "p2", standings[0].ID)
	assert.Equal(t, "p1", standings[1].ID)
}

func TestRules_CheckWinCondition(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra := startedState(t)
	assert.False(t, rules.CheckWinCondition(state))
	extra.MatchComplete = true
	assert.True(t, rules.CheckWinCondition(state))
}

// An absent player holding gin must not discard it away: gin scores the bonus and
// cannot be undercut, so knocking is strictly the better auto-play.
func TestRules_TimeoutAction_KnocksOnGin(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra := startedState(t)
	// Every card melds, and dropping the 2♥ still leaves the 3-6♥ run.
	state.Players[state.CurrentTurn].Cards = []deck.Card{
		c(deck.Two, deck.Hearts), c(deck.Three, deck.Hearts), c(deck.Four, deck.Hearts),
		c(deck.Five, deck.Hearts), c(deck.Six, deck.Hearts),
		c(deck.Jack, deck.Spades), c(deck.Jack, deck.Hearts), c(deck.Jack, deck.Diamonds),
		c(deck.Ace, deck.Clubs), c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts),
	}
	extra.Phase = PhaseAwaitingDiscard

	action := rules.TimeoutAction(state)

	knock, ok := action.(ActionKnock)
	require.True(t, ok, "a gin hand knocks, got %T", action)
	require.NoError(t, rules.ValidateAction(state, knock))
	require.NoError(t, rules.ApplyAction(state, knock))
	require.NotNil(t, extra.LastHandResult)
	assert.Equal(t, OutcomeGin, extra.LastHandResult.Outcome)
}

func TestRules_TimeoutAction_DrawStock(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, _ := startedState(t)
	assert.Equal(t, ActionDrawStock{}, rules.TimeoutAction(state))
}

func TestRules_TimeoutAction_NextHand(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra := startedState(t)
	extra.Phase = PhaseHandOver
	assert.Equal(t, ActionNextHand{}, rules.TimeoutAction(state))
}

func TestRules_TimeoutAction_MatchOver(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra := startedState(t)
	extra.Phase = PhaseHandOver
	extra.MatchComplete = true
	assert.Nil(t, rules.TimeoutAction(state))
}

// The engine removes a player whose auto-play the rules then refuse, so the move
// TimeoutAction picks must satisfy ValidateAction - including the upcard restriction.
func TestRules_TimeoutAction_NeverLaysBackTakenUpcard(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra, up := takeUpcard(t, rules)

	action := rules.TimeoutAction(state)
	require.NotNil(t, action)
	discard, ok := action.(ActionDiscard)
	require.True(t, ok, "awaiting a discard, got %T", action)
	assert.NotEqual(t, up, discard.Card)
	assert.NotNil(t, extra.TakenUpcard)
	require.NoError(t, rules.ValidateAction(state, action), "the safe move must be legal")
}

func TestAutoDiscard(t *testing.T) {
	t.Parallel()
	king, twoD := c(deck.King, deck.Clubs), c(deck.Two, deck.Diamonds)
	// Three melds plus two loose cards: K♣ (10 pts) and 2♦ (2 pts).
	hand := []deck.Card{
		c(deck.Two, deck.Hearts), c(deck.Three, deck.Hearts), c(deck.Four, deck.Hearts),
		c(deck.Jack, deck.Spades), c(deck.Jack, deck.Hearts), c(deck.Jack, deck.Diamonds),
		c(deck.Ace, deck.Clubs), c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts),
		king, twoD,
	}
	gin := []deck.Card{
		c(deck.Two, deck.Hearts), c(deck.Three, deck.Hearts), c(deck.Four, deck.Hearts),
		c(deck.Five, deck.Hearts), c(deck.Six, deck.Hearts),
		c(deck.Jack, deck.Spades), c(deck.Jack, deck.Hearts), c(deck.Jack, deck.Diamonds),
		c(deck.Ace, deck.Clubs), c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts),
	}

	tests := []struct {
		name      string
		hand      []deck.Card
		forbidden *deck.Card
		want      deck.Card
	}{
		{name: "sheds the priciest deadwood", hand: hand, want: king},
		{name: "skips the forbidden card", hand: hand, forbidden: &king, want: twoD},
		{name: "cheap deadwood still loses to expensive", hand: hand, forbidden: &twoD, want: king},
		{name: "gin breaks a meld rather than stalling", hand: gin, want: gin[0]},
		{name: "gin skips the forbidden card too", hand: gin, forbidden: &gin[0], want: gin[1]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := autoDiscard(slices.Clone(tt.hand), tt.forbidden)
			require.True(t, ok)
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("a single forbidden card leaves nothing to play", func(t *testing.T) {
		t.Parallel()
		_, ok := autoDiscard([]deck.Card{king}, &king)
		assert.False(t, ok)
	})
}

func TestRules_DrawKeepsTurn(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, _ := startedState(t)
	state.CurrentTurn = 0
	rules.ApplyAction(state, ActionDrawStock{})
	require.NotNil(t, state.OverrideNextTurn)
	assert.Equal(t, 0, *state.OverrideNextTurn)
	assert.Len(t, state.Players[0].Cards, 11)
}

// A hand nobody knocks in scores nothing, so the target score alone never arrives:
// before the hand cap a table that walled every time redealt forever.
func TestMatch_RepeatedWallsEndTheMatch(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra := startedState(t)

	for range maxHands + 5 {
		// Put the stock at its reserve so the next completed discard walls the hand.
		extra.Phase = PhaseAwaitingDiscard
		state.Deck = deck.New(deck.Standard()[:wallStockSize])
		card := state.Players[state.CurrentTurn].Cards[0]
		rules.ApplyAction(state, ActionDiscard{Card: card})
		require.NoError(t, rules.AfterAction(state, ActionDiscard{Card: card}))
		require.True(t, extra.HandComplete())
		require.Equal(t, OutcomeWall, extra.LastHandResult.Outcome)

		if extra.MatchComplete {
			assert.Equal(t, maxHands, extra.HandNumber, "the match ends at the cap")
			assert.True(t, rules.CheckWinCondition(state))
			assert.Zero(t, extra.CumulativeScores["p1"], "walls score nothing")
			assert.Zero(t, extra.CumulativeScores["p2"])
			require.ErrorContains(t, rules.ValidateAction(state, ActionNextHand{}), "match is over")
			return
		}

		require.NoError(t, rules.ValidateAction(state, ActionNextHand{}))
		require.NoError(t, rules.AfterAction(state, ActionNextHand{}))
	}
	t.Fatalf("the match never ended after %d walled hands", maxHands+5)
}

// Standings and StandingScore have to agree, or the engine splits a genuine draw by
// slice position and the seat that sorted first takes rating off the seat that did not.
func TestRules_StandingScore_TiedSeatsShareAPlace(t *testing.T) {
	t.Parallel()
	engine := game.NewEngine(&Rules{}, []*game.Player{{ID: "p1"}, {ID: "p2"}}, deck.Standard())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	engine.WithState(func(s *game.State) {
		extra := extra(t, s)
		extra.CumulativeScores["p1"] = 40
		extra.CumulativeScores["p2"] = 40
	})

	standings, places := engine.StandingsWithPlaces()
	require.Len(t, standings, 2)
	assert.Equal(t, []int{1, 1}, places, "equal totals are one place, not two")
}

// A defender may arrange their hand for the lowest total *after* layoffs, not the
// lowest raw deadwood. Here 3♠4♠5♠ is the cheaper meld on its own, but melding the
// three 3s instead frees 4♠ and 5♠ to extend the knocker's 6♠7♠8♠ run to nothing -
// which turns a 2-point loss into a 29-point undercut.
func TestRules_Knock_DefenderMeldsForTheBestLayoff(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	knocker := []deck.Card{
		c(deck.Six, deck.Spades), c(deck.Seven, deck.Spades), c(deck.Eight, deck.Spades),
		c(deck.Jack, deck.Spades), c(deck.Jack, deck.Hearts), c(deck.Jack, deck.Diamonds),
		c(deck.Ace, deck.Clubs), c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts),
		c(deck.Four, deck.Diamonds),
		c(deck.King, deck.Clubs), // discard
	}
	opponent := []deck.Card{
		c(deck.Three, deck.Spades), c(deck.Four, deck.Spades), c(deck.Five, deck.Spades),
		c(deck.Three, deck.Hearts), c(deck.Three, deck.Diamonds),
		c(deck.Eight, deck.Clubs), c(deck.Nine, deck.Clubs), c(deck.Ten, deck.Clubs),
		c(deck.Jack, deck.Clubs), c(deck.Queen, deck.Clubs),
	}
	state := game.NewState(rules, twoPlayers(knocker, opponent), nil)
	extra := &State{
		Phase:            PhaseAwaitingDiscard,
		FirstActor:       0,
		CumulativeScores: map[string]int{"p1": 0, "p2": 0},
	}
	state.Extra = extra
	state.CurrentTurn = 0
	state.Deck = deck.New(nil)
	state.Discard = deck.New(nil)

	require.NoError(t, rules.ValidateAction(state, ActionKnock{Discard: c(deck.King, deck.Clubs)}))
	rules.ApplyAction(state, ActionKnock{Discard: c(deck.King, deck.Clubs)})

	res := extra.LastHandResult
	require.NotNil(t, res)
	assert.Equal(t, 4, res.KnockerDeadwoodPoints, "4♦ is the knock's only deadwood")
	assert.Zero(t, res.OpponentDeadwoodPoints, "both loose spades lay off")
	assert.ElementsMatch(t,
		[]deck.Card{c(deck.Four, deck.Spades), c(deck.Five, deck.Spades)}, res.LaidOffCards)
	assert.Equal(t, OutcomeUndercut, res.Outcome, "0 <= 4 is an undercut")
	assert.Equal(t, "p2", res.Winner)
	assert.Equal(t, 4+undercutBonus, res.ScoreDelta)
}

func TestRules_TableSize(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	assert.Equal(t, 2, rules.MinPlayers(), "gin rummy is strictly heads-up")
	assert.Equal(t, 2, rules.MaxPlayers())
	assert.Len(t, rules.InitialDeck(), 52)
	assert.Equal(t, "ginrummy.DrawStock", ActionDrawStock{}.Name())
	assert.Equal(t, "ginrummy.DrawDiscard", ActionDrawDiscard{}.Name())
	assert.Equal(t, "ginrummy.Discard", ActionDiscard{}.Name())
	assert.Equal(t, "ginrummy.Knock", ActionKnock{}.Name())
	assert.Equal(t, "ginrummy.NextHand", ActionNextHand{}.Name())
}

// The between-hands prompt is a decision, not a move, so it gets longer than a turn.
// Zero elsewhere means "engine default", not "no clock": a real duration there would
// quietly redefine every draw-and-discard turn in the game.
func TestRules_TurnDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		handComplete bool
		want         time.Duration
	}{
		{name: "the hand-over prompt gets a minute", handComplete: true, want: time.Minute},
		{name: "a live hand keeps the engine default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state, extra := startedState(t)
			if tt.handComplete {
				extra.Phase = PhaseHandOver
			}
			assert.Equal(t, tt.want, (&Rules{}).TurnDuration(state))
		})
	}

	t.Run("a state that is not a gin rummy state has no opinion", func(t *testing.T) {
		t.Parallel()
		assert.Zero(t, (&Rules{}).TurnDuration(&game.State{}))
	})
}

// The ordinary knock: neither gin nor an undercut, which is the branch the whole game
// spends most of its time in and the one the two bonuses do not cover. The delta is the
// difference in deadwood and nothing else.
func TestRules_Knock_PlainWinScoresTheDifference(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	knocker := []deck.Card{
		c(deck.Two, deck.Hearts), c(deck.Three, deck.Hearts), c(deck.Four, deck.Hearts),
		c(deck.Jack, deck.Spades), c(deck.Jack, deck.Hearts), c(deck.Jack, deck.Diamonds),
		c(deck.Ace, deck.Clubs), c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts),
		c(deck.Five, deck.Diamonds), // the only deadwood: 5 points
		c(deck.King, deck.Clubs),    // discard
	}
	// No meld and nothing that attaches to a heart run of 2-4, a set of jacks or a set
	// of aces, so the hand is 69 points of deadwood however the defender arranges it.
	opponent := []deck.Card{
		c(deck.Nine, deck.Diamonds), c(deck.Eight, deck.Clubs), c(deck.Seven, deck.Spades),
		c(deck.Six, deck.Diamonds), c(deck.Four, deck.Clubs), c(deck.Three, deck.Spades),
		c(deck.Two, deck.Clubs), c(deck.Ten, deck.Hearts), c(deck.Queen, deck.Diamonds),
		c(deck.King, deck.Spades),
	}
	state := game.NewState(rules, twoPlayers(knocker, opponent), nil)
	extra := &State{
		Phase:            PhaseAwaitingDiscard,
		FirstActor:       0,
		CumulativeScores: map[string]int{"p1": 0, "p2": 0},
	}
	state.Extra = extra
	state.CurrentTurn = 0
	state.Deck = deck.New(nil)
	state.Discard = deck.New(nil)

	knock := ActionKnock{Discard: c(deck.King, deck.Clubs)}
	require.NoError(t, rules.ValidateAction(state, knock))
	require.NoError(t, rules.ApplyAction(state, knock))

	result := extra.LastHandResult
	require.NotNil(t, result)
	assert.NotEqual(t, OutcomeGin, result.Outcome, "five points of deadwood is not gin")
	assert.NotEqual(t, OutcomeUndercut, result.Outcome, "69 beats 5, so the defender did not undercut")
	assert.Empty(t, result.LaidOffCards, "nothing in the defender's hand attaches")
	assert.Equal(t, 5, result.KnockerDeadwoodPoints)
	assert.Equal(t, 69, result.OpponentDeadwoodPoints)
	assert.Equal(t, "p1", result.Winner)
	assert.Equal(t, 64, result.ScoreDelta, "the difference, with no bonus either way")
	assert.Equal(t, 64, extra.CumulativeScores["p1"])
	assert.Zero(t, extra.CumulativeScores["p2"], "the loser of a plain knock scores nothing")
}

func TestRules_ValidateAction_UnknownActionIsRefused(t *testing.T) {
	t.Parallel()
	state, _ := startedState(t)
	assert.ErrorContains(t, (&Rules{}).ValidateAction(state, foreignAction{}), "unknown action")
}

// A move from some other game: the validator is the only thing between it and a state
// machine that has no case for it.
type foreignAction struct{}

func (foreignAction) Name() string { return "not.AGinRummyAction" }

func TestRules_NotAGinRummyState(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := &game.State{}

	require.ErrorIs(t, rules.ValidateAction(state, ActionDrawStock{}), game.ErrInvalidState)
	require.ErrorIs(t, rules.ApplyAction(state, ActionDrawStock{}), game.ErrInvalidState)
	require.ErrorIs(t, rules.AfterAction(state, ActionDrawStock{}), game.ErrInvalidState)
	assert.Nil(t, rules.Standings(state))
	assert.Nil(t, rules.TimeoutAction(state))
	assert.Zero(t, rules.StandingScore(state, &game.Player{ID: "p1"}))
	assert.False(t, rules.CheckWinCondition(state))
}

// The engine plays TimeoutAction for a seat that has gone quiet. A move ValidateAction
// refuses is not a skipped turn: the clock re-arms and the seat is taken on the next
// expiry, so a player is removed for a mistake the rules made. The auto-player never
// knocks, so every hand runs to the wall and the match ends on the hand cap.
func TestSoak_TimeoutActionIsAlwaysLegal(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	rapid.Check(t, func(rt *rapid.T) {
		players := []*game.Player{{ID: "p1"}, {ID: "p2"}}
		engine := game.NewEngine(rules, players, deck.Standard())
		require.NoError(rt, engine.Start())
		defer engine.Close()

		for step := range maxHands * (maxHandTurns + 4) {
			if engine.IsFinished() {
				return
			}
			id := engine.CurrentPlayerID()
			var act game.Action
			engine.WithState(func(s *game.State) {
				act = rules.TimeoutAction(s)
				require.NotNil(rt, act, "step %d: no move for %s", step, id)
				require.NoError(rt, rules.ValidateAction(s, act),
					"step %d: %s is not a legal move", step, act.Name())
			})
			require.NoError(rt, engine.SubmitAction(id, act))
		}
		rt.Fatalf("a match played entirely by the clock never reached the %d-hand cap", maxHands)
	})
}
