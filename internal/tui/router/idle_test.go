package router

import (
	"context"
	"image/color"
	"reflect"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingModel remembers the last message it was handed, so a test can tell
// "the router swallowed this" from "the router handled it and passed it on".
type recordingModel struct{ last *tea.Msg }

func (m recordingModel) Init() tea.Cmd { return nil }
func (m recordingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	*m.last = msg
	return m, nil
}
func (m recordingModel) View() tea.View { return tea.NewView("recording") }

// isQuit identifies tea.Quit without running the command: the alternative branch
// returns the ten-second tick, and calling that would stall the test.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	return reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(tea.Quit).Pointer()
}

// The TUI derives every game route from the module slug, and internal/game knows
// nothing about routes, so this prefix is the whole contract between the two.
func TestGameRoute(t *testing.T) {
	t.Parallel()

	assert.Equal(t, Route("game_poker"), GameRoute("poker"))
	assert.Equal(t, Route(routeGamePrefix), GameRoute(""))
	assert.Greater(t, len(GameRoute("uno")), len(routeGamePrefix), "a slug contributes to its route")
}

// Goto on an unknown route is a silent no-op, so callers that cannot tolerate one ask
// first. A HasRoute that lied would send a player to a blank screen.
func TestHasRoute(t *testing.T) {
	t.Parallel()

	r := New(GlobalContext{})
	assert.False(t, r.HasRoute(RouteHome), "nothing is registered yet")

	r.Register(RouteHome, func(GlobalContext, any) tea.Model { return MockModel{} })
	assert.True(t, r.HasRoute(RouteHome))
	assert.False(t, r.HasRoute(GameRoute("poker")), "an unregistered game is not routable")
}

// Views run repository calls against this context. A nil one would panic rather than
// simply not being cancellable, and the zero GlobalContext is what every test and the
// first frame of a session hold.
func TestGlobalContext_RequestContext(t *testing.T) {
	t.Parallel()

	assert.Equal(t, context.Background(), GlobalContext{}.RequestContext(),
		"an unset session context falls back to Background rather than nil")

	ctx := t.Context()
	assert.Equal(t, ctx, GlobalContext{SessionCtx: ctx}.RequestContext(),
		"the session context is what carries the disconnect")
}

// A reconnecting player is routed straight back into their lobby instead of home, and
// the view still has to be built exactly once - twice is two listener goroutines
// racing for one subscription channel.
func TestRouter_SetInitialRouteIsWhatInitBuilds(t *testing.T) {
	t.Parallel()

	var home, lobby int
	r := New(GlobalContext{})
	r.Register(RouteHome, func(GlobalContext, any) tea.Model { return countingModel{inits: &home} })
	r.Register(RouteLobby, func(_ GlobalContext, ctx any) tea.Model {
		assert.Equal(t, "ABCD", ctx, "the context handed to SetInitialRoute reaches the factory")
		return countingModel{inits: &lobby}
	})

	r.SetInitialRoute(RouteLobby, "ABCD")
	r.Init()

	assert.Equal(t, RouteLobby, r.activeKey, "the session opens where it was told to")
	assert.Equal(t, 1, lobby, "the first view is armed exactly once")
	assert.Zero(t, home, "the default route is not built on the way past")
}

// An initial route nobody registered must not leave the session on a blank screen
// silently - it leaves no active view, which View reports.
func TestRouter_InitWithAnUnknownInitialRoute(t *testing.T) {
	t.Parallel()

	r := New(GlobalContext{Width: 100, Height: 40})
	r.SetInitialRoute("nowhere", nil)

	assert.NotNil(t, r.Init(), "the tick and the background query are armed regardless")
	assert.Nil(t, r.active)
	assert.Contains(t, r.View().Content, "No active view")
}

