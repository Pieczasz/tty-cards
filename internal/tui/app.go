// Package tui builds a session's Bubble Tea program: the router with every view
// registered, the games' routes derived from the catalog.
package tui

import (
	"context"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/views"
	"github.com/Pieczasz/tty-cards/internal/tui/views/home"
	"github.com/Pieczasz/tty-cards/internal/tui/views/leaderboard"
	"github.com/Pieczasz/tty-cards/internal/tui/views/lobby"
	"github.com/Pieczasz/tty-cards/internal/tui/views/profile"

	internallobby "github.com/Pieczasz/tty-cards/internal/lobby"

	tea "charm.land/bubbletea/v2"
)

// Deps is what New needs from the session and the server.
type Deps struct {
	SessionCtx   context.Context
	User         db.User
	Profiles     db.Profiles
	Leaderboard  db.Leaderboard
	LobbyManager *internallobby.Manager
	GameRegistry *game.Registry
}

// New builds the session's root model. It returns the router itself rather than a
// tea.Model: the ssh layer has to Close it when the session ends, and an interface
// value would hide the one method that releases the active view's subscription.
func New(deps Deps) *router.Router {
	global := router.GlobalContext{
		User:         &deps.User,
		Profiles:     deps.Profiles,
		Leaderboard:  deps.Leaderboard,
		LobbyManager: deps.LobbyManager,
		GameRegistry: deps.GameRegistry,
		SessionCtx:   deps.SessionCtx,
	}

	r := router.New(global)

	// Navigating away from a lobby unsubscribes but keeps the seat, so a player who
	// reached a menu would never see the game start - the engine would auto-play
	// until the idle timer took the seat.
	seatedOr := func(fallback func(router.GlobalContext) tea.Model) router.ViewFactory {
		return func(g router.GlobalContext, _ router.Payload) tea.Model {
			if l := g.LobbyManager.FindLobbyByPlayer(views.SessionPlayer(g)); l != nil {
				return lobby.New(g, l)
			}
			return fallback(g)
		}
	}

	r.Register(router.RouteHome, seatedOr(home.New))
	r.Register(router.RouteProfile, seatedOr(profile.New))
	r.Register(router.RouteLeaderboard, seatedOr(leaderboard.New))
	r.Register(router.RouteLobbyCreate, seatedOr(lobby.NewCreate))
	r.Register(router.RouteLobbyJoin, seatedOr(lobby.NewJoin))

	r.Register(router.RouteLobby, func(g router.GlobalContext, p router.Payload) tea.Model {
		if p.Lobby == nil {
			return home.New(g)
		}
		return lobby.New(g, p.Lobby)
	})

	registerGameViews(r)

	// No Goto here: the router builds its first view in Init, so that view's Init
	// runs exactly once. See Router.Init.
	return r
}

// ResumeSeat cancels the disconnect grace holding this session's seat, if any. The
// first view needs nothing more: home is built through seatedOr, which finds the seat
// and opens its lobby, and the lobby view routes onward into a running game. It is
// separate from New because the ssh layer may only call it once the session owns its
// tracker slot, and must run before the router's Init.
func ResumeSeat(r *router.Router) {
	r.Global.LobbyManager.ResumePlayer(views.SessionPlayer(r.Global))
}

// registerGameViews is where a seat reaches a view, so it is where the handle is held
// to the session's own player: a table bound to anyone else is not this session's to
// render or play.
func registerGameViews(r *router.Router) {
	for _, e := range catalog.Games {
		r.Register(router.GameRoute(e.Slug), func(g router.GlobalContext, p router.Payload) tea.Model {
			if p.Table == nil || p.Table.PlayerID() != views.SessionPlayerID(g) {
				return home.New(g)
			}
			return e.View(g, p.Table, e.Slug)
		})
	}
}
