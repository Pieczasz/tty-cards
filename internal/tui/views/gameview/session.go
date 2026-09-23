// Package gameview is the baseline every table view embeds (Session) and the layout they
// share: the three-band frame, the seat zones, the hero's hand and the turn clock.
package gameview

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/observability"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/views"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

// EventMsg carries an engine event into the bubbletea loop. Source is the feed that
// delivered it: a listener in flight when the router replaced a view hands its event
// to the next view, and handling it there re-armed it beside that view's own listener.
type EventMsg struct {
	game.Event
	Source <-chan game.Event
}

// Session is the plumbing every game view repeats: the engine binding, the event
// subscription, the cached base state, the hand cursor, the forfeit prompt and Init.
// A view embeds it and adds its own rules rendering.
//
// An embedding view must call Close on navigation (router.Closer): skipping it parks a
// listener goroutine and burns a subscriber slot on the engine.
type Session struct {
	Global router.GlobalContext
	Bound  *game.BoundEngine
	Events <-chan game.Event
	// gameName labels the rejected-action metric with the name db.Game.Name stores.
	gameName string

	Base BaseState
	// Selected indexes Base.Hand and is clamped by Sync as the hand shrinks.
	Selected int

	// ActionErr is the last move the engine rejected, or why this view cannot play at
	// all. Submit keeps it, so the hero band renders it without a copy in every view.
	ActionErr error

	// confirmLeave is the armed forfeit prompt (HandleLeaveKey).
	confirmLeave bool
}

// NewSession binds engine to the session player and subscribes to its events. The error
// is for display, not a failure: an unsubscribed view still renders, it just never
// updates, so it tells the player to rejoin.
func NewSession(global router.GlobalContext, engine *game.Engine, gameName string) (Session, error) {
	playerID := views.SessionPlayerID(global)

	s := Session{Global: global, Bound: game.Bind(engine, playerID), gameName: gameName}
	if s.Bound == nil {
		return s, nil
	}

	ch, err := s.Bound.Subscribe()
	if err != nil {
		slog.Error("game view could not subscribe to engine events",
			"error", err, "game", gameName, "player_id", playerID)
		s.ActionErr = fmt.Errorf("live table updates unavailable, leave and rejoin: %w", err)
		return s, s.ActionErr
	}
	s.Events = ch
	return s, nil
}

// Init arms the event listener and the turn clock, which is all a table view's Init
// has to do.
func (s *Session) Init() tea.Cmd {
	return tea.Batch(s.Listen(), s.ClockTick())
}

// Listen delivers the next engine event as an EventMsg tagged with this session's feed.
func (s *Session) Listen() tea.Cmd {
	ch := s.Events
	return views.ListenOn(ch, func(ev game.Event) tea.Msg { return EventMsg{Event: ev, Source: ch} })
}

// ClockTick starts this session's countdown before any deadline is known, which is
// what a view's Init has to work with.
func (s *Session) ClockTick() tea.Cmd {
	return clockTickFrom(s.Events, 0, false)
}

// IdleRemoved is this session's own player losing their seat; anyone else's removal is
// just another state change.
func (s *Session) IdleRemoved(ev game.Event) bool {
	return ev.Type == game.EventPlayerIdle && s.Bound != nil && ev.PlayerID == s.Bound.PlayerID()
}

// Sync refreshes the cached engine state and keeps the cursor inside the hand. fn
// reads the live *State in the same lock hold; it is not redacted for this player, so
// anything derived from it that reaches the screen has to be filtered by the caller.
func (s *Session) Sync(fn func(*game.State)) {
	s.Base = syncBaseState(s.Bound, fn)
	if s.Selected >= len(s.Base.Hand) {
		s.MoveCursor(0)
	}
}

// HandleFrame runs the part of Update that is the same at every table: the common
// window, theme and quit keys, the engine's event feed and the turn-clock tick. sync is
// the view's own state refresh; onEvent runs before an event-driven sync. It reports
// whether the message was consumed, so a view falls through to its own key bindings.
func (s *Session) HandleFrame(msg tea.Msg, sync func(), onEvent func()) (tea.Cmd, bool) {
	if handled, cmd := views.HandleCommonMsg(msg, &s.Global); handled {
		return cmd, true
	}

	// Something another session armed is consumed and dropped, never re-armed: that
	// session's chain ended with it, and this one runs its own.
	switch msg := msg.(type) {
	case EventMsg:
		if msg.Source != s.Events {
			return nil, true
		}
		return s.handleEvent(msg.Event, sync, onEvent), true
	case ClockTickMsg:
		if msg.Source != s.Events {
			return nil, true
		}
		return s.handleClockTick(sync), true
	}

	return nil, false
}

func (s *Session) handleEvent(ev game.Event, sync func(), onEvent func()) tea.Cmd {
	if s.IdleRemoved(ev) {
		// Quitting ends the bubbletea program, which tears the ssh session down through
		// the ordinary leave path.
		return tea.Quit
	}

	wasPlaying := s.Base.Phase == game.Playing
	if onEvent != nil {
		onEvent()
	}
	sync()

	cmds := []tea.Cmd{s.Listen()}
	// A view built while the lobby was still waiting stops its tick chain on the first
	// tick, so the clock has to be re-armed once the table starts playing or it never
	// runs again for that player.
	if !wasPlaying && s.Base.Phase == game.Playing {
		cmds = append(cmds, clockTickFrom(s.Events, s.Base.TurnRemaining, s.Base.MyTurn))
	}
	return tea.Batch(cmds...)
}