// A session idling at a menu is a parked SSH connection and a subscriber slot, so it
// is dropped - but a player watching a hand is not idle just because it is not their
// turn, and disconnecting them mid-game forfeits a match they were still in.
func TestRouter_IdleQuit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		route    Route
		idleFor  time.Duration
		keyPress bool
		// exempt, when set, is the active view's IdleExempt answer.
		exempt   *bool
		wantQuit bool
	}{
		{name: "idle at a menu is dropped", route: RouteHome, idleFor: 6 * time.Minute, wantQuit: true},
		{name: "recently active at a menu stays", route: RouteHome, idleFor: time.Minute},
		{name: "just inside the threshold stays", route: RouteHome, idleFor: 5*time.Minute - 30*time.Second},
		// The player is watching other seats act; the engine's own turn clock is what
		// removes someone who has genuinely stopped playing.
		{name: "idle at a live table is not dropped", route: GameRoute("poker"), idleFor: time.Hour, exempt: new(true)},
		// A game-over screen is a menu: nothing is left to forfeit, and it used to hold
		// the connection and a subscriber slot forever because its route is a game's.
		{name: "idle at a finished table is dropped", route: GameRoute("poker"), idleFor: 6 * time.Minute,
			exempt: new(false), wantQuit: true},
		{name: "a game route alone exempts nothing", route: GameRoute("poker"), idleFor: 6 * time.Minute, wantQuit: true},
		{name: "a key press resets the clock", route: RouteHome, idleFor: time.Hour, keyPress: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := New(GlobalContext{})
			r.Register(tt.route, func(GlobalContext, any) tea.Model {
				if tt.exempt != nil {
					return exemptModel{exempt: *tt.exempt}
				}
				return MockModel{}
			})
			r.Goto(tt.route, nil)

			r.lastActivity = time.Now().Add(-tt.idleFor)
			if tt.keyPress {
				r.Update(tuitest.Key("a"))
			}

			_, cmd := r.Update(tickMsg(time.Now()))
			assert.Equal(t, tt.wantQuit, isQuit(cmd))
			if !tt.wantQuit {
				assert.NotNil(t, cmd, "a session that stays has to re-arm the tick or it never checks again")
			}
		})
	}
}

type exemptModel struct {
	MockModel
	exempt bool
}

func (m exemptModel) IdleExempt() bool { return m.exempt }

// A mouse click is activity too - a player navigating with the mouse alone would
// otherwise be dropped mid-menu.
func TestRouter_MouseCountsAsActivity(t *testing.T) {
	t.Parallel()

	r := New(GlobalContext{})
	r.lastActivity = time.Now().Add(-time.Hour)
	r.Update(tea.MouseClickMsg{})

	_, cmd := r.Update(tickMsg(time.Now()))
	assert.False(t, isQuit(cmd), "the mouse is input like any other")
}

// tick has to keep re-arming itself: the idle check only runs when one lands, so a
// tick that stopped coming would leave every idle session parked forever.
func TestTick_KeepsTheIdleCheckComing(t *testing.T) {
	t.Parallel()

	require.NotNil(t, tick(), "Init arms the first one")

	// The re-arm is the part that matters: a tick the router handles has to leave
	// another one in flight. (The command itself is not run - it sleeps ten seconds.)
	r := New(GlobalContext{})
	_, cmd := r.Update(tickMsg(time.Now()))
	assert.NotNil(t, cmd, "handling a tick arms the next one")
	assert.False(t, isQuit(cmd))
}

// The router keeps the size and the palette for the views it builds next, but views
// hold their own copy of GlobalContext, so the message has to reach the active one
// too - a view that never sees the resize renders at the old width forever.
func TestRouter_TerminalMessagesUpdateGlobalAndFallThrough(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		msg    tea.Msg
		verify func(*testing.T, *Router)
	}{
		{
			name: "window size",
			msg:  tea.WindowSizeMsg{Width: 111, Height: 42},
			verify: func(t *testing.T, r *Router) {
				t.Helper()
				assert.Equal(t, 111, r.Global.Width)
				assert.Equal(t, 42, r.Global.Height)
			},
		},
		{
			name: "a light terminal re-themes the session",
			msg:  tea.BackgroundColorMsg{Color: color.White},
			verify: func(t *testing.T, r *Router) {
				t.Helper()
				assert.False(t, r.Global.Theme.Dark, "a white background is a light palette")
			},
		},
		{
			name: "and a dark one themes it back",
			msg:  tea.BackgroundColorMsg{Color: color.Black},
			verify: func(t *testing.T, r *Router) {
				t.Helper()
				assert.True(t, r.Global.Theme.Dark)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var last tea.Msg
			r := New(GlobalContext{})
			r.Register("mock", func(GlobalContext, any) tea.Model { return recordingModel{last: &last} })
			r.Goto("mock", nil)

			r.Update(tt.msg)

			tt.verify(t, r)
			assert.Equal(t, tt.msg, last, "the active view has its own copy to update")
		})
	}
}

