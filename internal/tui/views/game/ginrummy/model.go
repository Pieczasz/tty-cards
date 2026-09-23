package ginrummy

import (
	"maps"

	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/ginrummy"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	tea "charm.land/bubbletea/v2"
)

type Model struct {
	gameview.Session

	handPhase        logic.Phase
	handNumber       int
	cumulativeScores map[string]int
	handComplete     bool
	matchComplete    bool
	lastHandResult   *logic.HandResult
	stockSize        int
	seatOrder        []string
	seatNames        map[string]string

	lastActionErr error
}

// New creates a Gin Rummy TUI view bound to the session player.
func New(global router.GlobalContext, engine *game.Engine) tea.Model {
	session, err := gameview.NewSession(global, engine, "gin rummy")
	m := &Model{
		Session:          session,
		cumulativeScores: map[string]int{},
		seatNames:        map[string]string{},
		lastActionErr:    err,
	}
	m.syncState()
	return m
}

func (m *Model) syncState() {
	m.Sync(func(state *game.State) {
		if s, ok := state.Extra.(*logic.State); ok {
			m.handPhase = s.HandPhase
			m.handNumber = s.HandNumber
			m.cumulativeScores = maps.Clone(s.CumulativeScores)
			m.handComplete = s.HandComplete
			m.matchComplete = s.MatchComplete
			// Cloned: the view keeps rendering this after releasing the engine lock.
			m.lastHandResult = s.LastHandResult.Clone()
		}
	})
	m.stockSize = m.Base.DeckSize
	m.seatOrder = m.Base.SeatOrder()
	m.seatNames = m.Base.SeatNames()
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.Listen(), m.ClockTick())
}
