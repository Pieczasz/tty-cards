package lobby

import (
	"errors"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/crazyeight"
	"github.com/Pieczasz/tty-cards/internal/game/hearts"
	"github.com/Pieczasz/tty-cards/internal/game/poker"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bothGames offers Poker (max 9) and Crazy Eights (max 6), so switching between them
// exercises the clamp.
func bothGames() []game.Module {
	return []game.Module{
		{Name: "Poker", Slug: "poker", MinPlayers: poker.MinPlayers, MaxPlayers: poker.MaxPlayers},
		{Name: "Crazy Eights", Slug: "crazy_eights", MinPlayers: crazyeight.MinPlayers, MaxPlayers: crazyeight.MaxPlayers},
	}
}

func newCreateModel(t *testing.T) *createModel {
	t.Helper()
	global := router.GlobalContext{
		User:   testUser(1, "alice"),
		Games:  bothGames(),
		Width:  120,
		Height: 40,
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
	for i, mod := range m.gameOptions {
		switch mod.Slug {
		case "poker":
			pokerIdx = i
		case "crazy_eights":
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
	heartsOnly := []game.Module{{Name: "Hearts", Slug: "hearts", MinPlayers: hearts.MinPlayers, MaxPlayers: hearts.MaxPlayers}}
	m, ok := NewCreate(router.GlobalContext{User: testUser(1, "alice"), Games: heartsOnly}).(*createModel)
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
// game the registry could not build. No games on offer now means nothing offered.
func TestCreate_OffersOnlyWhatTheSessionHas(t *testing.T) {
	t.Parallel()

	global := router.GlobalContext{
		User:   testUser(1, "alice"),
		Width:  120,
		Height: 40,
	}
	m, ok := NewCreate(global).(*createModel)
	require.True(t, ok)

	assert.Empty(t, m.gameOptions, "no registered game means no game on the form")
	assert.Empty(t, m.selectedGame().Slug)

	require.NotPanics(t, func() { m.View() }, "an empty form still has to render")

	m.cursor = createCursorSubmit
	_, cmd := m.handleKey(tuitest.Key("enter"))
	assert.Nil(t, cmd, "there is nothing to create, so nowhere to navigate")
	assert.ErrorIs(t, m.err, errNoGames)
}

// Every game on offer must be on the form, in menu order.
func TestCreate_OffersEveryRegisteredGame(t *testing.T) {
	t.Parallel()
	m := newCreateModel(t)

	var want, got []string
	for _, mod := range m.global.Games {
		want = append(want, mod.Slug)
	}
	for _, mod := range m.gameOptions {
		got = append(got, mod.Slug)
	}
	assert.Equal(t, want, got)
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
				_, maxP := m.playerBounds()
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
				assert.True(t, m.isPrivate)
			},
		},
		{
			name: "visibility toggles back", cursor: createCursorVisibility, keys: []string{"l", "h"},
			check: func(t *testing.T, m *createModel) {
				t.Helper()
				assert.False(t, m.isPrivate)
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
				assert.False(t, m.isPrivate, "the form starts public: an unlisted table cannot be found")
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
	m.global.LobbyManager = newManager(t)
	m.maxPlayers = 5
	m.isPrivate = false
	m.isRanked = true
	m.cursor = createCursorSubmit

	_, cmd := m.Update(tuitest.Key("enter"))

	require.NotNil(t, cmd)
	change, ok := cmd().(router.ChangeViewMsg)
	require.True(t, ok)
	assert.Equal(t, router.RouteLobby, change.ViewName)
	created := change.Payload.Lobby
	require.NotNil(t, created, "the lobby view is handed the lobby that was just made")
	assert.Equal(t, m.selectedGame().Name, created.Snapshot().GameName)
	assert.Equal(t, 5, created.Snapshot().Limit)
	assert.False(t, created.Snapshot().Private)
	assert.True(t, created.Snapshot().Ranked)
	assert.NoError(t, m.err)
}

// A player already sitting at a table cannot open a second one. Without the error on
// screen the button would simply look broken.
func TestCreate_SubmitShowsTheManagersRefusal(t *testing.T) {
	t.Parallel()
	manager := newManager(t)
	m := newCreateModel(t)
	m.global.LobbyManager = manager
	_, err := manager.CreateLobby(lobby.NewPlayer(m.global.User), lobby.WithCardGame(m.selectedGame().Slug))
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
	m.global.LobbyManager = newManager(t)
	m.cursor = createCursorVisibility

	_, cmd := m.Update(tuitest.Key("enter"))

	assert.Nil(t, cmd)
	assert.NoError(t, m.err)
}

// The selected game is the only source of truth for seat counts, so a form with none to
// select has to fall back to a bound rather than leave the row free to run away.
func TestCreate_MaxPlayersFallsBackWithNoGameToSelect(t *testing.T) {
	t.Parallel()
	m := newCreateModel(t)
	m.gameOptions = nil

	minP, maxP := m.playerBounds()
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
	for _, size := range tuitest.FitSizes {
		t.Run(size.Name, func(t *testing.T) {
			t.Parallel()
			m := newCreateModel(t)
			m.global.Theme = styles.NewTheme(true)
			m.global.Width, m.global.Height = size.Width, size.Height
			m.err = errors.New("no games are available right now")

			out := m.View().Content

			assert.LessOrEqual(t, lg.Height(out), size.Height, "taller than the terminal")
			assert.LessOrEqual(t, lg.Width(out), size.Width, "wider than the terminal")
		})
	}
}

// Toggling a setting must not shift the "<" and ">" around it, so each label reads the
// same width in both states - on the create form and in the lobby alike.
func TestSettingLabels_KeepOneWidth(t *testing.T) {
	t.Parallel()

	// Toggling a row must not move its own "<" and ">": each option's labels share a
	// width. The rows start at different columns, so they need not share one another's.
	assert.Equal(t, "Ranked", modeLabel(true))
	assert.Len(t, visibilityLabel(false), len(visibilityLabel(true)))
	assert.Len(t, modeLabel(false), len(modeLabel(true)))
}

// Enter on a setting changes it and wraps, instead of doing nothing on four of the
// form's five rows.
func TestCreate_EnterCyclesTheHighlightedSetting(t *testing.T) {
	t.Parallel()
	m := newCreateModel(t)

	m.cursor = createCursorVisibility
	m.Update(tuitest.Key("enter"))
	assert.True(t, m.isPrivate)

	m.cursor = createCursorMode
	m.Update(tuitest.Key("enter"))
	assert.True(t, m.isRanked)

	m.cursor = createCursorPlayers
	minP, maxP := m.playerBounds()
	for range maxP - m.maxPlayers {
		m.Update(tuitest.Key("enter"))
	}
	require.Equal(t, maxP, m.maxPlayers)
	m.Update(tuitest.Key("enter"))
	assert.Equal(t, minP, m.maxPlayers, "past the ceiling it wraps to the floor")
}
