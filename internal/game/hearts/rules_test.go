package hearts

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

func fourPlayers(hands ...[]deck.Card) []*game.Player {
	out := make([]*game.Player, playerCount)
	for i := range playerCount {
		cards := []deck.Card{}
		if i < len(hands) {
			cards = hands[i]
		}
		out[i] = &game.Player{ID: fmt.Sprintf("p%d", i+1), Cards: cards}
	}
	return out
}

func createTestState(hands ...[]deck.Card) *game.State {
	rules := &Rules{}
	players := fourPlayers(hands...)
	state := game.NewState(rules, players, nil)
	extra := &State{
		Stage:            StageTrickPlay,
		TrickCards:       make(map[string]deck.Card, playerCount),
		HandPoints:       make(map[string]int, playerCount),
		CumulativeScores: make(map[string]int, playerCount),
		TargetScore:      DefaultTargetScore,
	}
	for _, p := range players {
		extra.HandPoints[p.ID] = 0
		extra.CumulativeScores[p.ID] = 0
	}
	state.Extra = extra
	state.CurrentTurn = 0
	state.Phase = game.Playing
	return state
}

func TestRules_ValidateAction_Pass(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	hand := []deck.Card{
		{Rank: deck.Two, Suit: deck.Clubs},
		{Rank: deck.Three, Suit: deck.Clubs},
		{Rank: deck.Four, Suit: deck.Clubs},
		{Rank: deck.Five, Suit: deck.Clubs},
	}

	t.Run("accepts three owned cards", func(t *testing.T) {
		t.Parallel()
		state := createTestState(hand)
		state.Extra.(*State).Stage = StagePassing
		state.Extra.(*State).Passed = map[string]bool{}
		err := rules.ValidateAction(state, ActionPassCards{Cards: hand[:3]})
		require.NoError(t, err)
	})

	t.Run("rejects wrong count", func(t *testing.T) {
		t.Parallel()
		state := createTestState(hand)
		state.Extra.(*State).Stage = StagePassing
		state.Extra.(*State).Passed = map[string]bool{}
		err := rules.ValidateAction(state, ActionPassCards{Cards: hand[:2]})
		require.ErrorContains(t, err, "exactly 3")
	})

	t.Run("rejects duplicate", func(t *testing.T) {
		t.Parallel()
		state := createTestState(hand)
		state.Extra.(*State).Stage = StagePassing
		state.Extra.(*State).Passed = map[string]bool{}
		err := rules.ValidateAction(state, ActionPassCards{Cards: []deck.Card{hand[0], hand[0], hand[1]}})
		require.ErrorContains(t, err, "duplicate")
	})

	t.Run("rejects second pass", func(t *testing.T) {
		t.Parallel()
		state := createTestState(hand)
		extra := state.Extra.(*State)
		extra.Stage = StagePassing
		extra.Passed = map[string]bool{"p1": true}
		err := rules.ValidateAction(state, ActionPassCards{Cards: hand[:3]})
		require.ErrorContains(t, err, "already passed")
	})
}

func TestRules_AfterAction_Pass_AppliesOnFourth(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	hands := make([][]deck.Card, 4)
	for i := range 4 {
		hands[i] = []deck.Card{
			{Rank: deck.Rank(i + 1), Suit: deck.Clubs},
			{Rank: deck.Rank(i + 1), Suit: deck.Diamonds},
			{Rank: deck.Rank(i + 1), Suit: deck.Hearts},
			{Rank: deck.Rank(i + 5), Suit: deck.Spades},
		}
	}
	// Seat 0 keeps 2♣ (index 3 is not passed) so they lead after the pass.
	hands[0][3] = twoOfClubs

	state := createTestState(hands...)
	extra := state.Extra.(*State)
	extra.Stage = StagePassing
	extra.PassDirection = PassLeft
	extra.PendingPasses = make(map[string][]deck.Card, 4)
	extra.Passed = make(map[string]bool, 4)

	for seat := range 4 {
		state.CurrentTurn = seat
		pass := hands[seat][:3]
		require.NoError(t, rules.ValidateAction(state, ActionPassCards{Cards: pass}))
		rules.ApplyAction(state, ActionPassCards{Cards: pass})
		require.NoError(t, rules.AfterAction(state, ActionPassCards{Cards: pass}))
	}

	assert.Equal(t, StageTrickPlay, extra.Stage)
	assert.Nil(t, extra.PendingPasses)
	assert.Len(t, state.Players[0].Cards, 4) // kept 1, received 3
	require.NotNil(t, state.OverrideNextTurn)
	assert.Equal(t, 0, *state.OverrideNextTurn)
	assert.True(t, slices.Contains(state.Players[0].Cards, twoOfClubs))
}

