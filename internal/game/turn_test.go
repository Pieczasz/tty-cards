package game

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/broadcaster"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestState_SetTurn(t *testing.T) {
	t.Parallel()
	s := &State{}
	s.SetTurn(2)
	require.NotNil(t, s.OverrideNextTurn)
	assert.Equal(t, 2, s.CurrentTurn)
	assert.Equal(t, 2, *s.OverrideNextTurn)

	s.OverrideTurn(0)
	assert.Equal(t, 2, s.CurrentTurn, "OverrideTurn leaves the cursor alone")
	assert.Equal(t, 0, *s.OverrideNextTurn)
}

func TestSeatAt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		i, n int
		want int
	}{
		{name: "inside the table", i: 2, n: 4, want: 2},
		{name: "past the last seat wraps", i: 5, n: 4, want: 1},
		{name: "a negative step wraps backwards", i: -1, n: 4, want: 3},
		{name: "far negative still lands on a seat", i: -9, n: 4, want: 3},
		{name: "an empty table is seat zero", i: 3, n: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, SeatAt(tt.i, tt.n))
		})
	}
}

func TestNextSeat(t *testing.T) {
	t.Parallel()
	all := func(int) bool { return true }
	assert.Equal(t, 3, NextSeat(2, 4, all), "the next seat clockwise")
	assert.Equal(t, 0, NextSeat(3, 4, all), "wraps past the last seat")
	assert.Equal(t, 2, NextSeat(2, 4, func(s int) bool { return s == 2 }), "from itself last")
	assert.Equal(t, 1, NextSeat(2, 4, func(s int) bool { return s == 1 }), "skips refused seats")
	assert.Equal(t, -1, NextSeat(2, 4, func(int) bool { return false }), "none accepted")
}

func TestValidateNextHand(t *testing.T) {
	t.Parallel()
	require.ErrorIs(t, ValidateNextHand(false, false), errHandInPlay)
	require.ErrorIs(t, ValidateNextHand(true, true), errMatchOver)
	require.NoError(t, ValidateNextHand(true, false))
}

func TestEndReason_String(t *testing.T) {
	t.Parallel()
	for reason, want := range map[EndReason]string{
		EndReasonUnknown: "unknown", EndReasonWin: "win", EndReasonRulesError: "rules_error",
		EndReasonForfeit: "forfeit", EndReasonAbandoned: "abandoned", EndReasonInterrupted: "interrupted",
		EndReason(250): "unknown",
	} {
		assert.Equal(t, want, reason.String())
	}
}

func TestEventType_String(t *testing.T) {
	t.Parallel()
	for typ, want := range map[EventType]string{
		EventUnknown: "unknown", EventTurnAdvanced: "turn_advanced", EventActionApplied: "action_applied",
		EventGameEnded: "game_ended", EventGameStarted: "game_started", EventTurnTimedOut: "turn_timed_out",
		EventPlayerIdle: "player_idle", EventPlayerLeft: "player_left", EventType(250): "unknown",
	} {
		assert.Equal(t, want, typ.String())
	}
}

func TestEngine_SubscribeKeepsTheSentinel(t *testing.T) {
	t.Parallel()
	e := NewEngine(bindRules{}, []*Player{{ID: "p1"}}, nil)
	e.Close()
	_, err := e.Subscribe()
	require.ErrorIs(t, err, broadcaster.ErrClosed)
	assert.Zero(t, e.Dropped())
}
