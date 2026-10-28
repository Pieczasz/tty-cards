package tui

import (
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"
	"github.com/Pieczasz/tty-cards/internal/tui/views"

	internallobby "github.com/Pieczasz/tty-cards/internal/lobby"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A game in the catalog with no route is a game the lobby can start and then fail to
// navigate to: the player sits in a lobby whose match has begun without them. The
// catalog's own test pins that every entry has a view; this pins that the view is
// actually reachable by the route the lobby derives from the slug.
func TestRegisterGameViews_EveryCatalogSlugResolves(t *testing.T) {
	t.Parallel()

	r := router.New(router.GlobalContext{})
	registerGameViews(r, catalog.Games)

	for _, e := range catalog.Games {
		assert.Truef(t, r.HasRoute(router.GameRoute(e.Slug)),
			"%s (slug %q) has no registered route", e.Name, e.Slug)
	}
}

// sessionModel builds the root model exactly as the ssh layer does, over a real lobby
// manager: every route factory reaches through LobbyManager, so a fake would only
// prove the fake works.
func sessionModel(t *testing.T) (*router.Router, *internallobby.Manager, *db.User) {
	t.Helper()

	manager := internallobby.NewManager(t.Context(), testutil.NopMatches{}, catalog.NewRegistry())
	user := &db.User{ID: testutil.UID(1), Username: "alice"}

	r := New(Deps{
		SessionCtx:   t.Context(),
		User:         *user,
		LobbyManager: manager,
		Games:        catalog.Games,
	})
	r.Global.Width, r.Global.Height = 120, 50
	return r, manager, user
}

// The menus and the routes read one list: a game offered with no route is a table the
// player creates and can never sit down at.
func TestNew_MenusOfferTheGamesItRoutes(t *testing.T) {
	t.Parallel()
	only := catalog.Games[1:2]

	r := New(Deps{User: db.User{ID: testutil.UID(1)}, Games: only})

	require.Len(t, r.Global.Games, 1)
	assert.Equal(t, only[0].Slug, r.Global.Games[0].Slug)
	assert.True(t, r.HasRoute(router.GameRoute(only[0].Slug)))
	assert.False(t, r.HasRoute(router.GameRoute(catalog.Games[0].Slug)), "a game not handed in has no route")
}

// The session's player is built once, so the seat a view takes and the copy it later
// compares that seat against are one pointer until a ranked match reseats it.
func TestNew_BuildsTheSessionPlayerOnce(t *testing.T) {
	t.Parallel()
	r := New(Deps{User: db.User{ID: testutil.UID(1), Username: "alice"}})

	require.NotNil(t, r.Global.Player)
	assert.Equal(t, testutil.UID(1), r.Global.Player.UserID)
	assert.Same(t, r.Global.Player, views.SessionPlayer(r.Global))
}

// Every route the views navigate to has to be registered, or Goto is a silent no-op
// and the player's key press does nothing at all.
func TestModel_RegistersEveryRoute(t *testing.T) {
	t.Parallel()
	r, _, _ := sessionModel(t)

	for _, route := range []router.Route{
		router.RouteHome,
		router.RouteProfile,
		router.RouteLeaderboard,
		router.RouteLobby,
		router.RouteLobbyCreate,
		router.RouteLobbyJoin,
	} {
		assert.Truef(t, r.HasRoute(route), "%s is not registered", route)
	}
}

// Navigating away from a lobby unsubscribes but keeps the seat, so a seated player who
// reached a menu would never see the game start - the engine would auto-play their
// turns until the idle timer took the seat. Every menu route bounces them back.
func TestModel_ASeatedPlayerIsBouncedBackToTheirLobby(t *testing.T) {
	t.Parallel()

	for _, route := range []router.Route{
		router.RouteHome,
		router.RouteLobbyCreate,
		router.RouteLobbyJoin,
		router.RouteProfile,
		router.RouteLeaderboard,
	} {
		t.Run(string(route), func(t *testing.T) {
			t.Parallel()
			r, manager, user := sessionModel(t)

			l, err := manager.CreateLobby(internallobby.NewPlayer(user), internallobby.WithCardGame(catalog.Games[0].Slug))
			require.NoError(t, err)
			t.Cleanup(func() { manager.LeaveLobby(internallobby.NewPlayer(user)) })

			r.Goto(route, router.Payload{})
			t.Cleanup(r.Close)

			assert.Contains(t, r.View().Content, l.Code(), "the lobby view names the code; home does not")
		})
	}
}

func TestModel_AnUnseatedPlayerGetsTheOrdinaryScreens(t *testing.T) {
	t.Parallel()
	r, _, _ := sessionModel(t)
	t.Cleanup(r.Close)

	r.Goto(router.RouteHome, router.Payload{})
	assert.True(t, greets(r.View().Content, "alice"), "home greets the player by name")
}

// The payload a route is handed comes from another view, so a missing one - or a seat
// bound to some other player - must land somewhere usable rather than panicking,
// rendering an empty screen or letting this session play another seat.
func TestModel_AWrongPayloadFallsBackToHome(t *testing.T) {
	t.Parallel()

	e := catalog.Games[0]
	rules := e.Factory()
	engine := game.NewEngine(rules, testutil.Players(e.MinPlayers))
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	for _, tc := range []struct {
		name    string
		route   router.Route
		payload router.Payload
	}{
		{name: "the lobby route without a lobby", route: router.RouteLobby},
		{name: "a game route without a seat", route: router.GameRoute(e.Slug)},
		{
			name:    "a game route with another player's seat",
			route:   router.GameRoute(e.Slug),
			payload: router.Payload{Table: game.Bind(engine, testutil.SeatID(2))},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, _, _ := sessionModel(t)
			t.Cleanup(r.Close)

			r.Goto(tc.route, tc.payload)
			assert.True(t, greets(r.View().Content, "alice"), "home is what a bad payload falls back to")
		})
	}
}

