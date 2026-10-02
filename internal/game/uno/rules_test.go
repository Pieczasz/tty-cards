package uno

import (
	"bytes"
	"fmt"
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
		{Rank: deck.Two, Suit: ColorRed},
		{Rank: deck.Five, Suit: ColorBlue},
		{Rank: Skip, Suit: ColorYellow},
		{Rank: Wild, Suit: ColorWild},
	}}}
	state := game.NewState(rules, players, initialDeck())
	state.Extra = &State{CurrentColor: ColorRed, Direction: 1}
	state.Discard = deck.New([]deck.Card{{Rank: deck.Three, Suit: ColorRed}})
	state.CurrentTurn = 0
	return state
}

func TestRules_ValidateAction_PlayCard(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	t.Run("matching color", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		require.NoError(t, rules.ValidateAction(state, ActionPlayCard{Card: deck.Card{Rank: deck.Two, Suit: ColorRed}}))
	})

	t.Run("matching number", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Discard = deck.New([]deck.Card{{Rank: deck.Five, Suit: ColorGreen}})
		extra(t, state).CurrentColor = ColorGreen
		require.NoError(t, rules.ValidateAction(state, ActionPlayCard{Card: deck.Card{Rank: deck.Five, Suit: ColorBlue}}))
	})

	t.Run("matching symbol", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Discard = deck.New([]deck.Card{{Rank: Skip, Suit: ColorBlue}})
		extra(t, state).CurrentColor = ColorBlue
		require.NoError(t, rules.ValidateAction(state, ActionPlayCard{Card: deck.Card{Rank: Skip, Suit: ColorYellow}}))
	})

	t.Run("wild always legal with color", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		require.NoError(t, rules.ValidateAction(state, ActionPlayCard{
			Card:       deck.Card{Rank: Wild, Suit: ColorWild},
			ChosenSuit: ColorBlue,
		}))
	})

	t.Run("wild without color rejected", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		err := rules.ValidateAction(state, ActionPlayCard{
			Card:       deck.Card{Rank: Wild, Suit: ColorWild},
			ChosenSuit: ColorWild, // NoSuit sentinel, same as crazy eights
		})
		require.ErrorContains(t, err, "must choose a valid color")
	})

	t.Run("invalid mismatch", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		err := rules.ValidateAction(state, ActionPlayCard{Card: deck.Card{Rank: deck.Five, Suit: ColorBlue}})
		require.ErrorContains(t, err, "card doesn't match")
	})

	t.Run("card not in hand", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		err := rules.ValidateAction(state, ActionPlayCard{Card: deck.Card{Rank: deck.Nine, Suit: ColorRed}})
		require.ErrorContains(t, err, "you don't have that card")
	})
}

// A Wild Draw Four is the one card whose legality depends on the rest of the hand:
// it may only be played by someone with nothing of the current colour to play. Left
// unchecked it is simply the strongest card in the deck and there is no reason ever
// to play anything else.
func TestRules_ValidateAction_WildDrawFourNeedsNoCurrentColor(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	wd4 := deck.Card{Rank: WildDrawFour, Suit: ColorWild}

	t.Run("rejected while a card of the current color is held", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Players[0].Cards = []deck.Card{wd4, {Rank: deck.Two, Suit: ColorRed}}
		extra(t, state).CurrentColor = ColorRed

		err := rules.ValidateAction(state, ActionPlayCard{Card: wd4, ChosenSuit: ColorBlue})
		require.ErrorContains(t, err, "no card of the current color")
	})

	t.Run("allowed once nothing matches the current color", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Players[0].Cards = []deck.Card{wd4, {Rank: deck.Two, Suit: ColorGreen}}
		extra(t, state).CurrentColor = ColorRed

		require.NoError(t, rules.ValidateAction(state, ActionPlayCard{Card: wd4, ChosenSuit: ColorBlue}))
	})

	t.Run("a matching number or symbol does not block it", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		// Playable on rank alone, but the gate is on colour, not on playability.
		state.Discard = deck.New([]deck.Card{{Rank: deck.Two, Suit: ColorRed}})
		state.Players[0].Cards = []deck.Card{wd4, {Rank: deck.Two, Suit: ColorGreen}}
		extra(t, state).CurrentColor = ColorRed

		require.NoError(t, rules.ValidateAction(state, ActionPlayCard{Card: wd4, ChosenSuit: ColorBlue}))
	})

	t.Run("a held wild is not a card of the current color", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Players[0].Cards = []deck.Card{wd4, {Rank: Wild, Suit: ColorWild}}
		extra(t, state).CurrentColor = ColorRed

		require.NoError(t, rules.ValidateAction(state, ActionPlayCard{Card: wd4, ChosenSuit: ColorBlue}))
	})

	t.Run("a plain wild is still unconditional", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Players[0].Cards = []deck.Card{{Rank: Wild, Suit: ColorWild}, {Rank: deck.Two, Suit: ColorRed}}
		extra(t, state).CurrentColor = ColorRed

		require.NoError(t, rules.ValidateAction(state, ActionPlayCard{
			Card:       deck.Card{Rank: Wild, Suit: ColorWild},
			ChosenSuit: ColorBlue,
		}))
	})
}