func TestRules_ValidateAction_Play(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	t.Run("must lead two of clubs on trick one", func(t *testing.T) {
		t.Parallel()
		state := createTestState([]deck.Card{
			twoOfClubs,
			{Rank: deck.Ace, Suit: deck.Spades},
		})
		err := rules.ValidateAction(state, ActionPlayCard{Card: deck.Card{Rank: deck.Ace, Suit: deck.Spades}})
		require.ErrorContains(t, err, "2 of clubs")
	})

	t.Run("must follow suit", func(t *testing.T) {
		t.Parallel()
		state := createTestState([]deck.Card{
			{Rank: deck.Ace, Suit: deck.Clubs},
			{Rank: deck.King, Suit: deck.Hearts},
		})
		extra := state.Extra.(*State)
		extra.TricksPlayed = 1
		extra.LedSuit = deck.Clubs
		extra.TrickCards["p2"] = deck.Card{Rank: deck.Two, Suit: deck.Clubs}
		err := rules.ValidateAction(state, ActionPlayCard{Card: deck.Card{Rank: deck.King, Suit: deck.Hearts}})
		require.ErrorContains(t, err, "follow suit")
	})

	t.Run("hearts cannot be led before broken", func(t *testing.T) {
		t.Parallel()
		state := createTestState([]deck.Card{
			{Rank: deck.Ace, Suit: deck.Hearts},
			{Rank: deck.King, Suit: deck.Spades},
		})
		extra := state.Extra.(*State)
		extra.TricksPlayed = 1
		err := rules.ValidateAction(state, ActionPlayCard{Card: deck.Card{Rank: deck.Ace, Suit: deck.Hearts}})
		require.ErrorContains(t, err, "not been broken")
	})

	t.Run("cannot play point card on trick one", func(t *testing.T) {
		t.Parallel()
		state := createTestState([]deck.Card{
			{Rank: deck.Ace, Suit: deck.Hearts},
			{Rank: deck.King, Suit: deck.Diamonds},
		})
		extra := state.Extra.(*State)
		extra.LedSuit = deck.Clubs
		extra.TrickCards["p2"] = deck.Card{Rank: deck.Two, Suit: deck.Clubs}
		err := rules.ValidateAction(state, ActionPlayCard{Card: deck.Card{Rank: deck.Ace, Suit: deck.Hearts}})
		require.ErrorContains(t, err, "point card")
	})
}

func TestRules_ApplyAction_Play_BreaksHeartsAndTrickWinner(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := createTestState(
		[]deck.Card{{Rank: deck.Five, Suit: deck.Diamonds}},
		[]deck.Card{{Rank: deck.Ace, Suit: deck.Diamonds}},
		[]deck.Card{{Rank: deck.Three, Suit: deck.Hearts}},
		[]deck.Card{{Rank: deck.Two, Suit: deck.Diamonds}},
	)
	extra := state.Extra.(*State)
	extra.TricksPlayed = 1

	plays := []struct {
		seat int
		card deck.Card
	}{
		{0, deck.Card{Rank: deck.Five, Suit: deck.Diamonds}},
		{1, deck.Card{Rank: deck.Ace, Suit: deck.Diamonds}},
		{2, deck.Card{Rank: deck.Three, Suit: deck.Hearts}},
		{3, deck.Card{Rank: deck.Two, Suit: deck.Diamonds}},
	}
	for _, play := range plays {
		state.CurrentTurn = play.seat
		rules.ApplyAction(state, ActionPlayCard{Card: play.card})
		require.NoError(t, rules.AfterAction(state, ActionPlayCard{Card: play.card}))
	}

	assert.True(t, extra.HeartsBroken)
	assert.Equal(t, "p2", extra.LastTrickWinner)
	assert.Equal(t, 1, extra.HandPoints["p2"])
	require.NotNil(t, state.OverrideNextTurn)
	assert.Equal(t, 1, *state.OverrideNextTurn)
}

