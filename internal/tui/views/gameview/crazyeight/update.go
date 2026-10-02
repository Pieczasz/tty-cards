package crazyeight

import (
	"github.com/Pieczasz/terminal-card/internal/deck"
	logic "github.com/Pieczasz/terminal-card/internal/game/crazyeight"
	"github.com/Pieczasz/terminal-card/internal/tui/router"

	tea "charm.land/bubbletea/v2"
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, handled := m.HandleFrame(msg, m.syncState, nil); handled {
		return m, cmd
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleKey(key.String())
	}
	return m, nil
}

func (m *model) handleKey(key string) (tea.Model, tea.Cmd) {
	// esc closes the picker before it can mean leaving the table.
	if key == "esc" && m.suit.Open {
		m.suit.Open = false
		return m, nil
	}
	if cmd, ok := m.HandleLeaveKey(key); ok {
		return m, cmd
	}

	switch key {
	case "left", "h":
		m.step(-1, 0)
	case "right", "l":
		m.step(1, 0)
	case "up", "k":
		m.step(0, -1)
	case "down", "j":
		m.step(0, 1)
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if !m.suit.Open {
			m.SelectDigit(key)
		}
	case "enter":
		m.handleEnter()
	case "d":
		if m.Base.MyTurn && !m.suit.Open {
			_ = m.Submit(logic.ActionDrawCard{}) // kept in ActionErr, which the hero band renders
		}
	}
	return m, nil
}

// step moves the suit picker's cursor while it is open, and the hand cursor
// otherwise. Left/right walk a row of the picker grid, up/down move between rows.
func (m *model) step(dx, dy int) {
	if !m.suit.Step(dx, dy) {
		m.MoveCursor(dx)
	}
}

func (m *model) handleEnter() {
	card, ok := m.SelectedCard()
	if !m.Base.MyTurn || !ok {
		return
	}

	if m.suit.Open {
		if suit, picked := m.suit.Pick(); picked {
			_ = m.Submit(logic.ActionPlayCard{Card: card, ChosenSuit: suit})
		}
		return
	}

	if card.Rank == deck.Eight {
		m.suit.Show()
		return
	}
	_ = m.Submit(logic.ActionPlayCard{Card: card})
}

// Close comes from the embedded Session: the router replaces this view on navigation
// and the ssh layer closes it on disconnect, so the esc/enter paths are not enough.
var _ router.Closer = (*model)(nil)
