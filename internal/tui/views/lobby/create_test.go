package lobby

import (
	"errors"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/game/crazyeight"
	"github.com/Pieczasz/terminal-card/internal/game/hearts"
	"github.com/Pieczasz/terminal-card/internal/game/poker"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bothGamesRegistry offers Poker (max 9) and Crazy Eights (max 6), so switching
// between them exercises the clamp.
func bothGamesRegistry(t *testing.T) *game.Registry {
	t.Helper()
	return game.NewRegistry(
		game.Module{
			Name: "Poker", Slug: "poker",
			Factory: func() game.Rules { return &poker.Rules{} },
		},
		game.Module{
			Name: "Crazy Eights", Slug: "crazy_eights",
			Factory: func() game.Rules { return &crazyeight.Rules{} },
		},
	)
}

func newCreateModel(t *testing.T) *createModel {
	t.Helper()
	global := router.GlobalContext{
		User:         testUser(1, "alice"),
		GameRegistry: bothGamesRegistry(t),
		Width:        120,
		Height:       40,
	}
	m, ok := NewCreate(global).(*createModel)
	require.True(t, ok)
	return m
}

// Switching from a game that allows 9 players to one that allows 6 must pull the
// setting down, or the lobby is created with a size its own rules reject.
func TestCreate_SwitchingGameClampsMaxPlayers(t *testing.T) {
	t.Parallel()
	m := newCreateModel(t)

	pokerIdx, crazyIdx := -1, -1
	for i, name := range m.gameOptions {
		switch name {
		case "Poker":
			pokerIdx = i
		case "Crazy Eights":
			crazyIdx = i
		}
	}
	require.NotEqual(t, -1, pokerIdx, "Poker must be offered")
	require.NotEqual(t, -1, crazyIdx, "Crazy Eights must be offered")

	m.gameIndex = pokerIdx
	m.maxPlayers = 9
	m.clampMaxPlayers()
	assert.Equal(t, 9, m.maxPlayers, "poker allows nine seats")

	m.gameIndex = crazyIdx
	m.clampMaxPlayers()
	assert.Equal(t, 6, m.maxPlayers, "crazy eights caps at six, so the setting must come down")
}

// Stepping the setting down stops at the game's own minimum, not at two: a Hearts
// table set to three seats is a lobby its rules can never start.
func TestCreate_StepDownStopsAtTheGamesMinimum(t *testing.T) {
	t.Parallel()
	r := game.NewRegistry(game.Module{
		Name: "Hearts", Slug: "hearts",
		Factory: func() game.Rules { return &hearts.Rules{} },
	})
	m, ok := NewCreate(router.GlobalContext{User: testUser(1, "alice"), GameRegistry: r}).(*createModel)
	require.True(t, ok)
	m.cursor = createCursorPlayers

	for range 3 {
		m.adjustSetting(-1)
	}

	assert.Equal(t, 4, m.maxPlayers, "hearts cannot go below four")
}

// The clamp must also raise a too-small setting to the game's minimum.
func TestCreate_ClampRaisesBelowMinimum(t *testing.T) {
	t.Parallel()
	m := newCreateModel(t)

	m.maxPlayers = 1
	m.clampMaxPlayers()
	assert.GreaterOrEqual(t, m.maxPlayers, 2, "no game starts with fewer than two seats")
}

// An in-range value must be left alone.
func TestCreate_ClampLeavesValidValueAlone(t *testing.T) {
	t.Parallel()
	m := newCreateModel(t)

	m.maxPlayers = 4
	m.clampMaxPlayers()
	assert.Equal(t, 4, m.maxPlayers)
}

// The form used to fall back to a hardcoded "Crazy Eights" when the registry came
// back empty, which made this a second place a game was declared and offered a
// game the registry could not build. An empty registry now offers nothing.
func TestCreate_OffersOnlyWhatTheRegistryHas(t *testing.T) {
	t.Parallel()

	global := router.GlobalContext{
		User:         testUser(1, "alice"),
		GameRegistry: game.NewRegistry(),
		Width:        120,
		Height:       40,
	}
	m, ok := NewCreate(global).(*createModel)
	require.True(t, ok)

	assert.Empty(t, m.gameOptions, "no registered game means no game on the form")
	assert.Empty(t, m.selectedGame())

	require.NotPanics(t, func() { m.View() }, "an empty form still has to render")

	m.cursor = createCursorSubmit
	_, cmd := m.handleKey(tuitest.Key("enter"))
	assert.Nil(t, cmd, "there is nothing to create, so nowhere to navigate")
	assert.ErrorIs(t, m.err, errNoGames)
}

// Every registered game must be on the form, in the registry's order.
func TestCreate_OffersEveryRegisteredGame(t *testing.T) {
	t.Parallel()
	m := newCreateModel(t)

	assert.ElementsMatch(t, m.global.GameRegistry.GameNames(), m.gameOptions)
}

func TestCreate_NavigationKeys(t *testing.T) {
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
		{name: "escape goes home", key: "esc", want: router.RouteHome},
		{name: "quit goes home", key: "q", want: router.RouteHome},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newCreateModel(t)
			_, cmd := m.Update(tuitest.Key(tt.key))
			assert.Equal(t, tt.want, routeOf(t, cmd))
		})
	}
}