// The card that starts the discard pile is played by the deck, and its action still
// happens: it lands on the seat the engine put on turn. Ignoring it lets the first
// player play on a Skip or a Draw Two as though it were a plain number.
func TestRules_OnGameStart_OpeningCardActsOnTheFirstPlayer(t *testing.T) {
	t.Parallel()

	// Draw takes from the end, so the last card is the one that opens the pile.
	openOn := func(t *testing.T, seats int, card deck.Card) (*game.State, *State) {
		t.Helper()
		players := make([]*game.Player, seats)
		for i := range players {
			players[i] = &game.Player{ID: fmt.Sprintf("p%d", i+1)}
		}
		stock := make([]deck.Card, 0, 12)
		for range 10 {
			stock = append(stock, deck.Card{Rank: deck.Seven, Suit: ColorGreen})
		}
		stock = append(stock, card)

		state := game.NewState(&Rules{}, players, nil)
		state.Deck = deck.New(stock)
		state.CurrentTurn = 0
		require.NoError(t, (&Rules{}).OnGameStart(state))
		return state, extra(t, state)
	}

	t.Run("skip costs the first player their turn", func(t *testing.T) {
		t.Parallel()
		state, _ := openOn(t, 3, deck.Card{Rank: Skip, Suit: ColorRed})

		assert.Equal(t, 1, state.CurrentTurn)
		require.NotNil(t, state.OverrideNextTurn)
		assert.Equal(t, 1, *state.OverrideNextTurn, "the engine settles the cursor after OnGameStart")
	})

	t.Run("draw two is drawn by the first player, who is then skipped", func(t *testing.T) {
		t.Parallel()
		state, _ := openOn(t, 3, deck.Card{Rank: DrawTwo, Suit: ColorRed})

		assert.Len(t, state.Players[0].Cards, 2, "the seat on turn pays the penalty")
		assert.Empty(t, state.Players[1].Cards)
		assert.Equal(t, 1, state.CurrentTurn)
	})

	t.Run("reverse turns the table around before the first play", func(t *testing.T) {
		t.Parallel()
		state, extra := openOn(t, 3, deck.Card{Rank: Reverse, Suit: ColorRed})

		assert.Equal(t, int8(-1), extra.Direction)
		assert.Equal(t, 0, state.CurrentTurn, "a reverse skips nobody")
	})

	t.Run("heads-up a reverse is a skip", func(t *testing.T) {
		t.Parallel()
		state, extra := openOn(t, 2, deck.Card{Rank: Reverse, Suit: ColorRed})

		assert.Equal(t, int8(1), extra.Direction, "with two seats there is nothing to reverse")
		assert.Equal(t, 1, state.CurrentTurn)
	})

	t.Run("a number card leaves the first player on turn", func(t *testing.T) {
		t.Parallel()
		state, extra := openOn(t, 3, deck.Card{Rank: deck.Five, Suit: ColorRed})

		assert.Equal(t, 0, state.CurrentTurn)
		assert.Equal(t, int8(1), extra.Direction)
		assert.Empty(t, state.Players[0].Cards)
	})
}

func TestRules_ApplyAction_NumberCard(t *testing.T) {
	t.Parallel()
	state := createTestState()
	rules := &Rules{}
	card := deck.Card{Rank: deck.Two, Suit: ColorRed}

	rules.ApplyAction(state, ActionPlayCard{Card: card})

	extra := extra(t, state)
	assert.Equal(t, ColorRed, extra.CurrentColor)
	assert.Len(t, state.Players[0].Cards, 3)
	require.NotNil(t, state.OverrideNextTurn)
	assert.Equal(t, 0, *state.OverrideNextTurn) // single player: advance wraps
	top, _ := state.Discard.Peek()
	assert.Equal(t, card, top)
}