// The negative case for HeartsBroken: a trick with no heart in it must leave hearts
// unbroken, or leading a heart becomes legal a trick too early.
func TestRules_ApplyAction_Play_TrickWithoutHeartsLeavesThemUnbroken(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := createTestState(
		[]deck.Card{{Rank: deck.Five, Suit: deck.Diamonds}},
		[]deck.Card{{Rank: deck.Ace, Suit: deck.Diamonds}},
		[]deck.Card{{Rank: deck.Three, Suit: deck.Clubs}},
		[]deck.Card{{Rank: deck.Two, Suit: deck.Diamonds}},
	)
	extra := state.Extra.(*State)
	extra.TricksPlayed = 1

	for seat, card := range []deck.Card{
		{Rank: deck.Five, Suit: deck.Diamonds},
		{Rank: deck.Ace, Suit: deck.Diamonds},
		{Rank: deck.Three, Suit: deck.Clubs},
		{Rank: deck.Two, Suit: deck.Diamonds},
	} {
		state.CurrentTurn = seat
		rules.ApplyAction(state, ActionPlayCard{Card: card})
		require.NoError(t, rules.AfterAction(state, ActionPlayCard{Card: card}))
	}

	assert.False(t, extra.HeartsBroken, "no heart was played")
	assert.Zero(t, extra.HandPoints["p2"], "a heartless trick scores nothing")
}

// The led suit is fixed by the first card of the trick, not the last. A higher card
// of another suit does not win, however high it is.
func TestRules_AfterAction_Play_LedSuitIsTheFirstCardNotTheLast(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := createTestState(
		[]deck.Card{{Rank: deck.Nine, Suit: deck.Clubs}},
		[]deck.Card{{Rank: deck.Ace, Suit: deck.Diamonds}},
		[]deck.Card{{Rank: deck.King, Suit: deck.Clubs}},
		[]deck.Card{{Rank: deck.Two, Suit: deck.Diamonds}},
	)
	extra := state.Extra.(*State)
	extra.TricksPlayed = 1

	for seat, card := range []deck.Card{
		{Rank: deck.Nine, Suit: deck.Clubs},   // leads clubs
		{Rank: deck.Ace, Suit: deck.Diamonds}, // higher rank, wrong suit
		{Rank: deck.King, Suit: deck.Clubs},   // highest club: takes the trick
		{Rank: deck.Two, Suit: deck.Diamonds}, // last card, and its suit must not lead
	} {
		state.CurrentTurn = seat
		rules.ApplyAction(state, ActionPlayCard{Card: card})
		require.NoError(t, rules.AfterAction(state, ActionPlayCard{Card: card}))
	}

	assert.Equal(t, "p3", extra.LastTrickWinner, "the king of clubs beats the ace of diamonds")
	require.NotNil(t, state.OverrideNextTurn)
	assert.Equal(t, 2, *state.OverrideNextTurn, "the trick winner leads the next one")
}

func TestThreeLowestCards(t *testing.T) {
	t.Parallel()

	t.Run("picks the three lowest by rank", func(t *testing.T) {
		t.Parallel()
		hand := []deck.Card{
			{Rank: deck.King, Suit: deck.Spades},
			{Rank: deck.Three, Suit: deck.Hearts},
			{Rank: deck.Ace, Suit: deck.Clubs},
			{Rank: deck.Two, Suit: deck.Diamonds},
			{Rank: deck.Ten, Suit: deck.Clubs},
			{Rank: deck.Four, Suit: deck.Spades},
		}
		got := threeLowestCards(hand)
		assert.Equal(t, []deck.Card{
			{Rank: deck.Two, Suit: deck.Diamonds},
			{Rank: deck.Three, Suit: deck.Hearts},
			{Rank: deck.Four, Suit: deck.Spades},
		}, got, "ace is high in Hearts, so it is never among the lowest")
	})

	t.Run("same-rank cards break the tie on suit", func(t *testing.T) {
		t.Parallel()
		hand := []deck.Card{
			{Rank: deck.Three, Suit: deck.Hearts},
			{Rank: deck.Two, Suit: deck.Clubs},
			{Rank: deck.King, Suit: deck.Spades},
			{Rank: deck.Two, Suit: deck.Diamonds},
		}
		assert.Equal(t, []deck.Card{
			{Rank: deck.Two, Suit: deck.Diamonds},
			{Rank: deck.Two, Suit: deck.Clubs},
			{Rank: deck.Three, Suit: deck.Hearts},
		}, threeLowestCards(hand), "equal ranks order by suit, so the pass is deterministic")
	})

	t.Run("a short hand passes whatever it has", func(t *testing.T) {
		t.Parallel()
		hand := []deck.Card{{Rank: deck.King, Suit: deck.Spades}, {Rank: deck.Two, Suit: deck.Clubs}}
		assert.Equal(t, hand, threeLowestCards(hand))
	})
}

