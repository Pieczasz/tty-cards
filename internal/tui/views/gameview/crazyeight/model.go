// Package crazyeight is the Crazy Eights table view.
package crazyeight

import (
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/crazyeight"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/views/gameview"

	tea "charm.land/bubbletea/v2"
)

type model struct {
	gameview.Session

	currentSuit deck.Suit
	suit        gameview.ChoicePicker
}

// suitPicker is the picker an eight opens, closed. Each model takes its own copy.
var suitPicker = gameview.ChoicePicker{
	Title: "Pick a suit:",
	Choices: []gameview.Choice{
		{Label: "♠ Spades", Suit: deck.Spades},
		{Label: "♥︎ Hearts", Suit: deck.Hearts},
		{Label: "♦ Diamonds", Suit: deck.Diamonds},
		{Label: "♣ Clubs", Suit: deck.Clubs},
	},
}

// New creates a new Crazy Eights TUI view bound to the session player.
func New(global router.GlobalContext, engine *game.Engine) tea.Model {
	// A subscribe failure is already in Session.ActionErr for the hero band.
	session, _ := gameview.NewSession(global, engine, "crazy eights")
	m := &model{Session: session, suit: suitPicker}
	m.syncState()
	return m
}

func (m *model) syncState() {
	m.Sync(func(state *game.State) {
		if s, ok := state.Extra.(*logic.State); ok {
			m.currentSuit = s.CurrentSuit
		}
	})
	// The picker only means anything while the hero is the one to act, so a turn
	// lost to the clock takes it down rather than leaving it over the table with
	// nothing left to confirm.
	if !m.Base.MyTurn {
		m.suit.Open = false
	}
}
