package lobbyview

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/elo"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/crazyeight"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Pieczasz/tty-cards/internal/testutil"
)

const (
	testGameSlug = "crazy_eights"
	testGameName = "Crazy Eights"
)

func testUser(id uint64, name string) *db.User {
	return &db.User{ID: testutil.UID(id), Username: name}
}

func testGames() []game.Module {
	return []game.Module{{
		Name:       testGameName,
		Slug:       testGameSlug,
		MinPlayers: crazyeight.MinPlayers,
		MaxPlayers: crazyeight.MaxPlayers,
		Factory:    func() game.Rules { return &crazyeight.Rules{} },
	}}
}

// newManager is a manager offering every game in the catalog, the way the server's is.
func newManager(tb testing.TB) *lobby.Manager {
	tb.Helper()
	return lobby.NewManager(tb.Context(), nil, catalog.NewRegistry())
}

// leaderView returns the lobby view as seen by the lobby's leader.
func leaderView(t *testing.T) (*model, *lobby.Lobby) {
	t.Helper()
	manager := newManager(t)
	leaderUser := testUser(1, "alice")
	leader := lobby.NewPlayer(leaderUser)

	l, err := manager.CreateLobby(leader,
		lobby.WithCardGame(testGameSlug),
		lobby.WithMaxPlayers(4),
		lobby.WithPrivate(false),
	)
	require.NoError(t, err)

	global := router.GlobalContext{
		Player:       leader,
		LobbyManager: manager,
		Games:        testGames(),
		Width:        120,
		Height:       40,
	}
	m, ok := New(global, l).(*model)
	require.True(t, ok)
	return m, l
}

// startGame seats a guest, readies the table and returns the start event the view's own
// feed delivered: that event is the only thing that can carry a table to the view.
func startGame(t *testing.T, m *model, l *lobby.Lobby) lobby.Event {
	t.Helper()
	guest := addGuest(t, m, l, 2, "bob")
	require.NoError(t, m.global.LobbyManager.ToggleReady(l.Snapshot().Leader.Player))
	require.NoError(t, m.global.LobbyManager.ToggleReady(guest))
	// Closing the engine is what ends the lobby's result watcher.
	t.Cleanup(l.ActiveGame().Close)
	for ev := range m.lobbyChan {
		if ev.Type == lobby.EventGameStarted {
			return ev
		}
	}
	t.Fatal("the lobby feed closed before the game started")
	return lobby.Event{}
}

func press(m *model, key string) (tea.Model, tea.Cmd) {
	return m.Update(tuitest.Key(key))
}

// routeOf runs a returned command and reports the route it navigates to.
func routeOf(t *testing.T, cmd tea.Cmd) router.Route {
	t.Helper()
	require.NotNil(t, cmd)
	change, ok := cmd().(router.ChangeViewMsg)
	require.True(t, ok, "command did not produce a ChangeViewMsg")
	return change.ViewName
}

// A seated player cannot leave for a menu: tui.New routes every menu back to this lobby,
// so a global shortcut would only tear the screen down and rebuild it. The lobby neither
// advertises nor handles them, and quitting still works.
func TestHandleKey_GlobalShortcutsAreNotOffered(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)

	out := tuitest.StripANSI(m.View().Content)
	assert.Contains(t, out, "ctrl+c - Quit")
	for _, action := range []string{"n - New Game", "f - Join Game", "p - Profile", "t - Leaderboard"} {
		assert.NotContains(t, out, action)
	}

	for _, key := range []string{"n", "f", "p", "t"} {
		_, cmd := press(m, key)
		assert.Nil(t, cmd, "%q must not navigate", key)
		assert.NotNil(t, m.lobbyChan, "%q must keep the lobby subscription", key)
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

	rulesMin, rulesMax := m.snap.MinPlayers, m.snap.MaxPlayers
	require.Equal(t, 2, rulesMin)
	require.Equal(t, 6, rulesMax)

	m.cursor = cursorMaxPlayers
	for range 10 {
		press(m, "l")
	}
	assert.Equal(t, rulesMax, m.snap.Limit)
	assert.Equal(t, rulesMax, l.Snapshot().Limit, "the lobby is updated, not just the view")

	for range 10 {
		press(m, "h")
	}
	assert.Equal(t, rulesMin, m.snap.Limit)
}