func TestRules_CheckWinCondition_AndStandings(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := createTestState()
	extra := state.Extra.(*State)

	assert.False(t, rules.CheckWinCondition(state))
	extra.MatchComplete = true
	assert.True(t, rules.CheckWinCondition(state))

	extra.CumulativeScores["p1"] = 40
	extra.CumulativeScores["p2"] = 10
	extra.CumulativeScores["p3"] = 10
	extra.CumulativeScores["p4"] = 80
	standings := rules.Standings(state)
	require.Len(t, standings, 4)
	assert.Equal(t, "p2", standings[0].ID)
	assert.Equal(t, "p3", standings[1].ID)
	assert.Equal(t, "p1", standings[2].ID)
	assert.Equal(t, "p4", standings[3].ID)
}

func TestRules_TimeoutAction(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	t.Run("passing returns three cards", func(t *testing.T) {
		t.Parallel()
		hand := []deck.Card{
			{Rank: deck.Ace, Suit: deck.Spades},
			{Rank: deck.Two, Suit: deck.Clubs},
			{Rank: deck.Three, Suit: deck.Diamonds},
			{Rank: deck.King, Suit: deck.Hearts},
		}
		state := createTestState(hand)
		state.Extra.(*State).Stage = StagePassing
		act := rules.TimeoutAction(state)
		pass, ok := act.(ActionPassCards)
		require.True(t, ok)
		assert.Len(t, pass.Cards, 3)
		require.NoError(t, rules.ValidateAction(state, act))
	})

	t.Run("hand over returns next hand", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Extra.(*State).Stage = StageHandOver
		act := rules.TimeoutAction(state)
		assert.Equal(t, ActionNextHand{}, act)
		require.NoError(t, rules.ValidateAction(state, act))
	})

	t.Run("match complete returns nil", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		extra := state.Extra.(*State)
		extra.Stage = StageHandOver
		extra.MatchComplete = true
		assert.Nil(t, rules.TimeoutAction(state))
	})
}

// The engine broadcasts a play after AfterAction returns, so a trick swept the
// moment its fourth card lands is gone before anyone is told about it: the three
// players who did not take it never see what beat them. It has to stay on the table
// until somebody leads the next one.
func TestRules_FinishedTrickStaysOnTheTableUntilTheNextLead(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	// Everyone follows clubs; p3 takes the trick with the king.
	state := createTestState(
		[]deck.Card{{Rank: deck.Five, Suit: deck.Clubs}},
		[]deck.Card{{Rank: deck.Three, Suit: deck.Clubs}},
		[]deck.Card{{Rank: deck.King, Suit: deck.Clubs}, {Rank: deck.Nine, Suit: deck.Diamonds}},
		[]deck.Card{{Rank: deck.Two, Suit: deck.Clubs}},
	)
	extra := state.Extra.(*State)
	extra.TricksPlayed = 1 // past the opening trick, so the 2♣ lead rule is done

	for seat, p := range state.Players {
		state.CurrentTurn = seat
		card := p.Cards[0]
		require.NoError(t, rules.ValidateAction(state, ActionPlayCard{Card: card}))
		rules.ApplyAction(state, ActionPlayCard{Card: card})
		require.NoError(t, rules.AfterAction(state, ActionPlayCard{Card: card}))
	}

	require.Equal(t, "p3", extra.LastTrickWinner)
	assert.True(t, extra.TrickComplete)
	assert.Len(t, extra.TrickCards, playerCount, "all four cards are still up for the broadcast")
	assert.Equal(t, deck.Clubs, extra.LedSuit, "and the suit that was led still reads correctly")

	// The winner leads the next trick. A won trick is not one in progress, so an
	// off-suit lead is a lead, not a failure to follow suit.
	next := deck.Card{Rank: deck.Nine, Suit: deck.Diamonds}
	require.NoError(t, rules.ValidateAction(state, ActionPlayCard{Card: next}))
	rules.ApplyAction(state, ActionPlayCard{Card: next})

	assert.False(t, extra.TrickComplete)
	assert.Equal(t, map[string]deck.Card{"p3": next}, extra.TrickCards, "the table was swept for the new trick")
	assert.Equal(t, deck.Diamonds, extra.LedSuit)
}

func TestRules_OnPlayerLeave_EndsMatch(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := createTestState()
	rules.OnPlayerLeave(state, "p2")
	assert.True(t, state.Extra.(*State).MatchComplete)
	assert.True(t, rules.CheckWinCondition(state))
	assert.True(t, state.Interrupted, "the match ended on a leave, not on the score")
}

// Hearts cannot go on three-handed, so one leave ends the match for everyone. That is
// not a result the seats still playing earned, and the engine has to say so.
func TestRules_LeaveEndsTheMatchAsInterrupted(t *testing.T) {
	t.Parallel()
	players := []*game.Player{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}, {ID: "p4"}}
	engine := game.NewEngine(&Rules{}, players, deck.StandardDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)
	events, err := engine.Broadcaster().Subscribe()
	require.NoError(t, err)

	engine.RemovePlayer("p2")

	require.True(t, engine.IsFinished())
	reason := game.EndReasonUnknown
	for len(events) > 0 {
		if ev := <-events; ev.Type == game.EventGameEnded {
			reason = ev.Reason
		}
	}
	assert.Equal(t, game.EndReasonInterrupted, reason)
}

