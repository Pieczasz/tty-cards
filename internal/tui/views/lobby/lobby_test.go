package lobby

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/elo"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/game/crazyeight"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Pieczasz/terminal-card/internal/testutil"
)

const testGameName = "Crazy Eights"

func testUser(id uint64, name string) *db.User {
	return &db.User{ID: testutil.UID(id), Username: name}
}

func testRegistry() *game.Registry {
	return game.NewRegistry(game.Module{
		Name:    testGameName,
		Slug:    "crazy_eights",
		Factory: func() game.Rules { return &crazyeight.Rules{} },
	})
}

// leaderView returns the lobby view as seen by the lobby's leader.
func leaderView(t *testing.T) (*model, *lobby.Lobby) {
	t.Helper()
	manager := lobby.NewManager(context.Background(), nil)
	leaderUser := testUser(1, "alice")
	leader := lobby.NewPlayer(leaderUser)

	l, err := manager.CreateLobby(leader,
		lobby.WithCardGame(testGameName),
		lobby.WithMaxPlayers(4),
		lobby.WithPrivate(false),
	)
	require.NoError(t, err)

	global := router.GlobalContext{
		User:         leaderUser,
		LobbyManager: manager,
		GameRegistry: testRegistry(),
		Width:        120,
		Height:       40,
	}
	m, ok := New(global, l).(*model)
	require.True(t, ok)
	return m, l
}

// keyMsg builds the message Bubble Tea would deliver for a keystroke. Named keys
// need their own Code and carry no Text; building one from key[0] would turn "esc"
// into the letter 'e'.
func keyMsg(key string) tea.KeyPressMsg {
	switch key {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "space":
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	}
}

func press(m *model, key string) (tea.Model, tea.Cmd) {
	return m.Update(keyMsg(key))
}

// routeOf runs a returned command and reports the route it navigates to.
func routeOf(t *testing.T, cmd tea.Cmd) string {
	t.Helper()
	require.NotNil(t, cmd)
	change, ok := cmd().(router.ChangeViewMsg)
	require.True(t, ok, "command did not produce a ChangeViewMsg")
	return change.ViewName
}

func TestHandleKey_Navigation(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"n": router.RouteLobbyCreate,
		"f": router.RouteLobbyJoin,
		"p": router.RouteProfile,
		"t": router.RouteLeaderboard,
	}

	for key, want := range cases {
		t.Run("key_"+key, func(t *testing.T) {
			t.Parallel()
			m, _ := leaderView(t)
			_, cmd := press(m, key)
			assert.Equal(t, want, routeOf(t, cmd))
			assert.Nil(t, m.lobbyChan, "navigating away releases the lobby subscription")
		})
	}
}

func TestHandleKey_LeaveConfirmFlow(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)

	_, cmd := press(m, "x")
	assert.True(t, m.showLeaveConfirm)
	assert.Nil(t, cmd, "asking for confirmation does not navigate")

	// Declining returns to the lobby and keeps the subscription.
	_, cmd = press(m, "n")
	assert.False(t, m.showLeaveConfirm)
	assert.Nil(t, cmd)
	assert.NotNil(t, m.lobbyChan)

	// Confirming leaves and goes home.
	press(m, "x")
	_, cmd = press(m, "y")
	assert.Equal(t, router.RouteHome, routeOf(t, cmd))
	assert.Nil(t, m.lobbyChan)
}

func TestHandleKey_CursorStaysInBounds(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)

	for range 10 {
		press(m, "k")
	}
	assert.Zero(t, m.cursor, "cursor never moves above the first row")

	for range 10 {
		press(m, "j")
	}
	assert.Equal(t, cursorMode, m.cursor, "with no guests the last row is the mode row")
}

func TestAdjustSetting_MaxPlayersRespectsRulesBounds(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)

	rulesMin, rulesMax := m.gamePlayerBounds()
	require.Equal(t, 2, rulesMin)
	require.Equal(t, 6, rulesMax)

	m.cursor = cursorMaxPlayers
	for range 10 {
		press(m, "l")
	}
	assert.Equal(t, rulesMax, m.maxPlayers)
	assert.Equal(t, rulesMax, l.MaxPlayers(), "the lobby is updated, not just the view")

	for range 10 {
		press(m, "h")
	}
	assert.Equal(t, rulesMin, m.maxPlayers)
}

