package gameview

import (
	"slices"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testContext(width, height int) router.GlobalContext {
	return router.GlobalContext{Theme: styles.NewTheme(true), Width: width, Height: height}
}

// The bands are laid out against the terminal, so whatever a game puts in them the
// frame stays inside it - a band that overran used to be handed to the terminal to
// wrap, and one wrapped row shifts every row under it.
func TestRenderBands_StaysInsideTheTerminal(t *testing.T) {
	t.Parallel()

	tall := strings.Repeat("x\n", 60)
	wide := strings.Repeat("y", 300)

	for _, tc := range []struct{ name, top, player, mid string }{
		{name: "an oversized top band", top: tall + wide},
		{name: "an oversized hand", player: tall + wide},
		{name: "an oversized table", mid: tall + wide},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := testContext(80, 24)
			out := RenderBands(g, tc.top, tc.player, "hints", func(int) string { return tc.mid })

			assert.LessOrEqual(t, lg.Width(out), 80)
			assert.LessOrEqual(t, lg.Height(out), 24)
		})
	}
}

// The hero's hand and the keys they act with are at the bottom, so that is the end
// that survives a frame which cannot fit.
func TestRenderBands_KeepsTheHeroBand(t *testing.T) {
	t.Parallel()

	g := testContext(80, 24)
	out := RenderBands(g, strings.Repeat("top\n", 40), "MY HAND", "hints", func(int) string { return "" })

	assert.Contains(t, out, "MY HAND")
}

// The middle band gets the rows the two fixed bands did not take, and no more: given
// its own overflow to absorb it would otherwise be trimmed out of the frame's total,
// which eats the opponents along the top instead.
func TestRenderTableRow_GivesTheCentreTheRoomTheSidesDoNotNeed(t *testing.T) {
	t.Parallel()

	row := RenderTableRow(64, 3, "left seat", "a rather long centre line", "right seat")
	assert.Equal(t, 64, lg.Width(row))
	assert.Contains(t, row, "a rather long centre line", "the centre is not squeezed by a fixed third")
}

func TestRenderTableRow_CapsTheSidesAtAThird(t *testing.T) {
	t.Parallel()

	row := RenderTableRow(64, 3, strings.Repeat("L", 200), "centre", strings.Repeat("R", 200))
	assert.Equal(t, 64, lg.Width(row))
}

// The tick chain is driven by the view: a tick while the table is still waiting
// returns no command, so a view built before the game started had a dead clock for
// the whole match unless the event that starts the game re-arms it.
func TestHandleFrame_ReArmsTheClockWhenTheTableStartsPlaying(t *testing.T) {
	t.Parallel()

	s := &Session{Base: BaseState{Phase: game.Waiting}}
	events := make(chan game.Event, 1)
	events <- game.Event{Type: game.EventGameStarted}
	s.Events = events

	// The sync a real view would run: the engine has started by the time the event
	// lands, so the cached phase moves to Playing.
	sync := func() { s.Base.Phase = game.Playing }

	cmd, handled := s.HandleFrame(EventMsg{Type: game.EventGameStarted, Source: events}, sync, nil)
	require.True(t, handled)
	require.NotNil(t, cmd, "the event has to arm both the listener and the clock")

	assert.True(t, hasClockTick(cmd), "the countdown has to be re-armed")
}

// While the table is playing the tick chain is already running, so an event must not
// start a second one - two chains means twice the renders for every client.
func TestHandleFrame_DoesNotStartASecondClock(t *testing.T) {
	t.Parallel()

	s := &Session{Base: BaseState{Phase: game.Playing}}
	cmd, handled := s.HandleFrame(EventMsg{Type: game.EventTurnAdvanced}, func() {}, nil)
	require.True(t, handled)
	assert.False(t, hasClockTick(cmd), "the running chain is not doubled")
}

// A phase that is not Playing ends the chain, which is what stops a finished table
// re-rendering once a second forever.
func TestHandleFrame_ClockStopsOffThePlayingPhase(t *testing.T) {
	t.Parallel()

	s := &Session{Base: BaseState{Phase: game.Finished}}
	cmd, handled := s.HandleFrame(ClockTickMsg{}, func() {}, nil)
	require.True(t, handled)
	assert.Nil(t, cmd)
}

// onEvent is what lets poker clear its rejection message when the table moves on,
// without the clock tick wiping it inside a second.
func TestHandleFrame_OnEventRunsOnlyForEvents(t *testing.T) {
	t.Parallel()

	s := &Session{Base: BaseState{Phase: game.Playing}}
	calls := 0
	onEvent := func() { calls++ }

	s.HandleFrame(ClockTickMsg{}, func() {}, onEvent)
	assert.Zero(t, calls, "a clock tick is not a table change")

	s.HandleFrame(EventMsg{Type: game.EventTurnAdvanced}, func() {}, onEvent)
	assert.Equal(t, 1, calls)
}

// A new event type - EventPlayerLeft, say - has to be just another resync, not a
// message the shared handler drops on the floor.
func TestHandleFrame_UnknownEventTypesStillResync(t *testing.T) {
	t.Parallel()

	s := &Session{Base: BaseState{Phase: game.Playing}}
	synced := false
	cmd, handled := s.HandleFrame(
		EventMsg{Type: game.EventPlayerLeft, Reason: game.EndReasonAbandoned},
		func() { synced = true }, nil)

	require.True(t, handled)
	assert.True(t, synced)
	assert.NotNil(t, cmd, "the listener has to stay armed")
}

// hasClockTick reports whether cmd eventually produces a ClockTickMsg. A batch is
// opaque, so it is run and its children are resolved.
func hasClockTick(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case ClockTickMsg:
		return true
	case tea.BatchMsg:
		return slices.ContainsFunc(msg, hasClockTick)
	}
	return false
}
