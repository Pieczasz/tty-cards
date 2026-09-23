package crazyeight

import (
	"bytes"
	"fmt"
	"log/slog"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

func createTestState() *game.State {
	rules := &Rules{}
	players := []*game.Player{{ID: "p1", Cards: []deck.Card{
		{Rank: deck.Two, Suit: deck.Spades},
		{Rank: deck.King, Suit: deck.Hearts},
		{Rank: deck.Eight, Suit: deck.Diamonds},
	}}}
	state := game.NewState(rules, players, deck.StandardDeck())
	state.Extra = &State{CurrentSuit: deck.Spades}
	state.Discard = deck.New([]deck.Card{{Rank: deck.Nine, Suit: deck.Spades}})
	state.CurrentTurn = 0
	return state
}

func TestRules_ValidateAction_PlayCard(t *testing.T) {
	t.Parallel()

	t.Run("valid matching suit", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		rules := &Rules{}
		action := ActionPlayCard{Card: deck.Card{Rank: deck.Two, Suit: deck.Spades}}

		err := rules.ValidateAction(state, action)
		require.NoError(t, err)
	})

	t.Run("valid matching rank", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Discard = deck.New([]deck.Card{{Rank: deck.King, Suit: deck.Clubs}})
		rules := &Rules{}
		action := ActionPlayCard{Card: deck.Card{Rank: deck.King, Suit: deck.Hearts}}

		err := rules.ValidateAction(state, action)
		require.NoError(t, err)
	})

	t.Run("valid eight wildcard", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		rules := &Rules{}
		// An eight matches neither the suit nor the rank on the pile; naming a suit
		// is what makes it playable.
		action := ActionPlayCard{
			Card: deck.Card{Rank: deck.Eight, Suit: deck.Diamonds},
			Suit: deck.Clubs,
		}

		err := rules.ValidateAction(state, action)
		require.NoError(t, err)
	})

	t.Run("invalid mismatch", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		rules := &Rules{}
		action := ActionPlayCard{Card: deck.Card{Rank: deck.King, Suit: deck.Hearts}}

		err := rules.ValidateAction(state, action)
		require.ErrorContains(t, err, "card doesn't match top discard")
	})

	t.Run("invalid card not in hand", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		rules := &Rules{}
		action := ActionPlayCard{Card: deck.Card{Rank: deck.Ace, Suit: deck.Spades}}

		err := rules.ValidateAction(state, action)
		require.ErrorContains(t, err, "you don't have that card")
	})
}

func TestRules_ApplyAction(t *testing.T) {
	t.Parallel()

	t.Run("card that matches rank or suit", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		rules := &Rules{}
		action := ActionPlayCard{Card: deck.Card{Rank: deck.Two, Suit: deck.Spades}}

		rules.ApplyAction(state, action)

		extra := state.Extra.(*State)

		assert.Equal(t, deck.Spades, extra.CurrentSuit)
		assert.Len(t, state.Players[0].Cards, 2)

		top, _ := state.Discard.Peek()
		assert.Equal(t, deck.Two, top.Rank)
		assert.Equal(t, deck.Spades, top.Suit)
	})

	t.Run("an eight changes the current suit and moves to the discard", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Players[0].Cards = []deck.Card{{Rank: deck.Eight, Suit: deck.Diamonds}, {Rank: deck.King, Suit: deck.Hearts}}
		rules := &Rules{}
		action := ActionPlayCard{Card: deck.Card{Rank: deck.Eight, Suit: deck.Diamonds}, Suit: deck.Clubs}

		rules.ApplyAction(state, action)

		extra := state.Extra.(*State)

		assert.Equal(t, deck.Clubs, extra.CurrentSuit)
		assert.Len(t, state.Players[0].Cards, 1)
	})

	t.Run("draw card", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		rules := &Rules{}
		action := ActionDrawCard{}

		initialSize := state.Deck.Size()
		rules.ApplyAction(state, action)

		assert.Len(t, state.Players[0].Cards, 4)
		assert.Equal(t, initialSize-1, state.Deck.Size())
	})
}

