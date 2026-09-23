package game

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Pieczasz/terminal-card/internal/tui/styles"

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
			assert.Equal(t, tt.want, FormatTurnClock(tt.remaining, true))
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
// second, and one in tenths cannot be driven at one.
func TestClockTickFor_RateFollowsTheReading(t *testing.T) {
	t.Parallel()

	// The command is a timer, so what it returns cannot be inspected without waiting
	// it out. A tenths-rate tick lands well inside a second; a whole-second one does
	// not land at all in that window.
	fast := make(chan tea.Msg, 1)
	go func() { fast <- clockTickFrom(nil, 2*time.Second, true)() }()
	select {
	case <-fast:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("a countdown in tenths must tick faster than twice a second")
	}

	slow := make(chan tea.Msg, 1)
	go func() { slow <- clockTickFrom(nil, 30*time.Second, true)() }()
	select {
	case <-slow:
		t.Fatal("a countdown in whole seconds must not tick ten times a second")
	case <-time.After(300 * time.Millisecond):
	}
}

func testTheme() styles.Theme {
	return styles.NewTheme(true)
}

// The tenth-second tick is the most expensive thing a table can do: every client
// re-rendering ten times a second costs the server thousands of allocations a frame.
func TestClockTickFor_OnlyThePlayerOnTurnTicksInTenths(t *testing.T) {
	t.Parallel()

	watching := make(chan tea.Msg, 1)
	go func() { watching <- clockTickFrom(nil, 2*time.Second, false)() }()

	select {
	case <-watching:
		t.Fatal("a session watching somebody else's clock must not tick ten times a second")
	case <-time.After(300 * time.Millisecond):
	}
}