func TestAdjustSetting_TogglesVisibilityAndMode(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)

	m.cursor = cursorVisibility
	press(m, "l")
	assert.True(t, m.isPrivate)
	assert.True(t, l.IsPrivate())

	m.cursor = cursorMode
	press(m, "l")
	assert.True(t, m.isRanked)
	assert.True(t, l.IsRanked())
}

// The game row is deliberately fixed once a lobby exists, and it reads straight off
// the lobby rather than from a one-entry option list the cursor pretended to walk.
func TestAdjustSetting_GameRowIsNoOp(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)

	m.cursor = cursorGame
	before := m.renderSettings(true)
	press(m, "l")
	press(m, "h")

	assert.Equal(t, before, m.renderSettings(true))
	assert.Contains(t, before, l.GameName(), "the game row names the lobby's game")
}

func TestHandleLobbyEvent_ClosedGoesHome(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)

	_, cmd := m.Update(lobbyMsg{Type: lobby.EventLobbyClosed, src: m.lobbyChan})
	assert.Equal(t, router.RouteHome, routeOf(t, cmd))
	assert.Nil(t, m.lobbyChan)
}

// An unregistered game must not eject the player, and must leave the listener
// armed so the view keeps receiving lobby events.
func TestHandleLobbyEvent_UnknownGameKeepsListening(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)
	m.global.GameRegistry = game.NewRegistry() // game no longer registered

	engine := game.NewEngine(&crazyeight.Rules{},
		[]*game.Player{{ID: "1"}, {ID: "2"}}, nil)

	_, cmd := m.Update(lobbyMsg{Type: lobby.EventGameStarted, Engine: engine, src: m.lobbyChan})
	require.NotNil(t, cmd, "listener must stay armed")
	assert.NotNil(t, m.lobbyChan, "subscription is retained")
}

func TestView_RendersLobbyAndConfirmPopup(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)

	out := m.View().Content
	assert.Contains(t, out, "alice")
	assert.Contains(t, out, "Max Players")
	assert.Contains(t, out, "Leave Lobby")

	m.showLeaveConfirm = true
	assert.Contains(t, m.View().Content, "Are you sure")
}

func TestView_NoActiveLobby(t *testing.T) {
	t.Parallel()
	m, ok := New(router.GlobalContext{}, nil).(*model)
	require.True(t, ok)
	assert.Contains(t, m.View().Content, "No active lobby")
}

// A view with no lobby used to return a ChangeViewMsg for every message it was handed,
// resize messages included, so the router rebuilt the home view on every keystroke and
// every terminal resize for as long as the view stayed on screen.
func TestUpdate_WithoutALobbyNavigatesHomeOnce(t *testing.T) {
	t.Parallel()

	m := &model{global: router.GlobalContext{}}

	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	require.NotNil(t, cmd, "the first message takes the player home")
	msg, ok := cmd().(router.ChangeViewMsg)
	require.True(t, ok)
	assert.Equal(t, router.RouteHome, msg.ViewName)

	for _, next := range []tea.Msg{
		tea.WindowSizeMsg{Width: 100, Height: 30},
		tea.KeyPressMsg{Code: rune("j"[0]), Text: "j"},
	} {
		_, cmd = m.Update(next)
		assert.Nil(t, cmd, "the navigation is asked for once, not on every message")
	}
}

// managerOf is the manager behind the view: tearing a lobby down is a server-side
// call the view itself never makes.
func managerOf(_ *testing.T, m *model) *lobby.Manager { return m.global.LobbyManager }

// addGuest seats another player in the lobby a view is already looking at, which is
// what turns on the rows, the cursor range and the kick key the leader-only tests need.
func addGuest(t *testing.T, m *model, l *lobby.Lobby, id uint64, name string) *game.Player {
	t.Helper()
	g := lobby.NewPlayer(testUser(id, name))
	_, err := m.global.LobbyManager.JoinLobbyByCode(l.Code(), g)
	require.NoError(t, err)
	return g
}

