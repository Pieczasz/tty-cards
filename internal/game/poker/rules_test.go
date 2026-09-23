package poker

import (
	"fmt"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestState() *game.State {
	rules := &Rules{}
	players := []*game.Player{
		{ID: "p1", Cards: []deck.Card{{Rank: deck.Two, Suit: deck.Spades}, {Rank: deck.King, Suit: deck.Hearts}}},
		{ID: "p2", Cards: []deck.Card{{Rank: deck.Three, Suit: deck.Diamonds}, {Rank: deck.Queen, Suit: deck.Clubs}}},
		{ID: "p3", Cards: []deck.Card{{Rank: deck.Four, Suit: deck.Clubs}, {Rank: deck.Jack, Suit: deck.Spades}}},
	}
	state := game.NewState(rules, players, deck.StandardDeck())
	state.Extra = &State{
		MainPool:   0,
		CurrentBet: 0,
		MinRaise:   10,
		SmallBlind: 5,
		BigBlind:   10,
		Phase:      PreFlop,
		Folded:     map[string]bool{"p1": false, "p2": false, "p3": false},
		PlayersAllIn: map[string]bool{
			"p1": false, "p2": false, "p3": false,
		},
		Table: make([]deck.Card, 0),
		PlayerChips: map[string]uint{
			"p1": 1000,
			"p2": 1000,
			"p3": 1000,
		},
		PlayerBets: map[string]uint{
			"p1": 0,
			"p2": 0,
			"p3": 0,
		},
		TotalContributed: map[string]uint{
			"p1": 0, "p2": 0, "p3": 0,
		},
		ActedThisRound: map[string]bool{
			"p1": false, "p2": false, "p3": false,
		},
		LastBetLevel: map[string]uint{},
	}
	state.CurrentTurn = 0
	state.Phase = game.Playing
	return state
}

func TestRules_Metadata(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	assert.Equal(t, 2, rules.MinPlayers())
	assert.Equal(t, 9, rules.MaxPlayers())
	assert.Equal(t, 0, rules.InitialDealCount(), "poker deals its own hole cards, once per hand")
}

func TestRules_ValidateAction(t *testing.T) {
	t.Parallel()

	t.Run("fold is always valid", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		assert.NoError(t, (&Rules{}).ValidateAction(state, ActionFold{}))
	})

	t.Run("check valid when nothing owed", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Extra.(*State).CurrentBet = 0
		assert.NoError(t, (&Rules{}).ValidateAction(state, ActionCheck{}))
	})

	t.Run("check invalid when facing a bet", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Extra.(*State).CurrentBet = 100
		err := (&Rules{}).ValidateAction(state, ActionCheck{})
		assert.ErrorContains(t, err, "cannot check")
	})

	t.Run("raise below current bet rejected", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Extra.(*State).CurrentBet = 100
		err := (&Rules{}).ValidateAction(state, ActionRaiseTo{Amount: 50})
		assert.ErrorContains(t, err, "above current bet")
	})

	t.Run("call valid when facing a bet", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		state.Extra.(*State).CurrentBet = 100
		assert.NoError(t, (&Rules{}).ValidateAction(state, ActionCall{}))
	})

	t.Run("raise invalid if not enough chips", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		extra := state.Extra.(*State)
		extra.CurrentBet = 100
		extra.PlayerChips["p1"] = 10
		err := (&Rules{}).ValidateAction(state, ActionRaiseTo{Amount: 200})
		assert.ErrorContains(t, err, "not enough chips")
	})
}

func TestRules_ApplyAction(t *testing.T) {
	t.Parallel()

	t.Run("folding marks folded", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		(&Rules{}).ApplyAction(state, ActionFold{})
		extra := state.Extra.(*State)
		assert.True(t, extra.Folded["p1"])
	})

	t.Run("check updates acted flag", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		(&Rules{}).ApplyAction(state, ActionCheck{})
		extra := state.Extra.(*State)
		assert.True(t, extra.ActedThisRound["p1"])
	})

	t.Run("raise updates current bet and pool", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		(&Rules{}).ApplyAction(state, ActionRaiseTo{Amount: 50})
		extra := state.Extra.(*State)
		assert.Equal(t, uint(50), extra.CurrentBet)
		assert.Equal(t, uint(50), extra.MainPool)
		assert.Equal(t, uint(950), extra.PlayerChips["p1"])
		assert.Equal(t, uint(50), extra.PlayerBets["p1"])
	})
}

