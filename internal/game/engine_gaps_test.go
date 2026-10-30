package game

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Pieczasz/tty-cards/internal/deck"
)

func TestEngine_Frame_ReportsTheRunningClock(t *testing.T) {
	t.Parallel()
	engine := newTimeoutEngine(t, &timeoutRules{safe: namedAction{name: "safe"}}, "a", "b")

	_, _, remaining := engine.Frame("a", nil)

	assert.Positive(t, remaining, "an armed turn has time left to show")
	assert.LessOrEqual(t, remaining, time.Hour)
}

func TestEngine_Snapshot_ShowsTheCardInPlay(t *testing.T) {
	t.Parallel()
	engine := newTimeoutEngine(t, &timeoutRules{}, "a", "b")
	top := deck.Card{Rank: deck.Queen, Suit: deck.Hearts}
	engine.WithState(func(s *State) {
		s.Discard = deck.New([]deck.Card{{Rank: deck.Two, Suit: deck.Clubs}, top})
	})

	assert.Equal(t, top, engine.Snapshot().TopDiscard)
}

type applyFailsRules struct{ *timeoutRules }

func (*applyFailsRules) ApplyAction(*State, Action) error { return errors.New("rules bug") }

func TestEngine_TurnTimeout_AutoPlayRulesErrorEndsTheTable(t *testing.T) {
	t.Parallel()
	engine := newTimeoutEngine(t, &applyFailsRules{&timeoutRules{safe: namedAction{name: "safe"}}}, "a", "b")
	events, err := engine.Subscribe()
	require.NoError(t, err)

	fireTurnTimeout(t, engine)

	assert.True(t, engine.IsFinished())
	assert.Equal(t, EndReasonRulesError, endedEvent(t, drainEvents(events)).Reason)
}

func TestEngine_TurnTimeout_CursorOffTheTableIsIgnored(t *testing.T) {
	t.Parallel()
	rules := &timeoutRules{safe: namedAction{name: "safe"}}
	engine := newTimeoutEngine(t, rules, "a", "b")
	engine.WithState(func(s *State) { s.CurrentTurn = len(s.Players) })

	fireTurnTimeout(t, engine)

	assert.Empty(t, rules.appliedActions(), "nobody is on turn, so nobody is auto-played")
	assert.False(t, engine.IsFinished())
}

// interruptingRules ends the match for everyone when any seat leaves, as hearts does.
type interruptingRules struct{ *timeoutRules }

func (*interruptingRules) OnPlayerLeave(*State, string) {}
func (*interruptingRules) AfterPlayerRemoved(*State, int) LeaveOutcome {
	return LeaveInterruptsMatch
}

// LeaveInterruptsMatch is what keeps a leave that ended the match from being recorded
// as the remaining seats' win: finalize charges only the leavers.
func TestEngine_RemovePlayer_InterruptedLeaveIsNotAWin(t *testing.T) {
	t.Parallel()
	engine := newTimeoutEngine(t, &interruptingRules{&timeoutRules{}}, "a", "b", "c")
	events, err := engine.Subscribe()
	require.NoError(t, err)

	engine.RemovePlayer("a")

	assert.True(t, engine.IsFinished())
	assert.Equal(t, EndReasonInterrupted, endedEvent(t, drainEvents(events)).Reason)
}

// sharedFirstRules ends the match on any move with the first two seats sharing first.
type sharedFirstRules struct{ *timeoutRules }

func (*sharedFirstRules) CheckWinCondition(*State) bool { return true }
func (*sharedFirstRules) Standings(s *State) []Standing {
	out := inOrder(s.Players)
	out[1].Place = 1
	return out
}

// PT-07: two seats sharing first place is a draw, and the database records it as one.
// The engine used to name the first of them as the match's sole winner.
func TestEngine_SharedFirstPlaceNamesNoSingleWinner(t *testing.T) {
	t.Parallel()
	engine := newTimeoutEngine(t, &sharedFirstRules{&timeoutRules{}}, "a", "b", "c")

	require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), namedAction{name: "end"}))

	snap := engine.Snapshot()
	require.Equal(t, Finished, snap.Phase)
	assert.Equal(t, EndReasonWin, snap.EndReason)
	assert.Empty(t, snap.WinnerName, "a shared first place has no single winner")
}
