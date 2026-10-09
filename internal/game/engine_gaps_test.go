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

func (*interruptingRules) OnPlayerLeave(s *State, _ string) { s.Interrupted = true }
func (*interruptingRules) AfterPlayerRemoved(*State, int)   {}
func (*interruptingRules) CheckWinCondition(s *State) bool  { return s.Interrupted }

// Interrupted is what keeps a leave that ended the match from being recorded as the
// remaining seats' win: finalize charges only the leavers.
func TestEngine_RemovePlayer_InterruptedLeaveIsNotAWin(t *testing.T) {
	t.Parallel()
	engine := newTimeoutEngine(t, &interruptingRules{&timeoutRules{}}, "a", "b", "c")
	events, err := engine.Subscribe()
	require.NoError(t, err)

	engine.RemovePlayer("a")

	assert.True(t, engine.IsFinished())
	assert.Equal(t, EndReasonInterrupted, endedEvent(t, drainEvents(events)).Reason)
}
