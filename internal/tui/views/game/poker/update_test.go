package poker

import (
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/poker"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	tea "charm.land/bubbletea/v2"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A disconnect never runs the view's esc/enter paths, so Close is the only thing that
// releases the engine subscription.
func TestClose_ReleasesEngineSubscription(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	broadcaster := engine.Broadcaster()

	require.Equal(t, 1, broadcaster.Len(), "the view subscribed on construction")

	// Park a listener on the channel exactly as the Bubble Tea runtime would. The
	// command is built here, on this goroutine, because Close writes m.Events. It is
	// Session.Listen rather than Init: Init also batches the turn-clock tick, and
	// this test is about the subscription alone.
	listen := m.Listen()
	done := make(chan tea.Msg, 1)
	go func() { done <- listen() }()

	m.Close()

	assert.Zero(t, broadcaster.Len(), "Close returns the subscriber slot")
	assert.Nil(t, <-done, "unsubscribing closes the channel so the listener returns")

	m.Close() // idempotent: the session teardown may run after a view already exited
	assert.Zero(t, broadcaster.Len())

	engine.Close()
}

// The router owns teardown, so a view swap must release the outgoing view too.
func TestClose_AfterEngineClosed(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	engine.Close()

	m.Close()
	assert.Zero(t, engine.Broadcaster().Len())
}

// stepRaise works in uint, so decreasing below the step would wrap to a huge number.
func TestStepRaise_ClampsWithinTheLegalBandAndNeverWraps(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)
	m.raising = true

	// Heads-up with DefaultStack=1000, SB=25, BB=50 the band is [100, 1000].
	const wantMin, wantMax = uint(100), uint(1000)

	m.raiseAmount = 0
	m.stepRaise(-1)
	assert.GreaterOrEqual(t, m.raiseAmount, wantMin, "decreasing from zero must not wrap")
	assert.LessOrEqual(t, m.raiseAmount, wantMax)

	for range 50 {
		m.stepRaise(-1)
		require.GreaterOrEqual(t, m.raiseAmount, wantMin, "repeated decrease stays legal")
	}

	for range 200 {
		m.stepRaise(+1)
		require.LessOrEqual(t, m.raiseAmount, wantMax, "repeated increase never exceeds the stack")
	}
	assert.Equal(t, wantMax, m.raiseAmount, "increasing eventually reaches the stack")
}

// Chips are how a raise is built, so a keyed chip must land on top of an already legal
// amount and never push the raise outside the band.
func TestAddChip_StacksOntoTheOpenRaise(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)
	// The button is dealt at random, so the prompt is opened directly rather than
	// through beginRaise, which needs the hero to be on turn.
	m.raising = true
	m.raiseAmount = m.raiseMin
	opening := m.raiseAmount

	m.addChip("3") // 25
	assert.Equal(t, opening+25, m.raiseAmount)

	m.addChip("1") // 100
	assert.Equal(t, opening+125, m.raiseAmount)

	for range 50 {
		m.addChip("1")
	}
	assert.Equal(t, m.raiseMax, m.raiseAmount, "chips stop at the top of the band")
}

func TestAddChip_IsANoOpWhenNotRaising(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)
	m.raising = false
	m.raiseAmount = 0

	m.addChip("1")

	assert.Zero(t, m.raiseAmount, "chips only move inside the raise prompt")
}

// Between hands the table waits on one player to deal.
func TestConfirm_DealsTheNextHandInsteadOfLeaving(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	t.Cleanup(engine.Close)

	// Fold the hand out heads-up; the match still has hands left to play.
	require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), logic.ActionFold{}))
	m.syncState()
	require.True(t, m.handComplete)
	require.False(t, m.matchComplete, "one hand is not the whole match")

	// The button lands on either seat, so both sides of the prompt get asserted.
	heroDeals := m.canDeal()
	_, cmd := m.confirm()
	assert.Nil(t, cmd, "enter between hands never navigates away")
	if !heroDeals {
		assert.True(t, m.handComplete, "the hero cannot deal on another player's button")
		require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), logic.ActionNextHand{}))
		m.syncState()
	}

	assert.False(t, m.handComplete, "the next hand is under way")
	assert.Equal(t, 2, m.handNumber)
}

