package crazyeight

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/game/gametest"

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
	state := game.NewState(rules, players, deck.Standard())
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

		extra := extra(t, state)

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

		extra := extra(t, state)

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
		extra := extra(t, state)

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

		extra := extra(t, state)
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

	top, ok := state.Discard.Peek()
	require.True(t, ok, "the game opens a discard pile")
	assert.Equal(t, top.Suit, extra(t, state).CurrentSuit)

}

// A full hand driven through the engine, the crazy-eights counterpart to poker's smoke test.
func TestSmoke_FullHandConservesTheDeck(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	players := []*game.Player{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	engine := game.NewEngine(rules, players, deck.Standard())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	countCards := func() int {
		var total int
		engine.WithState(func(s *game.State) { total = gametest.CardsInPlay(s) })
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

	state := shed.Table(t, 3, 3)

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
	assert.Equal(t, top.Suit, extra(t, state).CurrentSuit)
	assert.Equal(t, len(stacked), state.Deck.Size()+state.Discard.Size(),
		"the skipped eights go back into the stock")
}

// Cards are conserved through any legal sequence of play, including a seat leaving
// mid-hand: every card is in exactly one of the stock, the discard, a hand, or the
// hand of somebody who walked out.
func TestRules_CardConservation(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	rapid.Check(t, func(rt *rapid.T) {
		players := []*game.Player{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}}
		engine := game.NewEngine(rules, players, deck.Standard())
		require.NoError(rt, engine.Start())
		defer engine.Close()

		total := func() int {
			var n int
			engine.WithState(func(s *game.State) {
				n = gametest.CardsInPlay(s)
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
	state := shed.Table(t, 3, 3, 3)
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