func TestApplyBetIncrease_IncompleteRaiseRule(t *testing.T) {
	t.Parallel()

	// Regression: a sub-minimum all-in raise must advance the amount owed but must
	// NOT reopen the round for players who already acted (their ActedThisRound must
	// survive and MinRaise must not grow).
	t.Run("sub-minimum all-in does not reopen the round", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		extra := state.Extra.(*State)
		extra.CurrentBet = 100
		extra.MinRaise = 100
		// p2 and p3 have already acted and matched the current bet.
		for _, id := range []string{"p2", "p3"} {
			extra.ActedThisRound[id] = true
			extra.PlayerBets[id] = 100
		}
		// p1 shoves for a total of 150 -> raiseSize 50 < MinRaise 100.
		extra.PlayerBets["p1"] = 0
		extra.PlayerChips["p1"] = 150
		state.CurrentTurn = 0

		(&Rules{}).ApplyAction(state, ActionAllIn{})

		assert.Equal(t, uint(150), extra.CurrentBet, "bet owed advances to the shove total")
		assert.Equal(t, uint(100), extra.MinRaise, "sub-min all-in must not grow MinRaise")
		// resetActedExcept must NOT have run: already-acted opponents stay acted.
		assert.True(t, extra.ActedThisRound["p2"], "p2 must stay acted (round not reopened)")
		assert.True(t, extra.ActedThisRound["p3"], "p3 must stay acted (round not reopened)")
		// They still owe the extra chips because their street bet trails CurrentBet.
		assert.Equal(t, uint(50), ToCall(extra, "p2"), "p2 still owes the uncalled extra")
		assert.Equal(t, uint(50), ToCall(extra, "p3"), "p3 still owes the uncalled extra")
		assert.True(t, extra.PlayersAllIn["p1"], "shover is all-in")
	})

	t.Run("full raise reopens the round and grows MinRaise", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		extra := state.Extra.(*State)
		extra.CurrentBet = 100
		extra.MinRaise = 100
		for _, id := range []string{"p2", "p3"} {
			extra.ActedThisRound[id] = true
			extra.PlayerBets[id] = 100
		}
		extra.PlayerBets["p1"] = 0
		extra.PlayerChips["p1"] = 1000
		state.CurrentTurn = 0

		// Raise to 250 -> raiseSize 150 >= MinRaise 100, a full raise.
		(&Rules{}).ApplyAction(state, ActionRaiseTo{Amount: 250})

		assert.Equal(t, uint(250), extra.CurrentBet)
		assert.Equal(t, uint(150), extra.MinRaise, "full raise grows MinRaise to the raise size")
		assert.False(t, extra.ActedThisRound["p2"], "full raise reopens p2")
		assert.False(t, extra.ActedThisRound["p3"], "full raise reopens p3")
		assert.True(t, extra.ActedThisRound["p1"], "raiser is marked acted")
	})
}

func TestRules_CheckWinCondition(t *testing.T) {
	t.Parallel()

	t.Run("continues until hand complete", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		assert.False(t, (&Rules{}).CheckWinCondition(state))
	})

	t.Run("complete after uncontested award", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		extra := state.Extra.(*State)
		extra.MainPool = 150
		extra.Folded["p2"] = true
		extra.Folded["p3"] = true
		require.NoError(t, (&Rules{}).AfterAction(state, ActionFold{}))
		assert.True(t, extra.HandComplete)
		assert.True(t, (&Rules{}).CheckWinCondition(state))
		assert.Equal(t, uint(1150), extra.PlayerChips["p1"])
	})
}

func TestRules_Standings(t *testing.T) {
	t.Parallel()

	t.Run("all folded except one", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		extra := state.Extra.(*State)
		extra.Folded["p1"] = true
		extra.Folded["p3"] = true
		standings := (&Rules{}).Standings(state)
		assert.Equal(t, "p2", standings[0].ID)
	})

	t.Run("showdown ranks by EvaluateHand then stable id", func(t *testing.T) {
		t.Parallel()
		state := createTestState()
		extra := state.Extra.(*State)
		extra.ReachedShowdown = true
		extra.Table = []deck.Card{
			{Rank: deck.Ten, Suit: deck.Spades},
			{Rank: deck.Jack, Suit: deck.Hearts},
			{Rank: deck.Queen, Suit: deck.Diamonds},
			{Rank: deck.Two, Suit: deck.Clubs},
			{Rank: deck.Three, Suit: deck.Spades},
		}
		state.Players[0].Cards = []deck.Card{
			{Rank: deck.Ace, Suit: deck.Clubs},
			{Rank: deck.Four, Suit: deck.Hearts},
		}
		state.Players[1].Cards = []deck.Card{
			{Rank: deck.King, Suit: deck.Clubs},
			{Rank: deck.King, Suit: deck.Hearts},
		}
		state.Players[2].Cards = []deck.Card{
			{Rank: deck.Ace, Suit: deck.Spades},
			{Rank: deck.King, Suit: deck.Diamonds},
		}
		standings := (&Rules{}).Standings(state)
		assert.Equal(t, "p3", standings[0].ID)
		assert.Equal(t, "p2", standings[1].ID)
		assert.Equal(t, "p1", standings[2].ID)
	})
}

