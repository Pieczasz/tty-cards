// Package ginrummy is the Gin Rummy table view: the opponent, the stock and upcard,
// the hero's hand and the between-hands settle-up.
package ginrummy

import (
	"maps"

	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/ginrummy"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/views/gameview"

	tea "charm.land/bubbletea/v2"
)

// table is everything syncState lifts out of the engine for one frame. It is rebuilt
// whole on every sync and assigned once the lock is released, so nothing kept from it
// aliases the engine's state.
type table struct {
	phase            logic.Phase
	handNumber       int
	cumulativeScores map[string]int
	matchComplete    bool
	lastHandResult   *logic.HandResult
}

// model is the Gin Rummy view of one seat at the table.
type model struct {
	gameview.Session
	table
}

// New creates a Gin Rummy TUI view bound to the session player; slug is its catalog
// slug, the game_type its metrics carry.
func New(global router.GlobalContext, table *game.BoundEngine, slug string) tea.Model {
	// A subscribe failure is already in Session.ActionErr for the hero band.
	session, _ := gameview.NewSession(global, table, slug)
	m := &model{Session: session}
	m.syncState()
	return m
}

func (m *model) syncState() {
	var t table
	m.Sync(func(state *game.State) { t = readTable(state) })
	m.table = t
}

// readTable copies one frame of the table out of the live state, the scores and the
// last hand's result cloned. Caller holds the engine lock.
func readTable(state *game.State) table {
	s, ok := state.Extra.(*logic.State)
	if !ok {
		return table{}
	}
	return table{
		phase:            s.Phase,
		handNumber:       s.HandNumber,
		cumulativeScores: maps.Clone(s.CumulativeScores),
		matchComplete:    s.MatchComplete,
		lastHandResult:   s.LastHandResult.Clone(),
	}
}
