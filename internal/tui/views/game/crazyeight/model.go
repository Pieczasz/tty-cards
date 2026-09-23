package crazyeight

import (
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/crazyeight"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	tea "charm.land/bubbletea/v2"
)

type Model struct {
	gameview.Session

	// Crazy Eights specific
	currentSuit   deck.Suit
	pickingSuit   bool
	suitCursor    int
	lastActionErr error
}

// New creates a new Crazy Eights TUI view bound to the session player.
func New(global router.GlobalContext, engine *game.Engine) tea.Model {
	session, err := gameview.NewSession(global, engine, "crazy eights")
	m := &Model{Session: session, lastActionErr: err}
	m.syncState()
	return m
}

func (m *Model) syncState() {
	m.Sync(func(state *game.State) {
		if s, ok := state.Extra.(*logic.State); ok {
			m.currentSuit = s.CurrentSuit
		}
	})
	// The picker only means anything while the hero is the one to act, so a turn
	// lost to the clock takes it down rather than leaving it over the table with
	// nothing left to confirm.
	if !m.Base.MyTurn {
		m.pickingSuit = false
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.Listen(), m.ClockTick())
}
