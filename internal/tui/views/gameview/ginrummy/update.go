package ginrummy

import (
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/ginrummy"
	"github.com/Pieczasz/terminal-card/internal/tui/router"

	tea "charm.land/bubbletea/v2"
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, handled := m.HandleFrame(msg, m.syncState, nil); handled {
		return m, cmd
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleKey(key)
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.HandleLeaveKey(msg.String()); ok {
		return m, cmd
	}

	switch msg.String() {
	case "left", "h":
		m.MoveCursor(-1)
		return m, nil
	case "right", "l":
		m.MoveCursor(1)
		return m, nil
	case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9":
		m.SelectDigit(msg.String())
		return m, nil
	case "s":
		return m.submitIfTurn(logic.ActionDrawStock{})
	case "t":
		return m.submitIfTurn(logic.ActionDrawDiscard{})
	case "k":
		return m.handleKnock()
	case "enter":
		return m.handleEnter()
	}
	return m, nil
}

func (m *model) handleEnter() (tea.Model, tea.Cmd) {
	if m.handComplete && !m.matchComplete {
		if m.Base.MyTurn {
			return m.submit(logic.ActionNextHand{})
		}
		return m, nil
	}

	if !m.Base.MyTurn || len(m.Base.Hand) == 0 {
		return m, nil
	}
	card, ok := m.SelectedCard()
	if !ok || m.phase != logic.PhaseAwaitingDiscard {
		return m, nil
	}
	return m.submit(logic.ActionDiscard{Card: card})
}

func (m *model) handleKnock() (tea.Model, tea.Cmd) {
	card, ok := m.SelectedCard()
	if !m.Base.MyTurn || !ok || m.phase != logic.PhaseAwaitingDiscard {
		return m, nil
	}
	return m.submit(logic.ActionKnock{Discard: card})
}

func (m *model) submitIfTurn(action game.Action) (tea.Model, tea.Cmd) {
	if !m.Base.MyTurn {
		return m, nil
	}
	return m.submit(action)
}

func (m *model) submit(action game.Action) (tea.Model, tea.Cmd) {
	_ = m.Submit(action) // kept in ActionErr, which the hero band renders
	return m, nil
}

var _ router.Closer = (*model)(nil)