// The router closes the active view on navigation and the ssh layer closes the whole
// model on disconnect, so the same view is routinely closed twice. A second Close that
// panicked or unsubscribed again would take the session down on an ordinary logout.
func TestClose_IsIdempotent(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)
	require.NotNil(t, m.lobbyChan)

	m.Close()
	assert.Nil(t, m.lobbyChan, "the first close releases the subscription")
	assert.NotPanics(t, m.Close, "and the second is a no-op")
}

// Init has to hand back a live listener, and the command it returns has to wrap a real
// lobby event: a wrapper that dropped the event would leave the view deaf while still
// looking subscribed.
func TestInit_ArmsTheLobbyListener(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	t.Cleanup(m.Close)

	cmd := m.Init()
	require.NotNil(t, cmd)

	require.NoError(t, l.SetPrivate(l.Leader(), true))

	msg, ok := cmd().(lobbyMsg)
	require.True(t, ok, "the listener must deliver lobby events as lobbyMsg")
	assert.Equal(t, lobby.EventSettingsUpdated, msg.Type)
}

// The router rebuilds this view on every visit, and a listener still in flight from
// the last one hands its event to the new view. Handling it re-armed a listener on the
// new feed beside the one Init already armed: two readers racing for one channel.
func TestUpdate_DropsAnEventFromAnotherViewsFeed(t *testing.T) {
	t.Parallel()
	old, l := leaderView(t)
	t.Cleanup(old.Close)
	current, ok := New(old.global, l).(*model)
	require.True(t, ok)
	t.Cleanup(current.Close)

	_, cmd := current.Update(lobbyMsg{Type: lobby.EventPlayersUpdated, src: old.lobbyChan})

	assert.Nil(t, cmd, "a stale event must not arm a second listener")
}

// A reconnecting player whose seat survived the disconnect grace belongs back at the
// table, not on a roster screen for a game that is already running.
func TestInit_ReconnectIntoAnActiveGameRoutesToTheTable(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	guest := addGuest(t, m, l, 2, "bob")

	registry := m.global.GameRegistry
	require.NoError(t, l.ToggleReady(l.Leader(), registry))
	require.NoError(t, l.ToggleReady(guest, registry))
	engine := l.ActiveGame()
	require.NotNil(t, engine)
	// The lobby watches the engine on a goroutine; closing it is what lets that
	// goroutine finish rather than parking for the rest of the run.
	t.Cleanup(engine.Close)

	cmd := m.Init()

	assert.Equal(t, router.GameRoute("crazy_eights"), routeOf(t, cmd))
	assert.Nil(t, m.lobbyChan, "the lobby feed is released on the way to the table")
}

// seatedIn is what separates a reconnecting player from one the engine already took for
// idling: the second keeps the roster instead of being dropped back into a game with no
// seat for them.
func TestSeatedIn(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)
	t.Cleanup(m.Close)

	engine := game.NewEngine(&crazyeight.Rules{}, []*game.Player{{ID: testutil.SeatID(1)}, {ID: testutil.SeatID(2)}}, nil)
	assert.True(t, m.seatedIn(engine), "alice is player 1")

	taken := game.NewEngine(&crazyeight.Rules{}, []*game.Player{{ID: "7"}, {ID: "8"}}, nil)
	assert.False(t, m.seatedIn(taken), "a seat the engine removed is not a seat")

	// The lobby reopens a finished table on its own goroutine, so for a moment
	// ActiveGame still hands it back. Routing there bounced the player onto a
	// game-over screen whose esc brought them straight back here.
	rules := &crazyeight.Rules{}
	finished := game.NewEngine(rules, []*game.Player{{ID: testutil.SeatID(1)}, {ID: testutil.SeatID(2)}}, rules.InitialDeck())
	t.Cleanup(finished.Close)
	require.NoError(t, finished.Start())
	finished.RemovePlayer(testutil.SeatID(2))
	require.True(t, finished.IsFinished())
	assert.False(t, m.seatedIn(finished), "a finished table has no seat to return to")
}