func TestRules_DrawCard_Reshuffle(t *testing.T) {
	t.Parallel()

	t.Run("empty deck reshuffles discard so draw is allowed and conserves cards", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		rules := &Rules{}

		// Deck empty, discard holds several cards; the top card must stay in
		// play while the rest can refill the stock.
		state.Deck = deck.New([]deck.Card{})
		state.Discard = deck.New([]deck.Card{
			{Rank: deck.Nine, Suit: deck.Spades},
			{Rank: deck.Three, Suit: deck.Hearts},
			{Rank: deck.Jack, Suit: deck.Clubs},
			{Rank: deck.Four, Suit: deck.Diamonds},
		})

		handBefore := len(state.Players[0].Cards)
		totalBefore := handBefore + state.Deck.Size() + state.Discard.Size()

		action := ActionDrawCard{}

		// The discard pile can refill the stock, so the draw is legal.
		err := rules.ValidateAction(state, action)
		require.NoError(t, err)

		rules.ApplyAction(state, action)

		// Drawing player gained exactly one card.
		assert.Len(t, state.Players[0].Cards, handBefore+1)

		// Discard was reduced to just its top card.
		assert.Equal(t, 1, state.Discard.Size())

		// No cards were created or lost.
		totalAfter := len(state.Players[0].Cards) + state.Deck.Size() + state.Discard.Size()
		assert.Equal(t, totalBefore, totalAfter)
	})

	t.Run("exhausted board turns draw into a pass and ends the hand", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		rules := &Rules{}
		extra := state.Extra.(*State)

		state.Deck = deck.New([]deck.Card{})
		state.Discard = deck.New([]deck.Card{{Rank: deck.Nine, Suit: deck.Spades}})

		// Draw stays legal with nothing to draw; it becomes a forced pass.
		require.NoError(t, rules.ValidateAction(state, ActionDrawCard{}))

		for range state.Players {
			rules.ApplyAction(state, ActionDrawCard{})
		}
		assert.GreaterOrEqual(t, extra.Passes, len(state.Players))
		assert.True(t, rules.CheckWinCondition(state), "a deadlocked board must end the hand")
	})
}

func TestRules_PlayEight_SuitSelection(t *testing.T) {
	t.Parallel()

	t.Run("eight without a suit is rejected", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		rules := &Rules{}
		action := ActionPlayCard{
			Card: deck.Card{Rank: deck.Eight, Suit: deck.Diamonds},
			Suit: deck.NoSuit,
		}

		err := rules.ValidateAction(state, action)
		require.ErrorContains(t, err, "must choose a suit when playing an eight")
	})

	t.Run("eight with a valid suit is allowed and updates CurrentSuit", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		rules := &Rules{}
		action := ActionPlayCard{
			Card: deck.Card{Rank: deck.Eight, Suit: deck.Diamonds},
			Suit: deck.Hearts,
		}

		err := rules.ValidateAction(state, action)
		require.NoError(t, err)

		rules.ApplyAction(state, action)

		extra := state.Extra.(*State)
		assert.Equal(t, deck.Hearts, extra.CurrentSuit)
	})
}

func TestRules_CheckWinCondition(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	state := createTestState()
	assert.False(t, rules.CheckWinCondition(state))

	state.Players[0].Cards = []deck.Card{}
	assert.True(t, rules.CheckWinCondition(state))
}

func TestRules_Init(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	assert.Equal(t, 7, rules.InitialDealCount())

	d := rules.InitialDeck()
	assert.Len(t, d, 52)

	state := createTestState()
	err := rules.OnGameStart(state)
	require.NoError(t, err)

	extra := state.Extra.(*State)
	assert.NotNil(t, extra)

	top, ok := state.Discard.Peek()
	assert.True(t, ok)
	assert.Equal(t, top.Suit, extra.CurrentSuit)
}

// createMultiplayerState is the fixture the multi-seat behaviours need: turn order,
// standings, leave handling and the all-passed deadlock are all unreachable with the
// single-player createTestState above.
func createMultiplayerState(t *testing.T, hands ...int) *game.State {
	t.Helper()
	rules := &Rules{}
	stock := deck.New(deck.StandardDeck())

	players := make([]*game.Player, 0, len(hands))
	for i, n := range hands {
		cards, ok := stock.DrawNCards(n)
		require.True(t, ok, "fixture deck must hold %d cards", n)
		players = append(players, &game.Player{ID: fmt.Sprintf("p%d", i+1), Cards: cards})
	}

	top, ok := stock.Draw()
	require.True(t, ok)

	state := game.NewState(rules, players, nil)
	state.Deck = stock
	state.Discard = deck.New([]deck.Card{top})
	state.Extra = &State{CurrentSuit: top.Suit}
	state.CurrentTurn = 0
	return state
}

