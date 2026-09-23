package poker

import (
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seat describes one player's betting position for the round-progress helpers, which
// is all any of them read.
type seat struct {
	id     string
	chips  uint
	bet    uint
	acted  bool
	level  uint // the CurrentBet an acted seat acted on
	folded bool
	allIn  bool
}

func seatedRound(currentBet uint, seats ...seat) (*game.State, *State) {
	players := make([]*game.Player, 0, len(seats))
	extra := &State{
		CurrentBet: currentBet,
		BigBlind:   DefaultBigBlind,
		MinRaise:   DefaultBigBlind,
		Phase:      PhasePreFlop,
		Table:      make([]deck.Card, 0, BoardSize),
		Seats:      map[string]*Seat{},
	}
	for _, s := range seats {
		players = append(players, &game.Player{ID: s.id})
		extra.Seats[s.id] = &Seat{
			Chips:        s.chips,
			Bet:          s.bet,
			Acted:        s.acted,
			LastBetLevel: s.level,
			Folded:       s.folded,
			AllIn:        s.allIn,
		}
	}
	state := game.NewState(&Rules{}, players, deck.Standard())
	state.Extra = extra
	state.Phase = game.Playing
	return state, extra
}

// A round is over once everyone still able to bet has acted and matched the bet.
func TestBettingRoundComplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		currentBet uint
		seats      []seat
		want       bool
	}{
		{
			name:       "acted and matched exactly",
			currentBet: 100,
			seats: []seat{
				{id: "a", chips: 900, bet: 100, acted: true},
				{id: "b", chips: 900, bet: 100, acted: true},
			},
			want: true,
		},
		{
			name:       "one chip short still owes",
			currentBet: 100,
			seats: []seat{
				{id: "a", chips: 900, bet: 100, acted: true},
				{id: "b", chips: 901, bet: 99, acted: true},
			},
			want: false,
		},
		{
			name:       "matched but never acted",
			currentBet: 0,
			seats: []seat{
				{id: "a", chips: 900, acted: true},
				{id: "b", chips: 900},
			},
			want: false,
		},
		{
			name:       "folded, all-in and broke seats owe nothing",
			currentBet: 100,
			seats: []seat{
				{id: "a", chips: 900, bet: 100, acted: true},
				{id: "b", chips: 900, folded: true},
				{id: "c", chips: 900, allIn: true},
				{id: "d", chips: 0},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state, extra := seatedRound(tt.currentBet, tt.seats...)
			assert.Equal(t, tt.want, bettingRoundComplete(state, extra))
		})
	}
}

// nextToAct has to be able to come back around to the seat it started from: heads-up
// against an all-in opponent, that seat is the only one left who can act.
func TestNextToAct(t *testing.T) {
	t.Parallel()

	t.Run("wraps all the way back to the starting seat", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(0,
			seat{id: "a", chips: 900},
			seat{id: "b", chips: 900, folded: true},
		)

		assert.Equal(t, 0, nextToAct(state, extra, 0), "the only live seat is the one we started from")
	})

	t.Run("a seat that acted and matched is not asked again", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(100,
			seat{id: "a", chips: 900, bet: 100, acted: true},
			seat{id: "b", chips: 900, bet: 100, acted: true},
		)

		assert.Equal(t, -1, nextToAct(state, extra, 0), "nobody owes anything, so nobody is on turn")
	})

	t.Run("a seat that acted but was raised on owes again", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(200,
			seat{id: "a", chips: 900, bet: 200, acted: true},
			seat{id: "b", chips: 900, bet: 100, acted: true},
		)

		assert.Equal(t, 1, nextToAct(state, extra, 0))
	})

	t.Run("skips folded, all-in and broke seats", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(0,
			seat{id: "a", chips: 900, acted: true},
			seat{id: "b", chips: 900, folded: true},
			seat{id: "c", chips: 900, allIn: true},
			seat{id: "d", chips: 0},
			seat{id: "e", chips: 900},
		)

		assert.Equal(t, 4, nextToAct(state, extra, 0))
	})
}

