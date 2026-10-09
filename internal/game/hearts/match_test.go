package hearts

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/gametest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrickWinner_HighestOfLedSuit(t *testing.T) {
	t.Parallel()
	state := createTestState()
	extra := gametest.Extra[*State](t, state)
	extra.LedSuit = deck.Clubs
	extra.TrickLeader = 0
	extra.TrickCards = map[string]deck.Card{
		"p1": {Rank: deck.Five, Suit: deck.Clubs},
		"p2": {Rank: deck.Ace, Suit: deck.Hearts}, // off-suit, ignored
		"p3": {Rank: deck.King, Suit: deck.Clubs},
		"p4": {Rank: deck.Two, Suit: deck.Clubs},
	}
	assert.Equal(t, 2, trickWinner(state, extra), "p3's king is the highest club")

}

func TestTrickPoints(t *testing.T) {
	t.Parallel()
	pts := trickPoints(map[string]deck.Card{
		"p1": {Rank: deck.Ace, Suit: deck.Hearts},
		"p2": queenOfSpades,
		"p3": {Rank: deck.Two, Suit: deck.Clubs},
		"p4": {Rank: deck.Three, Suit: deck.Hearts},
	})
	assert.Equal(t, 15, pts)
}

func TestScoreHand_ShootTheMoon(t *testing.T) {
	t.Parallel()
	players := fourPlayers()
	extra := &State{
		HandPoints: map[string]int{
			"p1": penaltyPointsTotal,
			"p2": 0,
			"p3": 0,
			"p4": 0,
		},
		CumulativeScores: map[string]int{"p1": 0, "p2": 5, "p3": 10, "p4": 0},
	}
	scoreHand(&game.State{Players: players}, extra)
	assert.Equal(t, 0, extra.CumulativeScores["p1"])
	assert.Equal(t, 5+penaltyPointsTotal, extra.CumulativeScores["p2"])
	assert.Equal(t, 10+penaltyPointsTotal, extra.CumulativeScores["p3"])
	assert.Equal(t, penaltyPointsTotal, extra.CumulativeScores["p4"])
}

func TestPassRecipient(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1, passRecipient(0, PassLeft, 4))
	assert.Equal(t, 3, passRecipient(0, PassRight, 4))
	assert.Equal(t, 2, passRecipient(0, PassAcross, 4))
	assert.Equal(t, 0, passRecipient(0, PassNone, 4))
}

func TestMatch_PassDirectionCycles(t *testing.T) {
	t.Parallel()
	state := createTestState()
	extra := gametest.Extra[*State](t, state)
	extra.TargetScore = targetScore

	want := []PassDirection{PassLeft, PassRight, PassAcross, PassNone, PassLeft}
	dealer := 0
	for hand := range want {
		require.NoError(t, beginHand(state, extra, dealer))
		assert.Equal(t, want[hand], extra.PassDirection, "hand %d", hand+1)
		if want[hand] == PassNone {
			assert.Equal(t, PhaseTrickPlay, extra.Phase)
		} else {
			assert.Equal(t, PhasePassing, extra.Phase)
		}
		dealer = (dealer + 1) % playerCount
	}
}

func TestMatch_ShootTheMoon_AndTargetEnd(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := createTestState()
	extra := gametest.Extra[*State](t, state)
	extra.HandPoints["p1"] = penaltyPointsTotal
	scoreHand(state, extra)
	assert.Equal(t, 0, extra.CumulativeScores["p1"])
	assert.Equal(t, 26, extra.CumulativeScores["p2"])

	extra.TargetScore = 26
	assert.True(t, game.AnyScoreAtLeast(extra.CumulativeScores, extra.TargetScore))

	extra.Phase = PhaseHandOver
	extra.MatchComplete = true
	assert.True(t, rules.CheckWinCondition(state))
}

func TestMatch_NextHandRejectedWhileLive(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := createTestState()
	err := rules.ValidateAction(state, ActionNextHand{})
	require.ErrorContains(t, err, "still being played")
}

func TestMatch_CumulativeScoresCarry(t *testing.T) {
	t.Parallel()
	players := []*game.Player{
		{ID: "p1"}, {ID: "p2"}, {ID: "p3"}, {ID: "p4"},
	}
	extra := &State{
		HandPoints:       map[string]int{"p1": 5, "p2": 8, "p3": 3, "p4": 10},
		CumulativeScores: map[string]int{"p1": 20, "p2": 0, "p3": 15, "p4": 7},
	}
	scoreHand(&game.State{Players: players}, extra)
	assert.Equal(t, 25, extra.CumulativeScores["p1"])
	assert.Equal(t, 8, extra.CumulativeScores["p2"])
	assert.Equal(t, 18, extra.CumulativeScores["p3"])
	assert.Equal(t, 17, extra.CumulativeScores["p4"])
}

// The deal rotates one seat per hand. Nothing else pins the direction down, so a
// dealer that stood still or ran backwards would otherwise go unnoticed.
func TestMatch_DealerRotatesOneSeatPerHand(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	players := []*game.Player{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}, {ID: "p4"}}
	state := game.NewState(rules, players, deck.Standard())
	state.CurrentTurn = 1
	require.NoError(t, rules.OnGameStart(state))

	extra := gametest.Extra[*State](t, state)
	require.Equal(t, 1, extra.DealerIndex, "the opening dealer is whoever is on turn")

	for hand := 2; hand <= 5; hand++ {
		// Finish the hand outright so afterPlay picks the next dealer.
		extra.TricksPlayed = cardsPerHand - 1
		extra.TrickCards = map[string]deck.Card{}
		extra.Phase = PhaseTrickPlay
		for i, p := range state.Players {
			state.CurrentTurn = i
			card := deck.Card{Rank: deck.Rank(i + 2), Suit: deck.Clubs}
			p.Cards = []deck.Card{card}
			extra.TrickCards[p.ID] = card
		}
		extra.LedSuit = deck.Clubs
		afterPlay(state, extra)

		// Hand 1 dealt from seat 1, so hand N deals from seat N mod 4.
		wantDealer := hand % len(players)
		require.Equal(t, wantDealer, state.CurrentTurn, "hand %d deals to the next seat", hand)

		require.NoError(t, rules.ApplyAction(state, ActionNextHand{}))
		assert.Equal(t, wantDealer, extra.DealerIndex, "hand %d", hand)
		assert.Equal(t, hand, extra.HandNumber)
	}
}

// "hand 7" matches every hearts table on the server, so the scoring line has to go
// through the table's logger to say which one it is about. It is also a count: one
// line a hand (two on a moon shot), not one a trick, which is what keeps it at Info.
func TestScoreHand_LogsOnceAHandThroughTheTableLogger(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	sink := &gametest.LogSink{}
	engine := game.NewEngine(rules, fourPlayers(), game.WithLogger(sink.Logger()))
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	handOver := func() (over bool) {
		engine.WithState(func(s *game.State) { over = gametest.Extra[*State](t, s).Phase == PhaseHandOver })
		return over
	}
	for step := 0; !handOver(); step++ {
		require.Less(t, step, 100, "one hand is three passes and 52 plays")
		var act game.Action
		engine.WithState(func(s *game.State) { act = rules.TimeoutAction(s) })
		require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), act))
	}

	recs := sink.Records(t)
	require.NotEmpty(t, recs)
	assert.LessOrEqual(t, len(recs), 2, "a hand logs its score and at most a moon shot")
	for _, rec := range recs {
		assert.Equal(t, gametest.SinkLobby, rec["lobby"], "%v", rec["msg"])
	}
}