// cardsInPlay is the crazy-eights conservation invariant: every card is in exactly
// one of the hands, the stock or the discard.
func cardsInPlay(state *game.State) int {
	total := state.Deck.Size() + state.Discard.Size()
	for _, p := range state.Players {
		total += len(p.Cards)
	}
	return total
}

func TestRules_Standings_RanksByFewestCards(t *testing.T) {
	t.Parallel()
	state := createMultiplayerState(t, 5, 1, 3)

	standings := (&Rules{}).Standings(state)

	require.Len(t, standings, 3)
	assert.Equal(t, "p2", standings[0].ID, "one card is the best position")
	assert.Equal(t, "p3", standings[1].ID)
	assert.Equal(t, "p1", standings[2].ID, "five cards is the worst")
}

// Ties must keep a stable order so two players on the same count do not swap places between
// renders.
func TestRules_Standings_TiesAreStable(t *testing.T) {
	t.Parallel()
	state := createMultiplayerState(t, 2, 2, 2)

	first := (&Rules{}).Standings(state)
	second := (&Rules{}).Standings(state)

	require.Len(t, first, 3)
	for i := range first {
		assert.Equal(t, first[i].ID, second[i].ID, "position %d must not move between calls", i)
	}
}

// A player leaving mid-hand hands their cards back to the stock.
func TestRules_OnPlayerLeave_ReturnsCardsToTheStock(t *testing.T) {
	t.Parallel()
	state := createMultiplayerState(t, 4, 4, 4)
	before := cardsInPlay(state)
	stockBefore := state.Deck.Size()

	(&Rules{}).OnPlayerLeave(state, "p2")

	assert.Equal(t, before, cardsInPlay(state), "leaving must not create or destroy cards")
	assert.Equal(t, stockBefore+4, state.Deck.Size(), "their four cards went back to the stock")

	for _, p := range state.Players {
		if p.ID == "p2" {
			assert.Empty(t, p.Cards, "the leaver keeps no cards")
		}
	}
}

// Leaving with an unknown ID must be a no-op rather than disturbing the table.
func TestRules_OnPlayerLeave_UnknownPlayerChangesNothing(t *testing.T) {
	t.Parallel()
	state := createMultiplayerState(t, 3, 3)
	before := cardsInPlay(state)
	stockBefore := state.Deck.Size()

	(&Rules{}).OnPlayerLeave(state, "nobody")

	assert.Equal(t, before, cardsInPlay(state))
	assert.Equal(t, stockBefore, state.Deck.Size())
}

// With three seats the hand only ends once all three have passed in succession; fewer
// passes must not end it.
func TestRules_CheckWinCondition_EndsOnlyWhenEverySeatHasPassed(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := createMultiplayerState(t, 3, 3, 3)
	extra, ok := state.Extra.(*State)
	require.True(t, ok)

	for passes := range len(state.Players) {
		extra.Passes = passes
		assert.False(t, rules.CheckWinCondition(state),
			"%d of %d seats passed is not a deadlock", passes, len(state.Players))
	}

	extra.Passes = len(state.Players)
	assert.True(t, rules.CheckWinCondition(state), "every seat passing ends the hand")
}

// An emptied hand still wins outright, regardless of passes.
func TestRules_CheckWinCondition_EmptyHandWins(t *testing.T) {
	t.Parallel()
	state := createMultiplayerState(t, 0, 3, 3)

	assert.True(t, (&Rules{}).CheckWinCondition(state))
}