func TestAdjustSetting_TogglesVisibilityAndMode(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)

	m.cursor = cursorVisibility
	press(m, "l")
	assert.True(t, m.snap.Private)
	assert.True(t, l.Snapshot().Private)

	m.cursor = cursorMode
	press(m, "l")
	assert.True(t, m.snap.Ranked)
	assert.True(t, l.Snapshot().Ranked)
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
	assert.Contains(t, before, l.Snapshot().GameName, "the game row names the lobby's game")
}

func TestHandleLobbyEvent_ClosedGoesHome(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)

	_, cmd := m.Update(lobbyMsg{Type: lobby.EventLobbyClosed, src: m.lobbyChan})
	assert.Equal(t, router.RouteHome, routeOf(t, cmd))
	assert.Nil(t, m.lobbyChan)
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

// The leave confirmation answers only y, n and esc, so a footer still offering
// "r - Ready" under it advertised a key that did nothing.
func TestView_TheLeaveConfirmFooterOffersOnlyItsOwnKeys(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)
	t.Cleanup(m.Close)

	_, _ = press(m, "x")
	out := tuitest.StripANSI(m.View().Content)

	assert.NotContains(t, out, "r - Ready", "the confirmation ignores r")
	assert.NotContains(t, out, "x - Leave Lobby", "x does not answer the question")
	assert.Contains(t, out, "y - Leave")
	assert.Contains(t, out, "n - Stay")
	assert.Contains(t, out, "ctrl+c - Quit")
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
		tuitest.Key("j"),
	} {
		_, cmd = m.Update(next)
		assert.Nil(t, cmd, "the navigation is asked for once, not on every message")
	}
}

// addGuest seats another player in the lobby a view is already looking at, which is
// what turns on the rows, the cursor range and the kick key the leader-only tests need.
func addGuest(t *testing.T, m *model, l *lobby.Lobby, id uint64, name string) *game.Player {
	t.Helper()
	g := lobby.NewPlayer(testUser(id, name))
	_, err := m.global.LobbyManager.JoinLobbyByCode(t.Context(), l.Code(), g)
	require.NoError(t, err)
	rosterChanged(m)
	return g
}

