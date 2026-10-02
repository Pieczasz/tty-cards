package views_test

import (
	"image/color"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"
	"github.com/Pieczasz/terminal-card/internal/tui/views"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The footer advertises a set of shortcuts and GlobalRoute resolves them. Nothing
// tied the two together, so a key could be shown to players and silently do nothing.
func TestGlobalActionsAllRoute(t *testing.T) {
	t.Parallel()

	for _, action := range views.GlobalFooter {
		key, _, found := strings.Cut(action, " - ")
		require.True(t, found, "footer entry %q must read '<key> - <label>'", action)
		key = strings.TrimSpace(key)

		t.Run(key, func(t *testing.T) {
			t.Parallel()
			if key == "ctrl+c" {
				// Quit is handled by HandleCommonMsg, not by the route table.
				handled, cmd := views.HandleCommonMsg(tuitest.Key("ctrl+c"), &router.GlobalContext{})
				assert.True(t, handled, "ctrl+c must be handled")
				assert.NotNil(t, cmd, "ctrl+c must produce the quit command")
				return
			}
			route, ok := views.GlobalRoute(key)
			assert.True(t, ok, "footer offers %q but no route resolves it", key)
			assert.NotEmpty(t, route)
		})
	}
}

func TestNavigateOn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		want    router.Route
		handled bool
	}{
		{name: "new lobby", key: "n", want: router.RouteLobbyCreate, handled: true},
		{name: "join lobby", key: "f", want: router.RouteLobbyJoin, handled: true},
		{name: "profile", key: "p", want: router.RouteProfile, handled: true},
		{name: "leaderboard", key: "t", want: router.RouteLeaderboard, handled: true},
		{name: "escape means back", key: "esc", want: router.RouteHome, handled: true},
		{name: "q means back", key: "q", want: router.RouteHome, handled: true},
		{name: "an unbound key is left to the view", key: "z", handled: false},
		{name: "enter is left to the view", key: "enter", handled: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd, ok := views.NavigateOn(tt.key)
			require.Equal(t, tt.handled, ok)
			if !tt.handled {
				assert.Nil(t, cmd, "an unhandled key must not produce a command")
				return
			}

			require.NotNil(t, cmd)
			msg, isChange := cmd().(router.ChangeViewMsg)
			require.True(t, isChange, "a handled key must navigate")
			assert.Equal(t, tt.want, msg.ViewName)
		})
	}
}

// esc and q are deliberately absent from the shared table: the lobby view uses esc
// for its leave confirmation, so only NavigateOn may treat them as "back".
func TestGlobalRouteExcludesBackKeys(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"esc", "q"} {
		_, ok := views.GlobalRoute(key)
		assert.False(t, ok, "%q must not be a global shortcut", key)
	}
}

// A view builds its seat key from the session user. A nil user is the pre-auth
// window every view is constructed in, and returning a zero-value player there
// would seat "0" at the table.
func TestSessionPlayer(t *testing.T) {
	t.Parallel()

	t.Run("no user yet", func(t *testing.T) {
		t.Parallel()
		assert.Nil(t, views.SessionPlayer(router.GlobalContext{}))
		assert.Empty(t, views.SessionPlayerID(router.GlobalContext{}),
			"an empty ID is what a caller can test; a formatted zero is not")
	})

	t.Run("a signed-in user", func(t *testing.T) {
		t.Parallel()
		g := router.GlobalContext{User: &db.User{ID: testutil.UID(7),
			Username: "alice",
			Rankings: []db.Ranking{{Elo: 1600, Game: db.Game{Name: "Poker"}}},
		}}

		p := views.SessionPlayer(g)
		require.NotNil(t, p)
		assert.Equal(t, testutil.UID(7).String(), p.ID)
		assert.Equal(t, testutil.UID(7), p.UserID)
		assert.Equal(t, "alice", p.Name)
		assert.Equal(t, map[string]uint32{"Poker": 1600}, p.Ratings)

		// The ID has to be the same spelling the engine seats the player under, or a
		// subscription keyed on it silently never fires.
		assert.Equal(t, p.ID, views.SessionPlayerID(g))
	})
}

