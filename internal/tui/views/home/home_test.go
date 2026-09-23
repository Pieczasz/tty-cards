package home

import (
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/Pieczasz/terminal-card/internal/testutil"
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
	_, isChange := cmd().(router.ChangeViewMsg)
	assert.False(t, isChange, "q must not navigate")
}

func TestHome_Update_IgnoresUnboundKeys(t *testing.T) {
	t.Parallel()
	m := New(router.GlobalContext{})

	_, cmd := m.Update(tuitest.Key("z"))

	assert.Nil(t, cmd, "an unbound key does nothing")
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

	for _, size := range []struct {
		name string
		w, h int
	}{
		{"the declared minimum", styles.MinWidth, styles.MinHeight},
		{"a stock terminal", 80, 24},
		{"a tall terminal", 120, 50},
	} {
		t.Run(size.name, func(t *testing.T) {
			t.Parallel()
			m := New(router.GlobalContext{
				User:  &db.User{ID: testutil.UID(1), Username: "alice"},
				Theme: styles.NewTheme(true), Width: size.w, Height: size.h,
			})

			out := m.View().Content

			assert.LessOrEqual(t, lg.Height(out), size.h, "taller than the terminal")
			assert.LessOrEqual(t, lg.Width(out), size.w, "wider than the terminal")
		})
	}
}

func TestHome_View_Greeting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		user *db.User
		want string
	}{
		{name: "a signed-in player is greeted by name", user: &db.User{Username: "alice"}, want: "alice"},
		// The view is built before auth has resolved, so a nil user has to render
		// something rather than dereference nothing.
		{name: "no user yet falls back", user: nil, want: "Player"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := New(router.GlobalContext{
				User: tt.user, Theme: styles.NewTheme(true), Width: 120, Height: 50,
			})

			assert.Contains(t, m.View().Content, tt.want)
		})
	}
}

// The banner word is the fixed string "Welcome"; the username is styled text beside
// it. Baking the name into the figlet keyed the banner cache on a user-controlled
// string, so any account could mint entries, fill the cap, and push every real
// screen title back to re-parsing the whole figlet font on every frame. Two players
// with names of one length therefore see the same screen but for the name itself.
func TestHome_View_BannerIsNotKeyedOnTheUsername(t *testing.T) {
	t.Parallel()

	render := func(username string) string {
		m := New(router.GlobalContext{
			User:  &db.User{Username: username},
			Theme: styles.NewTheme(true), Width: 120, Height: 50,
		})
		return strings.Replace(m.View().Content, username, "<name>", 1)
	}

	alice := render("alice")
	require.Contains(t, alice, "<name>", "the name is on screen, or this test proves nothing")
	for _, username := range []string{"bobby", "carol", "david", "eve12"} {
		assert.Equal(t, alice, render(username), "%s changed more than the name line", username)
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
