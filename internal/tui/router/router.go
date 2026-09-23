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

// Route keys for every registered view. Games are registered dynamically, so
// their routes are derived from the module slug via GameRoute.
const (
	RouteHome        = "home"
	RouteProfile     = "profile"
	RouteLeaderboard = "leaderboard"
	RouteLobby       = "lobby"
	RouteLobbyCreate = "lobby_create"
	RouteLobbyJoin   = "lobby_join"
	RouteGamePrefix  = "game_"
)

func GameRoute(slug string) string {
	return RouteGamePrefix + slug
}

type ChangeViewMsg struct {
	ViewName string
	Context  any
}

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

type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(10*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

type Router struct {
	Global       GlobalContext
	views        map[string]func(GlobalContext, any) tea.Model
	active       tea.Model
	activeKey    string
	initialRoute string
	initialCtx   any
	lastActivity time.Time
}

func New(global GlobalContext) *Router {
	// Dark until the terminal says otherwise: most terminals are, and one that
	// never answers the background query must still be legible.
	global.Theme = styles.NewTheme(true)
	r := &Router{
		Global:       global,
		views:        make(map[string]func(GlobalContext, any) tea.Model),
		initialRoute: RouteHome,
		lastActivity: time.Now(),
	}
	return r
}

// SetInitialRoute picks the first view Init builds. A reconnecting player starts
// at their lobby instead of home; everyone else keeps the default.
func (r *Router) SetInitialRoute(name string, ctx any) {
	r.initialRoute = name
	r.initialCtx = ctx
}

func (r *Router) Register(name string, factory func(GlobalContext, any) tea.Model) {
	r.views[name] = factory
}

// HasRoute exists because Goto on an unknown route is a silent no-op.
func (r *Router) HasRoute(name string) bool {
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

func (r *Router) Goto(name string, context any) tea.Cmd {
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
	cmds := []tea.Cmd{tick(), tea.RequestBackgroundColor}
	// Goto is what runs a view's Init, so the first view is built here rather than at
	// construction: doing both armed every command twice, which for a view holding a
	// subscription is two listener goroutines racing for one channel.
	if cmd := r.Goto(r.initialRoute, r.initialCtx); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

func (r *Router) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyPressMsg, tea.MouseMsg:
		r.lastActivity = time.Now()
	case tickMsg:
		if time.Since(r.lastActivity) > 5*time.Minute && !r.activeIdleExempt() {
			return r, tea.Quit
		}
		cmds = append(cmds, tick())
	case tea.WindowSizeMsg:
		r.Global.Width = msg.Width
		r.Global.Height = msg.Height
	case tea.BackgroundColorMsg:
		// Arrives on start and again whenever the user switches terminal theme
		// mid-session. Views hold their own copy of GlobalContext, so the message
		// falls through to the active view too (see views.HandleCommonMsg).
		r.Global.Theme = styles.NewTheme(msg.IsDark())
	case ChangeViewMsg:
		cmd := r.Goto(msg.ViewName, msg.Context)
		cmds = append(cmds, cmd)
		return r, tea.Batch(cmds...)
	}

	if r.active != nil {
		var cmd tea.Cmd
		r.active, cmd = r.active.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}

	return r, tea.Batch(cmds...)
}

func (r *Router) View() tea.View {
	// Checked once here rather than in every view: below the minimum, lipgloss
	// wraps tables into unreadable confetti instead of failing, so no view can
	// render anything useful.
	if styles.TooSmall(r.Global.Width, r.Global.Height) {
		v := tea.NewView(r.Global.Theme.RenderTooSmall(r.Global.Width, r.Global.Height))
		v.AltScreen = true
		return v
	}
	if r.active != nil {
		v := r.active.View()
		v.AltScreen = true
		return v
	}
	v := tea.NewView("No active view")
	v.AltScreen = true
	return v
}