// A full hand driven through the engine, the crazy-eights counterpart to poker's smoke test.
func TestSmoke_FullHandConservesTheDeck(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	players := []*game.Player{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	engine := game.NewEngine(rules, players, deck.StandardDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	countCards := func() int {
		var total int
		engine.WithState(func(s *game.State) { total = cardsInPlay(s) })
		return total
	}
	const wantCards = 52
	require.Equal(t, wantCards, countCards(), "dealing must not lose cards")

	for step := range 300 {
		if engine.IsFinished() {
			break
		}
		id := engine.CurrentPlayerID()

		// Play a legal card if we hold one, otherwise draw.
		var played bool
		engine.WithState(func(s *game.State) {
			hand := s.Players[s.CurrentTurn].Cards
			for _, card := range hand {
				if rules.ValidateAction(s, ActionPlayCard{Card: card, Suit: deck.Spades}) == nil {
					played = true
					return
				}
			}
		})

		var err error
		if played {
			var choice deck.Card
			engine.WithState(func(s *game.State) {
				for _, card := range s.Players[s.CurrentTurn].Cards {
					if rules.ValidateAction(s, ActionPlayCard{Card: card, Suit: deck.Spades}) == nil {
						choice = card
						return
					}
				}
			})
			err = engine.SubmitAction(id, ActionPlayCard{Card: choice, Suit: deck.Spades})
		} else {
			err = engine.SubmitAction(id, ActionDrawCard{})
		}
		require.NoError(t, err, "step %d by %s", step, id)
		require.Equal(t, wantCards, countCards(), "cards changed at step %d", step)
	}

	assert.Equal(t, wantCards, countCards(), "the finished hand still holds every card")
	standings, _ := engine.StandingsWithPlaces()
	assert.NotEmpty(t, standings, "a finished hand ranks its players")
}

// Drawing is the only move ValidateAction accepts unconditionally, and on a dead board it
// degrades into the forced pass the turn loop already handles.
func TestRules_TimeoutAction_AlwaysDraws(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	assert.Equal(t, ActionDrawCard{}, rules.TimeoutAction(nil))

	state := createTestState()
	assert.NoError(t, rules.ValidateAction(state, rules.TimeoutAction(state)))
}

// An empty table is not a deadlock: with nobody seated there is no hand to end, and
// treating it as won would finish a game that never started.
func TestRules_CheckWinCondition_EmptyTableIsNotADeadlock(t *testing.T) {
	t.Parallel()
	state := createMultiplayerState(t)
	extra, ok := state.Extra.(*State)
	require.True(t, ok)
	extra.Passes = 3

	assert.False(t, (&Rules{}).CheckWinCondition(state), "no seats means no hand to deadlock")
}

// Passes is counted against the number of seats, so a leaver who arrives with the
// count part-way up leaves a table that reads as deadlocked without a single seat
// having passed. Their returned cards also refill the stock the count was measuring.
func TestRules_OnPlayerLeave_ClearsStalePasses(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := createMultiplayerState(t, 3, 3, 3)
	extra, ok := state.Extra.(*State)
	require.True(t, ok)

	extra.Passes = 2
	rules.OnPlayerLeave(state, "p3")
	state.Players = state.Players[:2] // the engine drops the seat after the hook

	assert.Zero(t, extra.Passes, "the count measured a table that no longer exists")
	assert.False(t, rules.CheckWinCondition(state), "nobody passed, so nothing is deadlocked")
}

// Playing a card sheds one copy of it. A hand can legitimately hold two of the same
// card once the discard has been reshuffled back through a multi-deck table, and
// shedding both would destroy one.
func TestRules_ApplyAction_ShedsOneCopyOfADuplicate(t *testing.T) {
	t.Parallel()
	state := createTestState()
	card := deck.Card{Rank: deck.Two, Suit: deck.Spades}
	state.Players[0].Cards = []deck.Card{card, card, {Rank: deck.King, Suit: deck.Hearts}}

	(&Rules{}).ApplyAction(state, ActionPlayCard{Card: card})

	assert.Equal(t, []deck.Card{card, {Rank: deck.King, Suit: deck.Hearts}}, state.Players[0].Cards)
}

// The leave path returns cards and reshuffles. An ordinary leave is not a failure, so
// nothing may be logged at error level: the log is how a real reshuffle failure - which
// would leave the stock in an unknown order - gets noticed at all.
//
//nolint:paralleltest // slog.SetDefault is process-wide, so this cannot share the process
func TestRules_OnPlayerLeave_NormalLeaveIsNotAnError(t *testing.T) {
	var logged bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(original) })

	state := createMultiplayerState(t, 3, 3)

	(&Rules{}).OnPlayerLeave(state, "p1")

	assert.Empty(t, logged.String(), "a successful reshuffle has nothing to report")
}