func TestSmoke_FullHandConservesTheDeck(t *testing.T) {
	t.Parallel()
	engine := game.NewEngine(&Rules{}, fourPlayers(), deck.StandardDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	// Drive the opening pass (or skip if hand 1 somehow had PassNone - it won't).
	for {
		var stage Stage
		engine.WithState(func(s *game.State) {
			stage = s.Extra.(*State).Stage
		})
		if stage != StagePassing {
			break
		}
		id := engine.CurrentPlayerID()
		var cards []deck.Card
		engine.WithState(func(s *game.State) {
			for _, p := range s.Players {
				if p.ID == id {
					cards = threeLowestCards(p.Cards)
					break
				}
			}
		})
		require.NoError(t, engine.SubmitAction(id, ActionPassCards{Cards: cards}))
	}

	for range cardsPerHand {
		for range playerCount {
			id := engine.CurrentPlayerID()
			var act game.Action
			engine.WithState(func(s *game.State) {
				extra := s.Extra.(*State)
				p := s.Players[s.CurrentTurn]
				card, ok := firstLegalCard(extra, p)
				require.True(t, ok, "seat %s must have a legal card", id)
				act = ActionPlayCard{Card: card}
			})
			require.NoError(t, engine.SubmitAction(id, act))
		}
	}

	engine.WithState(func(s *game.State) {
		extra := s.Extra.(*State)
		assert.Equal(t, StageHandOver, extra.Stage)
		assert.Equal(t, cardsPerHand, extra.TricksPlayed)
		total := 0
		for _, pts := range extra.HandPoints {
			total += pts
		}
		assert.Equal(t, penaltyPointsTotal, total)
		inHands := 0
		for _, p := range s.Players {
			inHands += len(p.Cards)
		}
		assert.Zero(t, inHands)
	})
}

// Only PassLeft was wired through applyAllPasses, and every direction is one modulo
// away from another: an inverted right or a misplaced across would ship green.
func TestApplyAllPasses_DeliversInEveryDirection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dir  PassDirection
		// want[seat] is the seat whose cards land in seat's hand.
		want [playerCount]int
	}{
		{name: "left", dir: PassLeft, want: [playerCount]int{3, 0, 1, 2}},
		{name: "right", dir: PassRight, want: [playerCount]int{1, 2, 3, 0}},
		{name: "across", dir: PassAcross, want: [playerCount]int{2, 3, 0, 1}},
		{name: "hold", dir: PassNone, want: [playerCount]int{0, 1, 2, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state := createTestState()
			extra := state.Extra.(*State)
			extra.PassDirection = tt.dir
			extra.PendingPasses = make(map[string][]deck.Card, playerCount)
			// One card per seat, its rank naming the seat it came from.
			for seat, p := range state.Players {
				p.Cards = nil
				extra.PendingPasses[p.ID] = []deck.Card{
					{Rank: deck.Rank(seat + 2), Suit: deck.Clubs},
				}
			}

			applyAllPasses(state, extra)

			for seat, p := range state.Players {
				require.Len(t, p.Cards, 1, "seat %d", seat)
				assert.Equal(t, deck.Rank(tt.want[seat]+2), p.Cards[0].Rank,
					"seat %d should receive from seat %d", seat, tt.want[seat])
			}
		})
	}
}