// A pot nobody contested is won face-down.
func TestSyncState_UncontestedPotKeepsOpponentCardsHidden(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	t.Cleanup(engine.Close)

	require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), logic.ActionFold{}))
	m.syncState()
	require.True(t, m.handComplete)

	for _, s := range m.seats {
		if s.IsHero {
			continue
		}
		assert.Empty(t, s.Hole, "%s never had to show", s.Name)
	}
}

// esc on the between-hands screen leaves the whole match, so the screen has to say so: it
// otherwise looks exactly like the end-of-game screen where esc was free.
func TestHandOverHint_SaysWhatEscCosts(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)
	m.handComplete = true
	m.matchComplete = false

	assert.Contains(t, m.handOverHint(), "forfeiting")

	m.matchComplete = true
	assert.NotContains(t, m.handOverHint(), "forfeiting", "the match is over, esc costs nothing")
}

// The between-hands hint names the hand that is about to be dealt.
func TestHandOverHint_NamesTheHandAboutToBeDealt(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)
	m.handComplete = true
	m.matchComplete = false
	m.handNumber = 1

	m.Base.MyTurn = true
	assert.Contains(t, m.handOverHint(), "deal hand 2", "the hero is on the button")

	m.Base.MyTurn = false
	m.Base.CurrentPlayer = "bob"
	assert.Contains(t, m.handOverHint(), "hand 2", "and bob is when he is")
}

// Stepping while the raise prompt is closed must do nothing at all.
func TestStepRaise_IsANoOpWhenNotRaising(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)
	m.raising = false
	m.raiseAmount = 0

	m.stepRaise(+1)
	m.stepRaise(-1)

	assert.Zero(t, m.raiseAmount, "no adjustment outside the raise prompt")
}

// The engine only broadcasts that a seat was taken for idling; ending the session is the
// view's half of the contract.
func TestIdleRemoved_MatchesOnlyOurOwnSeat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event game.Event
		want  bool
	}{
		{
			name:  "our own seat taken for idling",
			event: game.Event{Type: game.EventPlayerIdle, PlayerID: testutil.SeatID(1)},
			want:  true,
		},
		{
			name:  "another player idled out",
			event: game.Event{Type: game.EventPlayerIdle, PlayerID: testutil.SeatID(2)},
			want:  false,
		},
		{
			name:  "our own expired turn is not a removal",
			event: game.Event{Type: game.EventTurnTimedOut, PlayerID: testutil.SeatID(1)},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			engine, m := startedTable(t)
			t.Cleanup(engine.Close)

			assert.Equal(t, tt.want, m.IdleRemoved(tt.event))
		})
	}
}

// Only the quit path asserts on the returned command.
func TestUpdate_IdleRemovalQuitsTheSession(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	t.Cleanup(engine.Close)

	_, cmd := m.Update(gameview.EventMsg{
		Type: game.EventPlayerIdle, PlayerID: testutil.SeatID(1),
		Source: m.Events,
	})

	require.NotNil(t, cmd)
	_, isQuit := cmd().(tea.QuitMsg)
	assert.True(t, isQuit, "an idled-out player's session must end itself")
}

// The clock has to keep re-arming while the hand is live, and stop once it is not: a tick
// chain that outlives the game re-renders every session forever.
func TestUpdate_ClockTickReschedulesOnlyWhilePlaying(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	t.Cleanup(engine.Close)

	_, cmd := m.Update(gameview.ClockTickMsg{Source: m.Events})
	require.NotNil(t, cmd, "a live hand keeps counting down")
	_, isTick := cmd().(gameview.ClockTickMsg)
	assert.True(t, isTick)

	// Finished on the engine, not on the model: Update re-syncs from the engine before
	// it decides, so a phase written straight onto baseState would just be overwritten.
	engine.RemovePlayer(testutil.SeatID(2))
	require.True(t, engine.IsFinished())

	_, cmd = m.Update(gameview.ClockTickMsg{Source: m.Events})
	assert.Nil(t, cmd, "a finished game must not keep ticking")
}

