package gameview

import (
	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/tui/components"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"

	tea "charm.land/bubbletea/v2"
)

// ShedTable is the table the shedding games share: a hand played one card at a time
// from the cursor, a draw key, and a wild card that opens Picker to name what it
// becomes. Crazy eights and uno embed it in place of Session and add their centre.
type ShedTable struct {
	Session
	Picker ChoicePicker
}

// ShedMoves is what a shedding game's keys submit.
type ShedMoves struct {
	// Wild reports whether a card needs the picker's answer before it is played.
	Wild func(deck.Card) bool
	Play func(card deck.Card, choice deck.Suit) game.Action
	Draw game.Action
}

// Sync is Session.Sync, and the picker goes down with the hero's turn: a turn lost to
// the clock must not leave it over the table with nothing left to confirm.
func (t *ShedTable) Sync(fn func(*game.State)) {
	t.Session.Sync(fn)
	if !t.Base.MyTurn {
		t.Picker.Open = false
	}
}

// Handle is a shedding view's whole Update: sync is its state refresh.
func (t *ShedTable) Handle(msg tea.Msg, sync func(), moves ShedMoves) tea.Cmd {
	if cmd, handled := t.HandleFrame(msg, sync); handled {
		return cmd
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		return t.handleKey(key.String(), moves)
	}
	return nil
}

func (t *ShedTable) handleKey(key string, moves ShedMoves) tea.Cmd {
	// esc closes the picker before it can mean leaving the table.
	if key == components.KeyEsc && t.Picker.Open {
		t.Picker.Open = false
		return nil
	}
	if cmd, ok := t.HandleLeaveKey(key); ok {
		return cmd
	}

	if dx, ok := components.HorizontalNudge(key); ok {
		t.step(dx, 0)
		return nil
	}
	if dy, ok := components.VerticalNudge(key); ok {
		t.step(0, dy)
		return nil
	}

	switch key {
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if !t.Picker.Open {
			t.SelectDigit(key)
		}
	case components.KeyEnter:
		t.play(moves)
	case "d":
		if t.Base.MyTurn && !t.Picker.Open {
			_ = t.Submit(moves.Draw) // kept in ActionErr, which the hero band renders
		}
	}
	return nil
}

// step moves the picker's cursor while it is open, and the hand cursor otherwise.
// Left/right walk a row of the picker grid, up/down move between rows.
func (t *ShedTable) step(dx, dy int) {
	if !t.Picker.Step(dx, dy) {
		t.MoveCursor(dx)
	}
}

func (t *ShedTable) play(moves ShedMoves) {
	card, ok := t.SelectedCard()
	if !t.Base.MyTurn || !ok {
		return
	}

	if t.Picker.Open {
		if choice, picked := t.Picker.Pick(); picked {
			_ = t.Submit(moves.Play(card, choice))
		}
		return
	}

	if moves.Wild(card) {
		t.Picker.Show()
		return
	}
	_ = t.Submit(moves.Play(card, deck.NoSuit))
}

// HandCursor is the card the hand lifts: none while the picker has the keys, since a
// highlight on a card the player cannot move reads as live.
func (t *ShedTable) HandCursor() int {
	if t.Picker.Open {
		return -1
	}
	return t.Selected
}

// RenderTable is a shedding view's whole View. center is the game's pile and
// indicators, which the open picker replaces; aboveHand is an optional row drawn over
// the hand (uno's colours).
func (t *ShedTable) RenderTable(center, aboveHand string) tea.View {
	if screen, ok := t.LeaveConfirmScreen(); ok {
		return tea.NewView(screen)
	}
	g := t.Global
	if t.Base.Phase != game.Playing {
		return tea.NewView(RenderWaitingScreen(g, t.Base.Phase, t.Base.WinnerName))
	}

	// Seat art is the first thing to go: it costs seven rows per seat, and a name
	// with a hand count says everything a player reads off somebody else's seat.
	minimalSeats := IsCompact(g.Width, g.Height)
	top := RenderOpponentTop(g.Theme, t.Base, g.Width, minimalSeats)
	if t.Picker.Open {
		center = t.Picker.Render(g.Theme)
	}

	return tea.NewView(RenderBands(g, top, t.renderHero(aboveHand), HandKeyHints, func(height int) string {
		left, right := RenderOpponentSides(g.Theme, t.Base, height, minimalSeats)
		return RenderTableRow(g.Width, height, left, styles.MarginTop(1, center), right)
	}))
}

func (t *ShedTable) renderHero(aboveHand string) string {
	g := t.Global
	status := RenderStatus(g.Theme, t.Base.CurrentPlayerName, t.Base.MyTurn, t.Base.TurnRemaining)
	hand := RenderHand(g.Theme, t.Base.Hand, t.HandCursor(), nil, HandWidth(g.Width), HandRows(g.Height))
	return RenderHeroBand(g.Theme, t.ActionErr, status, aboveHand, hand)
}
