package gameview

import (
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Pieczasz/tty-cards/internal/tui/styles"

	"github.com/stretchr/testify/assert"
)

func TestFormatTurnClock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		remaining time.Duration
		want      string
	}{
		{name: "no clock running", remaining: 0, want: ""},
		{name: "an already-expired clock shows nothing", remaining: -time.Second, want: ""},
		{
			name: "a part tenth still reads a tenth, so the last moment is visible",
			// Truncating would show 0.0 while the player can still act, which reads as
			// "your time is up" when it is not.
			remaining: 10 * time.Millisecond,
			want:      "0.1",
		},
		{name: "the last seconds count in tenths", remaining: 5500 * time.Millisecond, want: "5.5"},
		{name: "tenths round up", remaining: 5240 * time.Millisecond, want: "5.3"},
		{name: "a whole second in the tenths band keeps its zero", remaining: 3 * time.Second, want: "3.0"},
		{
			name: "the threshold itself is still whole seconds",
			// Six seconds and above is time to think, and a tenth-second tick for a
			// whole thirty-second turn is renders nobody asked for.
			remaining: preciseClockThreshold,
			want:      "0:06",
		},
		{name: "whole seconds are exact", remaining: 30 * time.Second, want: "0:30"},
		{name: "seconds are zero padded", remaining: 9 * time.Second, want: "0:09"},
		{name: "a minute rolls over", remaining: time.Minute, want: "1:00"},
		{name: "past a minute", remaining: 90 * time.Second, want: "1:30"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, formatTurnClock(tt.remaining, true))
		})
	}
}

// The status line names the seat on turn; the countdown lives on that seat instead, so the
// same number is never drawn in two places.
func TestRenderStatus_NamesTheSeatOnTurn(t *testing.T) {
	t.Parallel()

	assert.Contains(t, RenderStatus(testTheme(), "alice", false, 0), "alice")
	assert.Contains(t, RenderStatus(testTheme(), "alice", true, 25*time.Second), "YOUR TURN")
	assert.Contains(t, RenderStatus(testTheme(), "alice", true, 25*time.Second), "0:25",
		"the hero clock shares the status line so the hand height stays put")
}

// The countdown is the only thing telling a player their clock is running, so it has to
// reach the rendered seat rather than merely exist on the model.
func TestRenderTurnClock(t *testing.T) {
	t.Parallel()

	assert.Contains(t, RenderTurnClock(testTheme(), 25*time.Second, true), "0:25")
	assert.Contains(t, RenderTurnClock(testTheme(), 3*time.Second, true), "3.0", "the player on turn counts in tenths")
	assert.Contains(t, RenderTurnClock(testTheme(), 3*time.Second, false), "0:03",
		"everybody watching reads whole seconds, so their session ticks once a second")
	assert.Empty(t, RenderTurnClock(testTheme(), 0, true), "no clock, nothing to draw")
}

// The clock has to sit where the player is already looking: under the seats across the
// table, and beside the ones down the sides, where another row would push the stack apart.
func TestAttachTurnClock(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "seat", AttachTurnClock("seat", "", OrientationTop), "no clock leaves the block alone")

	top := AttachTurnClock("seat", "9.9", OrientationTop)
	assert.Equal(t, []string{"seat", " 9.9"}, strings.Split(top, "\n"), "stacked under the seat, centred")

	left := AttachTurnClock("seat", "9.9", OrientationLeft)
	assert.Equal(t, "seat 9.9", left, "to the right of a left-hand seat")

	right := AttachTurnClock("seat", "9.9", OrientationRight)
	assert.Equal(t, "9.9 seat", right, "and to the left of a right-hand one")
}

// The tick rate follows the reading: a display in whole seconds does not need ten frames a
// second, and one in tenths cannot be driven at one. The tenth-second tick is also the
// most expensive thing a table can do - every client re-rendering ten times a second
// costs the server thousands of allocations a frame - so only the player on turn pays it.
func TestClockTickFor_RateFollowsTheReading(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		remaining time.Duration
		onTurn    bool
		want      time.Duration
	}{
		{name: "the last seconds of your own turn tick in tenths", remaining: 2 * time.Second, onTurn: true, want: tenthTickInterval},
		{name: "whole seconds tick once a second", remaining: 30 * time.Second, onTurn: true, want: time.Second},
		{name: "somebody else's last seconds tick once a second", remaining: 2 * time.Second, want: time.Second},
		// Three seats in four watch a clock that is not theirs; while it has time left it
		// reads in steps of five, so they re-render when the step changes, not each second.
		{name: "somebody else's clock ticks when its step changes", remaining: 30 * time.Second, want: 5 * time.Second},
		{name: "part way through a step", remaining: 28300 * time.Millisecond, want: 3300 * time.Millisecond},
		{name: "the step ends where the last seconds begin", remaining: 12 * time.Second, want: 2 * time.Second},
		{name: "somebody else's last ten seconds tick once a second", remaining: 10 * time.Second, want: time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Inside the bubble the clock only moves when the tick is all that is left
			// to wait on, so the time a tick takes is exactly its interval.
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				msg := clockTickFrom(nil, 0, tt.remaining, tt.onTurn)()
				assert.IsType(t, ClockTickMsg{}, msg)
				assert.Equal(t, tt.want, time.Since(start))
			})
		})
	}
}

func testTheme() *styles.Theme {
	return styles.NewTheme(true)
}

// The clock on somebody else's seat reads in steps of five until its last ten
// seconds, rounded up like every clock here so it never claims less time than is left.
func TestFormatTurnClock_SomebodyElsesClockReadsInSteps(t *testing.T) {
	t.Parallel()
	for remaining, want := range map[time.Duration]string{
		30 * time.Second:         "0:30",
		28300 * time.Millisecond: "0:30",
		25 * time.Second:         "0:25",
		12 * time.Second:         "0:15",
		10 * time.Second:         "0:10",
		7 * time.Second:          "0:07",
		90 * time.Second:         "1:30",
	} {
		assert.Equal(t, want, formatTurnClock(remaining, false), "%v", remaining)
	}
}
