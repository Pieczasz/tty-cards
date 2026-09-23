package poker

import (
	"fmt"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatch_ChipsAndButtonCarryIntoTheNextHand(t *testing.T) {
	t.Parallel()
	engine := startTable(t, 3)
	t.Cleanup(engine.Close)

	firstDealer := readExtra(t, engine, func(e *State) int { return e.DealerIndex })

	// Fold the table down to one player, ending hand one.
	for range 3 {
		if readExtra(t, engine, (*State).HandComplete) {
			break
		}
		require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), ActionFold{}))
	}

	require.True(t, readExtra(t, engine, (*State).HandComplete))
	require.False(t, engine.IsFinished())
	won := readExtra(t, engine, stacks)

	require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), ActionNextHand{}))

	withExtra(t, engine, func(next *State) {
		assert.Equal(t, 2, next.HandNumber)
		assert.NotEqual(t, firstDealer, next.DealerIndex, "the button moves on between hands")
		for id, want := range won {
			// Only the two blinds have paid anything into the new hand.
			assert.Equal(t, want, next.Seats[id].Chips+next.Seats[id].Bet,
				"player %s must start the hand with what they finished the last one with", id)
		}
	})

	engine.WithState(func(s *game.State) {
		for _, p := range s.Players {
			assert.Len(t, p.Cards, 2, "every funded player is dealt a fresh hand")
		}
	})
}

// Walking out mid-match forfeits it, however far ahead the leaver was: they place
// below everyone who saw the match through. Between themselves, leavers are still
// ranked on the chips they won, not on who quit first.
func TestStandings_LeavingForfeitsTheMatchButNotTheChipsWon(t *testing.T) {
	t.Parallel()
	engine := startTable(t, 3)
	t.Cleanup(engine.Close)

	engine.WithState(func(s *game.State) {
		extra := extra(t, s)
		// p2 is well clear of the table, as if they had taken a couple of hands.
		extra.Seats["p1"].Chips = 700
		extra.Seats["p2"].Chips = 1600
		extra.Seats["p3"].Chips = 700
		// Rewriting stacks mid-hand moves the conservation baseline with them.
		extra.handStartChips = chipsInPlay(extra)
	})

	engine.RemovePlayer("p2")
	assert.Equal(t, []string{"p1", "p3", "p2"}, standingIDs(engine),
		"the chip leader drops behind both players still at the table")

	engine.RemovePlayer("p3")
	assert.Equal(t, []string{"p1", "p2", "p3"}, standingIDs(engine),
		"between leavers the bigger stack still places higher")

	engine.WithState(func(s *game.State) {
		require.NotNil(t, s.Winner)
		assert.Equal(t, "p1", s.Winner.ID, "the last player standing wins the match")
	})
}

func TestMatch_NextHandRejectedWhileTheHandIsLive(t *testing.T) {
	t.Parallel()
	engine := startTable(t, 2)
	t.Cleanup(engine.Close)

	err := engine.SubmitAction(engine.CurrentPlayerID(), ActionNextHand{})
	assert.ErrorContains(t, err, "still being played")
}

func TestFinishHand(t *testing.T) {
	t.Parallel()

	t.Run("parks the turn on the next dealer", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		extra := extra(t, state)
		extra.HandNumber, extra.HandsTotal = 1, HandsPerMatch
		extra.DealerIndex = 0

		finishHand(state, extra)

		assert.False(t, extra.MatchComplete)
		require.NotNil(t, state.OverrideNextTurn)
		assert.Equal(t, 1, *state.OverrideNextTurn, "seat after the button deals next")
	})

	t.Run("ends the match once the hands run out", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		extra := extra(t, state)
		extra.HandNumber, extra.HandsTotal = HandsPerMatch, HandsPerMatch

		finishHand(state, extra)

		assert.True(t, extra.MatchComplete)
		assert.True(t, (&Rules{}).CheckWinCondition(state))
	})

	t.Run("ends the match once one player holds every chip", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		extra := extra(t, state)
		extra.HandNumber, extra.HandsTotal = 1, HandsPerMatch
		extra.Seats = seatsWithChips(map[string]uint{"p1": 3000, "p2": 0, "p3": 0})

		finishHand(state, extra)

		assert.True(t, extra.MatchComplete)
		assert.Equal(t, "p1", (&Rules{}).Standings(state)[0].ID, "the biggest stack wins the match")
	})
}