// Postflop action starts left of the button and, heads-up against an all-in, can land on
// the button itself.
func TestFirstToActPostflop(t *testing.T) {
	t.Parallel()

	t.Run("comes back around to the dealer", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(0,
			seat{id: "a", chips: 900},
			seat{id: "b", chips: 900, folded: true},
		)
		extra.DealerIndex = 0
		state.CurrentTurn = 1

		assert.Equal(t, 0, firstToActPostflop(state, extra))
	})

	t.Run("a seat with no chips left cannot open", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(0,
			seat{id: "a", chips: 900},
			seat{id: "b", chips: 0},
			seat{id: "c", chips: 900},
		)
		extra.DealerIndex = 0

		assert.Equal(t, 2, firstToActPostflop(state, extra), "seat 1 is broke, so seat 2 opens")
	})

	t.Run("nobody able to act leaves the turn where it was", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(0,
			seat{id: "a", chips: 900, allIn: true},
			seat{id: "b", chips: 900, allIn: true},
		)
		extra.DealerIndex = 0
		state.CurrentTurn = 1

		assert.Equal(t, 1, firstToActPostflop(state, extra))
	})
}

// Betting only continues while at least two players can still put chips in.
func TestSettleAndAdvance_RunsOutTheBoardWhenBettingCannotContinue(t *testing.T) {
	t.Parallel()

	t.Run("two who can still bet stop at the flop", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(100,
			seat{id: "a", chips: 900, bet: 100, acted: true},
			seat{id: "b", chips: 900, bet: 100, acted: true},
			seat{id: "c", chips: 500, folded: true},
		)

		require.NoError(t, settleAndAdvance(state, extra))

		assert.Equal(t, PhaseFlop, extra.Phase, "there is still betting to do")
		assert.Len(t, extra.Table, 3)
	})

	t.Run("one who can still bet runs the board out", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(100,
			seat{id: "a", chips: 900, bet: 100, acted: true},
			seat{id: "b", chips: 0, bet: 100, acted: true},
		)

		require.NoError(t, settleAndAdvance(state, extra))

		assert.Equal(t, PhaseShowdown, extra.Phase, "a lone live player just sees the board")
		assert.Len(t, extra.Table, 5)
	})

	t.Run("the river always shows down", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(0,
			seat{id: "a", chips: 900, acted: true},
			seat{id: "b", chips: 900, acted: true},
		)
		extra.Phase = PhaseRiver

		require.NoError(t, settleAndAdvance(state, extra))

		assert.Equal(t, PhaseShowdown, extra.Phase)
		assert.True(t, extra.ReachedShowdown)
	})
}

// The winners a hand announces are the ones awardPots actually paid. A hand cannot be
// won by somebody who folded, however good their cards were, and a hand nobody can
// classify still has to name a winner or the pot is stranded.
func TestAwardPots_NamesEveryPlayerItPaid(t *testing.T) {
	t.Parallel()

	t.Run("a folded player never wins", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(0,
			seat{id: "a", chips: 900, folded: true},
			seat{id: "b", chips: 900},
		)
		extra.Pots = []Pot{{Amount: 200, Eligible: []string{"b"}}}
		scores := map[string]int{"a": 9000, "b": 10}

		winners := awardPots(extra, contenders(state, extra), scores)

		require.Len(t, winners, 1)
		assert.Equal(t, "b", winners[0].ID, "the best hand at the table folded it")
	})

	t.Run("every tied player is named", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(0,
			seat{id: "a", chips: 900},
			seat{id: "b", chips: 900},
			seat{id: "c", chips: 900},
		)
		extra.Pots = []Pot{{Amount: 300, Eligible: []string{"a", "b", "c"}}}
		scores := map[string]int{"a": 500, "b": 500, "c": 10}

		winners := awardPots(extra, contenders(state, extra), scores)

		require.Len(t, winners, 2, "a split pot names all co-winners")
		assert.Equal(t, "a", winners[0].ID)
		assert.Equal(t, "b", winners[1].ID)
	})

	// Fewer than five cards score zero, which is what a hand shown down before the
	// board filled out looks like. Zero still has to beat "no winner at all".
	t.Run("an unclassifiable hand still wins", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(0,
			seat{id: "a", chips: 900},
			seat{id: "b", chips: 900},
		)
		extra.Pots = []Pot{{Amount: 200, Eligible: []string{"a", "b"}}}
		scores := map[string]int{"a": 0, "b": 0}

		assert.Len(t, awardPots(extra, contenders(state, extra), scores), 2)
	})

	// The best hand at the table can be a short stack who only paid into the main
	// pot. Naming them as the hand's winner would contradict the side pot.
	t.Run("a side pot names its own winner", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(0,
			seat{id: "short", chips: 0, allIn: true},
			seat{id: "mid", chips: 0, allIn: true},
			seat{id: "big", chips: 0, allIn: true},
		)
		extra.Pots = []Pot{
			{Amount: 300, Eligible: []string{"big", "mid", "short"}},
			{Amount: 400, Eligible: []string{"big", "mid"}},
		}
		scores := map[string]int{"short": 9000, "mid": 500, "big": 10}

		winners := awardPots(extra, contenders(state, extra), scores)

		require.Len(t, winners, 2)
		assert.Equal(t, "short", winners[0].ID, "main pot first")
		assert.Equal(t, "mid", winners[1].ID, "the side pot went somewhere else")
		assert.Equal(t, uint(300), extra.Seats["short"].Chips)
		assert.Equal(t, uint(400), extra.Seats["mid"].Chips)
	})
}