func TestGetElo(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)
	t.Cleanup(m.Close)

	def := elo.ToUint32(elo.DefaultRating)

	tests := []struct {
		name   string
		player *game.Player
		want   uint32
	}{
		{name: "no player at all", player: nil, want: def},
		{name: "rated for this game", player: &game.Player{ID: "9", Ratings: map[string]uint32{testGameName: 1750}}, want: 1750},
		{name: "rated only for another game", player: &game.Player{ID: "9", Ratings: map[string]uint32{"Poker": 1750}}, want: def},
		{name: "never played anything", player: &game.Player{ID: "9"}, want: def},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, m.getElo(tt.player))
		})
	}
}

// The bounds decide how far the max-players setting may travel. A missing registry or a
// game it cannot build must fall back rather than leave the row unbounded.
func TestGamePlayerBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		mutate           func(m *model)
		wantMin, wantMax int
	}{
		{name: "the lobby's real game", mutate: func(*model) {}, wantMin: 2, wantMax: 6},
		{name: "no registry at all", mutate: func(m *model) { m.global.GameRegistry = nil }, wantMin: 2, wantMax: 6},
		{
			name:    "a game the registry cannot build",
			mutate:  func(m *model) { m.global.GameRegistry = game.NewRegistry() },
			wantMin: 2, wantMax: 6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, _ := leaderView(t)
			t.Cleanup(m.Close)
			tt.mutate(m)

			minP, maxP := m.gamePlayerBounds()

			assert.Equal(t, tt.wantMin, minP)
			assert.Equal(t, tt.wantMax, maxP)
		})
	}
}

// The whole point of the lobby screen is that it hands the player over when the game
// starts; the payload is the engine every seat then renders from.
func TestHandleLobbyEvent_GameStartedRoutesToTheGameView(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)
	engine := game.NewEngine(&crazyeight.Rules{}, []*game.Player{{ID: "1"}, {ID: "2"}}, nil)

	_, cmd := m.Update(lobbyMsg{Type: lobby.EventGameStarted, Engine: engine, src: m.lobbyChan})

	require.NotNil(t, cmd)
	change, ok := cmd().(router.ChangeViewMsg)
	require.True(t, ok)
	assert.Equal(t, router.GameRoute("crazy_eights"), change.ViewName)
	assert.Same(t, engine, change.Context, "the view is handed the engine that started")
	assert.Nil(t, m.lobbyChan, "the lobby feed is released on the way to the table")
}

// A start with no engine is a server bug, not a reason to eject the player: the listener
// has to stay armed or this view goes deaf while still holding a subscriber slot.
func TestHandleLobbyEvent_GameStartedWithoutAnEngineKeepsListening(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)
	t.Cleanup(m.Close)

	_, cmd := m.Update(lobbyMsg{Type: lobby.EventGameStarted, src: m.lobbyChan})

	require.NotNil(t, cmd, "listener must stay armed")
	assert.NotNil(t, m.lobbyChan, "and the subscription is retained")
}

// Settings the leader changed have to land on the guests' screens, and the cursor has
// to come back inside a roster that just lost rows under it.
func TestHandleLobbyEvent_RefreshesSettingsAndClampsTheCursor(t *testing.T) {
	t.Parallel()

	for _, evType := range []lobby.EventType{lobby.EventSettingsUpdated, lobby.EventPlayersUpdated} {
		t.Run(evType.String(), func(t *testing.T) {
			t.Parallel()
			m, l := leaderView(t)
			t.Cleanup(m.Close)

			require.NoError(t, l.SetPrivate(l.Leader(), true))
			require.NoError(t, l.SetRanked(l.Leader(), true))
			require.NoError(t, l.SetMaxPlayers(l.Leader(), 5, 2, 6))
			m.cursor = cursorFirstGuest + 4 // a row for a guest who has since left

			_, cmd := m.Update(lobbyMsg{Type: evType, src: m.lobbyChan})

			require.NotNil(t, cmd, "the listener stays armed")
			assert.True(t, m.isPrivate)
			assert.True(t, m.isRanked)
			assert.Equal(t, 5, m.maxPlayers)
			assert.Equal(t, cursorMode, m.cursor, "with no guests the last row is the mode row")
		})
	}
}