func TestRules_ApplyAction_Skip(t *testing.T) {
	t.Parallel()
	state := suite.Table(t, 3, 3, 3)
	rules := &Rules{}
	state.CurrentTurn = 0
	state.Players[0].Cards = []deck.Card{{Rank: Skip, Suit: ColorRed}}
	extra(t, state).CurrentColor = ColorRed
	state.Discard = deck.New([]deck.Card{{Rank: deck.Two, Suit: ColorRed}})

	rules.ApplyAction(state, ActionPlayCard{Card: deck.Card{Rank: Skip, Suit: ColorRed}})

	require.NotNil(t, state.OverrideNextTurn)
	assert.Equal(t, 2, *state.OverrideNextTurn, "skip advances two seats")
}

func TestRules_ApplyAction_Reverse(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	t.Run("three players flips direction", func(t *testing.T) {
		t.Parallel()
		state := suite.Table(t, 2, 2, 2)
		state.CurrentTurn = 0
		state.Players[0].Cards = []deck.Card{{Rank: Reverse, Suit: ColorRed}}
		extra(t, state).CurrentColor = ColorRed
		state.Discard = deck.New([]deck.Card{{Rank: deck.Two, Suit: ColorRed}})

		rules.ApplyAction(state, ActionPlayCard{Card: deck.Card{Rank: Reverse, Suit: ColorRed}})

		extra := extra(t, state)
		assert.Equal(t, int8(-1), extra.Direction)
		require.NotNil(t, state.OverrideNextTurn)
		assert.Equal(t, 2, *state.OverrideNextTurn, "after reverse from seat 0, next is seat 2")
	})

	t.Run("two players acts as skip", func(t *testing.T) {
		t.Parallel()
		state := suite.Table(t, 2, 2)
		state.CurrentTurn = 0
		state.Players[0].Cards = []deck.Card{{Rank: Reverse, Suit: ColorRed}}
		extra(t, state).CurrentColor = ColorRed
		state.Discard = deck.New([]deck.Card{{Rank: deck.Two, Suit: ColorRed}})

		rules.ApplyAction(state, ActionPlayCard{Card: deck.Card{Rank: Reverse, Suit: ColorRed}})

		extra := extra(t, state)
		assert.Equal(t, int8(1), extra.Direction, "direction unchanged in 2-player")
		require.NotNil(t, state.OverrideNextTurn)
		assert.Equal(t, 0, *state.OverrideNextTurn, "same seat plays again")
	})
}

func TestRules_ApplyAction_DrawTwo(t *testing.T) {
	t.Parallel()
	state := suite.Table(t, 1, 1, 1)
	rules := &Rules{}
	state.CurrentTurn = 0
	state.Players[0].Cards = []deck.Card{{Rank: DrawTwo, Suit: ColorRed}}
	extra(t, state).CurrentColor = ColorRed
	state.Discard = deck.New([]deck.Card{{Rank: deck.Two, Suit: ColorRed}})
	victimBefore := len(state.Players[1].Cards)

	rules.ApplyAction(state, ActionPlayCard{Card: deck.Card{Rank: DrawTwo, Suit: ColorRed}})

	assert.Len(t, state.Players[1].Cards, victimBefore+2)
	require.NotNil(t, state.OverrideNextTurn)
	assert.Equal(t, 2, *state.OverrideNextTurn, "victim is skipped")
}

func TestRules_ApplyAction_WildDrawFour(t *testing.T) {
	t.Parallel()
	state := suite.Table(t, 1, 1, 1)
	rules := &Rules{}
	state.CurrentTurn = 0
	state.Players[0].Cards = []deck.Card{{Rank: WildDrawFour, Suit: ColorWild}}
	state.Discard = deck.New([]deck.Card{{Rank: deck.Two, Suit: ColorRed}})
	victimBefore := len(state.Players[1].Cards)

	rules.ApplyAction(state, ActionPlayCard{
		Card:       deck.Card{Rank: WildDrawFour, Suit: ColorWild},
		ChosenSuit: ColorBlue,
	})

	assert.Equal(t, ColorBlue, extra(t, state).CurrentColor)
	assert.Len(t, state.Players[1].Cards, victimBefore+4)
	require.NotNil(t, state.OverrideNextTurn)
	assert.Equal(t, 2, *state.OverrideNextTurn)
}