// rosterChanged hands the view the event its feed would deliver for a change a test
// made to the lobby behind its back: the view draws from the snapshot it last took.
func rosterChanged(m *model) {
	m.Update(lobbyMsg{Type: lobby.EventPlayersUpdated, src: m.lobbyChan})
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

	require.NoError(t, l.SetPrivate(l.Snapshot().Leader.Player, true))

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

// A kick that lands between the join and the router building this view was broadcast
// before the view subscribed, so no event will ever tell it. Init has to check the seat
// itself, or the player sits on a roster they are not in until something else happens.
func TestInit_APlayerNoLongerSeatedGoesHome(t *testing.T) {
	t.Parallel()
	leaderV, l := leaderView(t)
	t.Cleanup(leaderV.Close)
	bobUser := testUser(2, "bob")
	bob := lobby.NewPlayer(bobUser)
	_, err := leaderV.global.LobbyManager.JoinLobbyByCode(t.Context(), l.Code(), bob)
	require.NoError(t, err)
	require.NoError(t, leaderV.global.LobbyManager.Kick(l.Snapshot().Leader.Player, bob))

	g := leaderV.global
	g.Player = bob
	m, ok := New(g, l).(*model)
	require.True(t, ok)
	t.Cleanup(m.Close)

	cmd := m.Init()

	assert.Nil(t, m.lobbyChan, "the feed of a lobby the player is not in is released")
	// Checked before running cmd: on failure it is the listener, which blocks.
	require.True(t, m.leaving, "Init asked to leave")
	assert.Equal(t, router.RouteHome, routeOf(t, cmd))
}

// viewAs builds this screen for the session seated as p, on the leader view's manager.
func viewAs(t *testing.T, leaderV *model, l *lobby.Lobby, p *game.Player) *model {
	t.Helper()
	g := leaderV.global
	g.Player = p
	m, ok := New(g, l).(*model)
	require.True(t, ok)
	t.Cleanup(m.Close)
	return m
}

// assertInitGoesHome runs Init and checks it took the player home, once, with the
// feed released.
func assertInitGoesHome(t *testing.T, m *model) {
	t.Helper()
	cmd := m.Init()
	assert.Nil(t, m.lobbyChan, "the feed of a lobby the player is not in is released")
	// Checked before running cmd: on failure it is the listener, which blocks.
	require.True(t, m.leaving, "Init asked to leave")
	assert.Equal(t, router.RouteHome, routeOf(t, cmd))
}

// A closed table unmaps its seats but keeps its roster, so the snapshot still lists
// the player while the feed refuses them: the live seat index is what says they are
// gone, not the roster.
func TestInit_AClosedTableGoesHome(t *testing.T) {
	t.Parallel()
	leaderV, l := leaderView(t)
	t.Cleanup(leaderV.Close)
	bobUser := testUser(2, "bob")
	bob := lobby.NewPlayer(bobUser)
	_, err := leaderV.global.LobbyManager.JoinLobbyByCode(t.Context(), l.Code(), bob)
	require.NoError(t, err)
	leaderV.global.LobbyManager.RemoveLobby(l.Code())

	assertInitGoesHome(t, viewAs(t, leaderV, l, bob))
}

// A sole leader whose grace ran out closes the table on the way out; a reconnect that
// was handed that table just before must not sit on its dead roster.
func TestInit_ASoleLeadersClosedTableGoesHome(t *testing.T) {
	t.Parallel()
	leaderV, l := leaderView(t)
	leaderV.Close()
	leader := l.Snapshot().Leader.Player
	leaderV.global.LobbyManager.LeaveLobby(leader)

	assertInitGoesHome(t, viewAs(t, leaderV, l, leader))
}

// A kick that lands after the view took its snapshot is not in it, and its broadcast
// never reaches the kicked player's feed, which the kick closes.
func TestInit_AKickAfterTheViewWasBuiltGoesHome(t *testing.T) {
	t.Parallel()
	leaderV, l := leaderView(t)
	t.Cleanup(leaderV.Close)
	bobUser := testUser(2, "bob")
	bob := lobby.NewPlayer(bobUser)
	_, err := leaderV.global.LobbyManager.JoinLobbyByCode(t.Context(), l.Code(), bob)
	require.NoError(t, err)
	m := viewAs(t, leaderV, l, bob)
	require.NoError(t, leaderV.global.LobbyManager.Kick(l.Snapshot().Leader.Player, bob))

	assertInitGoesHome(t, m)
}

// The kick closes the kicked player's feed before its roster event goes out, so that
// event never arrives: the feed ending is what has to take them home, or they sit on
// a roster that still lists them, every key answering "player not in lobby".
func TestFeedEnd_AKickedGuestGoesHome(t *testing.T) {
	t.Parallel()
	leaderV, l := leaderView(t)
	t.Cleanup(leaderV.Close)
	bobUser := testUser(2, "bob")
	bob := lobby.NewPlayer(bobUser)
	_, err := leaderV.global.LobbyManager.JoinLobbyByCode(t.Context(), l.Code(), bob)
	require.NoError(t, err)
	m := viewAs(t, leaderV, l, bob)
	require.NotNil(t, m.Init())
	listen := listenToLobbyBroadcaster(m.lobbyChan)

	require.NoError(t, leaderV.global.LobbyManager.Kick(l.Snapshot().Leader.Player, bob))

	_, cmd := m.Update(listen())
	assert.Equal(t, router.RouteHome, routeOf(t, cmd))
	assert.Nil(t, m.lobbyChan)
}

// A feed that ends while the player is still seated leaves a roster that will not
// move again, and the player is told so rather than left watching it.
func TestFeedEnd_StillSeatedSaysTheRosterStoppedUpdating(t *testing.T) {
	t.Parallel()
	m, _ := leaderView(t)
	t.Cleanup(m.Close)

	_, cmd := m.Update(lobbyMsg{src: m.lobbyChan, ended: true})

	assert.Nil(t, cmd)
	assert.False(t, m.leaving)
	assert.Nil(t, m.lobbyChan)
	require.ErrorIs(t, m.actionErr, errFeedLost)
	assert.Contains(t, m.View().Content, "rejoin the lobby")
}

// A nil lobby has nothing to wait in, so Init takes the player home rather than waiting
// for the first message to arrive.
func TestInit_WithoutALobbyGoesHome(t *testing.T) {
	t.Parallel()
	m, ok := New(router.GlobalContext{}, nil).(*model)
	require.True(t, ok)

	assert.Equal(t, router.RouteHome, routeOf(t, m.Init()))
}

// A reconnecting player whose seat survived the disconnect grace belongs back at the
// table, not on a roster screen for a game that is already running.
func TestInit_ReconnectIntoAnActiveGameRoutesToTheTable(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	guest := addGuest(t, m, l, 2, "bob")

	require.NoError(t, m.global.LobbyManager.ToggleReady(l.Snapshot().Leader.Player))
	require.NoError(t, m.global.LobbyManager.ToggleReady(guest))
	engine := l.ActiveGame()
	require.NotNil(t, engine)
	// The lobby watches the engine on a goroutine; closing it is what lets that
	// goroutine finish rather than parking for the rest of the run.
	t.Cleanup(engine.Close)

	cmd := m.Init()

	assert.Equal(t, router.GameRoute("crazy_eights"), routeOf(t, cmd))
	change, ok := cmd().(router.ChangeViewMsg)
	require.True(t, ok)
	assert.Equal(t, l.Snapshot().Leader.Player.ID, change.Payload.Table.PlayerID(), "the seat resumed is this session's own")
	assert.Nil(t, m.lobbyChan, "the lobby feed is released on the way to the table")
}

// The whole point of the lobby screen is that it hands the player over when the game
// starts; the payload is this session's own seat at the table that started, never the
// engine behind it.
func TestHandleLobbyEvent_GameStartedRoutesToTheGameView(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	started := startGame(t, m, l)

	_, cmd := m.Update(lobbyMsg{Event: started, src: m.lobbyChan})

	require.NotNil(t, cmd)
	change, ok := cmd().(router.ChangeViewMsg)
	require.True(t, ok)
	assert.Equal(t, router.GameRoute("crazy_eights"), change.ViewName)
	require.NotNil(t, change.Payload.Table)
	assert.Equal(t, l.Snapshot().Leader.Player.ID, change.Payload.Table.PlayerID(), "the seat is bound to this session's player")
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

	for name, evType := range map[string]lobby.EventType{
		"settings updated": lobby.EventSettingsUpdated,
		"players updated":  lobby.EventPlayersUpdated,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, l := leaderView(t)
			t.Cleanup(m.Close)

			require.NoError(t, l.SetPrivate(l.Snapshot().Leader.Player, true))
			require.NoError(t, l.SetRanked(l.Snapshot().Leader.Player, true))
			require.NoError(t, l.SetMaxPlayers(l.Snapshot().Leader.Player, 5))
			m.cursor = cursorFirstGuest + 4 // a row for a guest who has since left

			_, cmd := m.Update(lobbyMsg{Type: evType, src: m.lobbyChan})

			require.NotNil(t, cmd, "the listener stays armed")
			assert.True(t, m.snap.Private)
			assert.True(t, m.snap.Ranked)
			assert.Equal(t, 5, m.snap.Limit)
			assert.Equal(t, cursorMode, m.cursor, "with no guests the last row is the mode row")
		})
	}
}

