// Package views holds what every full-screen view shares: the session player, the
// global shortcut table, the common message handling and the framed screen layout.
package views

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

// RequestTimeout bounds one repository round trip a view makes on the player's behalf.
const RequestTimeout = 5 * time.Second

// SessionPlayer is the session user as a seat, or nil before the user is known. It
// is GlobalContext.Player, which tui.New builds once and UpdatePlayer keeps current; a
// context built without one - as tests do - seats User as it is.
func SessionPlayer(g router.GlobalContext) *game.Player {
	if g.Player != nil {
		return g.Player
	}
	return lobby.NewPlayer(g.User)
}

// errSomethingWentWrong is what the player reads for a failure that was not theirs:
// the detail is for the log, and on screen it would be noise they cannot act on.
var errSomethingWentWrong = errors.New("something went wrong, please try again")

// PlayerError is what to show for err, a lobby call's result. A refusal is the
// player's own doing and is shown as it is, unlogged, so a held key cannot flood the
// log; anything else is a failure, logged here - the lobby returns it unlogged - and
// shown as a generic line. nil stays nil.
func PlayerError(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, lobby.ErrRefused) {
		return err
	}
	slog.ErrorContext(ctx, "lobby request failed", "error", err)
	return errSomethingWentWrong
}

// SessionPlayerID is the seat key the engine and lobby know this user by. It reads the
// ID off the player rather than formatting the user ID again, since a subscription keyed
// on a different spelling silently never fires.
func SessionPlayerID(g router.GlobalContext) string {
	p := SessionPlayer(g)
	if p == nil {
		return ""
	}
	return p.ID
}

// ListenOn delivers the next value from ch as a message. A closed or absent channel
// returns nil rather than blocking, so a view that released its subscription just stops
// updating.
func ListenOn[T any](ch <-chan T, wrap func(T) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		if ch == nil {
			return nil
		}
		v, ok := <-ch
		if !ok {
			return nil
		}
		return wrap(v)
	}
}

// globalActions is every screen's shortcut table: the key, what the footer calls it
// and where it goes. The footer and the key handling both read it, so a shortcut
// cannot be advertised without working. esc and q are absent: they mean "back", and
// each view decides what back means for it.
var globalActions = []struct {
	key   string
	label string
	route router.Route
}{
	{"n", "New Game", router.RouteLobbyCreate},
	{"f", "Join Game", router.RouteLobbyJoin},
	{"p", "Profile", router.RouteProfile},
	{"t", "Leaderboard", router.RouteLeaderboard},
}

// quitAction is the one global action every screen offers: HandleCommonMsg owns it.
const quitAction = "ctrl+c - Quit"

// globalFooter is the footer entries for globalActions, plus quit. Shared by every
// session, so it is only ever read: see Footer.
var globalFooter = func() []string {
	out := make([]string, 0, len(globalActions)+1)
	for _, a := range globalActions {
		out = append(out, a.key+" - "+a.label)
	}
	return append(out, quitAction)
}()

// GlobalRoute is where a global shortcut key goes.
func GlobalRoute(key string) (router.Route, bool) {
	for _, a := range globalActions {
		if a.key == key {
			return a.route, true
		}
	}
	return "", false
}

// NavigateOn resolves the navigation keys every full-screen view shares: the global
// shortcuts, plus esc/q for "back to home". Views where esc means something else (the
// lobby's leave confirmation) call GlobalRoute directly instead.
func NavigateOn(key string) (tea.Cmd, bool) {
	if route, ok := GlobalRoute(key); ok {
		return router.Navigate(route), true
	}
	if key == "esc" || key == "q" {
		return router.Navigate(router.RouteHome), true
	}
	return nil, false
}

// Footer is the action line: a view's own actions, then the global ones.
//
// slices.Concat, never append: globalFooter is shared by every session, and appending
// writes into the array the others are reading.
func Footer(t *styles.Theme, localActions []string) string {
	return t.RenderActionFooter(slices.Concat(localActions, globalFooter))
}

// SeatedFooter is the action line for a player holding a seat: a view's own actions
// and quit. Every menu route sends a seated player back to their table (tui.New), so
// advertising the global shortcuts there would advertise keys that go nowhere.
func SeatedFooter(t *styles.Theme, localActions []string) string {
	return t.RenderActionFooter(append(slices.Clip(localActions), quitAction))
}

// HandleCommonMsg handles what every view handles the same way - resizes, the theme
// switch, a reseated session player and ctrl+c - and reports whether msg was one of
// them.
func HandleCommonMsg(msg tea.Msg, global *router.GlobalContext) (bool, tea.Cmd) {
	// The router updates its own copy for views built later; this one is on screen
	// now, so a resize or a theme switch lands without navigating away.
	if global.Apply(msg) {
		return true, nil
	}
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" {
		return true, tea.Quit
	}
	return false, nil
}

// RenderScreen is the header/footer/frame ritual every full-screen view repeats.
// content receives the rows left once the title and footer have taken theirs.
func RenderScreen(g router.GlobalContext, title string, localActions []string, content func(height int) string) string {
	return renderFramed(g, screenHeader(g, title), Footer(g.Palette(), localActions), content)
}

// RenderSeatedScreen is RenderScreen with SeatedFooter, for a player holding a seat.
func RenderSeatedScreen(g router.GlobalContext, title string, localActions []string, content func(height int) string) string {
	return renderFramed(g, screenHeader(g, title), SeatedFooter(g.Palette(), localActions), content)
}

func renderFramed(g router.GlobalContext, header, footer string, content func(height int) string) string {
	return RenderCenteredLayout(g, header, content(styles.AvailableContentHeight(g.Width, g.Height, header, footer)), footer)
}

// ScreenContentHeight is the height RenderScreen will hand its content callback for
// this title and these actions. A view that has to *page* its rows needs that budget
// in Update, before render time, so this is the one computation both share - deriving
// it twice is how a board ends up paging by twenty and drawing however many fit.
func ScreenContentHeight(g router.GlobalContext, title string, localActions []string) int {
	return styles.AvailableContentHeight(g.Width, g.Height, screenHeader(g, title), Footer(g.Palette(), localActions))
}

func screenHeader(g router.GlobalContext, title string) string {
	return g.Palette().Title.Render(styles.RenderFigureASCII(
		title, styles.InnerWidth(g.Width), styles.TitleHeightBudget(g.Height)))
}

// RenderCenteredLayout frames content for a full-screen view.
func RenderCenteredLayout(g router.GlobalContext, header, content, footer string) string {
	return styles.Place(
		g.Width, g.Height,
		lg.Center, lg.Center,
		g.Palette().RenderMainLayout(g.Width, g.Height, header, content, footer),
	)
}
