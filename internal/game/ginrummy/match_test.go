package ginrummy

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/gametest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatch_ScoresCarryIntoNextHand(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra := startedState(t)

	knocker := []deck.Card{
		c(deck.Two, deck.Hearts), c(deck.Three, deck.Hearts), c(deck.Four, deck.Hearts), c(deck.Five, deck.Hearts),
		c(deck.Jack, deck.Spades), c(deck.Jack, deck.Hearts), c(deck.Jack, deck.Diamonds),
		c(deck.Ace, deck.Clubs), c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts),
		c(deck.King, deck.Clubs),
	}
	opponent := []deck.Card{
		c(deck.King, deck.Spades), c(deck.Queen, deck.Hearts), c(deck.Nine, deck.Diamonds),
		c(deck.Eight, deck.Clubs), c(deck.Seven, deck.Spades), c(deck.Six, deck.Hearts),
		c(deck.Three, deck.Clubs), c(deck.Two, deck.Diamonds), c(deck.Four, deck.Spades),
		c(deck.Five, deck.Diamonds),
	}
	state.Players[0].Cards = knocker
	state.Players[1].Cards = opponent
	extra.Phase = PhaseAwaitingDiscard
	state.CurrentTurn = 0

	rules.ApplyAction(state, ActionKnock{Discard: c(deck.King, deck.Clubs)})
	score := extra.CumulativeScores["p1"]
	require.Positive(t, score)
	require.True(t, extra.HandComplete())

	require.NoError(t, rules.ValidateAction(state, ActionNextHand{}))
	require.NoError(t, rules.ApplyAction(state, ActionNextHand{}))
	assert.Equal(t, score, extra.CumulativeScores["p1"], "scores carry across hands")
	assert.Equal(t, 2, extra.HandNumber)
	assert.False(t, extra.HandComplete())
	assert.Equal(t, PhaseAwaitingDraw, extra.Phase)
}

// Two players who only ever take the upcard never touch the stock, so the ordinary
// wall is unreachable. Before TakenUpcard + maxHandTurns this ran forever: each player
// laid the card they had just picked up straight back and nothing in the hand changed.
func TestMatch_UpcardTradingCannotStallTheHand(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra := startedState(t)
	openingStock := state.Deck.Size()

	for turn := range maxHandTurns * 2 {
		if extra.HandComplete() {
			require.NotNil(t, extra.LastHandResult)
			assert.Equal(t, OutcomeWall, extra.LastHandResult.Outcome, "a stalled hand settles as a wall")
			assert.Equal(t, openingStock, state.Deck.Size(), "no stock was ever drawn")
			assert.Equal(t, maxHandTurns, extra.TurnsThisHand)
			return
		}

		up, ok := state.Discard.Peek()
		require.True(t, ok)
		require.NoError(t, rules.ValidateAction(state, ActionDrawDiscard{}))
		rules.ApplyAction(state, ActionDrawDiscard{})

		require.ErrorContains(t, rules.ValidateAction(state, ActionDiscard{Card: up}),
			"just took from the discard pile", "turn %d laid the upcard straight back", turn)

		shed, ok := autoDiscard(state.Players[state.CurrentTurn].Cards, extra.TakenUpcard)
		require.True(t, ok)
		require.NoError(t, rules.ValidateAction(state, ActionDiscard{Card: shed}))
		require.NoError(t, rules.ApplyAction(state, ActionDiscard{Card: shed}))
		state.CurrentTurn = 1 - state.CurrentTurn
	}
	t.Fatalf("hand never terminated after %d turns", maxHandTurns*2)
}

func TestMatch_NextHandRejectedWhileHandLive(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, _ := startedState(t)
	err := rules.ValidateAction(state, ActionNextHand{})
	require.ErrorContains(t, err, "still being played")
}

