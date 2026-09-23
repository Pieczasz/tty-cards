// Package hearts is the Hearts table view: the four seats around the trick, the pass
// picker and the between-hands scores.
package hearts

import (
	"maps"
	"slices"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/hearts"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	tea "charm.land/bubbletea/v2"
)

// TopDiscard from the base state is unused - Hearts has no discard pile.
type model struct {
	gameview.Session

	// passSelected holds the cards staged for ActionPassCards (space toggles). Keyed
	// by card, not by hand position: the engine re-deals and re-sorts the hand under
	// the view, and positions saved across that point to cards nobody picked.
	passSelected map[deck.Card]struct{}

	phase            logic.Phase
	heartsBroken     bool
	trickCards       map[string]deck.Card
	handPoints       map[string]int
	cumulativeScores map[string]int
	handNumber       int
	passDirection    logic.PassDirection
	handComplete     bool
	matchComplete    bool
	lastTrickWinner  string
}

// New creates a Hearts TUI view bound to the session player.
func New(global router.GlobalContext, engine *game.Engine) tea.Model {
	// A subscribe failure is already in Session.ActionErr for the hero band.
	session, _ := gameview.NewSession(global, engine, "hearts")
	m := &model{
		Session:          session,
		passSelected:     map[deck.Card]struct{}{},
		trickCards:       map[string]deck.Card{},
		handPoints:       map[string]int{},
		cumulativeScores: map[string]int{},
	}
	m.syncState()
	return m
}

func (m *model) syncState() {
	m.Sync(func(state *game.State) {
		if s, ok := state.Extra.(*logic.State); ok {
			m.phase = s.Phase
			m.heartsBroken = s.HeartsBroken
			m.trickCards = maps.Clone(s.TrickCards)
			m.handPoints = maps.Clone(s.HandPoints)
			m.cumulativeScores = maps.Clone(s.CumulativeScores)
			m.handNumber = s.HandNumber
			m.passDirection = s.PassDirection
			m.handComplete = s.HandComplete()
			m.matchComplete = s.MatchComplete
			m.lastTrickWinner = s.LastTrickWinner
		}
	})
	m.prunePassSelection()
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