// The moon has to be reachable from real trick play, not only from a HandPoints map
// set by hand: one seat holding every club takes all thirteen tricks, and every heart
// and the queen land in them.
func TestMatch_MoonFromRealTrickPlay(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	suitOf := func(s deck.Suit) []deck.Card {
		cards := make([]deck.Card, 0, cardsPerHand)
		for r := deck.Two; r <= deck.King; r++ {
			cards = append(cards, deck.Card{Rank: r, Suit: s})
		}
		return append(cards, deck.Card{Rank: deck.Ace, Suit: s})
	}
	// p1 holds every club: they lead the 2 of clubs and nobody can ever follow suit.
	state := createTestState(
		suitOf(deck.Clubs), suitOf(deck.Spades), suitOf(deck.Hearts), suitOf(deck.Diamonds),
	)
	extra := state.Extra.(*State)
	extra.TargetScore = penaltyPointsTotal

	for trick := range cardsPerHand {
		for seat := range playerCount {
			state.CurrentTurn = seat
			p := state.Players[seat]
			card, ok := firstLegalCard(extra, p)
			require.True(t, ok, "trick %d seat %d has no legal card", trick, seat)
			require.NoError(t, rules.ValidateAction(state, ActionPlayCard{Card: card}))
			rules.ApplyAction(state, ActionPlayCard{Card: card})
			require.NoError(t, rules.AfterAction(state, ActionPlayCard{Card: card}))
		}
		require.Equal(t, "p1", extra.LastTrickWinner, "trick %d", trick)
	}

	assert.Equal(t, penaltyPointsTotal, extra.HandPoints["p1"], "every point landed in p1's tricks")
	assert.Equal(t, 0, extra.CumulativeScores["p1"], "the shooter takes nothing")
	for _, id := range []string{"p2", "p3", "p4"} {
		assert.Equal(t, penaltyPointsTotal, extra.CumulativeScores[id], id)
	}
	assert.True(t, extra.MatchComplete, "the moon pushed the other three over the target")
	assert.True(t, rules.CheckWinCondition(state))
}

// Both first-trick restrictions have an exemption for a hand with nothing else to
// play. Only the refusals were tested, so an exemption that never fired would have
// left those hands with no legal move at all.
func TestRules_ValidateAction_FirstTrickExemptions(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	t.Run("a hand of nothing but hearts may lead one", func(t *testing.T) {
		t.Parallel()
		state := createTestState([]deck.Card{
			{Rank: deck.Ace, Suit: deck.Hearts},
			{Rank: deck.Three, Suit: deck.Hearts},
		})
		state.Extra.(*State).TricksPlayed = 1

		require.NoError(t, rules.ValidateAction(state,
			ActionPlayCard{Card: deck.Card{Rank: deck.Ace, Suit: deck.Hearts}}))
	})

	t.Run("a hand of nothing but points may dump one on trick 1", func(t *testing.T) {
		t.Parallel()
		state := createTestState([]deck.Card{
			{Rank: deck.Ace, Suit: deck.Hearts},
			queenOfSpades,
		})
		extra := state.Extra.(*State)
		extra.LedSuit = deck.Clubs
		extra.TrickCards["p2"] = twoOfClubs

		require.NoError(t, rules.ValidateAction(state,
			ActionPlayCard{Card: deck.Card{Rank: deck.Ace, Suit: deck.Hearts}}))
		require.NoError(t, rules.ValidateAction(state, ActionPlayCard{Card: queenOfSpades}))
	})
}

// Trick play is the stage an absent player spends twelve of thirteen turns in, and
// the move has to be one ValidateAction accepts or the engine takes the seat.
func TestRules_TimeoutAction_TrickPlay(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	t.Run("plays the first legal card", func(t *testing.T) {
		t.Parallel()
		state := createTestState([]deck.Card{
			{Rank: deck.King, Suit: deck.Hearts}, // illegal: cannot dump points on trick 1
			{Rank: deck.Nine, Suit: deck.Diamonds},
		})
		extra := state.Extra.(*State)
		extra.LedSuit = deck.Clubs
		extra.TrickCards["p2"] = twoOfClubs

		act := rules.TimeoutAction(state)
		play, ok := act.(ActionPlayCard)
		require.True(t, ok, "got %T", act)
		assert.Equal(t, deck.Card{Rank: deck.Nine, Suit: deck.Diamonds}, play.Card)
		require.NoError(t, rules.ValidateAction(state, act), "the safe move must be legal")
	})

	t.Run("must follow suit when it can", func(t *testing.T) {
		t.Parallel()
		state := createTestState([]deck.Card{
			{Rank: deck.Nine, Suit: deck.Diamonds},
			{Rank: deck.Four, Suit: deck.Clubs},
		})
		extra := state.Extra.(*State)
		extra.TricksPlayed = 1
		extra.LedSuit = deck.Clubs
		extra.TrickCards["p2"] = twoOfClubs

		act := rules.TimeoutAction(state)
		require.NoError(t, rules.ValidateAction(state, act))
		assert.Equal(t, ActionPlayCard{Card: deck.Card{Rank: deck.Four, Suit: deck.Clubs}}, act)
	})

	t.Run("an empty hand has no move", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		assert.Nil(t, rules.TimeoutAction(state))
	})
}