// A keypress must turn into an updated table for every player well inside one
// terminal frame (~16ms). This plays a whole match - deal, blinds, every betting
// round, the street machine, the showdown and the re-deal between hands - so a
// full match comfortably under a millisecond means no single action can be felt.
func BenchmarkPlayFullMatch(b *testing.B) {
	for _, seats := range []int{2, 6, 9} {
		b.Run(fmt.Sprintf("seats=%d", seats), func(b *testing.B) {
			players := make([]*game.Player, 0, seats)
			for i := range seats {
				players = append(players, &game.Player{ID: fmt.Sprintf("p%d", i+1)})
			}

			b.ReportAllocs()
			for b.Loop() {
				engine := game.NewEngine(&Rules{}, players, deck.StandardDeck())
				if err := engine.Start(); err != nil {
					b.Fatal(err)
				}
				for range 200 * HandsPerMatch {
					if engine.IsFinished() {
						break
					}
					id := engine.CurrentPlayerID()
					if engine.SubmitAction(id, ActionCheck{}) == nil {
						continue
					}
					if engine.SubmitAction(id, ActionCall{}) == nil {
						continue
					}
					if engine.SubmitAction(id, ActionNextHand{}) == nil {
						continue
					}
					if engine.SubmitAction(id, ActionFold{}) != nil {
						break
					}
				}
				engine.Close()
			}
		})
	}
}

// adjustSeatIndex decides where the button and blinds land after someone leaves. It
// is a pure function and had no direct test, yet getting it wrong either steals a
// turn or points a marker at a seat that no longer exists.
func TestAdjustSeatIndex(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		seat    int
		removed int
		nAfter  int
		want    int
	}{
		{name: "seat before the leaver is unaffected", seat: 0, removed: 2, nAfter: 3, want: 0},
		{name: "seat after the leaver shifts down", seat: 2, removed: 1, nAfter: 3, want: 1},
		{name: "the leaver's own marker steps back", seat: 2, removed: 2, nAfter: 3, want: 1},
		{name: "seat 0 leaving wraps its marker to the last seat", seat: 0, removed: 0, nAfter: 3, want: 2},
		{name: "a marker past the end is clamped inside", seat: 5, removed: 4, nAfter: 2, want: 1},
		{name: "a marker exactly at the new end is clamped inside", seat: 3, removed: 0, nAfter: 2, want: 1},
		{name: "an empty table collapses to zero", seat: 3, removed: 1, nAfter: 0, want: 0},
		{name: "the leaver's own marker on an empty table collapses too", seat: 1, removed: 1, nAfter: 0, want: 0},
		{name: "a negative table size collapses to zero", seat: 1, removed: 0, nAfter: -1, want: 0},
		{name: "heads-up: the leaver's marker lands on the survivor", seat: 1, removed: 1, nAfter: 1, want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := adjustSeatIndex(tt.seat, tt.removed, tt.nAfter)
			assert.Equal(t, tt.want, got)
			if tt.nAfter > 0 {
				assert.GreaterOrEqual(t, got, 0, "a seat index is never negative")
				assert.Less(t, got, tt.nAfter, "a seat index always addresses a real seat")
			}
		})
	}
}

func TestRules_TimeoutAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		turn  int
		extra *State
		want  game.Action
	}{
		{
			name:  "nothing to call, so checking costs the player nothing",
			extra: &State{CurrentBet: 0},
			want:  ActionCheck{},
		},
		{
			name:  "facing a bet folds rather than paying with chips they did not commit",
			extra: &State{CurrentBet: DefaultBigBlind, PlayerChips: map[string]uint{"villain": DefaultStack}},
			want:  ActionFold{},
		},
		{
			name:  "between hands an absent dealer still deals, or the table freezes",
			extra: &State{HandComplete: true},
			want:  ActionNextHand{},
		},
		{
			name:  "a finished match has nothing left to play",
			extra: &State{HandComplete: true, MatchComplete: true},
			want:  nil,
		},
		{
			name:  "a turn cursor outside the table yields nothing to play",
			turn:  9,
			extra: &State{CurrentBet: 0},
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state := &game.State{
				Players:     []*game.Player{{ID: "hero"}, {ID: "villain"}},
				CurrentTurn: tt.turn,
				Extra:       tt.extra,
			}
			assert.Equal(t, tt.want, (&Rules{}).TimeoutAction(state))
		})
	}
}

