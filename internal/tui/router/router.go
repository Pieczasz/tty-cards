// Package router is the session's root Bubble Tea model: it owns the registered views,
// swaps the active one on a ChangeViewMsg, drops idle sessions and carries the
// per-session GlobalContext every view is built from.
package router

import (
	"context"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	tea "charm.land/bubbletea/v2"
)

// Route names a registered view.
type Route string

// Route keys for every registered view. Games are registered dynamically, so
// their routes are derived from the module slug via GameRoute.
const (
	RouteHome        Route = "home"
	RouteProfile     Route = "profile"
	RouteLeaderboard Route = "leaderboard"
	RouteLobby       Route = "lobby"
	RouteLobbyCreate Route = "lobby_create"
	RouteLobbyJoin   Route = "lobby_join"
)

const routeGamePrefix = "game_"

// GameRoute is the route a game's view is registered under, derived from its slug.
func GameRoute(slug string) Route {
	return Route(routeGamePrefix + slug)
}

// ViewFactory builds a view for a route. ctx is whatever the navigation carried: a
// *lobby.Lobby for the lobby route, a *game.Engine for a game route, nil otherwise.
type ViewFactory func(g GlobalContext, ctx any) tea.Model

// ChangeViewMsg asks the router to replace the active view with the one registered
// under ViewName, built with Context. An unknown route is ignored.
type ChangeViewMsg struct {
	ViewName Route
	Context  any
}

// Navigate is the command a view returns to move the session to route.
func Navigate(route Route, ctx any) tea.Cmd {
	return func() tea.Msg { return ChangeViewMsg{ViewName: route, Context: ctx} }
}

// GlobalContext is the session state every view is built from. Each view holds its own
// copy, so a change a view makes to it is local until the router rebuilds the view.
type GlobalContext struct {
	User           *db.User
	UserRepository db.UserRepository
	LobbyManager   *lobby.Manager
	GameRegistry   *game.Registry
	// SessionCtx is canceled when the SSH session ends.
	SessionCtx context.Context
	Width      int
	Height     int
	// Theme is per session, not global: two players can have opposite terminal
	// backgrounds, and a shared palette would leave one of them reading white on
	// white. It is resolved from tea.BackgroundColorMsg and defaults to dark.
	Theme styles.Theme
}

// RequestContext returns the session context, or Background if unset.
func (g GlobalContext) RequestContext() context.Context {
	if g.SessionCtx != nil {
		return g.SessionCtx
	}
	return context.Background()
}

const (
	// idleCheckInterval is how often the router asks whether the session went idle.
	idleCheckInterval = 10 * time.Second
	// idleTimeout is how long a session may send no input before it is dropped,
	// unless the active view is IdleExempt.
	idleTimeout = 5 * time.Minute
)

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(idleCheckInterval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Router is the session's root model. It holds one active view at a time and closes
// it (Closer) whenever it is replaced or the session ends.
type Router struct {
	Global       GlobalContext
	views        map[Route]ViewFactory
	active       tea.Model
	activeKey    Route
	initialRoute Route
	initialCtx   any
	lastActivity time.Time
}

func New(global GlobalContext) *Router {
	// Dark until the terminal says otherwise: most terminals are, and one that
	// never answers the background query must still be legible.
	global.Theme = styles.NewTheme(true)
	return &Router{
		Global:       global,
		views:        make(map[Route]ViewFactory),
		initialRoute: RouteHome,
		lastActivity: time.Now(),
	}
}

// SetInitialRoute picks the first view Init builds. A reconnecting player starts
// at their lobby instead of home; everyone else keeps the default.
func (r *Router) SetInitialRoute(name Route, ctx any) {
	r.initialRoute = name
	r.initialCtx = ctx
}

func (r *Router) Register(name Route, factory ViewFactory) {
	r.views[name] = factory
}

// HasRoute exists because Goto on an unknown route is a silent no-op.
func (r *Router) HasRoute(name Route) bool {
	_, ok := r.views[name]
	return ok
}

// Closer is implemented by views holding resources that outlive a render, such as
// an event-broadcaster subscription. The router releases them when the view is
// replaced or the session ends; without that, a player who disconnects mid-game
// leaves a listener goroutine parked and occupies a subscriber slot until the
// engine itself is closed.
type Closer interface {
	Close()
}

// IdleExempt is implemented by a view whose player may rightly send no input for a
// while: a seat watching other players act is not idle, and dropping it mid-game
// forfeits the match. Asked on every idle check, so a view answers for its current
// state - a game-over screen is exempt from nothing.
type IdleExempt interface {
	IdleExempt() bool
}

func (r *Router) activeIdleExempt() bool {
	e, ok := r.active.(IdleExempt)
	return ok && e.IdleExempt()
}

// closeActive releases the current view's resources if it holds any.
func (r *Router) closeActive() {
	if c, ok := r.active.(Closer); ok {
		c.Close()
	}
}

// Close releases the active view. Safe to call more than once, and safe to call
// on a router that never navigated anywhere.
func (r *Router) Close() {
	r.closeActive()
	r.active = nil
	r.activeKey = ""
}

func (r *Router) Goto(name Route, context any) tea.Cmd {
	factory, ok := r.views[name]
	if !ok {
		return nil
	}
	r.closeActive()
	r.active = factory(r.Global, context)
	r.activeKey = name
	return r.active.Init()
}

func (r *Router) Init() tea.Cmd {
	// Ask the terminal for its background so the theme can match it. Terminals
	// that don't answer simply leave the dark default in place.
	//
	// Goto is what runs a view's Init, so the first view is built here rather than at
	// construction: doing both armed every command twice, which for a view holding a
	// subscription is two listener goroutines racing for one channel.
	return tea.Batch(tick(), tea.RequestBackgroundColor, r.Goto(r.initialRoute, r.initialCtx))
}

func (r *Router) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var routerCmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg, tea.MouseMsg:
		r.lastActivity = time.Now()
	case tickMsg:
		if time.Since(r.lastActivity) > idleTimeout && !r.activeIdleExempt() {
			return r, tea.Quit
		}
		routerCmd = tick()
	case tea.WindowSizeMsg:
		r.Global.Width = msg.Width
		r.Global.Height = msg.Height
	case tea.BackgroundColorMsg:
		// Arrives on start and again whenever the user switches terminal theme
		// mid-session. Views hold their own copy of GlobalContext, so the message
		// falls through to the active view too (see views.HandleCommonMsg).
		r.Global.Theme = styles.NewTheme(msg.IsDark())
	case ChangeViewMsg:
		return r, r.Goto(msg.ViewName, msg.Context)
	}

	if r.active == nil {
		return r, routerCmd
	}
	var viewCmd tea.Cmd
	r.active, viewCmd = r.active.Update(msg)
	return r, tea.Batch(routerCmd, viewCmd)
}

func (r *Router) View() tea.View {
	var v tea.View
	switch {
	// Checked once here rather than in every view: below the minimum, lipgloss
	// wraps tables into unreadable confetti instead of failing, so no view can
	// render anything useful.
	case styles.TooSmall(r.Global.Width, r.Global.Height):
		v = tea.NewView(r.Global.Theme.RenderTooSmall(r.Global.Width, r.Global.Height))
	case r.active != nil:
		v = r.active.View()
	default:
		v = tea.NewView("No active view")
	}
	v.AltScreen = true
	return v
}