// tableWithChips seats one player per stack, in order, ready for beginHand.
func tableWithChips(stacks ...uint) (*game.State, *State) {
	players := make([]*game.Player, 0, len(stacks))
	extra := &State{
		SmallBlind: DefaultSmallBlind,
		BigBlind:   DefaultBigBlind,
		HandsTotal: HandsPerMatch,
		Table:      make([]deck.Card, 0, BoardSize),
		Seats:      make(map[string]*Seat, len(stacks)),
	}
	for i, chips := range stacks {
		id := fmt.Sprintf("p%d", i)
		players = append(players, &game.Player{ID: id})
		extra.Seats[id] = &Seat{Chips: chips}
	}
	state := game.NewState(&Rules{}, players, deck.Standard())
	state.Extra = extra
	state.Phase = game.Playing
	return state, extra
}

// Counting funded seats after the blinds are posted makes a full table where the
// blinds bust two short stacks look heads-up, which hands the button first action
// instead of the seat under the gun.
func TestBeginHand_ShortBlindsDoNotMakeTheTableLookHeadsUp(t *testing.T) {
	t.Parallel()
	// Seat 0 has the button, so seat 1 posts the small blind and seat 2 the big
	// blind - both smaller than the blind they owe, so both are all-in on posting.
	state, extra := tableWithChips(1000, 20, 30, 1470)

	require.NoError(t, beginHand(state, extra, 0))

	assert.Equal(t, 1, extra.SBIndex)
	assert.Equal(t, 2, extra.BBIndex)
	assert.Equal(t, 3, state.CurrentTurn, "the seat after the big blind is under the gun, not the button")
}

// Blinds that bust every funded seat run the board out inside beginHand, so the
// deal returns a hand that is already over. It has to be closed the same way any
// other hand is, or the match sits complete with nobody parked on turn: no dealer
// to submit ActionNextHand and no MatchComplete to end it.
func TestBeginHandOrFinish_ClosesAHandTheDealAlreadyFinished(t *testing.T) {
	t.Parallel()
	// Heads-up, both stacks smaller than the blind they owe. The deal runs the board
	// out before anyone acts; beginHandOrFinish must call finishHand on that path
	// or the match parks with nobody on turn.
	state, extra := tableWithChips(20, 20)

	require.NoError(t, beginHandOrFinish(state, extra, 0))

	require.True(t, extra.HandComplete(), "nobody could act, so the board ran out")
	assert.Equal(t, PhaseShowdown, extra.Phase, "the hand was closed, not left hanging")
	assert.NotEmpty(t, extra.Winners, "somebody took the chips")
	// finishHand either ends the match (one funded seat) or parks the next dealer
	// on turn - never leaves OverrideNextTurn nil while the match is still live.
	if extra.MatchComplete {
		assert.True(t, (&Rules{}).CheckWinCondition(state))
		assert.Nil(t, state.OverrideNextTurn)
	} else {
		require.NotNil(t, state.OverrideNextTurn, "a live match needs a dealer for NextHand")
		assert.Equal(t, *state.OverrideNextTurn, state.CurrentTurn)
	}
}

func TestStandings_BustedPlayersRankByHowLongTheyLasted(t *testing.T) {
	t.Parallel()
	state, extra := tableWithChips(3000, 0, 0)
	extra.HandNumber, extra.HandsTotal = HandsPerMatch, HandsPerMatch
	// p1 went out early, p2 survived nearly to the end. Both finish on zero chips,
	// so nothing but the bust-out hand can separate them.
	extra.Seats["p1"].BustedAtHand, extra.Seats["p2"].BustedAtHand = 2, 9

	standings := (&Rules{}).Standings(state)

	assert.Equal(t, []string{"p0", "p2", "p1"},
		[]string{standings[0].ID, standings[1].ID, standings[2].ID})
}

func TestFinishHand_StampsTheHandAPlayerWentOutOn(t *testing.T) {
	t.Parallel()
	state, extra := tableWithChips(3000, 0, 500)
	extra.HandNumber, extra.HandsTotal = 4, HandsPerMatch

	finishHand(state, extra)

	assert.Equal(t, map[string]int{"p1": 4}, bustedAt(extra))

	// A later hand must not restamp a player who was already out.
	extra.HandNumber = 5
	extra.Seats["p2"].Chips = 0
	finishHand(state, extra)
	assert.Equal(t, map[string]int{"p1": 4, "p2": 5}, bustedAt(extra))
}

// bustedAt is the hand each busted seat went out on, leaving out those still in.
func bustedAt(extra *State) map[string]int {
	out := map[string]int{}
	for id, seat := range extra.Seats {
		if seat.BustedAtHand > 0 {
			out[id] = seat.BustedAtHand
		}
	}
	return out
}