// A safe move the rules then refuse would leave the seat stalling the whole table: the
// turn re-arms and the engine takes the seat on the next expiry instead of playing for
// it. So the two are checked against each other on every turn of a whole match, the
// between-hands deal included - that is the one timeout an absent player would
// otherwise freeze the table with.
func TestRules_TimeoutAction_IsAcceptedByValidateAction(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	engine := game.NewEngine(rules, []*game.Player{{ID: "a"}, {ID: "b"}, {ID: "c"}}, rules.InitialDeck())
	t.Cleanup(engine.Close)
	require.NoError(t, engine.Start())

	sawDeal := false
	for range 500 {
		if engine.IsFinished() {
			break
		}
		var action game.Action
		engine.WithState(func(state *game.State) {
			action = rules.TimeoutAction(state)
			require.NotNil(t, action, "a live table must always have a safe move")
			require.NoError(t, rules.ValidateAction(state, action))
		})
		if _, isDeal := action.(ActionNextHand); isDeal {
			sawDeal = true
		}
		require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), action))
	}

	assert.True(t, engine.IsFinished(), "auto-playing every turn has to finish the match")
	assert.True(t, sawDeal, "and that includes dealing for an absent dealer")
}

// Action names go into logs, metrics and the engine's event stream, so they are part
// of the wire vocabulary rather than an implementation detail.
func TestActionNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		action game.Action
		want   string
	}{
		{ActionFold{}, "poker.Fold"},
		{ActionCheck{}, "poker.Check"},
		{ActionCall{}, "poker.Call"},
		{ActionRaiseTo{Amount: 100}, "poker.RaiseTo"},
		{ActionAllIn{}, "poker.AllIn"},
		{ActionNextHand{}, "poker.NextHand"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.action.Name())
		})
	}
}

// Phase names are what the chip-conservation and bad-deal log lines are read by, so an
// unnamed phase would make those unreadable rather than merely ugly.
func TestRoundPhase_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		phase RoundPhase
		want  string
	}{
		{PreFlop, "PREFLOP"},
		{Flop, "FLOP"},
		{Turn, "TURN"},
		{River, "RIVER"},
		{Showdown, "SHOWDOWN"},
		{PhaseUnknown, "UNKNOWN"},
		{RoundPhase(200), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.phase.String())
		})
	}
}

// Every rules hook reads State.Extra, and the engine hands it whatever the game put
// there. Another game's state must be ignored rather than type-asserted into a panic.
func TestRules_ForeignStateIsNotReadAsPoker(t *testing.T) {
	t.Parallel()
	rules := &Rules{}
	state := game.NewState(rules, []*game.Player{{ID: "a"}, {ID: "b"}}, deck.StandardDeck())
	state.Extra = struct{ NotPoker bool }{}

	assert.False(t, rules.CheckWinCondition(state))
	assert.Nil(t, rules.Standings(state))
	assert.Zero(t, rules.StandingScore(state, state.Players[0]))
	assert.Nil(t, rules.TimeoutAction(state))
	assert.Zero(t, rules.TurnTimeout(state))
	require.ErrorIs(t, rules.ValidateAction(state, ActionFold{}), game.ErrInvalidState)
	require.ErrorIs(t, rules.ApplyAction(state, ActionFold{}), game.ErrInvalidState)
	require.ErrorIs(t, rules.AfterAction(state, ActionFold{}), game.ErrInvalidState)
	assert.NotPanics(t, func() { rules.OnPlayerLeave(state, "a") })
	assert.NotPanics(t, func() { rules.AfterPlayerRemoved(state, 0) })
}

// A call that no opponent can make the player pay for is free: the short big blind
// is all-in for 10, so everything the button adds past that comes straight back at
// showdown. Folding there hands the blind a pot the button had already covered.
func TestRules_TimeoutAction_CallsWhenTheCallIsRefundedInFull(t *testing.T) {
	t.Parallel()
	state, extra := tableWithChips(1000, 10)
	rules := &Rules{}
	require.NoError(t, rules.beginHand(state, extra, 0))
	require.Equal(t, 0, state.CurrentTurn, "heads-up the button acts first")

	action := rules.TimeoutAction(state)

	require.Equal(t, ActionCall{}, action)
	require.NoError(t, rules.ValidateAction(state, action))
	require.NoError(t, rules.ApplyAction(state, action))
	require.NoError(t, rules.AfterAction(state, action))
	assert.True(t, extra.ReachedShowdown, "the hand is shown down, not folded")
	assert.Equal(t, uint(1010), extra.PlayerChips["p0"]+extra.PlayerChips["p1"])
	assert.GreaterOrEqual(t, extra.PlayerChips["p0"], uint(990), "the button risked only the 10 the blind covered")
}