// A kicked player's view still holds a live feed, so the roster event is the only thing
// that tells it to go. Without the once-guard it would re-issue the navigation for every
// later event and the router would rebuild the home view each time.
func TestHandleLobbyEvent_PlayerNoLongerInTheRosterGoesHomeOnce(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	guest := addGuest(t, m, l, 2, "bob")

	global := m.global
	global.User = testUser(2, "bob")
	guestModel, ok := New(global, l).(*model)
	require.True(t, ok)

	require.NoError(t, m.global.LobbyManager.Kick(l.Leader(), guest))

	_, cmd := guestModel.Update(lobbyMsg{Type: lobby.EventPlayersUpdated, src: guestModel.lobbyChan})
	assert.Equal(t, router.RouteHome, routeOf(t, cmd))
	assert.Nil(t, guestModel.lobbyChan)

	_, cmd = guestModel.Update(lobbyMsg{Type: lobby.EventPlayersUpdated, src: guestModel.lobbyChan})
	assert.Nil(t, cmd, "the navigation is asked for once, not on every event")

	m.Close()
}

// Only the leader owns the settings. A guest pressing the same keys must not move a
// cursor they cannot act on, nor flip a setting the lobby would reject anyway.
func TestHandleKey_GuestCannotDriveTheForm(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	t.Cleanup(m.Close)
	addGuest(t, m, l, 2, "bob")

	global := m.global
	global.User = testUser(2, "bob")
	guestModel, ok := New(global, l).(*model)
	require.True(t, ok)
	t.Cleanup(guestModel.Close)

	for _, key := range []string{"j", "k", "l", "h"} {
		press(guestModel, key)
	}

	assert.Zero(t, guestModel.cursor, "a guest has no cursor to move")
	assert.False(t, l.IsPrivate(), "and no settings to change")
	assert.Equal(t, 4, l.MaxPlayers())
}

func TestHandleKey_EnterKicksTheSelectedGuest(t *testing.T) {
	t.Parallel()

	t.Run("the leader removes a guest", func(t *testing.T) {
		t.Parallel()
		m, l := leaderView(t)
		t.Cleanup(m.Close)
		addGuest(t, m, l, 2, "bob")
		require.Len(t, l.Guests(), 1)

		m.cursor = cursorFirstGuest
		press(m, "enter")

		assert.Empty(t, l.Guests())
	})

	// The cursor is clamped on roster events, but an enter racing a guest's departure
	// must not index past the list.
	t.Run("a guest row with nobody on it", func(t *testing.T) {
		t.Parallel()
		m, l := leaderView(t)
		t.Cleanup(m.Close)

		m.cursor = cursorFirstGuest
		assert.NotPanics(t, func() { press(m, "enter") })
		assert.Empty(t, l.Guests())
	})

	// A lobby torn down underneath the view still answers IsLeader, so the kick reaches
	// the manager and fails there. The view has to survive that, not crash on it.
	t.Run("the lobby is already gone", func(t *testing.T) {
		t.Parallel()
		m, l := leaderView(t)
		addGuest(t, m, l, 2, "bob")
		m.cursor = cursorFirstGuest
		managerOf(t, m).RemoveLobby(l.Code())

		assert.NotPanics(t, func() { press(m, "enter") })
	})
}

// r is the one key a guest does own. A rejected toggle has to surface, since a player
// who sees nothing happen will just press it again.
func TestHandleKey_ReadyTogglesAndReportsFailure(t *testing.T) {
	t.Parallel()

	t.Run("a seated player toggles ready", func(t *testing.T) {
		t.Parallel()
		m, l := leaderView(t)
		t.Cleanup(m.Close)
		addGuest(t, m, l, 2, "bob") // a second seat, so readying up does not start the game

		m.actionErr = assert.AnError
		press(m, "r")

		require.NoError(t, m.actionErr, "a successful toggle clears the last error")
		assert.True(t, l.IsReady(l.Leader()))
	})

	t.Run("a player with no seat is told why", func(t *testing.T) {
		t.Parallel()
		m, l := leaderView(t)
		t.Cleanup(m.Close)

		global := m.global
		global.User = testUser(3, "carol") // never joined
		outsider, ok := New(global, l).(*model)
		require.True(t, ok)
		t.Cleanup(outsider.Close)

		press(outsider, "r")

		require.Error(t, outsider.actionErr)
		assert.Contains(t, outsider.View().Content, outsider.actionErr.Error(),
			"the failure is on screen, not only in the log")
	})
}