// Dead money - chips from players who all folded - has to end up somewhere, and the
// side pots are what put it there.
func TestBuildSidePots_DeadMoney(t *testing.T) {
	t.Parallel()

	t.Run("a folded short stack pays into the pot it could not win", func(t *testing.T) {
		t.Parallel()
		state, extra := sidePotState(t, map[string]uint{"a": 100, "b": 100, "c": 30}, "c")

		pots := buildSidePots(extra, contenders(state, extra))

		require.Len(t, pots, 2, "c's level closes a layer even though c cannot win it")
		assert.Equal(t, Pot{Amount: 90, Eligible: []string{"a", "b"}}, pots[0], "c's dead 30 is in here")
		assert.Equal(t, Pot{Amount: 140, Eligible: []string{"a", "b"}}, pots[1])
		assert.Equal(t, uint(230), potTotal(pots), "no contributed chip may be lost")
	})

	t.Run("layers split by what each contributor could cover", func(t *testing.T) {
		t.Parallel()
		state, extra := sidePotState(t, map[string]uint{"short": 100, "a": 300, "b": 300})

		pots := buildSidePots(extra, contenders(state, extra))

		require.Len(t, pots, 2)
		assert.Equal(t, Pot{Amount: 300, Eligible: []string{"a", "b", "short"}}, pots[0])
		assert.Equal(t, Pot{Amount: 400, Eligible: []string{"a", "b"}}, pots[1])
		assert.Equal(t, uint(700), potTotal(pots), "no contributed chip may be lost")
	})
}

// The part of a bet nobody could match never belonged in the pot, so it goes back to
// the player who bet it before any layer is cut - whether or not they are still in the
// hand. Folding that money into the live pot would pay it to their opponents.
func TestRefundUncalled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contributed map[string]uint
		wantRefund  map[string]uint // only the entries that change
		wantAfter   map[string]uint
	}{
		{
			name:        "the lone over-bettor gets the unmatched part back",
			contributed: map[string]uint{"a": 100, "b": 100, "over": 1100},
			wantRefund:  map[string]uint{"over": 1000},
			wantAfter:   map[string]uint{"a": 100, "b": 100, "over": 100},
		},
		{
			name:        "a bet two players matched is not uncalled",
			contributed: map[string]uint{"a": 100, "b": 1100, "c": 1100},
			wantRefund:  map[string]uint{},
			wantAfter:   map[string]uint{"a": 100, "b": 1100, "c": 1100},
		},
		{
			name:        "an even table refunds nobody",
			contributed: map[string]uint{"a": 50, "b": 50},
			wantRefund:  map[string]uint{},
			wantAfter:   map[string]uint{"a": 50, "b": 50},
		},
		{
			name:        "only the amount above the second largest comes back",
			contributed: map[string]uint{"a": 20, "b": 70, "c": 90},
			wantRefund:  map[string]uint{"c": 20},
			wantAfter:   map[string]uint{"a": 20, "b": 70, "c": 70},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, extra := sidePotState(t, tt.contributed)
			poolBefore := extra.Pool

			refundUncalled(extra)

			var refunded uint
			for id := range tt.contributed {
				assert.Equal(t, tt.wantRefund[id], extra.Seats[id].Chips, "refund to %s", id)
				refunded += tt.wantRefund[id]
			}
			assert.Equal(t, tt.wantAfter, contributions(extra), "contributions after the refund")

			assert.Equal(t, poolBefore-refunded, extra.Pool, "the refund leaves the pool")
		})
	}
}

