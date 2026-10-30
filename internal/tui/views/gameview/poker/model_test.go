package poker

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/poker"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"
	"github.com/Pieczasz/tty-cards/internal/tui/views"

	"uuid"

	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testUser(name string) *db.User {
	return &db.User{ID: testutil.UID(1), Username: name}
}

// startedTable returns a two-handed table mid-hand plus the view bound to seat 1.
func startedTable(t *testing.T) (*game.Engine, *model) {
	t.Helper()
	engine := game.NewEngine(&logic.Rules{}, testutil.NamedPlayers("alice", "bob"))

	require.NoError(t, engine.Start())

	global := router.GlobalContext{Player: lobby.NewPlayer(testUser("alice"))}
	m, ok := New(global, game.Bind(engine, views.SessionPlayerID(global)), "poker").(*model)
	require.True(t, ok)
	// The view first: closing it unsubscribes, which a closed engine no longer needs.
	t.Cleanup(engine.Close)
	t.Cleanup(m.Close)
	return engine, m
}

// extra is the poker state behind a table, failing the test on anything else.
func extra(t *testing.T, s *game.State) *logic.State {
	t.Helper()
	e, ok := s.Extra.(*logic.State)
	require.True(t, ok, "state.Extra is %T, not *poker.State", s.Extra)
	return e
}

func TestSyncState_BuildsSeatsFromEngine(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)

	require.Len(t, m.seats, 2)
	assert.Equal(t, logic.DefaultSmallBlind+logic.DefaultBigBlind, m.pot)
	assert.Equal(t, "PREFLOP", m.street)
	assert.False(t, m.handComplete)
	assert.Equal(t, 2*logic.DefaultBigBlind, m.raiseMin, "a full raise over the big blind")

	hero := m.heroSeat()
	require.NotNil(t, hero)
	assert.Equal(t, testutil.SeatID(1), hero.PlayerID)
	assert.Equal(t, "alice", hero.Name)
	assert.Len(t, hero.Hole, 2, "hero sees their own hole cards")

	for _, s := range m.seats {
		if s.IsHero {
			continue
		}
		assert.Empty(t, s.Hole, "opponent hole cards stay hidden before showdown")
		assert.Equal(t, 2, s.HandSize)
	}
}

// Seat flags drive the whole table render. Assert what a player can observe - who
// paid which blind, and that the markers are unique - rather than re-stating the
// assignment in syncState, which no change to the code could ever contradict.
func TestSyncState_SeatFlagsMatchBlinds(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)

	var sb, bb, dealer, turns int
	for _, s := range m.seats {
		if s.IsSB {
			sb++
			assert.Equal(t, logic.DefaultSmallBlind, s.Bet, "the small-blind seat posted the small blind")
		}
		if s.IsBB {
			bb++
			assert.Equal(t, logic.DefaultBigBlind, s.Bet, "the big-blind seat posted the big blind")
		}
		if s.IsDealer {
			dealer++
		}
		if s.IsTurn {
			turns++
		}
	}

	assert.Equal(t, 1, sb, "exactly one small blind")
	assert.Equal(t, 1, bb, "exactly one big blind")
	assert.Equal(t, 1, dealer, "exactly one dealer button")
	assert.Equal(t, 1, turns, "exactly one seat is on turn mid-hand")

	// Heads-up, the button posts the small blind.
	for _, s := range m.seats {
		if s.IsDealer {
			assert.True(t, s.IsSB, "heads-up: the dealer is the small blind")
		}
	}
}

func TestSyncState_ChipsAndBetsSumToStacks(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)

	var total uint
	for _, s := range m.seats {
		total += s.Chips + s.Bet
	}
	assert.Equal(t, 2*logic.DefaultStack, total, "blinds move chips into bets, never destroy them")
}

func TestClampRaise_BoundsToLegalRange(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)

	// Hardcoded rather than recomputed from m: deriving the expectation from
	// currentBet+minRaise and streetBetMax would restate clampRaise's own body, and
	// swapping its min and max would still pass. Heads-up with DefaultStack=1000,
	// SB=25 and BB=50 the legal band is exactly [100, 1000].
	const wantMin, wantMax = uint(100), uint(1000)

	assert.Equal(t, wantMin, m.clampRaise(0), "below the minimum raises up to it")
	assert.Equal(t, wantMax, m.clampRaise(50_000), "above the stack clamps down to it")
	assert.Equal(t, uint(500), m.clampRaise(500), "an in-range amount passes through")
}

