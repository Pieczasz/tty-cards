package game

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// twoStepRules is gin rummy's shape: a turn is a draw that keeps the seat, then a
// discard that passes it on. Its fields are only touched under the engine lock.
type twoStepRules struct {
	*timeoutRules
	drawn bool
}

func (r *twoStepRules) ApplyAction(state *State, action Action) error {
	_ = r.timeoutRules.ApplyAction(state, action)
	r.drawn = action.Name() == "draw"
	if r.drawn {
		cur := state.CurrentTurn
		state.OverrideNextTurn = &cur
	}
	return nil
}

func (r *twoStepRules) TimeoutAction(*State) Action {
	if r.drawn {
		return namedAction{name: "discard"}
	}
	return namedAction{name: "draw"}
}

// stretchOnDrawRules asks for a longer clock once the seat has drawn: a change in the
// turn's length is a new turn, the way hearts' pass phase and hand-over prompt are.
type stretchOnDrawRules struct{ *twoStepRules }

func (r stretchOnDrawRules) TurnTimeout(*State) time.Duration {
	if r.drawn {
		return 2 * time.Hour
	}
	return 0
}

func newTwoStep() *twoStepRules {
	return &twoStepRules{timeoutRules: &timeoutRules{}}
}

func setDeadline(e *Engine, d time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.turnDeadline = d
}

func TestEngine_TurnClock_SameSeatKeepsItsDeadline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		remaining     time.Duration
		action        string
		wantRemaining time.Duration
	}{
		{name: "draw keeps the running deadline", remaining: 20 * time.Minute, action: "draw", wantRemaining: 20 * time.Minute},
		{name: "a nearly spent turn gets the floor", remaining: time.Second, action: "draw", wantRemaining: minTurnRemaining},
		{name: "passing the turn starts a fresh clock", remaining: time.Second, action: "discard", wantRemaining: time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			engine := newTimeoutEngine(t, newTwoStep(), "a", "b")
			setDeadline(engine, time.Now().Add(tt.remaining))

			require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), namedAction{name: tt.action}))

			assert.InDelta(t, tt.wantRemaining.Seconds(), time.Until(engine.TurnDeadline()).Seconds(), 1)
		})
	}
}

func TestEngine_TurnClock_ALongerTurnIsAFreshTurn(t *testing.T) {
	t.Parallel()
	engine := newTimeoutEngine(t, stretchOnDrawRules{newTwoStep()}, "a", "b")
	setDeadline(engine, time.Now().Add(time.Second))

	require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), namedAction{name: "draw"}))

	assert.InDelta(t, (2 * time.Hour).Seconds(), time.Until(engine.TurnDeadline()).Seconds(), 1,
		"the rules asked for a different length, so this is a new turn and gets it whole")
}

func TestEngine_TurnClock_SomeoneElseLeavingKeepsTheClock(t *testing.T) {
	t.Parallel()
	engine := newTimeoutEngine(t, &timeoutRules{safe: namedAction{name: "safe"}}, "a", "b", "c")
	onTurn := engine.CurrentPlayerID()
	deadline := time.Now().Add(20 * time.Minute)
	setDeadline(engine, deadline)

	leaver := "a"
	if onTurn == leaver {
		leaver = "b"
	}
	engine.RemovePlayer(leaver)

	require.Equal(t, onTurn, engine.CurrentPlayerID())
	assert.WithinDuration(t, deadline, engine.TurnDeadline(), time.Millisecond,
		"a leave elsewhere at the table must not hand the seat on turn a fresh clock")
}

// Draw-then-discard is one turn. Counting a miss per expiry charged an absent gin
// player two misses a turn, so they lost the seat after a turn and a half.
func TestEngine_TurnClock_OneMissPerSeatTurn(t *testing.T) {
	t.Parallel()
	engine := newTimeoutEngine(t, newTwoStep(), "a", "b")
	first := engine.CurrentPlayerID()

	// Two full turns each, at two expiries a turn.
	for range 2 * 2 * 2 {
		fireTurnTimeout(t, engine)
	}
	require.False(t, engine.IsFinished(), "two absent turns each is not yet the limit")
	engine.WithState(func(state *State) {
		for _, p := range state.Players {
			assert.Equal(t, 2, engine.missedTurns[p.ID], "one miss per turn for %s", p.ID)
		}
	})

	fireTurnTimeout(t, engine)
	assert.True(t, engine.IsFinished(), "the third absent turn takes the seat")
	assert.Zero(t, engine.MissedTurns(first), "reaped with the seat")
}
