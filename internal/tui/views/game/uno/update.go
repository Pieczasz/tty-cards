package uno

import (
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/uno"
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
	if msg.String() == "esc" && m.pickingColor {
		m.pickingColor = false
		return m, nil
	}
	if cmd, ok := m.HandleLeaveKey(msg.String()); ok {
		return m, cmd
	}

	switch key := msg.String(); key {
	case "left", "h":
		return m.step(-1, 0)
	case "right", "l":
		return m.step(1, 0)
	case "up", "k":
		return m.step(0, -1)
	case "down", "j":
		return m.step(0, 1)
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if !m.pickingColor {
			m.SelectDigit(key)
		}
		return m, nil
	case "enter":
		return m.handleEnter()
	case "d":
		return m.handleDraw()
	}
	return m, nil
}

// step moves the colour picker's cursor while it is open, and the hand cursor
// otherwise. Left/right walk a row of the picker grid, up/down move between rows.
func (m *Model) step(dx, dy int) (tea.Model, tea.Cmd) {
	if m.pickingColor {
		m.colorCursor = components.GridStep(m.colorCursor, len(colorChoices), dx, dy)
		return m, nil
	}
	m.MoveCursor(dx)
	return m, nil
}

func (m *Model) handleEnter() (tea.Model, tea.Cmd) {
	card, ok := m.SelectedCard()
	if !m.Base.MyTurn || !ok {
		return m, nil
	}

	if m.pickingColor {
		return m.submitColorPick(card)
	}

	if card.Rank == logic.Wild || card.Rank == logic.WildDrawFour {
		m.pickingColor = true
		m.colorCursor = 0
		return m, nil
	}

	return m.submit(logic.ActionPlayCard{Card: card})
}

func (m *Model) submitColorPick(card deck.Card) (tea.Model, tea.Cmd) {
	if m.colorCursor < 0 || m.colorCursor >= len(colorChoices) {
		return m, nil
	}
	m.pickingColor = false
	return m.submit(logic.ActionPlayCard{
		Card:        card,
		ChosenColor: colorChoices[m.colorCursor].color,
	})
}

func (m *Model) handleDraw() (tea.Model, tea.Cmd) {
	if !m.Base.MyTurn || m.pickingColor {
		return m, nil
	}
	return m.submit(logic.ActionDrawCard{})
}

func (m *Model) submit(action game.Action) (tea.Model, tea.Cmd) {
	m.lastActionErr = m.Submit(action)
	return m, nil
}

var _ router.Closer = (*Model)(nil)