func (s *Session) handleClockTick(sync func()) tea.Cmd {
	sync()
	if s.Base.Phase != game.Playing {
		return nil
	}
	return clockTickFrom(s.Events, s.Base.TurnRemaining, s.Base.MyTurn)
}

var errNotSeated = errors.New("you are not seated at this table")

// Submit sends action as this session's player and keeps the outcome in ActionErr, so
// an accepted move clears the last complaint. The error is rendered to the player
// as-is, hence no wrap.
func (s *Session) Submit(action game.Action) error {
	if s.Bound == nil {
		s.ActionErr = errNotSeated
		return s.ActionErr
	}
	s.ActionErr = s.Bound.Submit(action)
	if s.ActionErr != nil {
		// Background, not the session context: a rejection counts even when the
		// disconnect itself caused it.
		observability.ActionRejected(context.Background(), s.gameName)
	}
	return s.ActionErr
}

// SelectedCard is the card under the hand cursor, if the hand has one there.
func (s *Session) SelectedCard() (deck.Card, bool) {
	if s.Selected < 0 || s.Selected >= len(s.Base.Hand) {
		return deck.Card{}, false
	}
	return s.Base.Hand[s.Selected], true
}

// MoveCursor steps the hand cursor, stopping at either end.
func (s *Session) MoveCursor(delta int) {
	s.Selected = components.StepCursor(s.Selected, delta, len(s.Base.Hand)-1)
}

// SelectDigit moves the cursor to the card a number key names, so only the first ten
// cards are reachable this way.
func (s *Session) SelectDigit(key string) {
	if len(key) != 1 || key[0] < '0' || key[0] > '9' {
		return
	}
	if idx := int(key[0] - '0'); idx < len(s.Base.Hand) {
		s.Selected = idx
	}
}

// unsubscribe releases the engine subscription. Safe to call more than once.
func (s *Session) unsubscribe() {
	if s.Bound != nil && s.Events != nil {
		s.Bound.Unsubscribe(s.Events)
		s.Events = nil
	}
}

// Close implements router.Closer.
func (s *Session) Close() {
	s.unsubscribe()
}

// IdleExempt implements router.IdleExempt. A seat watching other players act is not
// idle, and the engine's own turn clock removes one that stopped playing; a game-over
// screen is a menu like any other.
func (s *Session) IdleExempt() bool {
	return s.Base.Phase == game.Playing
}

var _ router.IdleExempt = (*Session)(nil)

// HandleLeaveKey owns the keys that leave a table, and a view calls it before its own
// bindings - after closing any prompt of its own on esc (decision D-8). Mid-game one
// esc used to forfeit, a ranked loss on a stray key, so while playing esc only arms a
// confirmation: y then leaves, and any other key disarms and is swallowed rather than
// also playing a card. Once the game is over esc and enter leave at once. It reports
// whether the key was consumed.
func (s *Session) HandleLeaveKey(key string) (tea.Cmd, bool) {
	armed := s.confirmingLeave()
	s.confirmLeave = false
	switch {
	case armed && key == "y":
		return s.Leave(), true
	case armed:
		return nil, true
	case key == "esc" && s.Base.Phase == game.Playing:
		s.confirmLeave = true
		return nil, true
	case key == "esc", key == "enter" && s.Base.Phase == game.Finished:
		return s.Leave(), true
	}
	return nil, false
}

// confirmingLeave drops a prompt the game outlived: a finished table has nothing left
// to forfeit.
func (s *Session) confirmingLeave() bool {
	return s.confirmLeave && s.Base.Phase == game.Playing
}

// LeaveConfirmScreen is the forfeit prompt while it is armed; a view returns it from
// View before anything else. It takes the whole screen rather than a row of the table
// because every table already spends its full height, and the between-hands screens
// have no hero band to put a row in.
func (s *Session) LeaveConfirmScreen() (string, bool) {
	if !s.confirmingLeave() {
		return "", false
	}
	t := s.Global.Theme
	return renderGameNotice(s.Global, lg.JoinVertical(lg.Center,
		t.ErrorText.Render("Leave and forfeit this game?"),
		"",
		t.Muted.Render("y - leave and forfeit | any other key - keep playing"),
	)), true
}

// Leave navigates away from the table: back to the lobby once the game has finished,
// otherwise out of the lobby entirely, since leaving mid-game forfeits the seat.
func (s *Session) Leave() tea.Cmd {
	p := views.SessionPlayer(s.Global)
	finished := s.Base.Phase == game.Finished

	if p != nil && !finished {
		s.Global.LobbyManager.LeaveLobby(p)
	}
	s.unsubscribe()

	if p == nil || !finished {
		return router.Navigate(router.RouteHome, nil)
	}
	return router.Navigate(router.RouteLobby, s.Global.LobbyManager.FindLobbyByPlayer(p))
}
