// Package router is the session's root Bubble Tea model: it owns the registered views,
// swaps the active one on a ChangeViewMsg, drops idle sessions and carries the
// per-session GlobalContext every view is built from.
package router

import (
	"context"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"

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

// Payload is what a navigation hands the view it builds. Each route reads the one field
// it takes, so a factory gets its argument already typed rather than casting an any:
// the wrong kind of value for a route is a compile error. The zero Payload is what every
// other route takes; a route whose field is nil falls back as its factory decides.
type Payload struct {
	// Lobby is RouteLobby's table.
	Lobby *lobby.Lobby
	// Table is a GameRoute's seat, already bound to the session's own player: a view
	// never holds the *game.Engine behind it.
	Table *game.BoundEngine
}

// ViewFactory builds a view for a route from what the navigation carried.
type ViewFactory func(g GlobalContext, p Payload) tea.Model

// ChangeViewMsg asks the router to replace the active view with the one registered
// under ViewName, built with Payload. An unknown route is ignored.
type ChangeViewMsg struct {
	ViewName Route
	Payload  Payload
}

// Navigate is the command a view returns to move the session to a route that takes no
// payload.
func Navigate(route Route) tea.Cmd {
	return navigate(route, Payload{})
}

// NavigateToLobby moves the session to l's waiting room.
func NavigateToLobby(l *lobby.Lobby) tea.Cmd {
	return navigate(RouteLobby, Payload{Lobby: l})
}

// NavigateToGame moves the session to the table view registered for slug, seated at
// table.
func NavigateToGame(slug string, table *game.BoundEngine) tea.Cmd {
	return navigate(GameRoute(slug), Payload{Table: table})
}

func navigate(route Route, p Payload) tea.Cmd {
	return func() tea.Msg { return ChangeViewMsg{ViewName: route, Payload: p} }
}

// PlayerUpdatedMsg replaces the session's player, in the router's GlobalContext and
// the active view's copy. See UpdatePlayer.
type PlayerUpdatedMsg struct{ Player *game.Player }

// UpdatePlayer is the command a view returns when the lobby has reseated the
// session's player with fresh ratings, so the screens built after it - and the
// matchmaking they ask for - read those rather than the login's.
func UpdatePlayer(p *game.Player) tea.Cmd {
	return func() tea.Msg { return PlayerUpdatedMsg{Player: p} }
}

// GlobalContext is the session state every view is built from. Each view holds its own
// copy, so a change a view makes to it is local until the router rebuilds the view.
type GlobalContext struct {
	User *db.User
	// Player is the session's seat, built once from User. Its ratings are the login's
	// until a ranked match reseats it with new ones: UpdatePlayer carries that copy
	// here and to the active view.
	Player       *game.Player
	Profiles     db.Profiles
	Leaderboard  db.Leaderboard
	LobbyManager *lobby.Manager
	// Games is every game on offer, in menu order: the list the lobby manager checks
	// a new table against, so a menu cannot offer what CreateLobby refuses.
	Games []game.Module
	// SessionCtx is canceled when the SSH connection ends, not this session: it
	// derives from s.Context(), which every channel on the connection shares, so a
	// closed session's context lives on while a sibling channel keeps the connection up.
	SessionCtx context.Context
	Width      int
	Height     int
	// Theme is per session, not global: two players can have opposite terminal
	// backgrounds, and a shared palette would leave one of them reading white on
	// white. It is resolved from tea.BackgroundColorMsg and defaults to dark.
	Theme *styles.Theme
}

// EnsureTheme fills the dark palette when the session has not resolved one.
// A render holds the pointer; the unset zero is nil, not a usable palette.
func (g *GlobalContext) EnsureTheme() {
	if g.Theme == nil {
		g.Theme = styles.NewTheme(true)
	}
}

// Palette is the session theme, or the shared dark one when none is set yet.
func (g GlobalContext) Palette() *styles.Theme {
	return styles.Or(g.Theme)
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
	lastActivity time.Time
}

// New is a router for one session, starting at RouteHome with no views registered.
func New(global GlobalContext) *Router {
	// Dark until the terminal says otherwise: most terminals are, and one that
	// never answers the background query must still be legible.
	global.Theme = styles.NewTheme(true)
	return &Router{
		Global:       global,
		views:        make(map[Route]ViewFactory),
		lastActivity: time.Now(),
	}
}

// Register makes factory the builder for name, replacing any earlier one.
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

// Goto closes the active view and builds name's with p. An unregistered route
// changes nothing and returns nil.
func (r *Router) Goto(name Route, p Payload) tea.Cmd {
	factory, ok := r.views[name]
	if !ok {
		return nil
	}
	r.closeActive()
	r.active = factory(r.Global, p)
	r.activeKey = name
	return r.active.Init()
}

// Init starts the idle clock, asks for the terminal background and builds the first view.
func (r *Router) Init() tea.Cmd {
	// Ask the terminal for its background so the theme can match it. Terminals
	// that don't answer simply leave the dark default in place.
	//
	// Goto is what runs a view's Init, so the first view is built here rather than at
	// construction: doing both armed every command twice, which for a view holding a
	// subscription is two listener goroutines racing for one channel.
	return tea.Batch(tick(), tea.RequestBackgroundColor, r.Goto(RouteHome, Payload{}))
}

// Update handles the session-wide messages and hands the rest to the active view.
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
	case PlayerUpdatedMsg:
		// Falls through to the active view as the theme does.
		r.Global.Player = msg.Player
	case ChangeViewMsg:
		cmd := r.Goto(msg.ViewName, msg.Payload)
		return r, cmd
	}

	if r.active == nil {
		return r, routerCmd
	}
	var viewCmd tea.Cmd
	r.active, viewCmd = r.active.Update(msg)
	return r, tea.Batch(routerCmd, viewCmd)
}

// View is the active view, or the too-small notice when the terminal cannot hold one.
func (r *Router) View() tea.View {
	var v tea.View
	switch {
	// Checked once here rather than in every view: below the minimum, lipgloss
	// wraps tables into unreadable confetti instead of failing, so no view can
	// render anything useful.
	case styles.TooSmall(r.Global.Width, r.Global.Height):
		v = tea.NewView(r.Global.Palette().RenderTooSmall(r.Global.Width, r.Global.Height))
	case r.active != nil:
		v = r.active.View()
	default:
		v = tea.NewView("No active view")
	}
	v.AltScreen = true
	return v
}