// awardPots decides who is paid from each layer.
func TestAwardPots(t *testing.T) {
	t.Parallel()

	t.Run("a tie splits the pot", func(t *testing.T) {
		t.Parallel()
		state, extra := sidePotState(t, map[string]uint{"a": 100, "b": 100, "c": 100})
		extra.Pots = []Pot{{Amount: 300, Eligible: []string{"a", "b", "c"}}}

		awardPots(extra, contenders(state, extra), map[string]int{"a": 500, "b": 500, "c": 10})

		assert.Equal(t, uint(150), extra.Seats["a"].Chips)
		assert.Equal(t, uint(150), extra.Seats["b"].Chips)
		assert.Zero(t, extra.Seats["c"].Chips, "a losing hand is paid nothing")
	})

	t.Run("the best hand takes it outright", func(t *testing.T) {
		t.Parallel()
		state, extra := sidePotState(t, map[string]uint{"a": 100, "b": 100})
		extra.Pots = []Pot{{Amount: 200, Eligible: []string{"a", "b"}}}

		awardPots(extra, contenders(state, extra), map[string]int{"a": 10, "b": 500})

		assert.Zero(t, extra.Seats["a"].Chips)
		assert.Equal(t, uint(200), extra.Seats["b"].Chips)
	})

	t.Run("hands nobody can classify are still paid", func(t *testing.T) {
		t.Parallel()
		state, extra := sidePotState(t, map[string]uint{"a": 100, "b": 100})
		extra.Pots = []Pot{{Amount: 200, Eligible: []string{"a", "b"}}}

		awardPots(extra, contenders(state, extra), map[string]int{"a": 0, "b": 0})

		assert.Equal(t, uint(200), extra.Seats["a"].Chips+extra.Seats["b"].Chips,
			"a pot must never be left unawarded")
	})
}

// The match is decided by the stack a player walks away with, so chips lead the ranking;
// hand strength only separates players who finished level.
func TestRankPlayers_Order(t *testing.T) {
	t.Parallel()

	t.Run("the bigger stack finishes higher", func(t *testing.T) {
		t.Parallel()
		state, extra := tableWithChips(100, 900)

		ranked := rankPlayers(state, extra)

		require.Len(t, ranked, 2)
		assert.Equal(t, "p1", ranked[0].ID, "900 beats 100")
		assert.Equal(t, "p0", ranked[1].ID)
	})

	// Two players who finish level on chips are separated by the hand they were
	// holding, which needs a board to evaluate against.
	t.Run("level stacks are separated by the hand", func(t *testing.T) {
		t.Parallel()
		state, extra := tableWithChips(500, 500)
		extra.ReachedShowdown = true
		state.Players[0].Cards = []deck.Card{
			{Rank: deck.Two, Suit: deck.Clubs}, {Rank: deck.Three, Suit: deck.Diamonds},
		}
		state.Players[1].Cards = []deck.Card{
			{Rank: deck.Ace, Suit: deck.Clubs}, {Rank: deck.Ace, Suit: deck.Diamonds},
		}
		extra.Table = []deck.Card{
			{Rank: deck.Ace, Suit: deck.Hearts},
			{Rank: deck.Seven, Suit: deck.Spades},
			{Rank: deck.Nine, Suit: deck.Clubs},
		}

		ranked := rankPlayers(state, extra)

		assert.Equal(t, "p1", ranked[0].ID, "trip aces on the flop outranks nothing")
	})
}