// ListenOn must never block on a stream that is finished or was never started:
// bubbletea runs the command on its own goroutine, and one that never returns is
// a goroutine parked for the life of the session.
func TestListenOn(t *testing.T) {
	t.Parallel()

	open := make(chan int, 1)
	open <- 42

	closed := make(chan int)
	close(closed)

	tests := []struct {
		name string
		ch   <-chan int
		want tea.Msg
	}{
		{name: "a nil channel is a released subscription", ch: nil, want: nil},
		{name: "a closed channel ends the stream", ch: closed, want: nil},
		{name: "a value is handed to the wrapper", ch: open, want: "42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cmd := views.ListenOn(tt.ch, func(v int) tea.Msg { return strconv.Itoa(v) })
			require.NotNil(t, cmd)

			got := make(chan tea.Msg, 1)
			go func() { got <- cmd() }()

			select {
			case msg := <-got:
				assert.Equal(t, tt.want, msg)
			case <-time.After(2 * time.Second):
				t.Fatal("ListenOn blocked; it must return rather than park a goroutine")
			}
		})
	}
}

func TestHandleCommonMsg(t *testing.T) {
	t.Parallel()

	// A resize has to land on the view's own copy of the context, not just the
	// router's: the view renders from its copy and would keep the stale size.
	t.Run("a resize updates the view's context", func(t *testing.T) {
		t.Parallel()
		g := router.GlobalContext{Width: 10, Height: 5}

		handled, cmd := views.HandleCommonMsg(tea.WindowSizeMsg{Width: 120, Height: 50}, &g)

		assert.True(t, handled)
		assert.Nil(t, cmd)
		assert.Equal(t, 120, g.Width)
		assert.Equal(t, 50, g.Height)
	})

	// Same reason: a theme switch mid-session must reach the screen already on
	// display, not only the views built after it.
	t.Run("a background change swaps the theme in place", func(t *testing.T) {
		t.Parallel()
		g := router.GlobalContext{Theme: styles.NewTheme(true)}
		require.True(t, g.Theme.Dark)

		handled, cmd := views.HandleCommonMsg(tea.BackgroundColorMsg{Color: color.White}, &g)

		assert.True(t, handled)
		assert.Nil(t, cmd)
		assert.False(t, g.Theme.Dark, "a light terminal must not keep the dark palette")

		handled, _ = views.HandleCommonMsg(tea.BackgroundColorMsg{Color: color.Black}, &g)
		assert.True(t, handled)
		assert.True(t, g.Theme.Dark)
	})

	t.Run("ctrl+c quits", func(t *testing.T) {
		t.Parallel()
		g := router.GlobalContext{}

		handled, cmd := views.HandleCommonMsg(tuitest.Key("ctrl+c"), &g)

		assert.True(t, handled)
		require.NotNil(t, cmd)
		assert.IsType(t, tea.QuitMsg{}, cmd())
	})

	// Every other key has to fall through, or no view could bind a key of its own.
	t.Run("any other key is left to the view", func(t *testing.T) {
		t.Parallel()
		g := router.GlobalContext{}

		for _, key := range []tea.KeyPressMsg{
			tuitest.Key("g"),
			tuitest.Key("enter"),
			// A bare c, without the ctrl modifier.
			{Code: 'c'},
		} {
			handled, cmd := views.HandleCommonMsg(key, &g)
			assert.False(t, handled, "%s must reach the view", key.String())
			assert.Nil(t, cmd)
		}
	})

	t.Run("an unrelated message is not ours", func(t *testing.T) {
		t.Parallel()
		g := router.GlobalContext{}

		handled, cmd := views.HandleCommonMsg("something else", &g)

		assert.False(t, handled)
		assert.Nil(t, cmd)
	})
}

