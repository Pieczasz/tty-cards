package poker

import (
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/poker"
	"github.com/Pieczasz/terminal-card/internal/tui/router"

	tea "charm.land/bubbletea/v2"
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// The table has moved on, so a complaint about a move the player tried against
	// the old one has nothing left to refer to. Only on an event: clearing it on the
	// clock tick too would wipe the message inside a second of it being shown.
	clearErr := func() { m.ActionErr = nil }
	if cmd, handled := m.HandleFrame(msg, m.syncState, clearErr); handled {
		return m, cmd
	}
	if key, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleKey(key)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// esc takes the raise prompt down before it can mean leaving the table.
	if msg.String() == "esc" && m.raising {
		m.raising = false
		return m, nil
	}
	if cmd, ok := m.HandleLeaveKey(msg.String()); ok {
		return m, cmd
	}

	switch msg.String() {
	case "f":
		if m.canFold() {
			return m.submit(logic.ActionFold{})
		}
		return m, nil
	case "c":
		if m.canCheck() {
			return m.submit(logic.ActionCheck{})
		}
		if m.canCall() {
			return m.submit(logic.ActionCall{})
		}
		return m, nil
	case "a":
		if m.canAllIn() {
			m.raising = false
			return m.submit(logic.ActionAllIn{})
		}
		return m, nil
	case "r":
		return m.beginRaise()
	case "1", "2", "3", "4":
		m.addChip(msg.String())
		return m, nil
	case "[", "h":
		m.stepRaise(-1)
		return m, nil
	case "]", "l":
		m.stepRaise(+1)
		return m, nil
	case "enter":
		return m.confirm()
	}
	return m, nil
}

// addChip pushes one chip of the keyed denomination into the pending raise, which
// is how a raise is built up: start at the minimum, then stack chips on top.
func (m *Model) addChip(key string) {
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
func (m *Model) stepRaise(direction int) {
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

// confirm deals the next hand, leaves a finished match, or commits the pending raise.
func (m *Model) confirm() (tea.Model, tea.Cmd) {
	if m.matchComplete {
		// Separate statement on purpose: m is returned by value and Leave mutates it
		// through the pointer receiver; the order of those two in one return is unspecified.
		cmd := m.Leave()
		return m, cmd
	}
	if m.canDeal() {
		return m.submit(logic.ActionNextHand{})
	}
	if m.raising && m.Base.MyTurn {
		amount := m.raiseAmount
		m.raising = false
		return m.submit(logic.ActionRaiseTo{Amount: amount})
	}
	return m, nil
}

// beginRaise opens the raise prompt at the smallest legal raise, so every chip
// the player then adds is on top of an amount that is already valid.
func (m *Model) beginRaise() (tea.Model, tea.Cmd) {
	if !m.canRaise() {
		return m, nil
	}
	m.raising = true
	m.raiseAmount = m.raiseMin
	return m, nil
}

func (m *Model) submit(action game.Action) (tea.Model, tea.Cmd) {
	if m.Bound == nil || !m.Base.MyTurn {
		return m, nil
	}
	if err := m.Submit(action); err != nil {
		return m, nil
	}
	// Re-sync immediately so the turn indicator and actions reflect the applied
	// move without waiting for the broadcast event to round-trip.
	m.raising = false
	m.syncState()
	return m, nil
}

// Close comes from the embedded Session. Without it a mid-game disconnect never runs
// the esc/enter paths, so the listener goroutine stays parked on the event channel.
var _ router.Closer = (*Model)(nil)
