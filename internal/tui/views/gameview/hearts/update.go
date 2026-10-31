package hearts

import (
	"errors"
	"maps"
	"slices"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/hearts"
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
	// "space", not " ": that is what KeyPressMsg.String() normalises the spacebar to,
	// and matching the literal meant the pass phase could not be played by hand at all
	// - the engine's 45-second auto-pass was the only way out of it.
	case "space":
		m.handleSpace()
	case "enter":
		m.handleEnter()
	}
	return nil
}

func (m *model) handleSpace() {
	if m.phase != logic.PhasePassing || !m.Base.MyTurn {
		return
	}
	card, ok := m.SelectedCard()
	if !ok {
		return
	}
	if _, staged := m.passSelected[card]; staged {
		delete(m.passSelected, card)
		return
	}
	if len(m.passSelected) >= 3 {
		return
	}
	m.passSelected[card] = struct{}{}
}

func (m *model) handleEnter() {
	if m.phase == logic.PhaseHandOver && !m.matchComplete {
		if m.Base.MyTurn {
			m.submit(logic.ActionNextHand{})
		}
		return
	}

	if !m.Base.MyTurn || len(m.Base.Hand) == 0 {
		return
	}

	if m.phase == logic.PhasePassing {
		m.submitPass()
		return
	}

	if card, ok := m.SelectedCard(); ok {
		m.submit(logic.ActionPlayCard{Card: card})
	}
}

func (m *model) submitPass() {
	if len(m.passSelected) != 3 {
		m.Refuse(errNeedThreeCards)
		return
	}
	m.submit(logic.ActionPassCards{Cards: slices.Collect(maps.Keys(m.passSelected))})
}

var errNeedThreeCards = errors.New("select exactly 3 cards (space to toggle)")

func (m *model) submit(action game.Action) {
	if m.Submit(action) == nil {
		m.passSelected = map[deck.Card]struct{}{}
	}
}

var _ router.Closer = (*model)(nil)
