package poker

import (
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/poker"
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
	// esc takes the raise prompt down before it can mean leaving the table.
	if msg.String() == components.KeyEsc && m.raising {
		m.raising = false
		return m, nil
	}
	if cmd, ok := m.HandleLeaveKey(msg.String()); ok {
		return m, cmd
	}

	switch msg.String() {
	case "f":
		return m.ifLegal(m.canFold(), logic.ActionFold{})
	case "c":
		if m.canCheck() {
			return m.submit(logic.ActionCheck{})
		}
		return m.ifLegal(m.canCall(), logic.ActionCall{})
	case "a":
		return m.ifLegal(m.canAllIn(), logic.ActionAllIn{})
	case "r":
		return m.beginRaise()
	case "1", "2", "3", "4":
		m.addChip(msg.String())
		return m, nil
	case "0", "[", "h", "left", "]", "l", "right":
		m.adjustRaise(msg.String())
		return m, nil
	case components.KeyEnter:
		return m.confirm()
	}
	return m, nil
}

// ifLegal submits action when the view's own check says it is legal now, and swallows
// the key otherwise: a fold on someone else's turn is not an error to show.
func (m *model) ifLegal(legal bool, action game.Action) (tea.Model, tea.Cmd) {
	if !legal {
		return m, nil
	}
	return m.submit(action)
}

// adjustRaise handles the keys that only shape a pending raise: the arrows and brackets
// step it, and 0 resets it (the chip keys only add, so without it the way to undo a
// stack of them was esc and starting over).
func (m *model) adjustRaise(key string) {
	switch key {
	case "0":
		if m.raising {
			m.raiseAmount = m.raiseMin
		}
	case "[", "h", "left":
		m.stepRaise(-1)
	default:
		m.stepRaise(+1)
	}
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

// confirm deals the next hand, leaves a finished match, or commits the pending raise.
func (m *model) confirm() (tea.Model, tea.Cmd) {
	if m.matchComplete {
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
func (m *model) beginRaise() (tea.Model, tea.Cmd) {
	if !m.canRaise() {
		return m, nil
	}
	m.raising = true
	m.raiseAmount = m.raiseMin
	return m, nil
}

func (m *model) submit(action game.Action) (tea.Model, tea.Cmd) {
	if m.HeroID() == "" || !m.Base.MyTurn {
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
var _ router.Closer = (*model)(nil)
