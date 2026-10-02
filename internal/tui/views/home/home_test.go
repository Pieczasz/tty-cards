package home

import (
	"strings"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHome_Update_Navigation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
		want router.Route
	}{
		{name: "new game", key: "n", want: router.RouteLobbyCreate},
		{name: "join game", key: "f", want: router.RouteLobbyJoin},
		{name: "profile", key: "p", want: router.RouteProfile},
		{name: "leaderboard", key: "t", want: router.RouteLeaderboard},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Built per subtest: sharing one model across parallel subtests is safe
			// only while home.model keeps value receivers, and every other view in
			// this tree has since moved to pointer receivers.
			m := New(router.GlobalContext{})

			_, cmd := m.Update(tuitest.Key(tt.key))

			require.NotNil(t, cmd, "%q must navigate", tt.key)
			msg, ok := cmd().(router.ChangeViewMsg)
			require.True(t, ok, "%q must produce a ChangeViewMsg", tt.key)
			assert.Equal(t, tt.want, msg.ViewName)
		})
	}
}

// q quits outright rather than navigating; it is the only bound key on this screen
// that does not produce a ChangeViewMsg.
func TestHome_Update_QuitsOnQ(t *testing.T) {
	t.Parallel()
	m := New(router.GlobalContext{})

	_, cmd := m.Update(tuitest.Key("q"))

	require.NotNil(t, cmd)
	assert.IsType(t, tea.QuitMsg{}, cmd(), "q quits rather than navigating")
}

func TestHome_Update_IgnoresWhatItDoesNotBind(t *testing.T) {
	t.Parallel()

	for name, msg := range map[string]tea.Msg{
		"an unbound key":    tuitest.Key("z"),
		"a non-key message": tea.FocusMsg{},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, cmd := New(router.GlobalContext{}).Update(msg)
			assert.Nil(t, cmd)
		})
	}
}

// Home holds nothing that needs arming - no subscription, no query - so Init has
// nothing to return. A command here would be one the router runs on every visit.
func TestHome_Init(t *testing.T) {
	t.Parallel()
	assert.Nil(t, New(router.GlobalContext{}).Init())
}

// Home is a full-screen view, so it has to fit the screen at every size the app
// claims to support: a frame taller than the terminal is handed to the terminal to
// wrap, and one wrapped row shifts every row under it.
func TestHome_View_FitsTheTerminal(t *testing.T) {
	t.Parallel()

	for _, size := range tuitest.FitSizes {
		t.Run(size.Name, func(t *testing.T) {
			t.Parallel()
			m := New(router.GlobalContext{
				User:  &db.User{ID: testutil.UID(1), Username: "alice"},
				Theme: styles.NewTheme(true), Width: size.Width, Height: size.Height,
			})

			out := m.View().Content

			assert.LessOrEqual(t, lg.Height(out), size.Height, "taller than the terminal")
			assert.LessOrEqual(t, lg.Width(out), size.Width, "wider than the terminal")
		})
	}
}

// blockAt is the first screen row, at or after from, where banner's rows appear on
// consecutive lines, or -1. A banner's short rows (a "/____/" descender) recur in
// other banners, so a row is only found as part of its whole block.
func blockAt(screen, banner []string, from int) int {
	for i := max(from, 0); i+len(banner) <= len(screen); i++ {
		match := true
		for k, row := range banner {
			if !strings.Contains(screen[i+k], row) {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// bannerRows are a banner's non-blank rows, trimmed, the way they appear on screen.
func bannerRows(banner string) []string {
	var rows []string
	for row := range strings.Lines(banner) {
		if row = strings.TrimRight(row, " \n"); strings.TrimSpace(row) != "" {
			rows = append(rows, row)
		}
	}
	return rows
}

func homeScreen(t *testing.T, user *db.User, width, height int) []string {
	t.Helper()
	m := New(router.GlobalContext{User: user, Theme: styles.NewTheme(true), Width: width, Height: height})
	return strings.Split(tuitest.StripANSI(m.View().Content), "\n")
}

// The title, the greeting and the player's name read as one group: the name is as
// big as the word above it, with a line of air between them, and the group sits
// together rather than the title pinned to the top and the rest floating below.
func TestHome_View_GreetsThePlayerByNameInBigLetters(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		user *db.User
		want string
	}{
		{name: "a signed-in player", user: &db.User{Username: "alice"}, want: "alice"},
		// The view is built before auth has resolved, so a nil user has to render
		// something rather than dereference nothing.
		{name: "no user yet", user: nil, want: "Player"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			screen := homeScreen(t, tt.user, 120, 40)

			title := bannerRows(styles.RenderFigureASCII("tty cards", 200, 20))
			welcome := bannerRows(styles.RenderFigureASCII("Welcome", 200, 20))
			name := bannerRows(styles.RenderFigureUncached(tt.want, 200, 20))
			titleTop := blockAt(screen, title, 0)
			require.NotEqual(t, -1, titleTop, "the title banner is not on screen")
			welcomeTop := blockAt(screen, welcome, titleTop+len(title))
			require.NotEqual(t, -1, welcomeTop, "the greeting banner is not under the title")
			nameTop := blockAt(screen, name, welcomeTop+len(welcome))
			require.NotEqual(t, -1, nameTop, "the name is not drawn as a banner under the greeting")

			assert.LessOrEqual(t, welcomeTop-(titleTop+len(title)), 2, "the greeting floats away from the title")
			gap := nameTop - (welcomeTop + len(welcome))
			assert.GreaterOrEqual(t, gap, 1, "the name is pressed against the greeting")
			assert.LessOrEqual(t, gap, 2, "the name drifts away from the greeting")
		})
	}
}

// Two players whose names draw banners of one size (same length, no descender like
// a "y" adding a row) see the same screen but for the name banner: nothing else on
// it depends on who is looking.
func TestHome_View_OnlyTheNameBannerDependsOnThePlayer(t *testing.T) {
	t.Parallel()

	render := func(username string) string {
		screen := homeScreen(t, &db.User{Username: username}, 120, 40)
		name := bannerRows(styles.RenderFigureUncached(username, 200, 20))
		welcome := bannerRows(styles.RenderFigureASCII("Welcome", 200, 20))
		at := blockAt(screen, name, blockAt(screen, welcome, 0)+len(welcome))
		if at < 0 {
			return strings.Join(screen, "\n")
		}
		for k := range name {
			screen[at+k] = "<name>"
		}
		return strings.Join(screen, "\n")
	}

	alice := render("alice")
	require.Contains(t, alice, "<name>", "the name is on screen, or this test proves nothing")
	for _, username := range []string{"frank", "carol", "david", "eve12"} {
		assert.Equal(t, alice, render(username), "%s changed more than the name", username)
	}
}

// The shared handler runs first, so a resize has to be swallowed here rather than
// falling through to the key switch.
func TestHome_Update_HandlesCommonMessages(t *testing.T) {
	t.Parallel()
	m := New(router.GlobalContext{})

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})

	assert.Nil(t, cmd)
	assert.Equal(t, 120, updated.(*model).global.Width)
	assert.Equal(t, 50, updated.(*model).global.Height)
}