// validateRaiseTo is the only thing standing between a player and an illegal bet, so every
// limit it enforces is checked at the exact value where it starts to bite.
func TestValidateRaiseTo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		currentBet uint
		minRaise   uint
		bet        uint
		chips      uint
		oppChips   uint // the one opponent's stack; 0 means "deep enough to call anything"
		oppBet     uint
		amount     uint
		wantErr    string
	}{
		{
			name:       "a full raise is allowed",
			currentBet: 100, minRaise: 100, chips: 1000, amount: 200,
		},
		{
			name:       "raising to exactly the current bet is not a raise",
			currentBet: 100, minRaise: 100, chips: 1000, amount: 100,
			wantErr: "above current bet",
		},
		{
			name:       "raising to exactly the minimum is allowed",
			currentBet: 100, minRaise: 50, chips: 1000, amount: 150,
		},
		{
			name:       "one chip under the minimum is refused",
			currentBet: 100, minRaise: 50, chips: 1000, amount: 149,
			wantErr: "minimum raise is 50",
		},
		{
			name:       "chips already in the pot count towards the raise",
			currentBet: 100, minRaise: 50, bet: 100, chips: 100, amount: 200,
		},
		{
			name:       "more than the stack is refused",
			currentBet: 100, minRaise: 50, chips: 100, amount: 201,
			wantErr: "not enough chips",
		},
		{
			name:       "raising the whole stack is allowed",
			currentBet: 100, minRaise: 50, chips: 200, amount: 200,
		},
		{
			name:       "an all-in below the minimum is still allowed",
			currentBet: 100, minRaise: 500, chips: 150, amount: 150,
		},
		{
			name:       "raising to exactly what the opponent can cover is allowed",
			currentBet: 100, minRaise: 50, chips: 1000, oppChips: 300, amount: 300,
		},
		{
			name:       "one chip past what the opponent can cover is refused",
			currentBet: 100, minRaise: 50, chips: 1000, oppChips: 300, amount: 301,
			wantErr: "no opponent can call more than 300",
		},
		{
			name:       "the opponent's own street bet counts towards what they can cover",
			currentBet: 100, minRaise: 50, chips: 1000, oppChips: 200, oppBet: 100, amount: 300,
		},
		{
			name:       "an opponent too short for a full raise can still be put all-in",
			currentBet: 0, minRaise: 50, chips: 1000, oppChips: 30, amount: 30,
		},
		{
			name:       "but not for less than their stack",
			currentBet: 0, minRaise: 50, chips: 1000, oppChips: 30, amount: 29,
			wantErr: "minimum raise is 30",
		},
		{
			name:       "nothing can be raised past an opponent who is already all-in for less",
			currentBet: 100, minRaise: 50, chips: 1000, oppChips: 0, oppBet: 100, amount: 150,
			wantErr: "no opponent can call more than 100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opp := seat{id: "b", chips: tt.oppChips, bet: tt.oppBet}
			if tt.oppChips == 0 && tt.oppBet == 0 {
				opp.chips = 1_000_000
			}
			state, extra := seatedRound(tt.currentBet, seat{id: "a", chips: tt.chips, bet: tt.bet}, opp)
			extra.MinRaise = tt.minRaise

			err := validateRaiseTo(state, extra, &game.Player{ID: "a"}, tt.amount)

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// Going all-in has to move every last chip, including when the player already has more in
// front of them this street than they have left behind it.
func TestApplyAction_AllInCommitsTheWholeStack(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		bet     uint
		chips   uint
		wantBet uint
	}{
		{name: "nothing committed yet", bet: 0, chips: 400, wantBet: 400},
		{name: "already bet more than is left", bet: 100, chips: 40, wantBet: 140},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state, extra := seatedRound(0, seat{id: "a", chips: tt.chips, bet: tt.bet})
			extra.MinRaise = DefaultBigBlind
			state.CurrentTurn = 0

			(&Rules{}).ApplyAction(state, ActionAllIn{})

			assert.Zero(t, extra.Seats["a"].Chips, "all-in leaves no chips behind")
			assert.Equal(t, tt.wantBet, extra.Seats["a"].Bet)
			assert.True(t, extra.Seats["a"].AllIn)
		})
	}
}