// The fourth hand of a match is held, not passed. It still has to open on the 2 of
// clubs, which is a different code path from the one the passing hands take.
func TestRules_BeginHand_HoldHandSeatsTheTwoOfClubs(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := createTestState()
	extra := state.Extra.(*State)
	extra.HandNumber = 3 // the next hand is the fourth: PassNone

	require.NoError(t, rules.beginHand(state, extra, 2))

	require.Equal(t, PassNone, extra.PassDirection)
	assert.Equal(t, StageTrickPlay, extra.Stage, "nothing to pass, so trick play starts at once")
	assert.Nil(t, extra.PendingPasses)
	require.NotNil(t, state.OverrideNextTurn)
	assert.Equal(t, findTwoOfClubs(state), *state.OverrideNextTurn)
	assert.Contains(t, state.Players[state.CurrentTurn].Cards, twoOfClubs)
}

// The queen of spades is a point card but not a heart: playing her must not unlock
// leading hearts a trick early.
func TestRules_ApplyAction_QueenOfSpadesDoesNotBreakHearts(t *testing.T) {
	t.Parallel()
	state := createTestState([]deck.Card{queenOfSpades})
	extra := state.Extra.(*State)
	extra.TricksPlayed = 1
	extra.LedSuit = deck.Spades
	extra.TrickCards["p2"] = deck.Card{Rank: deck.Two, Suit: deck.Spades}

	(&Rules{}).ApplyAction(state, ActionPlayCard{Card: queenOfSpades})

	assert.False(t, extra.HeartsBroken, "the queen is a point card, not a heart")
}

// Standings and StandingScore have to agree, or the engine splits a genuine draw by
// slice position and the seat that sorted first takes rating off the seat that did not.
func TestRules_StandingScore_TiedSeatsShareAPlace(t *testing.T) {
	t.Parallel()
	engine := game.NewEngine(&Rules{}, fourPlayers(), deck.StandardDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	engine.WithState(func(s *game.State) {
		extra := s.Extra.(*State)
		extra.CumulativeScores = map[string]int{"p1": 10, "p2": 10, "p3": 5, "p4": 20}
	})

	standings, places := engine.StandingsWithPlaces()
	require.Len(t, standings, playerCount)
	assert.Equal(t, "p3", standings[0].ID, "the fewest points wins hearts")
	assert.Equal(t, []int{1, 2, 2, 4}, places, "equal totals are one place, not two")
}

// A table that ends mid-hand on a disconnect has a live hand nobody has scored yet.
// Those points decide the winner; once scoreHand folds them into the totals they must
// not be counted a second time, or every completed match doubles its last hand.
func TestRules_StandingScore_CountsTheLiveHandExactlyOnce(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	engine := game.NewEngine(rules, fourPlayers(), deck.StandardDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	engine.WithState(func(s *game.State) {
		extra := s.Extra.(*State)
		extra.CumulativeScores = map[string]int{"p1": 0, "p2": 0, "p3": 0, "p4": 0}
		extra.HandPoints = map[string]int{"p1": 0, "p2": 0, "p3": 0, "p4": 25}
		extra.HandComplete = false

		assert.Equal(t, 25, rules.StandingScore(s, s.Players[3]), "mid-hand, the live hand counts")
		assert.Equal(t, 0, rules.StandingScore(s, s.Players[0]))

		// scoreHand has run: totals now hold the hand, and HandPoints still does too.
		extra.CumulativeScores["p4"] = 25
		extra.HandComplete = true
		assert.Equal(t, 25, rules.StandingScore(s, s.Players[3]), "scored once, not twice")
	})
}

func TestRules_TableSize(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	assert.Equal(t, playerCount, rules.MinPlayers(), "hearts is a four-handed game either way")
	assert.Equal(t, playerCount, rules.MaxPlayers())
	assert.Len(t, rules.InitialDeck(), 52)
	assert.Equal(t, "hearts.PassCards", ActionPassCards{}.Name())
	assert.Equal(t, "hearts.PlayCard", ActionPlayCard{}.Name())
	assert.Equal(t, "hearts.NextHand", ActionNextHand{}.Name())
}

// Two stages need longer than a play: choosing three cards to give away, and the
// between-hands prompt, which is a decision rather than a move. Zero everywhere else
// means "engine default", not "no clock" - returning a real duration there would
// quietly redefine the turn length for every trick.
func TestRules_TurnTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		stage Stage
		want  time.Duration
	}{
		{name: "passing gets 45 seconds", stage: StagePassing, want: 45 * time.Second},
		{name: "the hand-over prompt gets a minute", stage: StageHandOver, want: time.Minute},
		{name: "trick play keeps the engine default", stage: StageTrickPlay},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state := createTestState()
			state.Extra.(*State).Stage = tt.stage
			assert.Equal(t, tt.want, (&Rules{}).TurnTimeout(state))
		})
	}

	t.Run("a state that is not a hearts state has no opinion", func(t *testing.T) {
		t.Parallel()
		assert.Zero(t, (&Rules{}).TurnTimeout(&game.State{}))
	})
}

