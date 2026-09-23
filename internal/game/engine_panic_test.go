package game

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// applyPanicRules panics in ApplyAction, the hook a player's own move reaches.
type applyPanicRules struct{ *timeoutRules }

func (*applyPanicRules) ApplyAction(*State, Action) error { panic("rules bug in apply") }

// standingsPanicRules panics in TimeoutAction and again in Standings: the recovery
// must not ask the corrupted state who won.
type standingsPanicRules struct{ *timeoutRules }

func (*standingsPanicRules) TimeoutAction(*State) Action { panic("rules bug in auto-play") }
func (*standingsPanicRules) Standings(*State) []*Player  { panic("rules bug in standings") }

func endedEvent(t *testing.T, events []Event) Event {
	t.Helper()
	for _, ev := range events {
		if ev.Type == EventGameEnded {
			return ev
		}
	}
	t.Fatal("the end was never announced, so the lobby cannot finalize")
	return Event{}
}

// A panic on the player path unwinds into the ssh session, whose recover leaves the
// table running with half-applied state. It has to end this table as a rules error.
func TestEngine_SubmitAction_RulesPanicEndsTheTable(t *testing.T) {
	t.Parallel()
	engine := newTimeoutEngine(t, &applyPanicRules{&timeoutRules{safe: namedAction{name: "safe"}}}, "a", "b")
	events, err := engine.Subscribe()
	require.NoError(t, err)

	var submitErr error
	require.NotPanics(t, func() {
		submitErr = engine.SubmitAction(engine.CurrentPlayerID(), namedAction{name: "boom"})
	})

	require.Error(t, submitErr, "the view has to be told its move did not land")
	assert.True(t, engine.IsFinished())
	assert.True(t, engine.turnDeadline().IsZero(), "a finished table must not auto-play")
	assert.Equal(t, EndReasonRulesError, endedEvent(t, drainEvents(events)).Reason)
}

func TestEngine_TurnTimeout_PanickingStandingsDoesNotEscape(t *testing.T) {
	t.Parallel()
	engine := newTimeoutEngine(t, &standingsPanicRules{&timeoutRules{safe: namedAction{name: "safe"}}}, "a", "b")
	events, err := engine.Subscribe()
	require.NoError(t, err)

	require.NotPanics(t, func() { fireTurnTimeout(t, engine) })

	assert.True(t, engine.IsFinished())
	assert.Equal(t, EndReasonRulesError, endedEvent(t, drainEvents(events)).Reason)

	var standings []Standing
	require.NotPanics(t, func() { standings = engine.Standings() },
		"finalize reads standings on the lobby's goroutine; a panic there kills it")
	assert.Empty(t, standings, "empty standings are what finalize drops as unrecordable")
}