func TestMatch_EndsOnceCumulativeScoreCrossesTarget(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra := startedState(t)
	extra.CumulativeScores["p1"] = targetScore - 1

	knocker := []deck.Card{
		c(deck.Two, deck.Hearts), c(deck.Three, deck.Hearts), c(deck.Four, deck.Hearts), c(deck.Five, deck.Hearts),
		c(deck.Jack, deck.Spades), c(deck.Jack, deck.Hearts), c(deck.Jack, deck.Diamonds),
		c(deck.Ace, deck.Clubs), c(deck.Ace, deck.Spades), c(deck.Ace, deck.Hearts),
		c(deck.King, deck.Clubs),
	}
	opponent := []deck.Card{
		c(deck.King, deck.Spades), c(deck.Queen, deck.Hearts), c(deck.Nine, deck.Diamonds),
		c(deck.Eight, deck.Clubs), c(deck.Seven, deck.Spades), c(deck.Six, deck.Hearts),
		c(deck.Three, deck.Clubs), c(deck.Two, deck.Diamonds), c(deck.Four, deck.Spades),
		c(deck.Five, deck.Diamonds),
	}
	state.Players[0].Cards = knocker
	state.Players[1].Cards = opponent
	extra.Phase = PhaseAwaitingDiscard
	state.CurrentTurn = 0

	rules.ApplyAction(state, ActionKnock{Discard: c(deck.King, deck.Clubs)})
	assert.True(t, extra.MatchComplete)
	assert.True(t, rules.CheckWinCondition(state))
	assert.GreaterOrEqual(t, extra.CumulativeScores["p1"], targetScore)
}

func TestStandings_LeavingForfeitsScoresStay(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	players := []*game.Player{
		{ID: "p1"},
		{ID: "p2"},
	}
	engine := game.NewEngine(rules, players)
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	var scoreBefore map[string]int
	engine.WithState(func(s *game.State) {
		extra := gametest.Extra[*State](t, s)
		extra.CumulativeScores["p1"] = 30
		extra.CumulativeScores["p2"] = 12
		scoreBefore = map[string]int{"p1": 30, "p2": 12}
	})

	engine.RemovePlayer("p2")
	assert.Equal(t, game.Finished, engine.Snapshot().Phase)
	standings := engine.Standings()
	require.NotEmpty(t, standings)
	assert.Equal(t, "p1", standings[0].Player.ID)

	engine.WithState(func(s *game.State) {
		extra := gametest.Extra[*State](t, s)
		assert.Equal(t, scoreBefore["p1"], extra.CumulativeScores["p1"])
		assert.Equal(t, scoreBefore["p2"], extra.CumulativeScores["p2"])
	})
}

func TestMatch_FirstActorAlternates(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state, extra := startedState(t)
	assert.Equal(t, 0, extra.FirstActor)

	extra.Phase = PhaseHandOver
	extra.FirstActor = 0
	next := 1
	extra.FirstActor = next
	state.CurrentTurn = next
	require.NoError(t, rules.ApplyAction(state, ActionNextHand{}))
	assert.Equal(t, 1, extra.FirstActor)
	assert.Equal(t, 1, state.CurrentTurn)
}

// "hand 7" matches every gin table on the server, so the hand's result line has to go
// through the table's logger to say which one it is about. One line a hand, the knock
// or the wall, is what keeps it at Info.
func TestHandResult_LogsOnceAHandThroughTheTableLogger(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	sink := &gametest.LogSink{}
	engine := game.NewEngine(rules, twoPlayers(), game.WithLogger(sink.Logger()))
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	handOver := func() (over bool) {
		engine.WithState(func(s *game.State) { over = gametest.Extra[*State](t, s).Phase == PhaseHandOver })
		return over
	}
	for step := 0; !handOver(); step++ {
		require.Less(t, step, 2*(maxHandTurns+4), "a hand ends on a knock or at the wall")
		var act game.Action
		engine.WithState(func(s *game.State) { act = rules.TimeoutAction(s) })
		require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), act))
	}

	recs := sink.Records(t)
	require.Len(t, recs, 1, "a hand logs its knock or its wall, nothing per turn")
	assert.Equal(t, gametest.SinkLobby, recs[0]["lobby"], "%v", recs[0]["msg"])
}
