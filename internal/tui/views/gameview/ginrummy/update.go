package ginrummy

import (
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/ginrummy"
	"github.com/Pieczasz/tty-cards/internal/tui/router"

	tea "charm.land/bubbletea/v2"
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, handled := m.HandleFrame(msg, m.syncState); handled {
		return m, cmd
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		cmd := m.handleKey(key)
		return m, cmd
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	if cmd, ok := m.HandleLeaveKey(msg.String()); ok {
		return cmd
	}

	switch msg.String() {
	case "left", "h":
		m.MoveCursor(-1)
	case "right", "l":
		m.MoveCursor(1)
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		m.SelectDigit(msg.String())
	case "s":
		m.submitIfTurn(logic.ActionDrawStock{})
	case "t":
		m.submitIfTurn(logic.ActionDrawDiscard{})
	case "k":
		m.handleKnock()
	case "enter":
		m.handleEnter()
	}
	return nil
}

func (m *model) handleEnter() {
	if m.phase == logic.PhaseHandOver && !m.matchComplete {
		m.submitIfTurn(logic.ActionNextHand{})
		return
	}

	if !m.Base.MyTurn || len(m.Base.Hand) == 0 {
		return
	}
	if card, ok := m.SelectedCard(); ok && m.phase == logic.PhaseAwaitingDiscard {
		m.submit(logic.ActionDiscard{Card: card})
	}
}

func (m *model) handleKnock() {
	card, ok := m.SelectedCard()
	if !m.Base.MyTurn || !ok || m.phase != logic.PhaseAwaitingDiscard {
		return
	}
	m.submit(logic.ActionKnock{Discard: card})
}

func (m *model) submitIfTurn(action game.Action) {
	if m.Base.MyTurn {
		m.submit(action)
	}
}

func (m *model) submit(action game.Action) {
	// A refusal is kept in ActionErr, which the hero band renders. An accepted move is
	// read back at once rather than when its event arrives: until then the keys act on
	// the old phase, and a second enter on the hand-over sent a deal the engine refused.
	if m.Submit(action) == nil {
		m.syncState()
	}
}

var _ router.Closer = (*model)(nil)