// An Eight is the wild card: the deck cannot name a suit for one it turned up itself,
// so the opening flip keeps going until a plain card surfaces.
func TestRules_OnGameStart_NeverOpensOnAnEight(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	// Draw takes from the end, so these are flipped right-to-left: two eights first.
	stacked := []deck.Card{
		{Rank: deck.Seven, Suit: deck.Clubs},
		{Rank: deck.Four, Suit: deck.Hearts},
		{Rank: deck.Eight, Suit: deck.Spades},
		{Rank: deck.Eight, Suit: deck.Diamonds},
	}
	state := game.NewState(rules, []*game.Player{{ID: "p1"}, {ID: "p2"}}, nil)
	state.Deck = deck.New(stacked)

	require.NoError(t, rules.OnGameStart(state))

	top, ok := state.Discard.Peek()
	require.True(t, ok)
	assert.NotEqual(t, deck.Eight, top.Rank, "opened on %v", top)
	assert.Equal(t, top.Suit, state.Extra.(*State).CurrentSuit)
	assert.Equal(t, len(stacked), state.Deck.Size()+state.Discard.Size(),
		"the skipped eights go back into the stock")
}

// Standings and StandingScore have to agree, or the engine splits a genuine draw by
// slice position and the seat that sorted first takes rating off the seat that did not.
func TestRules_StandingScore_TiedSeatsShareAPlace(t *testing.T) {
	t.Parallel()
	players := []*game.Player{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}}
	engine := game.NewEngine(&Rules{}, players, deck.StandardDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	engine.WithState(func(s *game.State) {
		s.Players[0].Cards = s.Players[0].Cards[:3]
		s.Players[1].Cards = s.Players[1].Cards[:3]
		s.Players[2].Cards = s.Players[2].Cards[:1]
	})

	standings, places := engine.StandingsWithPlaces()
	require.Len(t, standings, 3)
	assert.Equal(t, "p3", standings[0].ID, "one card is the best position")
	assert.Equal(t, []int{1, 2, 2}, places, "equal card counts are one place, not two")
}

// Cards are conserved through any legal sequence of play, including a seat leaving
// mid-hand: every card is in exactly one of the stock, the discard, a hand, or the
// hand of somebody who walked out.
func TestRules_CardConservation(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	rapid.Check(t, func(rt *rapid.T) {
		players := []*game.Player{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}}
		engine := game.NewEngine(rules, players, deck.StandardDeck())
		require.NoError(rt, engine.Start())
		defer engine.Close()

		total := func() int {
			var n int
			engine.WithState(func(s *game.State) {
				n = cardsInPlay(s)
				for _, p := range s.LeftPlayers {
					n += len(p.Cards)
				}
			})
			return n
		}
		const wantCards = 52
		require.Equal(rt, wantCards, total())

		leaveAt := rapid.IntRange(1, 30).Draw(rt, "leaveAt")
		for step := range 50 {
			if engine.IsFinished() {
				break
			}
			if step == leaveAt {
				engine.RemovePlayer(engine.CurrentPlayerID())
				require.Equal(rt, wantCards, total(), "a leave at step %d lost cards", step)
				continue
			}

			id := engine.CurrentPlayerID()
			suit := rapid.SampledFrom([]deck.Suit{deck.Spades, deck.Hearts, deck.Diamonds, deck.Clubs}).
				Draw(rt, "suit")
			var playable []deck.Card
			engine.WithState(func(s *game.State) {
				for _, card := range s.Players[s.CurrentTurn].Cards {
					if rules.ValidateAction(s, ActionPlayCard{Card: card, Suit: suit}) == nil {
						playable = append(playable, card)
					}
				}
			})

			var err error
			if len(playable) > 0 {
				pick := rapid.SampledFrom(playable).Draw(rt, "card")
				err = engine.SubmitAction(id, ActionPlayCard{Card: pick, Suit: suit})
			} else {
				err = engine.SubmitAction(id, ActionDrawCard{})
			}
			require.NoError(rt, err, "step %d", step)

			require.Equal(rt, wantCards, total(), "cards changed at step %d", step)
			engine.WithState(func(s *game.State) {
				require.GreaterOrEqual(rt, s.Discard.Size(), 1, "the card in play never leaves the pile")
			})
		}
	})
}