// A hearts match that ends on a disconnect is scored where it stood, and where it stood
// includes the hand in progress: the totals alone would rank four seats that have all
// played twelve tricks purely by who sat first. The seat that walked out ranks last
// whatever they were holding.
func TestStandings_MidHandLeaveCountsTheLiveHand(t *testing.T) {
	t.Parallel()
	players := []*game.Player{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}, {ID: "p4"}}
	engine := game.NewEngine(&Rules{}, players, deck.StandardDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	engine.WithState(func(s *game.State) {
		extra := s.Extra.(*State)
		extra.Stage = StageTrickPlay
		extra.HandComplete = false
		extra.CumulativeScores = map[string]int{"p1": 10, "p2": 0, "p3": 10, "p4": 10}
		// p3 took the queen this hand, so on the totals alone they would tie p1 and p4
		// and place ahead of both on seat order.
		extra.HandPoints = map[string]int{"p1": 0, "p2": 5, "p3": 13, "p4": 1}
	})

	engine.RemovePlayer("p2")

	standings, places := engine.StandingsWithPlaces()
	require.Len(t, standings, 4)
	ids := make([]string, len(standings))
	for i, p := range standings {
		ids[i] = p.ID
	}
	assert.Equal(t, []string{"p1", "p4", "p3", "p2"}, ids,
		"lowest live total first, and the leaver last whatever they scored")
	assert.Equal(t, []int{1, 2, 3, 4}, places,
		"p2 left on 5 points and must not tie anyone still at the table")
}

// The stage machine is what stops a client replaying a move from a phase that is over.
func TestRules_ValidateAction_StageGates(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	tests := []struct {
		name    string
		stage   Stage
		action  game.Action
		wantErr string
	}{
		{
			name: "a play during the pass phase", stage: StagePassing,
			action: ActionPlayCard{}, wantErr: "must pass cards during passing phase",
		},
		{
			name: "a pass during trick play", stage: StageTrickPlay,
			action:  ActionPassCards{Cards: make([]deck.Card, cardsToPass)},
			wantErr: "must play a card during trick play",
		},
		{
			name: "any move once the hand is over", stage: StageHandOver,
			action: ActionPlayCard{}, wantErr: "hand is over",
		},
		{
			name: "the next hand while this one is live", stage: StageTrickPlay,
			action: ActionNextHand{}, wantErr: "the hand is still being played",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state := createTestState()
			state.Extra.(*State).Stage = tt.stage
			assert.ErrorContains(t, rules.ValidateAction(state, tt.action), tt.wantErr)
		})
	}

	t.Run("the next hand once the match is over", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		extra := state.Extra.(*State)
		extra.Stage = StageHandOver
		extra.MatchComplete = true
		assert.ErrorContains(t, rules.ValidateAction(state, ActionNextHand{}), "the match is over")
	})

	t.Run("a state that is not a hearts state", func(t *testing.T) {
		t.Parallel()
		state := &game.State{}
		require.ErrorIs(t, rules.ValidateAction(state, ActionNextHand{}), game.ErrInvalidState)
		assert.Nil(t, rules.Standings(state))
		assert.Zero(t, rules.StandingScore(state, &game.Player{ID: "p1"}))
		assert.Nil(t, rules.TimeoutAction(state))
	})
}

// PassDirection.String is the one label table the logs and the TUI banner share, so a
// value neither of them expects still has to name itself.
func TestPassDirection_String(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "left", PassLeft.String())
	assert.Equal(t, "right", PassRight.String())
	assert.Equal(t, "across", PassAcross.String())
	assert.Equal(t, "hold", PassNone.String())
	assert.Equal(t, "unknown", PassDirection(9).String())
}

// The engine plays TimeoutAction for a seat that has gone quiet. A move ValidateAction
// refuses is not a skipped turn: the clock re-arms and the seat is taken on the next
// expiry, so a player is removed for a mistake the rules made. Hearts is the hardest
// case - the pass phase, the 2♣ lead, following suit and the first-trick point ban are
// four different reasons a plausible move is illegal.
func TestSoak_TimeoutActionIsAlwaysLegal(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	rapid.Check(t, func(rt *rapid.T) {
		players := []*game.Player{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}, {ID: "p4"}}
		engine := game.NewEngine(rules, players, deck.StandardDeck())
		require.NoError(rt, engine.Start())
		defer engine.Close()

		for step := range 2000 {
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
		rt.Fatalf("a match played entirely by the clock never reached %d points", DefaultTargetScore)
	})
}