func TestRules_DrawCard_Reshuffle(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	t.Run("empty deck reshuffles and conserves", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Deck = deck.New([]deck.Card{})
		state.Discard = deck.New([]deck.Card{
			{Rank: deck.Three, Suit: ColorRed},
			{Rank: deck.Four, Suit: ColorBlue},
			{Rank: deck.Five, Suit: ColorGreen},
			{Rank: deck.Six, Suit: ColorYellow},
		})
		handBefore := len(state.Players[0].Cards)
		totalBefore := gametest.CardsInPlay(state)

		rules.ApplyAction(state, ActionDrawCard{})

		assert.Len(t, state.Players[0].Cards, handBefore+1)
		assert.Equal(t, 1, state.Discard.Size())
		assert.Equal(t, totalBefore, gametest.CardsInPlay(state))
	})

	t.Run("exhausted board is a pass", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		extra := extra(t, state)
		state.Deck = deck.New(nil)
		state.Discard = deck.New([]deck.Card{{Rank: deck.Three, Suit: ColorRed}})

		rules.ApplyAction(state, ActionDrawCard{})
		assert.Equal(t, 1, extra.Passes)
	})
}

// Official Uno never starts on a Wild, so OnGameStart keeps flipping until a coloured
// card surfaces and shuffles the skipped wilds back into the stock.
func TestRules_OnGameStart_NeverOpensOnAWild(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	players := []*game.Player{{ID: "p1"}, {ID: "p2"}}

	// Draw takes from the end, so these are flipped right-to-left: two wilds first.
	stacked := []deck.Card{
		{Rank: deck.Seven, Suit: ColorGreen},
		{Rank: deck.Four, Suit: ColorBlue},
		{Rank: WildDrawFour, Suit: ColorWild},
		{Rank: Wild, Suit: ColorWild},
	}
	state := game.NewState(rules, players, nil)
	state.Deck = deck.New(stacked)

	require.NoError(t, rules.OnGameStart(state))

	top, ok := state.Discard.Peek()
	require.True(t, ok)
	assert.False(t, isWild(top.Rank), "opened on %v", top)
	assert.Equal(t, top.Suit, extra(t, state).CurrentColor)
	assert.Equal(t, len(stacked), state.Deck.Size()+state.Discard.Size(),
		"the skipped wilds go back into the stock")
}

// Passes counts consecutive fruitless turns and ends a deadlocked hand. A draw that
// actually yielded a card has to clear it, or a live hand can still time itself out.
func TestRules_DrawCard_SuccessfulDrawClearsThePassCount(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := suite.Table(t, 3, 3)
	extra := extra(t, state)
	extra.Passes = 1
	stockBefore := state.Deck.Size()

	rules.ApplyAction(state, ActionDrawCard{})

	assert.Zero(t, extra.Passes, "a card was drawn, so the hand is not stalled")
	assert.Len(t, state.Players[0].Cards, 4)
	assert.Equal(t, stockBefore-1, state.Deck.Size())
	assert.False(t, rules.CheckWinCondition(state))
}

func TestRules_Init(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	assert.Equal(t, 7, rules.InitialDealCount())
	assert.Len(t, rules.InitialDeck(), 108)

	t.Run("opening wild redrawn", func(t *testing.T) {
		t.Parallel()
		players := []*game.Player{{ID: "a"}, {ID: "b"}}
		// Stock top is a wild, then a colored card.
		stock := []deck.Card{
			{Rank: Wild, Suit: ColorWild},
			{Rank: deck.Five, Suit: ColorGreen},
			{Rank: deck.Two, Suit: ColorRed},
		}
		state := game.NewState(rules, players, stock)
		require.NoError(t, rules.OnGameStart(state))

		extra := extra(t, state)
		top, ok := state.Discard.Peek()
		require.True(t, ok)
		assert.False(t, isWild(top.Rank))
		assert.Equal(t, top.Suit, extra.CurrentColor)
		assert.Equal(t, int8(1), extra.Direction)
	})
}