// A running clock is what the countdown renders from, so it has to survive the trip from
// the engine into the view's own state.
func TestSyncState_CarriesTheTurnCountdown(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	t.Cleanup(engine.Close)

	assert.Positive(t, m.Base.TurnRemaining, "a started hand has a clock running")
	assert.LessOrEqual(t, m.Base.TurnRemaining, game.DefaultTurnTimeout)
}

// heroOnTurn advances the hand until the hero is the one to act. The button is dealt
// at random, so a test that needs the hero on turn cannot assume it.
func heroOnTurn(t *testing.T, engine *game.Engine, m *Model) {
	t.Helper()
	if m.Base.MyTurn {
		return
	}
	// Heads-up preflop the small blind acts first; calling passes the turn to the
	// big blind without ending the hand.
	require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), logic.ActionCall{}))
	m.syncState()
	require.True(t, m.Base.MyTurn, "the hero has to be on turn for this test to mean anything")
}

// The error line is what tells a player their table has gone quiet, so it must stay empty
// on a healthy connection or every session opens on a false alarm.
func TestNew_HealthyTableReportsNoError(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	t.Cleanup(engine.Close)

	require.NoError(t, m.lastActionErr, "a successful subscription is not an error")
	assert.NotNil(t, m.Events, "and the feed is live")
}

// matchComplete is what turns esc from "forfeit" into "leave", and what stops the hero being
// offered actions after the match is over.
func TestSyncState_MatchDoneTracksTheEnginePhase(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	t.Cleanup(engine.Close)

	require.False(t, m.matchComplete, "the match has only just started")

	engine.WithState(func(state *game.State) { state.Phase = game.Finished })
	m.syncState()

	assert.True(t, m.matchComplete)

	// Without readable poker state the engine phase is all there is to go on, and it
	// still has to be believed: this is the path a view lands on if Extra is not there.
	engine.WithState(func(state *game.State) { state.Extra = nil })
	m.syncState()

	assert.True(t, m.matchComplete, "a finished engine is a finished match either way")
}

// The action prompts are the only thing standing between a player and an action the engine
// will reject, so each condition is checked on its own.
func TestCanAllInAndHeroBusted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		myTurn       bool
		handComplete bool
		chips        uint
		wantAllIn    bool
		wantBusted   bool
	}{
		{name: "on turn with chips", myTurn: true, chips: 500, wantAllIn: true},
		{name: "not on turn", myTurn: false, chips: 500},
		{name: "hand already over", myTurn: true, handComplete: true, chips: 500},
		{name: "no chips left", myTurn: true, chips: 0, wantBusted: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			engine, m := startedTable(t)
			t.Cleanup(engine.Close)

			hero := m.heroSeat()
			require.NotNil(t, hero)
			hero.Chips = tt.chips
			m.Base.MyTurn = tt.myTurn
			m.handComplete = tt.handComplete

			assert.Equal(t, tt.wantAllIn, m.canAllIn(), "canAllIn")
			assert.Equal(t, tt.wantBusted, m.heroBusted(), "heroBusted")
		})
	}
}

// A step has to actually move the amount: a prompt that ignores the key looks broken, and
// only a negative direction steps down.
func TestStepRaise_MovesByExactlyOneChip(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)
	m.raising = true
	step := smallestChip()
	// Well clear of both ends of the band so the clamp cannot hide the movement.
	m.raiseAmount = m.raiseMin + 10*step

	opening := m.raiseAmount
	m.stepRaise(-1)
	assert.Equal(t, opening-step, m.raiseAmount, "one chip down")

	m.stepRaise(+1)
	assert.Equal(t, opening, m.raiseAmount, "and back up")

	m.stepRaise(0)
	assert.Equal(t, opening+step, m.raiseAmount, "only a negative direction steps down")
}