// The view moves the setting first and asks the lobby second, so a rejection has to put
// it back or the form shows a value the server never accepted.
func TestAdjustSetting_RevertsWhenTheLobbyRejects(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cursor int
		check  func(t *testing.T, m *model)
	}{
		{
			name: "max players", cursor: cursorMaxPlayers,
			check: func(t *testing.T, m *model) {
				t.Helper()
				assert.Equal(t, 4, m.maxPlayers)
			},
		},
		{
			name: "visibility", cursor: cursorVisibility,
			check: func(t *testing.T, m *model) {
				t.Helper()
				assert.False(t, m.isPrivate)
			},
		},
		{
			name: "mode", cursor: cursorMode,
			check: func(t *testing.T, m *model) {
				t.Helper()
				assert.False(t, m.isRanked)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, l := leaderView(t)
			t.Cleanup(m.Close)
			imposter := addGuest(t, m, l, 2, "bob")

			m.cursor = tt.cursor
			m.adjustSetting(imposter, +1)

			tt.check(t, m)
		})
	}
}

// Shrinking the table below the players already sitting at it would leave somebody
// seated at a lobby that says it is full.
func TestAdjustSetting_MaxPlayersStopsAtTheSeatedRoster(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	t.Cleanup(m.Close)
	addGuest(t, m, l, 2, "bob")
	addGuest(t, m, l, 3, "carol")
	require.Equal(t, 3, l.CurrentPlayers())

	m.cursor = cursorMaxPlayers
	for range 5 {
		press(m, "h")
	}

	assert.Equal(t, 3, m.maxPlayers, "the floor is the roster, not the rules minimum")
	assert.Equal(t, 3, l.MaxPlayers())
}

// A resize must reach the layout, and anything else must be ignored rather than
// mistaken for a keystroke.
func TestUpdate_HandlesResizesAndIgnoresTheRest(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)
	t.Cleanup(m.Close)

	_, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	assert.Nil(t, cmd)
	assert.Equal(t, 100, m.global.Width)
	assert.Equal(t, 30, m.global.Height)

	_, cmd = m.Update(refreshMsg{})
	assert.Nil(t, cmd, "a message this view knows nothing about does nothing")
}

// New has to say so when the feed is unavailable: a roster that silently stops updating
// looks exactly like a lobby nobody is joining.
func TestNew_ReportsASubscriptionFailure(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	m.Close()
	managerOf(t, m).RemoveLobby(l.Code())

	broken, ok := New(m.global, l).(*model)
	require.True(t, ok)

	require.Error(t, broken.actionErr)
	assert.Contains(t, broken.actionErr.Error(), "rejoin the lobby")
	assert.Nil(t, broken.lobbyChan)
	assert.Contains(t, broken.View().Content, "rejoin the lobby")
	assert.NotPanics(t, broken.Close, "there is nothing to release, and that is fine")
}

// The roster is the screen: who is here, what they are rated, who is ready, and - for
// the leader alone - which of them the kick key would take.
func TestRenderPlayerList(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	t.Cleanup(m.Close)
	bob := addGuest(t, m, l, 2, "bob")
	addGuest(t, m, l, 3, "carol")
	require.NoError(t, l.ToggleReady(bob, m.global.GameRegistry))

	m.cursor = cursorFirstGuest // bob's row

	leaderRows := m.renderPlayerList(true)
	require.Len(t, leaderRows, 4, "a heading, the leader and one row per guest")
	assert.Contains(t, leaderRows[1], "[Leader]")
	assert.Contains(t, leaderRows[1], "alice")
	assert.Contains(t, leaderRows[1], fmt.Sprintf("Elo: %d", elo.ToUint32(elo.DefaultRating)))
	assert.NotContains(t, leaderRows[1], "Ready", "the leader has not readied up")
	assert.Contains(t, leaderRows[2], "> ", "the cursor marks the guest the kick key would take")
	assert.Contains(t, leaderRows[2], "bob")
	assert.Contains(t, leaderRows[2], "Ready")
	assert.NotContains(t, leaderRows[3], "> ")
	assert.NotContains(t, leaderRows[3], "Ready", "carol has not")

	guestRows := m.renderPlayerList(false)
	for _, row := range guestRows {
		assert.NotContains(t, row, "> ", "only the leader can kick, so only they get a cursor")
	}
}