// A roster event that no longer lists the player takes them home. Without the
// once-guard it would re-issue the navigation for every later event and the router
// would rebuild the home view each time.
func TestHandleLobbyEvent_PlayerNoLongerInTheRosterGoesHomeOnce(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	guest := addGuest(t, m, l, 2, "bob")

	global := m.global
	global.Player = lobby.NewPlayer(testUser(2, "bob"))
	guestModel, ok := New(global, l).(*model)
	require.True(t, ok)

	require.NoError(t, m.global.LobbyManager.Kick(l.Snapshot().Leader.Player, guest))

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
	global.Player = lobby.NewPlayer(testUser(2, "bob"))
	guestModel, ok := New(global, l).(*model)
	require.True(t, ok)
	t.Cleanup(guestModel.Close)

	for _, key := range []string{"j", "k", "l", "h"} {
		press(guestModel, key)
	}

	assert.Zero(t, guestModel.cursor, "a guest has no cursor to move")
	assert.False(t, l.Snapshot().Private, "and no settings to change")
	assert.Equal(t, 4, l.Snapshot().Limit)
}

func TestHandleKey_EnterKicksTheSelectedGuest(t *testing.T) {
	t.Parallel()

	t.Run("the leader removes a guest", func(t *testing.T) {
		t.Parallel()
		m, l := leaderView(t)
		t.Cleanup(m.Close)
		addGuest(t, m, l, 2, "bob")
		require.Len(t, l.Snapshot().Guests, 1)

		m.cursor = cursorFirstGuest
		press(m, "enter")

		assert.Empty(t, l.Snapshot().Guests)
	})

	// The cursor is clamped on roster events, but an enter racing a guest's departure
	// must not index past the list.
	t.Run("a guest row with nobody on it", func(t *testing.T) {
		t.Parallel()
		m, l := leaderView(t)
		t.Cleanup(m.Close)

		m.cursor = cursorFirstGuest
		assert.NotPanics(t, func() { press(m, "enter") })
		assert.Empty(t, l.Snapshot().Guests)
	})

	// A lobby torn down underneath the view still answers IsLeader, so the kick reaches
	// the manager and fails there. The view has to survive that, not crash on it.
	t.Run("the lobby is already gone", func(t *testing.T) {
		t.Parallel()
		m, l := leaderView(t)
		addGuest(t, m, l, 2, "bob")
		m.cursor = cursorFirstGuest
		// Tearing a lobby down is a server-side call the view itself never makes.
		m.global.LobbyManager.RemoveLobby(l.Code())

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
		assert.True(t, l.Snapshot().Leader.Ready)
	})

	t.Run("a player with no seat is told why", func(t *testing.T) {
		t.Parallel()
		m, l := leaderView(t)
		t.Cleanup(m.Close)

		global := m.global
		global.Player = lobby.NewPlayer(testUser(3, "carol")) // never joined
		outsider, ok := New(global, l).(*model)
		require.True(t, ok)
		t.Cleanup(outsider.Close)

		press(outsider, "r")

		require.Error(t, outsider.actionErr)
		assert.Contains(t, outsider.View().Content, outsider.actionErr.Error(),
			"the failure is on screen, not only in the log")
	})
}