// submit is the single path from a keypress to the engine.
func TestSubmit(t *testing.T) {
	t.Parallel()

	t.Run("an accepted action is applied and clears the error line", func(t *testing.T) {
		t.Parallel()
		engine, m := startedTable(t)
		t.Cleanup(engine.Close)
		heroOnTurn(t, engine, m)

		m.lastActionErr = assert.AnError
		m.raising = true

		// Which of the two is free depends on where the button landed.
		action := game.Action(logic.ActionCheck{})
		if m.toCall > 0 {
			action = logic.ActionCall{}
		}
		_, cmd := m.submit(action)

		assert.Nil(t, cmd)
		// Submit returns nil only when the engine applied the move, so a clear
		// error line is also the proof the action reached the engine.
		require.NoError(t, m.lastActionErr, "a successful move clears the previous complaint")
		assert.False(t, m.raising, "and closes the raise prompt")
	})

	t.Run("a rejected action is reported and changes nothing", func(t *testing.T) {
		t.Parallel()
		engine, m := startedTable(t)
		t.Cleanup(engine.Close)
		heroOnTurn(t, engine, m)

		street := m.street
		_, cmd := m.submit(logic.ActionRaiseTo{Amount: 1})

		assert.Nil(t, cmd)
		require.Error(t, m.lastActionErr, "the player has to be told why nothing happened")
		assert.Equal(t, street, m.street, "and the table has not moved")
	})

	t.Run("acting out of turn never reaches the engine", func(t *testing.T) {
		t.Parallel()
		engine, m := startedTable(t)
		t.Cleanup(engine.Close)
		m.Base.MyTurn = false
		m.lastActionErr = nil

		_, cmd := m.submit(logic.ActionFold{})

		assert.Nil(t, cmd)
		require.NoError(t, m.lastActionErr, "there is nothing to report, the key is simply not ours")
		assert.False(t, m.handComplete, "and the hand is untouched")
	})
}

// The turn marker is how a player knows the table is waiting on them.
func TestBuildSeats_TurnMarkerNamesOneLiveSeat(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	t.Cleanup(engine.Close)

	onTurn := make([]string, 0, 1)
	for _, s := range m.seats {
		if s.IsTurn {
			onTurn = append(onTurn, s.PlayerID)
		}
	}
	require.Len(t, onTurn, 1, "one seat, and only one, is on turn")
	assert.Equal(t, engine.CurrentPlayerID(), onTurn[0])

	require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), logic.ActionFold{}))
	m.syncState()
	require.True(t, m.handComplete)

	for _, s := range m.seats {
		assert.Falsef(t, s.IsTurn, "%s cannot be on turn between hands", s.Name)
	}
}

// The countdown belongs to the seat that is on turn: a number under somebody else's cards
// tells the table the wrong player is being waited on.
func TestView_CountdownIsDrawnOnTheSeatOnTurn(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	t.Cleanup(engine.Close)

	rendered := stripANSI(m.View().Content)

	clock := gameview.FormatTurnClock(m.Base.TurnRemaining, m.Base.MyTurn)
	require.NotEmpty(t, clock, "a started hand has a clock running")
	assert.Equal(t, 1, strings.Count(rendered, clock),
		"the countdown is drawn exactly once, on the seat that owes an action")
}

// A finished hand has nobody on turn, so no seat may still be counting down.
func TestView_NoCountdownBetweenHands(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	t.Cleanup(engine.Close)
	require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), logic.ActionFold{}))
	m.syncState()
	require.True(t, m.handComplete)

	for _, s := range m.seats {
		assert.Falsef(t, s.IsTurn, "%s cannot be on turn between hands", s.Name)
	}
}

func TestInit_ArmsBothTheFeedAndTheClock(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	t.Cleanup(engine.Close)

	// Batched, so the one command carries the event listener and the countdown: a
	// view that armed only one of them either stops updating or freezes its clock.
	assert.NotNil(t, m.Init())
}