// The form is inert until a key arrives: an Init that scheduled anything would tick
// against a screen that never changes on its own.
func TestCreate_InitSchedulesNothing(t *testing.T) {
	t.Parallel()
	assert.Nil(t, newCreateModel(t).Init())
}

// Every row of the form is reachable by keyboard alone, and each one has to answer
// left/right the way it renders - "< value >" promises both directions work.
func TestCreate_AdjustSettingPerRow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cursor int
		keys   []string
		check  func(t *testing.T, m *createModel)
	}{
		{
			name: "game steps forward through the options", cursor: createCursorGame, keys: []string{"l"},
			check: func(t *testing.T, m *createModel) {
				t.Helper()
				assert.Equal(t, 1, m.gameIndex)
			},
		},
		{
			name: "game will not step past the last option", cursor: createCursorGame,
			keys: []string{"l", "l", "l", "l"},
			check: func(t *testing.T, m *createModel) {
				t.Helper()
				assert.Equal(t, len(m.gameOptions)-1, m.gameIndex)
			},
		},
		{
			name: "game will not step below the first", cursor: createCursorGame, keys: []string{"h"},
			check: func(t *testing.T, m *createModel) {
				t.Helper()
				assert.Zero(t, m.gameIndex)
			},
		},
		{
			name: "max players climbs to the game's ceiling", cursor: createCursorPlayers,
			keys: []string{"l", "l", "l", "l", "l", "l", "l", "l"},
			check: func(t *testing.T, m *createModel) {
				t.Helper()
				_, maxP := gamePlayerBounds(m.global.GameRegistry, m.selectedGame())
				assert.Equal(t, maxP, m.maxPlayers)
			},
		},
		{
			name: "max players stops at two", cursor: createCursorPlayers,
			keys: []string{"h", "h", "h", "h", "h"},
			check: func(t *testing.T, m *createModel) {
				t.Helper()
				assert.Equal(t, 2, m.maxPlayers)
			},
		},
		{
			name: "visibility toggles either way", cursor: createCursorVisibility, keys: []string{"l"},
			check: func(t *testing.T, m *createModel) {
				t.Helper()
				assert.False(t, m.isPrivate)
			},
		},
		{
			name: "visibility toggles back", cursor: createCursorVisibility, keys: []string{"l", "h"},
			check: func(t *testing.T, m *createModel) {
				t.Helper()
				assert.True(t, m.isPrivate)
			},
		},
		{
			name: "mode toggles either way", cursor: createCursorMode, keys: []string{"h"},
			check: func(t *testing.T, m *createModel) {
				t.Helper()
				assert.True(t, m.isRanked)
			},
		},
		{
			name: "the submit row has nothing to adjust", cursor: createCursorSubmit, keys: []string{"l", "h"},
			check: func(t *testing.T, m *createModel) {
				t.Helper()
				assert.Zero(t, m.gameIndex)
				assert.Equal(t, 4, m.maxPlayers)
				assert.True(t, m.isPrivate)
				assert.False(t, m.isRanked)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newCreateModel(t)
			m.cursor = tt.cursor
			for _, key := range tt.keys {
				m.Update(tuitest.Key(key))
			}
			tt.check(t, m)
		})
	}
}

// The cursor walks the whole form and stops at both ends.
func TestCreate_CursorStaysInBounds(t *testing.T) {
	t.Parallel()
	m := newCreateModel(t)

	for range 10 {
		m.Update(tuitest.Key("j"))
	}
	assert.Equal(t, createCursorSubmit, m.cursor)

	for range 10 {
		m.Update(tuitest.Key("k"))
	}
	assert.Equal(t, createCursorGame, m.cursor)
}