// Committing to a target moves exactly the difference between what a player has in and
// what is owed: no
// more (that would be a raise nobody asked for) and no less.
func TestCommitTo_MovesExactlyWhatIsOwed(t *testing.T) {
	t.Parallel()

	t.Run("with chips to spare", func(t *testing.T) {
		t.Parallel()
		_, extra := seatedRound(200, seat{id: "a", chips: 500, bet: 50})

		extra.commitTo(extra.Seats["a"], 200)

		assert.Equal(t, uint(200), extra.Seats["a"].Bet, "the bet lands on the amount owed")
		assert.Equal(t, uint(350), extra.Seats["a"].Chips, "only the difference leaves the stack")
		assert.Equal(t, uint(150), extra.Seats["a"].Contributed)
		assert.False(t, extra.Seats["a"].AllIn)
	})

	t.Run("a short stack calls for what it has", func(t *testing.T) {
		t.Parallel()
		_, extra := seatedRound(500, seat{id: "a", chips: 100, bet: 50})

		extra.commitTo(extra.Seats["a"], 500)

		assert.Equal(t, uint(150), extra.Seats["a"].Bet)
		assert.Zero(t, extra.Seats["a"].Chips)
		assert.True(t, extra.Seats["a"].AllIn)
	})
}

// Whether a raise is "full" decides if already-acted players get fresh action.
func TestApplyBetIncrease_Boundaries(t *testing.T) {
	t.Parallel()

	t.Run("exactly the minimum raise reopens the round", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(100,
			seat{id: "a", chips: 900, bet: 200},
			seat{id: "b", chips: 900, bet: 100, acted: true},
		)
		extra.MinRaise = 100

		applyBetIncrease(state, extra, state.Players[0], 200)

		assert.Equal(t, uint(200), extra.CurrentBet)
		assert.Equal(t, uint(100), extra.MinRaise, "a raise of exactly the minimum is a full raise")
		assert.False(t, extra.Seats["b"].Acted, "so it reopens the round")
	})

	t.Run("an increase to what is already owed changes nothing", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(100,
			seat{id: "a", chips: 900, bet: 100},
			seat{id: "b", chips: 900, bet: 100, acted: true},
		)
		// A zero MinRaise is what makes the difference observable: without the guard,
		// a raise of nothing would count as full and hand everybody fresh action.
		extra.MinRaise = 0

		applyBetIncrease(state, extra, state.Players[0], 100)

		assert.Equal(t, uint(100), extra.CurrentBet)
		assert.True(t, extra.Seats["b"].Acted, "nobody gets to act again over a non-raise")
	})
}

// A sub-minimum all-in advances what is owed without reopening the round, so the
// players it passes owe the difference and may only call or fold. Letting them
// raise instead turns a short shove into free extra action for the whole table.
func TestValidateAction_NoRaiseWhenBettingIsNotReopened(t *testing.T) {
	t.Parallel()
	rules := &Rules{}

	// "a" opened to 200, "b" shoved their last 50 on top - a raise of 50 against a
	// minimum of 150 - and the action came back round to "a".
	notReopened := func() (*game.State, *State) {
		state, extra := seatedRound(250,
			seat{id: "a", chips: 800, bet: 200, acted: true, level: 200},
			seat{id: "b", chips: 0, bet: 250, acted: true, level: 250, allIn: true},
			seat{id: "c", chips: 800, bet: 200, acted: true, level: 200},
		)
		extra.MinRaise = 150
		state.CurrentTurn = 0
		return state, extra
	}

	t.Run("a raise is refused", func(t *testing.T) {
		t.Parallel()
		state, _ := notReopened()

		err := rules.ValidateAction(state, ActionRaiseTo{Amount: 400})

		require.ErrorContains(t, err, "betting is not reopened")
	})

	t.Run("a shove is refused, because a shove above the bet is a raise", func(t *testing.T) {
		t.Parallel()
		state, _ := notReopened()

		err := rules.ValidateAction(state, ActionAllIn{})

		require.ErrorContains(t, err, "betting is not reopened")
	})

	t.Run("calling and folding stay open", func(t *testing.T) {
		t.Parallel()
		state, _ := notReopened()

		require.NoError(t, rules.ValidateAction(state, ActionCall{}))
		require.NoError(t, rules.ValidateAction(state, ActionFold{}))
	})

	t.Run("a shove that only calls is still allowed", func(t *testing.T) {
		t.Parallel()
		state, extra := notReopened()
		// Short enough that going all-in cannot get past the current bet, so it is
		// a call with everything rather than a raise.
		extra.Seats["a"].Chips = 30

		require.NoError(t, rules.ValidateAction(state, ActionAllIn{}))
	})

	t.Run("a full raise reopens the round for everyone it passed", func(t *testing.T) {
		t.Parallel()
		state, extra := notReopened()
		// What a full-size raise does: applyBetIncrease clears ActedThisRound.
		extra.Seats["a"].Acted = false

		require.NoError(t, rules.ValidateAction(state, ActionRaiseTo{Amount: 400}))
		require.NoError(t, rules.ValidateAction(state, ActionAllIn{}))
	})
}