// An uncontested pot is won face-down. With hands left to play, showing those
// cards would hand the rest of the table a free read.
func TestFoldedOutHand_IsNotShownDown(t *testing.T) {
	t.Parallel()
	engine := startTable(t, 3)
	t.Cleanup(engine.Close)

	for range 3 {
		if readExtra(t, engine, (*State).HandComplete) {
			break
		}
		require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), ActionFold{}))
	}

	withExtra(t, engine, func(extra *State) {
		require.True(t, extra.HandComplete())
		assert.False(t, extra.ReachedShowdown, "nobody called, so nobody has to show")
	})
}

func TestShowdown_MarksTheHandAsShownDown(t *testing.T) {
	t.Parallel()
	engine := startTable(t, 3)
	t.Cleanup(engine.Close)

	for range 10 {
		if readExtra(t, engine, (*State).HandComplete) {
			break
		}
		require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), ActionAllIn{}))
	}

	withExtra(t, engine, func(extra *State) {
		require.True(t, extra.HandComplete())
		assert.True(t, extra.ReachedShowdown, "an all-in board that runs out is shown down")
	})

}

func TestBeginHand_BustedPlayerSitsOut(t *testing.T) {
	t.Parallel()
	state := createTestState()
	extra := extra(t, state)
	extra.HandsTotal = HandsPerMatch
	extra.Seats["p2"].Chips = 0

	require.NoError(t, beginHand(state, extra, 0))

	assert.True(t, extra.Seats["p2"].Folded, "a busted player is folded for the rest of the match")
	assert.Empty(t, state.Players[1].Cards, "a busted player is not dealt in")
	assert.NotEqual(t, 1, extra.SBIndex)
	assert.NotEqual(t, 1, extra.BBIndex)
	assert.NotEqual(t, 1, state.CurrentTurn, "the turn cursor skips the empty seat")
}

// A big blind too short to post in full is all-in for less, but the bring-in stays at
// the full big blind and the shortfall is dead money. Letting CurrentBet follow what
// was actually posted opened the betting below the blind and dragged the first legal
// raise down with it, since MinRaise is measured from CurrentBet.
func TestBeginHand_AShortBigBlindDoesNotLowerTheBringIn(t *testing.T) {
	t.Parallel()
	// Button on seat 0, so seat 1 posts the small blind and seat 2 owes the big blind
	// with only 30 chips to post it with.
	state, extra := tableWithChips(1000, 1000, 30)
	rules := &Rules{}

	require.NoError(t, beginHand(state, extra, 0))

	require.Equal(t, 0, state.CurrentTurn, "the seat after the big blind is under the gun")
	assert.Equal(t, uint(30), extra.Seats["p2"].Bet, "the short blind posts what it has")
	assert.True(t, extra.Seats["p2"].AllIn, "and is all-in for it")
	assert.Equal(t, DefaultBigBlind, extra.CurrentBet, "the bring-in is still a full big blind")
	assert.Equal(t, DefaultBigBlind, extra.ToCall("p0"))

	require.ErrorContains(t, rules.ValidateAction(state, ActionRaiseTo{Amount: 99}),
		"minimum raise is 50", "a raise under a full blind on top of the bring-in is not one")
	require.NoError(t, rules.ValidateAction(state, ActionRaiseTo{Amount: 100}))
}

// Nobody can be raised past what they are able to put in. A raise above the largest
// opponent stack is chips no one can call, and the showdown would only hand them
// straight back, so it is refused at the point the player asks for it.
func TestValidateAction_RaiseIsCappedByTheLargestOpponentStack(t *testing.T) {
	t.Parallel()
	// Seat 0 is deep, its two opponents are short; the blinds leave p1 all-in.
	state, extra := tableWithChips(1000, 20, 300)
	rules := &Rules{}

	require.NoError(t, beginHand(state, extra, 0))
	require.Equal(t, 0, state.CurrentTurn)

	// p2 posted the big blind of 50 out of 300, so 300 is the most it can ever have out.
	require.NoError(t, rules.ValidateAction(state, ActionRaiseTo{Amount: 300}))
	require.ErrorContains(t, rules.ValidateAction(state, ActionRaiseTo{Amount: 301}),
		"no opponent can call more than 300")
	// Shoving stays legal: the uncalled part is refunded rather than staged.
	require.NoError(t, rules.ValidateAction(state, ActionAllIn{}))
}

func standingIDs(engine *game.Engine) []string {
	standings := engine.Standings()
	ids := make([]string, len(standings))
	for i, s := range standings {
		ids[i] = s.Player.ID
	}
	return ids
}