// Submitting is the only thing this screen exists to do, so the settings on the form
// have to be the settings the lobby is built with.
func TestCreate_SubmitBuildsTheLobbyFromTheForm(t *testing.T) {
	t.Parallel()
	m := newCreateModel(t)
	m.global.LobbyManager = lobby.NewManager(t.Context(), nil)
	m.maxPlayers = 5
	m.isPrivate = false
	m.isRanked = true
	m.cursor = createCursorSubmit

	_, cmd := m.Update(tuitest.Key("enter"))

	require.NotNil(t, cmd)
	change, ok := cmd().(router.ChangeViewMsg)
	require.True(t, ok)
	assert.Equal(t, router.RouteLobby, change.ViewName)
	created, ok := change.Context.(*lobby.Lobby)
	require.True(t, ok, "the lobby view is handed the lobby that was just made")
	assert.Equal(t, m.selectedGame(), created.GameName())
	assert.Equal(t, 5, created.MaxPlayers())
	assert.False(t, created.IsPrivate())
	assert.True(t, created.IsRanked())
	assert.NoError(t, m.err)
}

// A player already sitting at a table cannot open a second one. Without the error on
// screen the button would simply look broken.
func TestCreate_SubmitShowsTheManagersRefusal(t *testing.T) {
	t.Parallel()
	manager := lobby.NewManager(t.Context(), nil)
	m := newCreateModel(t)
	m.global.LobbyManager = manager
	_, err := manager.CreateLobby(lobby.NewPlayer(m.global.User), lobby.WithCardGame(m.selectedGame()))
	require.NoError(t, err)

	m.cursor = createCursorSubmit
	_, cmd := m.Update(tuitest.Key("enter"))

	assert.Nil(t, cmd, "nothing was created, so there is nowhere to navigate")
	require.Error(t, m.err)
	assert.Contains(t, m.View().Content, "Error:")
}

// Enter anywhere but the submit row must not create anything: the row the cursor is on
// is the only thing separating a settings tweak from a commit.
func TestCreate_EnterOffTheSubmitRowDoesNothing(t *testing.T) {
	t.Parallel()
	m := newCreateModel(t)
	m.global.LobbyManager = lobby.NewManager(t.Context(), nil)
	m.cursor = createCursorVisibility

	_, cmd := m.Update(tuitest.Key("enter"))

	assert.Nil(t, cmd)
	assert.NoError(t, m.err)
}

// The registry is the only source of truth for seat counts, so a game it cannot build
// has to fall back to a bound rather than leave the row free to run away.
func TestCreate_MaxPlayersFallsBackForAnUnbuildableGame(t *testing.T) {
	t.Parallel()
	m := newCreateModel(t)
	m.global.GameRegistry = game.NewRegistry()

	minP, maxP := gamePlayerBounds(m.global.GameRegistry, m.selectedGame())
	assert.Equal(t, fallbackMinPlayers, minP)
	assert.Equal(t, fallbackMaxPlayers, maxP)
}

// Anything that is not a keystroke reaches this form too - a resize has to land on the
// layout and everything else has to be ignored rather than mistaken for input.
func TestCreate_UpdateIgnoresWhatIsNotAKey(t *testing.T) {
	t.Parallel()
	m := newCreateModel(t)

	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	assert.Nil(t, cmd)
	assert.Equal(t, 80, m.global.Width)
	assert.Equal(t, 24, m.global.Height)

	_, cmd = m.Update(refreshMsg{})
	assert.Nil(t, cmd)
	assert.Zero(t, m.cursor, "and it is not read as a cursor move")
}

// The form is a full-screen view, so it has to fit every size the app claims to
// support - with the error line showing, which is the tallest it ever gets.
func TestCreateView_FitsTheTerminal(t *testing.T) {
	t.Parallel()
	for _, size := range []struct {
		name string
		w, h int
	}{
		{name: "the declared minimum", w: styles.MinWidth, h: styles.MinHeight},
		{name: "a stock terminal", w: 80, h: 24},
		{name: "a tall terminal", w: 120, h: 50},
	} {
		t.Run(size.name, func(t *testing.T) {
			t.Parallel()
			m := newCreateModel(t)
			m.global.Theme = styles.NewTheme(true)
			m.global.Width, m.global.Height = size.w, size.h
			m.err = errors.New("no games are available right now")

			out := m.View().Content

			assert.LessOrEqual(t, lg.Height(out), size.h, "taller than the terminal")
			assert.LessOrEqual(t, lg.Width(out), size.w, "wider than the terminal")
		})
	}
}
