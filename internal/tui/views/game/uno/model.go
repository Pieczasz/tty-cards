package uno

import (
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/uno"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	tea "charm.land/bubbletea/v2"
)

type Model struct {
	gameview.Session

	currentColor  deck.Suit
	direction     int8
	pickingColor  bool
	colorCursor   int
	lastActionErr error
}

// New creates a Uno TUI view bound to the session player.
func New(global router.GlobalContext, engine *game.Engine) tea.Model {
	session, err := gameview.NewSession(global, engine, "uno")
	m := &Model{Session: session, direction: 1, lastActionErr: err}
	m.syncState()
	return m
}

func (m *Model) syncState() {
	m.Sync(func(state *game.State) {
		if s, ok := state.Extra.(*logic.State); ok {
			m.currentColor = s.CurrentColor
			m.direction = s.Direction
		}
	})
	// The picker only means anything while the hero is the one to act, so a turn
	// lost to the clock takes it down rather than leaving it over the table with
	// nothing left to confirm.
	if !m.Base.MyTurn {
		m.pickingColor = false
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.Listen(), m.ClockTick())
}