// lipgloss word-wraps a column rather than shrinking it, so a two-column form on a
// narrow terminal spills sideways instead of stacking.
func TestRenderForm_StacksWhenTheColumnsDoNotFit(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	t.Cleanup(m.Close)
	addGuest(t, m, l, 2, "bob")

	// A height budget big enough for the whole roster either way, so the only thing
	// under test is the column layout.
	wide := m.renderForm(true, 200, 40)
	narrow := m.renderForm(true, 30, 40)

	assert.Greater(t, lg.Height(narrow), lg.Height(wide), "the narrow layout stacks the columns")
	assert.LessOrEqual(t, lg.Width(narrow), lg.Width(wide), "and never gets wider for it")
}

// The lobby is a full-screen view, so it has to fit every size the app claims to
// support, with the roster full rather than the one-player case that always fits.
func TestLobbyView_FitsTheTerminal(t *testing.T) {
	t.Parallel()
	for _, size := range []struct {
		name string
		w, h int
		// overflows marks the one size where this view is known to spill: View hands
		// its content callback the row budget and the lobby form ignores it, so six
		// guests plus an error line run past a 64x20 terminal and the terminal wraps
		// the overflow. Reported rather than fixed here; the width bound still holds.
		overflows bool
	}{
		{name: "the declared minimum", w: styles.MinWidth, h: styles.MinHeight},
		{name: "a stock terminal", w: 80, h: 24},
		{name: "a tall terminal", w: 120, h: 50},
	} {
		t.Run(size.name, func(t *testing.T) {
			t.Parallel()
			m, l := leaderView(t)
			t.Cleanup(m.Close)
			require.NoError(t, l.SetMaxPlayers(l.Leader(), 6, 2, 6))
			for i := 2; i <= 6; i++ {
				addGuest(t, m, l, uint64(i), fmt.Sprintf("player-number-%d", i))
			}
			m.global.Theme = styles.NewTheme(true)
			m.global.Width, m.global.Height = size.w, size.h
			m.actionErr = errors.New("live updates unavailable, rejoin the lobby")

			out := m.View().Content

			assert.LessOrEqual(t, lg.Width(out), size.w, "wider than the terminal")
			if !size.overflows {
				assert.LessOrEqual(t, lg.Height(out), size.h, "taller than the terminal")
			}
		})
	}
}

// The roster is the only part of this screen that grows, so it is what gives when the
// terminal cannot hold the form. Rendering past the frame instead hands the overflow to
// the terminal to wrap, which shifts every row above it - the lobby code included.
func TestCapRoster_TrimsToItsBudgetAndSaysHowManyItHid(t *testing.T) {
	t.Parallel()

	rows := []string{"heading", "leader", "guest1", "guest2", "guest3"}

	for _, tc := range []struct {
		name    string
		maxRows int
		want    []string
	}{
		{name: "everything fits", maxRows: 5, want: rows},
		{name: "more room than rows", maxRows: 9, want: rows},
		{name: "one row short", maxRows: 4, want: []string{"heading", "leader", "guest1", "  ... and 2 more"}},
		{name: "only the heading fits", maxRows: 1, want: []string{"  ... and 5 more"}},
		{name: "no room at all", maxRows: 0},
		{name: "a negative budget", maxRows: -3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A fresh copy each time: capRoster reslices in place, and a shared backing
			// array would let one case rewrite another's rows.
			got := capRoster(slices.Clone(rows), tc.maxRows)

			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, len(got), max(tc.maxRows, 0), "never more rows than it was given")
		})
	}
}