// ScreenContentHeight is the budget a view pages by and RenderScreen is what draws
// it. They are two derivations of the same number, and the moment they disagree the
// view either overflows the terminal or leaves a gap it could have filled.
func TestRenderScreen_AgreesWithScreenContentHeight(t *testing.T) {
	t.Parallel()

	for _, size := range []struct {
		name string
		w, h int
	}{
		{"the declared minimum", styles.MinWidth, styles.MinHeight},
		{"a stock terminal", 80, 24},
		{"a roomy terminal", 100, 30},
		{"a tall terminal", 120, 50},
	} {
		t.Run(size.name, func(t *testing.T) {
			t.Parallel()
			g := router.GlobalContext{Theme: styles.NewTheme(true), Width: size.w, Height: size.h}
			actions := []string{"g - Filter", "↑/↓ - Page"}

			budget := views.ScreenContentHeight(g, "Leaderboard", actions)
			require.Positive(t, budget, "a supported size must leave room for content")

			var handed int
			out := views.RenderScreen(g, "Leaderboard", actions, func(height int) string {
				handed = height
				return strings.TrimRight(strings.Repeat("x\n", budget), "\n")
			})

			assert.Equal(t, budget, handed,
				"the budget a view pages by must be the one it is handed to draw into")
			assert.LessOrEqual(t, lg.Height(out), size.h, "the frame is taller than the terminal")
			assert.LessOrEqual(t, lg.Width(out), size.w, "the frame is wider than the terminal")
		})
	}
}

// The global footer is one slice shared by every session. Appending the local
// actions onto it would write into the array the other sessions are reading, so
// the footer is built with slices.Concat - this is what notices if that regresses.
func TestRenderScreen_LeavesGlobalActionsAlone(t *testing.T) {
	t.Parallel()

	before := slices.Clone(views.GlobalFooter)
	g := router.GlobalContext{Theme: styles.NewTheme(true), Width: 100, Height: 30}

	views.RenderScreen(g, "Profile", []string{"g - Game", "r - Result"}, func(int) string { return "" })
	views.ScreenContentHeight(g, "Profile", []string{"x - One", "y - Two", "z - Three"})

	assert.Equal(t, before, views.GlobalFooter, "the shared footer list must not be written through")
	assert.Len(t, views.GlobalFooter, len(before))
}

// The local actions come first so a view's own keys read before the global ones.
func TestRenderScreen_ShowsLocalAndGlobalActions(t *testing.T) {
	t.Parallel()

	g := router.GlobalContext{Theme: styles.NewTheme(true), Width: 120, Height: 50}
	out := views.RenderScreen(g, "Profile", []string{"g - Game"}, func(int) string { return "body" })

	assert.Contains(t, out, "g - Game")
	assert.Contains(t, out, "body")
	for _, action := range views.GlobalFooter {
		assert.Contains(t, out, action, "the footer must still advertise %q", action)
	}
}

// RenderCenteredLayout is what places the framed box on the screen. A zero size is
// the window every session opens in, before the first WindowSizeMsg arrives.
func TestRenderCenteredLayout(t *testing.T) {
	t.Parallel()

	t.Run("fills the terminal", func(t *testing.T) {
		t.Parallel()
		g := router.GlobalContext{Theme: styles.NewTheme(true), Width: 100, Height: 30}

		out := views.RenderCenteredLayout(g, "header", "content", "footer")

		assert.Contains(t, out, "content")
		assert.LessOrEqual(t, lg.Height(out), 30)
		assert.LessOrEqual(t, lg.Width(out), 100)
	})

	t.Run("survives a terminal that has not reported its size", func(t *testing.T) {
		t.Parallel()
		g := router.GlobalContext{Theme: styles.NewTheme(true)}

		assert.NotPanics(t, func() {
			views.RenderCenteredLayout(g, "header", "content", "footer")
		})
	})
}
