package hearts

import (
	"errors"
	"maps"
	"slices"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/hearts"
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
	// "space", not " ": that is what KeyPressMsg.String() normalises the spacebar to,
	// and matching the literal meant the pass phase could not be played by hand at all
	// - the engine's 45-second auto-pass was the only way out of it.
	case "space":
		return m.handleSpace()
	case "enter":
		return m.handleEnter()
	}
	return m, nil
}

func (m *Model) handleSpace() (tea.Model, tea.Cmd) {
	if m.stage != logic.StagePassing || !m.Base.MyTurn {
		return m, nil
	}
	card, ok := m.SelectedCard()
	if !ok {
		return m, nil
	}
	if _, staged := m.passSelected[card]; staged {
		delete(m.passSelected, card)
		return m, nil
	}
	if len(m.passSelected) >= 3 {
		return m, nil
	}
	m.passSelected[card] = struct{}{}
	return m, nil
}

func (m *Model) handleEnter() (tea.Model, tea.Cmd) {
	if m.stage == logic.StageHandOver && !m.matchComplete {
		if m.Base.MyTurn {
			return m.submit(logic.ActionNextHand{})
		}
		return m, nil
	}

	if !m.Base.MyTurn || len(m.Base.Hand) == 0 {
		return m, nil
	}

	if m.stage == logic.StagePassing {
		return m.submitPass()
	}

	card, ok := m.SelectedCard()
	if !ok {
		return m, nil
	}
	return m.submit(logic.ActionPlayCard{Card: card})
}

func (m *Model) submitPass() (tea.Model, tea.Cmd) {
	if len(m.passSelected) != 3 {
		m.ActionErr = errNeedThreeCards
		return m, nil
	}
	return m.submit(logic.ActionPassCards{Cards: slices.Collect(maps.Keys(m.passSelected))})
}

var errNeedThreeCards = errors.New("select exactly 3 cards (space to toggle)")

func (m *Model) submit(action game.Action) (tea.Model, tea.Cmd) {
	if m.Submit(action) == nil {
		m.passSelected = map[deck.Card]struct{}{}
	}
	return m, nil
}

var _ router.Closer = (*Model)(nil)