func TestSmoke_FullHandConservesTheDeck(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	players := []*game.Player{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	engine := game.NewEngine(rules, players, initialDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	countCards := func() int {
		var total int
		engine.WithState(func(s *game.State) { total = gametest.CardsInPlay(s) })
		return total
	}
	const wantCards = 108
	require.Equal(t, wantCards, countCards())

	for step := range 400 {
		if engine.IsFinished() {
			break
		}
		id := engine.CurrentPlayerID()
		var choice *deck.Card
		engine.WithState(func(s *game.State) {
			hand := s.Players[s.CurrentTurn].Cards
			for _, card := range hand {
				act := ActionPlayCard{Card: card, ChosenSuit: ColorRed}
				if rules.ValidateAction(s, act) == nil {
					c := card
					choice = &c
					return
				}
			}
		})

		var err error
		if choice != nil {
			err = engine.SubmitAction(id, ActionPlayCard{Card: *choice, ChosenSuit: ColorRed})
		} else {
			err = engine.SubmitAction(id, ActionDrawCard{})
		}
		require.NoError(t, err, "step %d by %s", step, id)
		require.Equal(t, wantCards, countCards(), "cards changed at step %d", step)
	}
	assert.Equal(t, wantCards, countCards())
}

//nolint:paralleltest // slog.SetDefault is process-wide
func TestRules_OnPlayerLeave_NormalLeaveIsNotAnError(t *testing.T) {
	var logged bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(original) })

	state := suite.Table(t, 3, 3)
	(&Rules{}).OnPlayerLeave(state, "p1")
	assert.Empty(t, logged.String())
}

// The engine's own fix-up hands the turn to the seat that followed the leaver
// clockwise. On a reversed table that is the wrong neighbour: the seat the turn
// actually owed gets skipped, and it is the leaver's own turn that exposes it.
func TestRules_AfterPlayerRemoved_HonorsDirection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		direction int8
		onTurn    int
		leaver    string
		wantTurn  string
	}{
		{name: "clockwise, seat on turn leaves", direction: 1, onTurn: 2, leaver: "p3", wantTurn: "p4"},
		{name: "counterclockwise, seat on turn leaves", direction: -1, onTurn: 2, leaver: "p3", wantTurn: "p2"},
		{name: "counterclockwise, first seat on turn leaves", direction: -1, onTurn: 0, leaver: "p1", wantTurn: "p4"},
		{name: "clockwise, last seat on turn leaves", direction: 1, onTurn: 3, leaver: "p4", wantTurn: "p1"},
		{name: "counterclockwise, last seat on turn leaves", direction: -1, onTurn: 3, leaver: "p4", wantTurn: "p3"},
		{name: "clockwise, a seat before the cursor leaves", direction: 1, onTurn: 2, leaver: "p1", wantTurn: "p3"},
		{name: "counterclockwise, a seat before the cursor leaves", direction: -1, onTurn: 2, leaver: "p1", wantTurn: "p3"},
		{name: "clockwise, a seat after the cursor leaves", direction: 1, onTurn: 1, leaver: "p4", wantTurn: "p2"},
		{name: "counterclockwise, a seat after the cursor leaves", direction: -1, onTurn: 1, leaver: "p4", wantTurn: "p2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			players := []*game.Player{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}, {ID: "p4"}}
			engine := game.NewEngine(&Rules{}, players, initialDeck())
			require.NoError(t, engine.Start())
			t.Cleanup(engine.Close)

			engine.WithState(func(s *game.State) {
				extra(t, s).Direction = tt.direction
				s.CurrentTurn = tt.onTurn
				s.OverrideNextTurn = nil
			})

			engine.RemovePlayer(tt.leaver)

			assert.Equal(t, tt.wantTurn, engine.CurrentPlayerID())
		})
	}
}

