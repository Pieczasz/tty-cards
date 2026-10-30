package poker

import (
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/poker"
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
	// esc takes the raise prompt down before it can mean leaving the table.
	if msg.String() == "esc" && m.raising {
		m.raising = false
		return nil
	}
	if cmd, ok := m.HandleLeaveKey(msg.String()); ok {
		return cmd
	}

	switch msg.String() {
	case "f":
		if m.canFold() {
			m.submit(logic.ActionFold{})
		}
	case "c":
		if m.canCheck() {
			m.submit(logic.ActionCheck{})
		} else if m.canCall() {
			m.submit(logic.ActionCall{})
		}
	case "a":
		if m.canAllIn() {
			m.raising = false
			m.submit(logic.ActionAllIn{})
		}
	case "r":
		m.beginRaise()
	case "1", "2", "3", "4":
		m.addChip(msg.String())
	case "[", "h":
		m.stepRaise(-1)
	case "]", "l":
		m.stepRaise(+1)
	case "enter":
		m.confirm()
	}
	return nil
}

// addChip pushes one chip of the keyed denomination into the pending raise, which
// is how a raise is built up: start at the minimum, then stack chips on top.
func (m *model) addChip(key string) {
	if !m.raising {
		return
	}
	value, ok := chipForKey(key)
	if !ok {
		return
	}
	m.raiseAmount = m.clampRaise(m.raiseAmount + value)
}

// stepRaise nudges the pending raise by the smallest chip, staying inside the
// legal range. It is a no-op unless the raise prompt is open.
func (m *model) stepRaise(direction int) {
	if !m.raising {
		return
	}
	step := smallestChip()
	if direction < 0 {
		// uint: only subtract when it would not wrap.
		if m.raiseAmount > step {
			m.raiseAmount -= step
		}
	} else {
		m.raiseAmount += step
	}
	m.raiseAmount = m.clampRaise(m.raiseAmount)
}

// confirm deals the next hand or commits the pending raise. Leaving a finished match
// is HandleLeaveKey's, the one leave path, so it is never a forfeit unasked.
func (m *model) confirm() {
	if m.canDeal() {
		m.submit(logic.ActionNextHand{})
		return
	}
	if m.raising && m.Base.MyTurn {
		amount := m.raiseAmount
		m.raising = false
		m.submit(logic.ActionRaiseTo{Amount: amount})
	}
}

// beginRaise opens the raise prompt at the smallest legal raise, so every chip
// the player then adds is on top of an amount that is already valid.
func (m *model) beginRaise() {
	if !m.canRaise() {
		return
	}
	m.raising = true
	m.raiseAmount = m.raiseMin
}

func (m *model) submit(action game.Action) {
	if m.HeroID() == "" || !m.Base.MyTurn {
		return
	}
	if err := m.Submit(action); err != nil {
		return
	}
	// Re-sync immediately so the turn indicator and actions reflect the applied
	// move without waiting for the broadcast event to round-trip.
	m.raising = false
	m.syncState()
}

// Close comes from the embedded Session. Without it a mid-game disconnect never runs
// the esc/enter paths, so the listener goroutine stays parked on the event channel.
var _ router.Closer = (*model)(nil)