// A hand that cannot be dealt has to be reported.
func TestAfterAction_NextHandReportsABadDeal(t *testing.T) {
	t.Parallel()
	// Only one player still has chips, so there is no hand to deal.
	state, extra := tableWithChips(1000, 0)
	extra.HandNumber = 1

	err := (&Rules{}).AfterAction(state, ActionNextHand{})

	require.ErrorContains(t, err, "not enough funded players")
}

// Whoever is removed, the cursor the rules hand back has to address a real seat. The
// engine clamps State.CurrentTurn before it calls AfterPlayerRemoved, so the rules
// carry no guard of their own; this drives the real removal path to prove it.
func TestAfterPlayerRemoved_LeavesTheCursorOnARealSeat(t *testing.T) {
	t.Parallel()

	for _, victim := range []string{"a", "b", "c"} {
		t.Run("removing "+victim, func(t *testing.T) {
			t.Parallel()
			engine := game.NewEngine(&Rules{},
				[]*game.Player{{ID: "a"}, {ID: "b"}, {ID: "c"}}, deck.Standard())
			t.Cleanup(engine.Close)
			require.NoError(t, engine.Start())

			engine.RemovePlayer(victim)

			engine.WithState(func(s *game.State) {
				assert.GreaterOrEqual(t, s.CurrentTurn, 0)
				assert.Less(t, s.CurrentTurn, len(s.Players))
			})
		})
	}
}

// Seat 0 is a real answer from nextToAct, not the absence of one.
func TestAfterBettingAction_SeatZeroCanBeNext(t *testing.T) {
	t.Parallel()
	state, extra := seatedRound(100,
		seat{id: "a", chips: 900, bet: 50, acted: true},
		seat{id: "b", chips: 900, bet: 100, acted: true},
	)
	extra.Phase = PhaseFlop
	state.CurrentTurn = 1

	require.NoError(t, afterBettingAction(state, extra))

	require.NotNil(t, state.OverrideNextTurn)
	assert.Equal(t, 0, *state.OverrideNextTurn, "seat 0 still owes chips, so it is on turn")
	assert.Equal(t, PhaseFlop, extra.Phase, "the round is not over, so the street does not advance")
}

// The blinds walk forward from the button over seats that still have chips.
func TestNextFundedSeat_SkipsBrokeSeatsForward(t *testing.T) {
	t.Parallel()

	t.Run("steps over the broke seat next door", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(0,
			seat{id: "a", chips: 900},
			seat{id: "b", chips: 0},
			seat{id: "c", chips: 900},
		)

		assert.Equal(t, 2, nextFundedSeat(state, extra, 0), "seat 1 is out, so the blind moves to seat 2")
	})

	t.Run("nobody else funded leaves the marker where it was", func(t *testing.T) {
		t.Parallel()
		state, extra := seatedRound(0,
			seat{id: "a", chips: 900},
			seat{id: "b", chips: 0},
		)

		assert.Equal(t, 0, nextFundedSeat(state, extra, 0))
	})
}

// Seat 0 is a legitimate answer for "who is under the gun".
func TestBeginHand_SeatZeroCanBeUnderTheGun(t *testing.T) {
	t.Parallel()
	// Heads-up the button acts first, so with the button on seat 0 the first actor is
	// seat 0 itself.
	state, extra := tableWithChips(1000, 1000)

	require.NoError(t, beginHand(state, extra, 0))

	assert.Equal(t, PhasePreFlop, extra.Phase, "the hand starts with betting, not a board")
	assert.Empty(t, extra.Table)
	assert.Equal(t, 0, state.CurrentTurn, "the button is under the gun heads-up")
}