// A player who reconnects inside the disconnect grace still holds a seat at a match
// already in play, so the session has to open at their lobby - which routes onward
// into the game, seated as this session's player. A home screen would pretend nothing
// is happening while the engine auto-plays their hand.
//
// The router's first commands include its idle tick, so this runs in a synctest bubble
// where running that tick costs no real time.
func TestModel_AReconnectingPlayerResumesTheirSeat(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		manager := internallobby.NewManager(t.Context(), testutil.NopMatches{}, catalog.NewRegistry())

		user := &db.User{ID: testutil.UID(1), Username: "alice"}
		host := internallobby.NewPlayer(user)
		l, err := manager.CreateLobby(host,
			internallobby.WithCardGame(catalog.Games[0].Slug),
			internallobby.WithMaxPlayers(2),
		)
		require.NoError(t, err)
		// Closing the table closes its engine, which is what ends the lobby's watcher.
		t.Cleanup(func() { manager.RemoveLobby(l.Code()) })

		guest := &game.Player{ID: testutil.SeatID(2), UserID: testutil.UID(2), Name: "bob"}
		_, err = manager.JoinLobbyByCode(l.Code(), guest)
		require.NoError(t, err)
		require.NoError(t, manager.ToggleReady(host))
		require.NoError(t, manager.ToggleReady(guest))
		require.NotNil(t, l.Seat(host.ID), "the match has to be under way for the seat to survive")

		// The grace window is what keeps the seat; without it the drop is a forfeit.
		manager.DisconnectPlayer(host)

		r := New(Deps{
			SessionCtx:   t.Context(),
			User:         *user,
			LobbyManager: manager,
			Games:        catalog.Games,
		})
		r.Global.Width, r.Global.Height = 120, 50
		ResumeSeat(r)
		t.Cleanup(r.Close)

		change := navigationIn(t, r.Init())
		assert.Equal(t, router.GameRoute(catalog.Games[0].Slug), change.ViewName)
		require.NotNil(t, change.Payload.Table)
		assert.Equal(t, host.ID, change.Payload.Table.PlayerID(), "the seat is this session's and nobody else's")

		r.Update(change)
		out := r.View().Content
		assert.NotContains(t, out, l.Code(), "the session is back at the table, not the roster")
		assert.Contains(t, out, "bob", "the table shows the opponent")
	})
}

// navigationIn runs cmd, unpacking batches, and returns the one ChangeViewMsg it sends.
func navigationIn(t *testing.T, cmd tea.Cmd) router.ChangeViewMsg {
	t.Helper()
	var found []router.ChangeViewMsg
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			for _, inner := range msg {
				walk(inner)
			}
		case router.ChangeViewMsg:
			found = append(found, msg)
		}
	}
	walk(cmd)
	require.Len(t, found, 1)
	return found[0]
}

// Every route the session registers has to build a view that renders inside the
// terminal. A factory that panics or returns an oversized frame is a dead screen, and
// the router has no fallback once it has replaced the active view.
func TestModel_EveryRouteRendersInsideTheTerminal(t *testing.T) {
	t.Parallel()

	routes := []router.Route{
		router.RouteHome,
		router.RouteProfile,
		router.RouteLeaderboard,
		router.RouteLobbyCreate,
		router.RouteLobbyJoin,
	}

	for _, size := range tuitest.FitSizes {
		for _, route := range routes {
			t.Run(fmt.Sprintf("%dx%d_%s", size.Width, size.Height, route), func(t *testing.T) {
				t.Parallel()
				r, _, _ := sessionModel(t)
				r.Global.Width, r.Global.Height = size.Width, size.Height

				r.Goto(route, router.Payload{})
				t.Cleanup(r.Close)

				out := r.View().Content
				assert.NotEmpty(t, out)
				assert.LessOrEqual(t, lg.Width(out), size.Width)
				assert.LessOrEqual(t, lg.Height(out), size.Height)
			})
		}
	}
}

// The catalog is the single point of game registration, and a view constructor that
// cannot be built from a live engine is a match the lobby starts and nobody can join.
func TestModel_EveryCatalogGameBuildsItsViewFromAnEngine(t *testing.T) {
	t.Parallel()

	for _, e := range catalog.Games {
		t.Run(e.Slug, func(t *testing.T) {
			t.Parallel()

			rules := e.Factory()
			players := testutil.Players(e.MinPlayers)
			engine := game.NewEngine(rules, players)
			require.NoError(t, engine.Start())
			t.Cleanup(engine.Close)

			r, _, _ := sessionModel(t)
			r.Goto(router.GameRoute(e.Slug), router.Payload{Table: game.Bind(engine, views.SessionPlayerID(r.Global))})
			t.Cleanup(r.Close)

			out := r.View().Content
			assert.NotEmpty(t, out)
			assert.LessOrEqual(t, lg.Width(out), r.Global.Width)
			assert.LessOrEqual(t, lg.Height(out), r.Global.Height)
		})
	}
}

// greets reports whether screen is home greeting name, which it draws as a banner.
func greets(screen, name string) bool {
	plain := tuitest.StripANSI(screen)
	for row := range strings.Lines(styles.RenderFigureUncached(name, 200, 20)) {
		if row = strings.TrimRight(row, " \n"); strings.TrimSpace(row) != "" && !strings.Contains(plain, row) {
			return false
		}
	}
	return true
}