// The session starts dark because most terminals are, and one that never answers the
// background query still has to be legible.
func TestRouter_DefaultsToTheDarkPalette(t *testing.T) {
	t.Parallel()
	assert.True(t, New(GlobalContext{}).Global.Theme.Dark)
}

// Navigating is what the router is for, and an unknown destination must not tear down
// the screen the player is still looking at.
func TestRouter_ChangeViewMsgRoutes(t *testing.T) {
	t.Parallel()

	var last tea.Msg
	r := New(GlobalContext{})
	r.Register("first", func(GlobalContext, any) tea.Model { return recordingModel{last: &last} })
	r.Register("second", func(GlobalContext, any) tea.Model { return MockModel{} })
	r.Goto("first", nil)

	r.Update(ChangeViewMsg{ViewName: "second", Context: "ctx"})
	assert.Equal(t, Route("second"), r.activeKey)

	r.Update(ChangeViewMsg{ViewName: "nope"})
	assert.Equal(t, Route("second"), r.activeKey, "an unknown route leaves the player where they were")

	// The navigation message is consumed rather than handed on; the view that receives
	// it has already been replaced.
	assert.Nil(t, last, "the outgoing view is not asked to handle its own replacement")
}

// Every frame the session emits has to claim the alt screen, or the terminal keeps the
// player's scrollback and paints the game over it.
func TestRouter_View(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		w, h     int
		register bool
		contains string
	}{
		{name: "an undersized terminal gets the resize prompt", w: 40, h: 12, register: true, contains: "Terminal too small"},
		{name: "the prompt wins even with a view mounted", w: 20, h: 5, register: true, contains: "need"},
		{name: "a mounted view renders itself", w: 100, h: 40, register: true, contains: "mock"},
		{name: "nothing mounted says so rather than rendering blank", w: 100, h: 40, contains: "No active view"},
		// Zero is "the first WindowSizeMsg has not arrived", which is not too small.
		{name: "an unknown size is not the resize prompt", w: 0, h: 0, register: true, contains: "mock"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := New(GlobalContext{Width: tt.w, Height: tt.h})
			if tt.register {
				r.Register("mock", func(GlobalContext, any) tea.Model { return MockModel{} })
				r.Goto("mock", nil)
			}

			v := r.View()
			assert.True(t, v.AltScreen, "the session owns the whole screen")
			assert.Contains(t, v.Content, tt.contains)
		})
	}
}

// The prompt is rendered in the session's own palette, not a global one: two players
// can have opposite terminal backgrounds.
func TestRouter_ViewUsesTheSessionTheme(t *testing.T) {
	t.Parallel()

	r := New(GlobalContext{Width: 40, Height: 12})
	r.Update(tea.BackgroundColorMsg{Color: color.White})

	assert.Equal(t, styles.NewTheme(false).RenderTooSmall(40, 12), r.View().Content)
}

// commandingModel is a view that has work to do on arrival and on every message: a
// game view subscribing to its engine feed is exactly this shape.
type commandingModel struct{ cmd tea.Cmd }

func (m commandingModel) Init() tea.Cmd                       { return m.cmd }
func (m commandingModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, m.cmd }
func (m commandingModel) View() tea.View                      { return tea.NewView("commanding") }

// A view's own commands have to reach the runtime, batched alongside the router's.
// Dropping them is what leaves a game view mounted but never subscribed.
func TestRouter_CarriesTheViewsOwnCommands(t *testing.T) {
	t.Parallel()

	type ranMsg struct{}
	ran := func() tea.Msg { return ranMsg{} }

	r := New(GlobalContext{})
	r.Register(RouteHome, func(GlobalContext, any) tea.Model { return commandingModel{cmd: ran} })

	initCmd := r.Init()
	require.NotNil(t, initCmd, "the view's Init command is batched with the router's own")
	assert.IsType(t, tea.BatchMsg{}, initCmd(), "the tick, the background query and the view's command")

	_, cmd := r.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	require.NotNil(t, cmd, "a resize the view answers must not be swallowed")
	assert.Equal(t, ranMsg{}, cmd(), "one command needs no batch around it")
}