// Cards are conserved through any legal sequence of play, including a seat leaving
// mid-hand: every card is in exactly one of the stock, the discard, a hand, or the
// hand of somebody who walked out.
func TestRules_CardConservation(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	rapid.Check(t, func(rt *rapid.T) {
		players := []*game.Player{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}}
		engine := game.NewEngine(rules, players, initialDeck())
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
		const wantCards = 108
		require.Equal(rt, wantCards, total())

		leaveAt := rapid.IntRange(1, 40).Draw(rt, "leaveAt")
		for step := range 60 {
			if engine.IsFinished() {
				break
			}
			if step == leaveAt {
				engine.RemovePlayer(engine.CurrentPlayerID())
				require.Equal(rt, wantCards, total(), "a leave at step %d lost cards", step)
				continue
			}

			id := engine.CurrentPlayerID()
			color := rapid.SampledFrom([]deck.Suit{ColorRed, ColorYellow, ColorGreen, ColorBlue}).
				Draw(rt, "color")
			var playable []deck.Card
			engine.WithState(func(s *game.State) {
				for _, card := range s.Players[s.CurrentTurn].Cards {
					if rules.ValidateAction(s, ActionPlayCard{Card: card, ChosenSuit: color}) == nil {
						playable = append(playable, card)
					}
				}
			})

			var err error
			if len(playable) > 0 {
				pick := rapid.SampledFrom(playable).Draw(rt, "card")
				err = engine.SubmitAction(id, ActionPlayCard{Card: pick, ChosenSuit: color})
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
	assert.Equal(t, 2, rules.MinPlayers(), "uno needs somebody to play against")
	assert.Equal(t, 10, rules.MaxPlayers())
	assert.Equal(t, "uno.PlayCard", ActionPlayCard{}.Name())
	assert.Equal(t, "uno.DrawCard", ActionDrawCard{}.Name())
}

// Direction is an int8 whose zero value is neither clockwise nor counterclockwise:
// advance would multiply every step by zero and hand the same seat the turn forever.
// OnGameStart is the only thing standing between the table and that, so it is pinned.
func TestRules_OnGameStart_StartsClockwise(t *testing.T) {
	t.Parallel()
	players := []*game.Player{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	state := game.NewState(&Rules{}, players, initialDeck())
	state.Deck.Shuffle()

	require.NoError(t, (&Rules{}).OnGameStart(state))

	extra := extra(t, state)
	// An opening Reverse legitimately turns the table the other way, so only the zero
	// value is wrong - it would multiply every step by nothing.
	assert.Contains(t, []int8{1, -1}, extra.Direction, "a zero direction never leaves the first seat")
	assert.NotEqual(t, deck.NoSuit, extra.CurrentColor, "the opening card names a colour")
}

// Heads-up the victim of a forced draw is also the next seat the skip lands on, so the
// player who laid the card takes the turn straight back.
func TestRules_ApplyAction_ForcedDrawHeadsUp(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		card deck.Card
		drew int
	}{
		{name: "draw two", card: deck.Card{Rank: DrawTwo, Suit: ColorRed}, drew: 2},
		{name: "wild draw four", card: deck.Card{Rank: WildDrawFour, Suit: ColorWild}, drew: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state := suite.Table(t, 1, 1)
			state.CurrentTurn = 0
			state.Players[0].Cards = []deck.Card{tt.card}
			extra(t, state).CurrentColor = ColorRed
			state.Discard = deck.New([]deck.Card{{Rank: deck.Two, Suit: ColorRed}})
			victimBefore := len(state.Players[1].Cards)
			total := gametest.CardsInPlay(state)

			require.NoError(t, (&Rules{}).ApplyAction(state,
				ActionPlayCard{Card: tt.card, ChosenSuit: ColorBlue}))

			assert.Len(t, state.Players[1].Cards, victimBefore+tt.drew)
			require.NotNil(t, state.OverrideNextTurn)
			assert.Equal(t, 0, *state.OverrideNextTurn, "two seats: the skip wraps back")
			assert.Equal(t, total, gametest.CardsInPlay(state))
		})
	}
}

// Two seats left and the one on turn walks out: the survivor has nobody to pass to, and
// an override naming a seat would tell the engine the table still has work to do, which
// costs them the forfeit win.
func TestRules_AfterPlayerRemoved_TwoSeatsCollapseToOne(t *testing.T) {
	t.Parallel()
	for _, dir := range []int8{1, -1} {
		t.Run(fmt.Sprintf("direction %d", dir), func(t *testing.T) {
			t.Parallel()
			state := suite.Table(t, 3, 3)
			extra := extra(t, state)
			extra.Direction = dir
			state.CurrentTurn = 0
			extra.leaverWasOnTurn = true
			state.Players = state.Players[1:] // the engine removes the seat first

			(&Rules{}).AfterPlayerRemoved(state, 0)

			assert.Nil(t, state.OverrideNextTurn, "one seat left has no next turn to take")
			assert.False(t, extra.leaverWasOnTurn, "the flag is consumed, not left to fire again")
		})
	}
}
