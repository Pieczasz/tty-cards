package crazyeight

import (
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/crazyeight"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/router"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, handled := m.HandleFrame(msg, m.syncState, nil); handled {
		return m, cmd
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleKey(key)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// esc closes the picker before it can mean leaving the table.
	if msg.String() == "esc" && m.pickingSuit {
		m.pickingSuit = false
		return m, nil
	}
	if cmd, ok := m.HandleLeaveKey(msg.String()); ok {
		return m, cmd
	}

	switch msg.String() {
	case "left", "h":
		return m.step(-1, 0)
	case "right", "l":
		return m.step(1, 0)
	case "up", "k":
		return m.step(0, -1)
	case "down", "j":
		return m.step(0, 1)
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		return m.handleNumberSelection(msg.String())
	case "enter":
		return m.handleEnter()
	case "d":
		return m.handleDraw()
	}
	return m, nil
}

// step moves the suit picker's cursor while it is open, and the hand cursor
// otherwise. Left/right walk a row of the picker grid, up/down move between rows.
func (m *Model) step(dx, dy int) (tea.Model, tea.Cmd) {
	if m.pickingSuit {
		m.suitCursor = components.GridStep(m.suitCursor, len(suitChoices), dx, dy)
		return m, nil
	}
	m.MoveCursor(dx)
	return m, nil
}

func (m *Model) handleNumberSelection(key string) (tea.Model, tea.Cmd) {
	if !m.pickingSuit {
		m.SelectDigit(key)
	}
	return m, nil
}

func (m *Model) handleEnter() (tea.Model, tea.Cmd) {
	card, ok := m.SelectedCard()
	if !m.Base.MyTurn || !ok {
		return m, nil
	}

	if m.pickingSuit {
		return m.submitSuitPick(card)
	}

	if card.Rank == deck.Eight {
		m.pickingSuit = true
		m.suitCursor = 0
		return m, nil
	}

	return m.submit(logic.ActionPlayCard{Card: card})
}

func (m *Model) submitSuitPick(card deck.Card) (tea.Model, tea.Cmd) {
	if m.suitCursor < 0 || m.suitCursor >= len(suitChoices) {
		return m, nil
	}
	m.pickingSuit = false
	return m.submit(logic.ActionPlayCard{Card: card, Suit: suitChoices[m.suitCursor].suit})
}

func (m *Model) handleDraw() (tea.Model, tea.Cmd) {
	if !m.Base.MyTurn || m.pickingSuit {
		return m, nil
	}
	return m.submit(logic.ActionDrawCard{})
}

func (m *Model) submit(action game.Action) (tea.Model, tea.Cmd) {
	_ = m.Submit(action) // kept in ActionErr, which the hero band renders
	return m, nil
}

// Close comes from the embedded Session: the router replaces this view on navigation
// and the ssh layer closes it on disconnect, so the esc/enter paths are not enough.
var _ router.Closer = (*Model)(nil)
