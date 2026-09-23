package tui

import (
	"context"
	"fmt"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/catalog"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	internallobby "github.com/Pieczasz/terminal-card/internal/lobby"

	lg "charm.land/lipgloss/v2"
	"github.com/Pieczasz/terminal-card/internal/testutil"
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
	registerGameViews(r)

	for _, e := range catalog.All {
		assert.Truef(t, r.HasRoute(router.GameRoute(e.Slug)),
			"%s (slug %q) has no registered route", e.Name, e.Slug)
	}
}

// sessionModel builds the root model exactly as the ssh layer does, over a real lobby
// manager: every route factory reaches through LobbyManager, so a fake would only
// prove the fake works.
func sessionModel(t *testing.T) (*router.Router, *internallobby.Manager, *db.User) {
	t.Helper()

	manager := internallobby.NewManager(context.Background(), nil)
	user := &db.User{ID: testutil.UID(1), Username: "alice"}
	registry := game.NewRegistry()
	for _, e := range catalog.All {
		registry.RegisterModule(e.Module())
	}

	r := Model(ModelDependencies{
		SessionCtx:   context.Background(),
		User:         *user,
		LobbyManager: manager,
		GameRegistry: registry,
	})
	r.Global.Width, r.Global.Height = 120, 50
	return r, manager, user
}

// Every route the views navigate to has to be registered, or Goto is a silent no-op
// and the player's key press does nothing at all.
func TestModel_RegistersEveryRoute(t *testing.T) {
	t.Parallel()
	r, _, _ := sessionModel(t)

	for _, route := range []string{
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

	for _, route := range []string{
		router.RouteHome,
		router.RouteLobbyCreate,
		router.RouteLobbyJoin,
		router.RouteProfile,
		router.RouteLeaderboard,
	} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			r, manager, user := sessionModel(t)

			l, err := manager.New(internallobby.NewPlayer(user), internallobby.WithCardGame(catalog.All[0].Name))
			require.NoError(t, err)
			t.Cleanup(func() { manager.LeaveLobby(internallobby.NewPlayer(user)) })

			r.Goto(route, nil)
			t.Cleanup(r.Close)

			assert.Contains(t, r.View().Content, l.Code(), "the lobby view names the code; home does not")
		})
	}
}

func TestModel_AnUnseatedPlayerGetsTheOrdinaryScreens(t *testing.T) {
	t.Parallel()
	r, _, _ := sessionModel(t)
	t.Cleanup(r.Close)

	r.Goto(router.RouteHome, nil)
	assert.Contains(t, r.View().Content, "alice", "home greets the player by name")
}

// The context a route is handed comes from another view, so a mistyped one must land
// somewhere usable rather than panicking or rendering an empty screen.
func TestModel_AWrongContextFallsBackToHome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, route string }{
		{name: "the lobby route without a lobby", route: router.RouteLobby},
		{name: "a game route without an engine", route: router.GameRoute(catalog.All[0].Slug)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, _, _ := sessionModel(t)
			t.Cleanup(r.Close)

			r.Goto(tc.route, "not the context this route expects")
			assert.Contains(t, r.View().Content, "alice", "home is what a bad context falls back to")
		})
	}
}

// A player who reconnects inside the disconnect grace still holds a seat at a match
// already in play, so the session has to open at their lobby - which routes onward
// into the game. A home screen would pretend nothing is happening while the engine
// auto-plays their hand.
func TestModel_AReconnectingPlayerStartsAtTheirLobby(t *testing.T) {
	t.Parallel()

	manager := internallobby.NewManager(context.Background(), nil)
	registry := game.NewRegistry()
	for _, e := range catalog.All {
		registry.RegisterModule(e.Module())
	}

	user := &db.User{ID: testutil.UID(1), Username: "alice"}
	host := internallobby.NewPlayer(user)
	l, err := manager.New(host,
		internallobby.WithCardGame(catalog.All[0].Name),
		internallobby.WithMaxPlayers(2),
	)
	require.NoError(t, err)

	guest := &game.Player{ID: testutil.SeatID(2), UserID: testutil.UID(2), Name: "bob"}
	require.NoError(t, manager.JoinLobbyByCode(l.Code(), guest))
	require.NoError(t, l.ToggleReady(host, registry))
	require.NoError(t, l.ToggleReady(guest, registry))
	require.NotNil(t, l.ActiveGame(), "the match has to be under way for the seat to survive")

	// The grace window is what keeps the seat; without it the drop is a forfeit.
	manager.DisconnectPlayer(host)

	r := Model(ModelDependencies{
		SessionCtx:   context.Background(),
		User:         *user,
		LobbyManager: manager,
		GameRegistry: registry,
	})
	r.Global.Width, r.Global.Height = 120, 50
	ResumeSeat(r)
	r.Init()
	t.Cleanup(r.Close)

	// The lobby view's own Init routes straight on into the running game, so what is
	// on screen is the table - not the home banner a lost seat would have shown.
	assert.NotContains(t, r.View().Content, "Welcome", "a resumed session never opens at home")
}

// Every route the session registers has to build a view that renders inside the
// terminal. A factory that panics or returns an oversized frame is a dead screen, and
// the router has no fallback once it has replaced the active view.
func TestModel_EveryRouteRendersInsideTheTerminal(t *testing.T) {
	t.Parallel()

	routes := []string{
		router.RouteHome,
		router.RouteProfile,
		router.RouteLeaderboard,
		router.RouteLobbyCreate,
		router.RouteLobbyJoin,
	}

	for _, size := range []struct{ w, h int }{
		{styles.MinWidth, styles.MinHeight},
		{80, 24},
		{120, 50},
	} {
		for _, route := range routes {
			t.Run(fmt.Sprintf("%dx%d_%s", size.w, size.h, route), func(t *testing.T) {
				t.Parallel()
				r, _, _ := sessionModel(t)
				r.Global.Width, r.Global.Height = size.w, size.h

				r.Goto(route, nil)
				t.Cleanup(r.Close)

				out := r.View().Content
				assert.NotEmpty(t, out)
				assert.LessOrEqual(t, lg.Width(out), size.w)
				assert.LessOrEqual(t, lg.Height(out), size.h)
			})
		}
	}
}

// The catalog is the single point of game registration, and a view constructor that
// cannot be built from a live engine is a match the lobby starts and nobody can join.
func TestModel_EveryCatalogGameBuildsItsViewFromAnEngine(t *testing.T) {
	t.Parallel()

	for _, e := range catalog.All {
		t.Run(e.Slug, func(t *testing.T) {
			t.Parallel()

			rules := e.Rules()
			players := make([]*game.Player, 0, rules.MinPlayers())
			for i := range rules.MinPlayers() {
				players = append(players, &game.Player{
					ID: testutil.SeatID(i + 1), UserID: testutil.UID(i + 1), Name: fmt.Sprintf("p%d", i+1),
				})
			}
			engine := game.NewEngine(rules, players, rules.InitialDeck())
			require.NoError(t, engine.Start())
			t.Cleanup(engine.Close)

			r, _, _ := sessionModel(t)
			r.Goto(router.GameRoute(e.Slug), engine)
			t.Cleanup(r.Close)

			out := r.View().Content
			assert.NotEmpty(t, out)
			assert.LessOrEqual(t, lg.Width(out), r.Global.Width)
			assert.LessOrEqual(t, lg.Height(out), r.Global.Height)
		})
	}
}