func TestRules_TableSize(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	assert.Equal(t, 2, rules.MinPlayers(), "crazy eights needs somebody to play against")
	assert.Equal(t, 6, rules.MaxPlayers())
	assert.Equal(t, "crazyeight.PlayCard", ActionPlayCard{}.Name())
	assert.Equal(t, "crazyeight.DrawCard", ActionDrawCard{}.Name())
}

// A draw is unconditionally legal, and TimeoutAction plays one. Reading the top of the
// discard before the switch made the validator reject it on a pile that came up empty,
// which is the shape that turns a quiet seat into a kicked one.
func TestRules_ValidateAction_DrawNeedsNoDiscard(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := createTestState()
	state.Discard = deck.New(nil)

	require.NoError(t, rules.ValidateAction(state, ActionDrawCard{}))
	assert.Error(t, rules.ValidateAction(state, ActionPlayCard{
		Card: state.Players[0].Cards[0],
	}), "a card still needs something to match against")
}

// ApplyAction assigns the chosen suit to an eight unconditionally, which is only sound
// because the validator refuses every suit that is not one of the four.
func TestRules_ValidateAction_EightNeedsARealSuit(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	eight := deck.Card{Rank: deck.Eight, Suit: deck.Diamonds}

	for _, suit := range []deck.Suit{deck.NoSuit, deck.Suit(200)} {
		state := createTestState()
		state.Players[0].Cards = append(state.Players[0].Cards, eight)

		err := rules.ValidateAction(state, ActionPlayCard{Card: eight, Suit: suit})

		require.ErrorContains(t, err, "must choose a suit when playing an eight",
			"suit %d", suit)
	}
}

// The generic cursor fix-up in the engine is the whole story here: crazy eights has no
// direction to honour, so the hook must not move the turn.
func TestRules_AfterPlayerRemoved_LeavesTheCursorAlone(t *testing.T) {
	t.Parallel()
	state := createMultiplayerState(t, 3, 3, 3)
	state.CurrentTurn = 1

	(&Rules{}).AfterPlayerRemoved(state, 0)

	assert.Equal(t, 1, state.CurrentTurn)
	assert.Nil(t, state.OverrideNextTurn)
}

// The table cannot open on an Eight, so a stock of nothing but eights has no opening
// card at all. The deck has to come back whole: the caller reports a table that could
// not start, and a lost stock would be a silently short deck on the next attempt.
func TestRules_OnGameStart_AllEightsCannotOpen(t *testing.T) {
	t.Parallel()
	state := game.NewState(&Rules{}, []*game.Player{{ID: "a"}, {ID: "b"}}, []deck.Card{
		{Rank: deck.Eight, Suit: deck.Spades},
		{Rank: deck.Eight, Suit: deck.Hearts},
	})

	err := (&Rules{}).OnGameStart(state)

	require.ErrorContains(t, err, "not enough cards to start")
	assert.Equal(t, 2, state.Deck.Size(), "the stock comes back whole")
}

// The engine plays TimeoutAction for a seat that has gone quiet. A move ValidateAction
// refuses is not a skipped turn: the clock re-arms and the seat is taken on the next
// expiry, so a player is removed for a mistake the rules made.
func TestSoak_TimeoutActionIsAlwaysLegal(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(2, 6).Draw(rt, "players")
		players := make([]*game.Player, n)
		for i := range players {
			players[i] = &game.Player{ID: fmt.Sprintf("p%d", i+1)}
		}
		engine := game.NewEngine(rules, players, deck.StandardDeck())
		require.NoError(rt, engine.Start())
		defer engine.Close()

		for step := range 300 {
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
		rt.Fatalf("a table of %d that only ever draws never ran out of cards", n)
	})
}