// A rejected setting must leave the form where the lobby left it, or it shows a value
// the server never accepted.
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
				assert.Equal(t, 4, m.snap.Limit)
			},
		},
		{
			name: "visibility", cursor: cursorVisibility,
			check: func(t *testing.T, m *model) {
				t.Helper()
				assert.False(t, m.snap.Private)
			},
		},
		{
			name: "mode", cursor: cursorMode,
			check: func(t *testing.T, m *model) {
				t.Helper()
				assert.False(t, m.snap.Ranked)
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
	require.Equal(t, 3, l.Snapshot().Players())

	m.cursor = cursorMaxPlayers
	for range 5 {
		press(m, "h")
	}

	assert.Equal(t, 3, m.snap.Limit, "the floor is the roster, not the rules minimum")
	assert.Equal(t, 3, l.Snapshot().Limit)
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
	m.global.LobbyManager.RemoveLobby(l.Code())

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
	require.NoError(t, m.global.LobbyManager.ToggleReady(bob))
	rosterChanged(m)

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

func TestLobbyView_IdleExemptWhileTheTableIsFilling(t *testing.T) {
	t.Parallel()
	alone, _ := leaderView(t)
	t.Cleanup(alone.Close)
	assert.False(t, alone.IdleExempt(), "a solo host still times out")

	filling, l := leaderView(t)
	t.Cleanup(filling.Close)
	addGuest(t, filling, l, 2, "guest")
	assert.True(t, filling.IdleExempt(), "watching a filling roster is not idle")

	assert.False(t, (&model{}).IdleExempt(), "no lobby is a menu")
}

// The lobby is a full-screen view, so it has to fit every size the app claims to
// support, with the roster full rather than the one-player case that always fits.
func TestLobbyView_FitsTheTerminal(t *testing.T) {
	t.Parallel()
	for _, size := range tuitest.FitSizes {
		t.Run(size.Name, func(t *testing.T) {
			t.Parallel()
			m, l := leaderView(t)
			t.Cleanup(m.Close)
			require.NoError(t, l.SetMaxPlayers(l.Snapshot().Leader.Player, 6))
			for i := 2; i <= 6; i++ {
				addGuest(t, m, l, uint64(i), fmt.Sprintf("player-number-%d", i))
			}
			m.global.Theme = styles.NewTheme(true)
			m.global.Width, m.global.Height = size.Width, size.Height
			m.actionErr = errors.New("live updates unavailable, rejoin the lobby")

			out := m.View().Content

			assert.LessOrEqual(t, lg.Width(out), size.Width, "wider than the terminal")
			assert.LessOrEqual(t, lg.Height(out), size.Height, "taller than the terminal")
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
		focus   int
		want    []string
	}{
		{name: "everything fits", maxRows: 5, focus: -1, want: rows},
		{name: "more room than rows", maxRows: 9, focus: 4, want: rows},
		{name: "one row short", maxRows: 4, focus: -1, want: []string{"heading", "leader", "guest1", "  ... and 2 more"}},
		{name: "a visible focus", maxRows: 4, focus: 2, want: []string{"heading", "leader", "guest1", "  ... and 2 more"}},
		{name: "a hidden focus is drawn", maxRows: 4, focus: 4, want: []string{"heading", "leader", "guest3", "  ... and 2 more"}},
		{name: "a hidden focus in a tight budget", maxRows: 2, focus: 3, want: []string{"guest2", "  ... and 4 more"}},
		{name: "only the heading fits", maxRows: 1, focus: 3, want: []string{"  ... and 5 more"}},
		{name: "no room at all", maxRows: 0, focus: -1},
		{name: "a negative budget", maxRows: -3, focus: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// A fresh copy each time: capRoster reslices in place, and a shared backing
			// array would let one case rewrite another's rows.
			got := capRoster(slices.Clone(rows), tc.maxRows, tc.focus)

			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, len(got), max(tc.maxRows, 0), "never more rows than it was given")
		})
	}
}

// The fit check has to measure the gap it then lays out: measured four columns short,
// a roster row one ready mark too wide for the terminal was laid out side by side
// anyway, and lipgloss wrapped " - Ready" onto a line of its own under the settings.
func TestView_AReadyMarkStaysOnItsPlayersRowAt80x24(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	t.Cleanup(m.Close)
	require.NoError(t, l.SetMaxPlayers(l.Snapshot().Leader.Player, 6))
	for i := 2; i <= 4; i++ {
		g := addGuest(t, m, l, uint64(i), fmt.Sprintf("load_%04d", i))
		require.NoError(t, m.global.LobbyManager.ToggleReady(g))
	}
	rosterChanged(m)
	m.global.Theme = styles.NewTheme(true)
	m.global.Width, m.global.Height = 80, 24

	out := tuitest.StripANSI(m.View().Content)

	for i := 2; i <= 4; i++ {
		name := fmt.Sprintf("load_%04d", i)
		row := slices.IndexFunc(strings.Split(out, "\n"), func(line string) bool { return strings.Contains(line, name) })
		require.GreaterOrEqual(t, row, 0, "%s is on the roster", name)
		assert.Contains(t, strings.Split(out, "\n")[row], "- Ready", "%s's ready mark wrapped off its row:\n%s", name, out)
	}
}

// A ranked match written while the player sits here reseats them with a copy carrying
// the new ratings. The session's player has to follow, or the next screen - the join
// list's matchmaking, a new table's seat - is built from the login's ratings again.
func TestLobbyEvent_ReseatedPlayerUpdatesTheSession(t *testing.T) {
	t.Parallel()
	m, l := leaderView(t)
	t.Cleanup(m.Close)
	seat := l.Snapshot().Leader.Player
	m.global.Player = lobby.NewPlayer(testUser(1, "alice")) // the session's copy, before the reseat
	require.NotSame(t, seat, m.global.Player)

	_, cmd := m.Update(lobbyMsg{Type: lobby.EventPlayersUpdated, src: m.lobbyChan})

	require.NotNil(t, cmd)
	batch, ok := cmd().(tea.BatchMsg)
	require.True(t, ok, "the feed listener and the player update")
	require.Len(t, batch, 2)
	// batch[0] is the feed listener, which blocks until the next event.
	assert.Equal(t, router.PlayerUpdatedMsg{Player: seat}, batch[1]())
	assert.Same(t, seat, m.global.Player, "the view acts as the reseated player at once")

	assert.Nil(t, m.syncPlayer(), "an unchanged seat was announced again")
}

// minimumLobby is the leader's view at the declared minimum size with guests seated.
func minimumLobby(t *testing.T, guests ...string) *model {
	t.Helper()
	m, l := leaderView(t)
	t.Cleanup(m.Close)
	require.NoError(t, l.SetMaxPlayers(l.Snapshot().Leader.Player, 6))
	for i, name := range guests {
		addGuest(t, m, l, uint64(i+2), name)
	}
	m.global.Theme = styles.NewTheme(true)
	m.global.Width, m.global.Height = styles.MinWidth, styles.MinHeight
	return m
}

// The roster is the point of the waiting room. At 64x20 the stacked gap above it used
// to be kept while every seat hid behind "... and N more", with blank rows to spare.
func TestView_TheRosterShowsItsPlayersAtTheMinimumSize(t *testing.T) {
	t.Parallel()
	m := minimumLobby(t, "bob")

	out := tuitest.StripANSI(m.View().Content)

	assert.Contains(t, out, "alice", "the leader is hidden:\n%s", out)
	assert.Contains(t, out, "bob", "the guest is hidden:\n%s", out)
	assert.NotContains(t, out, "more", out)
	assert.LessOrEqual(t, lg.Height(out), styles.MinHeight, "taller than the terminal")
}

// Enter kicks the guest under the cursor, so that guest's row has to be on screen: at
// 64x20 the cursor used to walk onto a seat hidden behind "... and N more".
func TestView_TheSelectedGuestIsAlwaysDrawn(t *testing.T) {
	t.Parallel()
	guests := []string{"bob", "carol", "dave", "erin", "frank"}
	m := minimumLobby(t, guests...)

	for i, name := range guests {
		m.cursor = cursorFirstGuest + i
		out := tuitest.StripANSI(m.View().Content)
		lines := strings.Split(out, "\n")
		line := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, name) })
		require.GreaterOrEqual(t, line, 0, "the cursor is on %s, whose row is not drawn:\n%s", name, out)
		assert.Contains(t, lines[line], "> ", "%s's row carries the cursor", name)
		assert.LessOrEqual(t, lg.Height(out), styles.MinHeight, "taller than the terminal")
	}
}
