package hearts

import (
	"errors"
	"maps"
	"slices"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/hearts"
	"github.com/Pieczasz/tty-cards/internal/tui/components"
	"github.com/Pieczasz/tty-cards/internal/tui/router"

	tea "charm.land/bubbletea/v2"
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, handled := m.HandleFrame(msg, m.syncState); handled {
		return m, cmd
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleKey(key)
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.HandleHandKey(msg.String()); ok {
		return m, cmd
	}

	switch msg.String() {
	// KeySpace, not " ": matching the literal meant the pass phase could not be played
	// by hand at all - the engine's 45-second auto-pass was the only way out of it.
	case components.KeySpace:
		return m.handleSpace()
	case components.KeyEnter:
		return m.handleEnter()
	}
	return m, nil
}

func (m *model) handleSpace() (tea.Model, tea.Cmd) {
	if m.phase != logic.PhasePassing || !m.Base.MyTurn {
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

func (m *model) handleEnter() (tea.Model, tea.Cmd) {
	if m.phase == logic.PhaseHandOver && !m.matchComplete {
		if m.Base.MyTurn {
			return m.submit(logic.ActionNextHand{})
		}
		return m, nil
	}

	if !m.Base.MyTurn || len(m.Base.Hand) == 0 {
		return m, nil
	}

	if m.phase == logic.PhasePassing {
		return m.submitPass()
	}

	card, ok := m.SelectedCard()
	if !ok {
		return m, nil
	}
	return m.submit(logic.ActionPlayCard{Card: card})
}

func (m *model) submitPass() (tea.Model, tea.Cmd) {
	if len(m.passSelected) != 3 {
		m.Refuse(errNeedThreeCards)
		return m, nil
	}
	return m.submit(logic.ActionPassCards{Cards: slices.Collect(maps.Keys(m.passSelected))})
}

var errNeedThreeCards = errors.New("select exactly 3 cards (space to toggle)")

func (m *model) submit(action game.Action) (tea.Model, tea.Cmd) {
	if m.Submit(action) == nil {
		m.passSelected = map[deck.Card]struct{}{}
	}
	return m, nil
}

var _ router.Closer = (*model)(nil)