// tableOnTurn seats the view as whichever player the engine put on turn, so the
// test does not depend on where the button landed.
func tableOnTurn(t *testing.T, seats int) (*game.Engine, *model) {
	t.Helper()
	engine := game.NewEngine(&logic.Rules{}, testutil.Players(seats))
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	id, err := uuid.Parse(engine.CurrentPlayerID())
	require.NoError(t, err)

	m, ok := New(router.GlobalContext{Player: lobby.NewPlayer(&db.User{ID: id, Username: "hero"})}, game.Bind(engine, id.String()), "poker").(*model)
	require.True(t, ok)
	require.True(t, m.Base.MyTurn, "the view has to be bound to the seat on turn")
	return engine, m
}

// A raise being built belongs to the turn it is being built on. It used to survive
// the action moving on, so the prompt stayed up over a table the player could no
// longer bet into, and enter submitted into somebody else's turn.
func TestSyncState_ClosesTheRaisePromptWhenTheTurnIsLost(t *testing.T) {
	t.Parallel()
	engine, m := tableOnTurn(t, 3)

	m.raising = true
	m.raiseAmount = 500
	require.NoError(t, engine.SubmitAction(m.HeroID(), logic.ActionFold{}))
	m.syncState()

	require.False(t, m.Base.MyTurn, "folding passes the action on")
	assert.False(t, m.raising, "a half-built raise cannot outlive the turn it belongs to")
}

func TestSyncState_KeepsTheRaisePromptWhileTheTurnIsStillYours(t *testing.T) {
	t.Parallel()
	_, m := tableOnTurn(t, 3)

	m.raising = true
	m.raiseAmount = 500
	m.syncState()

	assert.True(t, m.raising, "a refresh mid-turn must not close the prompt")
	assert.Equal(t, uint(500), m.raiseAmount, "and must not lose the amount built so far")
}

func TestSyncState_NilBoundIsInert(t *testing.T) {
	t.Parallel()
	m, ok := New(router.GlobalContext{}, nil, "poker").(*model)
	require.True(t, ok)

	assert.Empty(t, m.seats)
	assert.Zero(t, m.pot)
	assert.False(t, m.canFold())
	assert.False(t, m.canRaise())
	assert.False(t, m.canAllIn())
}

// On the flop against a 30-chip stack a full raise is out of reach, but putting that
// stack all-in is not: the prompt has to offer exactly 30 and the engine accept it.
func TestRaise_AgainstAShortStackOffersExactlyTheirStack(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	heroID := m.HeroID()
	engine.WithState(func(state *game.State) {
		e := extra(t, state)
		for i, p := range state.Players {
			*e.Seats[p.ID] = logic.Seat{Chips: 30}
			if p.ID == heroID {
				e.Seats[p.ID].Chips = 1000
				state.CurrentTurn = i
			}
		}
		e.CurrentBet = 0

		e.Phase = logic.PhaseFlop
	})
	m.syncState()
	require.True(t, m.canRaise())

	m.beginRaise()
	assert.Equal(t, uint(30), m.raiseAmount)
	m.stepRaise(+1)
	assert.Equal(t, uint(30), m.raiseAmount, "nothing past what the opponent can call")

	m.confirm()
	require.NoError(t, m.ActionErr, "the engine accepts the amount the view offered")
}

// otherSeat is the one opponent at a heads-up table.
func otherSeat(t *testing.T, state *game.State, heroID string) *game.Player {
	t.Helper()
	for _, p := range state.Players {
		if p.ID != heroID {
			return p
		}
	}
	require.FailNow(t, "no opponent seated")
	return nil
}

// PT-01: the hero raises to 600 and the big blind shoves 925, a raise of 325 under
// the 550 minimum. The betting is not reopened, so the hero may only call or fold; a
// bar that still offered a: all-in offered a move the engine refuses.
func TestActionBar_HidesAllInWhenTheBettingIsNotReopened(t *testing.T) {
	t.Parallel()
	engine, m := tableOnTurn(t, 2)
	heroID := m.HeroID()

	var villainID string
	engine.WithState(func(state *game.State) {
		villain := otherSeat(t, state, heroID)
		villainID = villain.ID
		// The big blind has 50 posted, so 875 behind is a 925 shove.
		extra(t, state).Seats[villainID].Chips = 875
	})
	require.NoError(t, engine.SubmitAction(heroID, logic.ActionRaiseTo{Amount: 600}))
	require.NoError(t, engine.SubmitAction(villainID, logic.ActionAllIn{}))
	m.syncState()

	require.True(t, m.Base.MyTurn)
	engine.WithState(func(state *game.State) {
		require.Error(t, state.Rules.ValidateAction(state, logic.ActionAllIn{}), "the rules refuse the shove")
	})
	bar := tuitest.StripANSI(m.renderActionBar())
	assert.Contains(t, bar, "c: call 325")
	assert.NotContains(t, bar, "a: all-in", "the bar offers only what the engine accepts")
}
