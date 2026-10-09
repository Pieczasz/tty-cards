// Package hearts is the Hearts table view: the four seats around the trick, the pass
// picker and the between-hands scores.
package hearts

import (
	"maps"
	"slices"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/hearts"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/views/gameview"

	tea "charm.land/bubbletea/v2"
)

// table is everything syncState lifts out of the engine for one frame. It is rebuilt
// whole on every sync and assigned once the lock is released, so nothing kept from it
// aliases the engine's state.
type table struct {
	phase            logic.Phase
	heartsBroken     bool
	trickCards       map[string]deck.Card
	handPoints       map[string]int
	cumulativeScores map[string]int
	handNumber       int
	passDirection    logic.PassDirection
	matchComplete    bool
	// leavers are who left, by name, for the end screen of a match a leave ended.
	leavers []string
}

// TopDiscard from the base state is unused - Hearts has no discard pile.
type model struct {
	gameview.Session
	table

	// passSelected holds the cards staged for ActionPassCards (space toggles). Keyed
	// by card, not by hand position: the engine re-deals and re-sorts the hand under
	// the view, and positions saved across that point to cards nobody picked.
	passSelected map[deck.Card]struct{}
}

// New creates a Hearts TUI view bound to the session player; slug is its catalog
// slug, the game_type its metrics carry.
func New(global router.GlobalContext, table *game.BoundEngine, slug string) tea.Model {
	// A subscribe failure is already in Session.ActionErr for the hero band.
	session, _ := gameview.NewSession(global, table, slug)
	m := &model{Session: session, passSelected: map[deck.Card]struct{}{}}
	m.syncState()
	return m
}

func (m *model) syncState() {
	var t table
	m.Sync(func(state *game.State) { t = readTable(state) })
	m.table = t
	m.prunePassSelection()
}

// readTable copies one frame of the table out of the live state, the maps cloned.
// Caller holds the engine lock.
func readTable(state *game.State) table {
	t := table{leavers: make([]string, 0, len(state.LeftPlayers))}
	for _, p := range state.LeftPlayers {
		t.leavers = append(t.leavers, p.Name)
	}
	s, ok := state.Extra.(*logic.State)
	if !ok {
		return t
	}
	t.phase = s.Phase
	t.heartsBroken = s.HeartsBroken
	t.trickCards = maps.Clone(s.TrickCards)
	t.handPoints = maps.Clone(s.HandPoints)
	t.cumulativeScores = maps.Clone(s.CumulativeScores)
	t.handNumber = s.HandNumber
	t.passDirection = s.PassDirection
	t.matchComplete = s.MatchComplete
	return t
}

// prunePassSelection drops staged cards that are no longer in the hand, and clears the
// staging outright once the pass is over.
func (m *model) prunePassSelection() {
	if m.phase != logic.PhasePassing {
		m.passSelected = map[deck.Card]struct{}{}
		return
	}
	maps.DeleteFunc(m.passSelected, func(c deck.Card, _ struct{}) bool {
		return !slices.Contains(m.Base.Hand, c)
	})
}

// passIndices maps the staged cards onto their positions in the hand as it stands
// right now, which is what the multi-select fan draws its markers from.
func (m *model) passIndices() map[int]struct{} {
	idx := make(map[int]struct{}, len(m.passSelected))
	for i, c := range m.Base.Hand {
		if _, ok := m.passSelected[c]; ok {
			idx[i] = struct{}{}
		}
	}
	return idx
}
