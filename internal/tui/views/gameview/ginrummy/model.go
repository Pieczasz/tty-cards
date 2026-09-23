// Package ginrummy is the Gin Rummy table view: the opponent, the stock and upcard,
// the hero's hand and the between-hands settle-up.
package ginrummy

import (
	"maps"

	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/ginrummy"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/views/gameview"

	tea "charm.land/bubbletea/v2"
)

// model is the Gin Rummy view of one seat at the table.
type model struct {
	gameview.Session

	phase            logic.Phase
	handNumber       int
	cumulativeScores map[string]int
	handComplete     bool
	matchComplete    bool
	lastHandResult   *logic.HandResult
}

// New creates a Gin Rummy TUI view bound to the session player.
func New(global router.GlobalContext, engine *game.Engine) tea.Model {
	// A subscribe failure is already in Session.ActionErr for the hero band.
	session, _ := gameview.NewSession(global, engine, "gin rummy")
	m := &model{
		Session:          session,
		cumulativeScores: map[string]int{},
	}
	m.syncState()
	return m
}

func (m *model) syncState() {
	m.Sync(func(state *game.State) {
		if s, ok := state.Extra.(*logic.State); ok {
			m.phase = s.Phase
			m.handNumber = s.HandNumber
			m.cumulativeScores = maps.Clone(s.CumulativeScores)
			m.handComplete = s.HandComplete()
			m.matchComplete = s.MatchComplete
			// Cloned: the view keeps rendering this after releasing the engine lock.
			m.lastHandResult = s.LastHandResult.Clone()
		}
	})
}