// Being asked whether to deal the next hand is not a move under pressure, so it gets its
// own clock.
func TestRules_TurnTimeout_DealGetsALongerClock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*State)
		want   time.Duration
	}{
		{name: "a betting turn keeps the engine's clock", mutate: func(*State) {}},
		{
			name:   "between hands the dealer gets a minute",
			mutate: func(e *State) { e.Phase = PhaseShowdown },
			want:   dealTurnTimeout,
		},
		{
			name:   "a finished match needs no deal clock",
			mutate: func(e *State) { e.Phase, e.MatchComplete = PhaseShowdown, true },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state, extra := seatedRound(0, seat{id: "a", chips: 900}, seat{id: "b", chips: 900})
			tt.mutate(extra)

			assert.Equal(t, tt.want, (&Rules{}).TurnTimeout(state))
		})
	}
}

// A state that is not poker's must not be read as one.
func TestRules_TurnTimeout_ForeignStateFallsBackToTheDefault(t *testing.T) {
	t.Parallel()
	state := game.NewState(&Rules{}, nil, deck.Standard())

	assert.Zero(t, (&Rules{}).TurnTimeout(state))
}

// Short all-ins that are each below a full raise but together reach one reopen the
// betting for a player who already acted (the TDA rule): what they face is measured
// against the bet they last acted on, not against the last shove alone.
func TestValidateAction_ShortAllInsThatAddUpToAFullRaiseReopen(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cAction  game.Action
		wantOpen bool
	}{
		{name: "150 then 220 is 120 over the 100 a bet, a full raise", cAction: ActionAllIn{}, wantOpen: true},
		{name: "150 alone is 50 over the 100 a bet, still short", cAction: ActionFold{}, wantOpen: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rules := &Rules{}
			state, extra := seatedRound(0,
				seat{id: "a", chips: 1000},
				seat{id: "b", chips: 150},
				seat{id: "c", chips: 220},
				seat{id: "d", chips: 1000},
			)
			act := func(seat int, action game.Action) {
				state.CurrentTurn = seat
				require.NoError(t, rules.ValidateAction(state, action))
				require.NoError(t, rules.ApplyAction(state, action))
			}

			act(0, ActionRaiseTo{Amount: 100})
			act(1, ActionAllIn{})
			act(2, tt.cAction)
			act(3, ActionCall{})
			state.CurrentTurn = 0

			err := rules.ValidateAction(state, ActionRaiseTo{Amount: extra.CurrentBet + extra.MinRaise})
			if tt.wantOpen {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "betting is not reopened")
		})
	}
}

// RaiseBounds is what the view builds its prompt from, so every amount in the band
// has to pass ValidateAction and nothing just outside it may.
func TestRaiseBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		currentBet     uint
		hero, opp      seat
		wantLo, wantHi uint
		wantOK         bool
	}{
		{
			name: "a full raise up to the stack", currentBet: 50,
			hero: seat{id: "a", chips: 1000}, opp: seat{id: "b", chips: 1000},
			wantLo: 100, wantHi: 1000, wantOK: true,
		},
		{
			name: "capped by what the opponent can call", currentBet: 0,
			hero: seat{id: "a", chips: 1000}, opp: seat{id: "b", chips: 30},
			wantLo: 30, wantHi: 30, wantOK: true,
		},
		{
			name: "an opponent all-in for the bet leaves nothing to raise", currentBet: 50,
			hero: seat{id: "a", chips: 1000}, opp: seat{id: "b", bet: 50, allIn: true},
		},
		{
			name: "betting not reopened", currentBet: 70,
			hero: seat{id: "a", chips: 1000, bet: 50, acted: true, level: 50}, opp: seat{id: "b", chips: 1000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state, _ := seatedRound(tt.currentBet, tt.hero, tt.opp)
			state.CurrentTurn = 0

			lo, hi, ok := RaiseBounds(state, "a")

			require.Equal(t, tt.wantOK, ok)
			if !ok {
				return
			}
			assert.Equal(t, tt.wantLo, lo)
			assert.Equal(t, tt.wantHi, hi)
			rules := &Rules{}
			require.NoError(t, rules.ValidateAction(state, ActionRaiseTo{Amount: lo}))
			require.NoError(t, rules.ValidateAction(state, ActionRaiseTo{Amount: hi}))
			require.Error(t, rules.ValidateAction(state, ActionRaiseTo{Amount: lo - 1}))
			require.Error(t, rules.ValidateAction(state, ActionRaiseTo{Amount: hi + 1}))
		})
	}
}
