// Package uno is the Uno table view.
package uno

import (
	"image/color"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/uno"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/views/gameview"

	tea "charm.land/bubbletea/v2"
)

type model struct {
	gameview.Session

	currentColor deck.Suit
	direction    int8
	color        gameview.ChoicePicker
}

// colorPicker is the picker a wild opens, closed. Each model takes its own copy. Every
// cell is drawn in the colour it plays.
var colorPicker = gameview.ChoicePicker{
	Title: "Pick a color:",
	Choices: []gameview.Choice{
		{Label: "♥ Red", Suit: logic.ColorRed, Tone: func(t styles.Theme) color.Color { return t.UnoRed }},
		{Label: "♦ Yellow", Suit: logic.ColorYellow, Tone: func(t styles.Theme) color.Color { return t.UnoYellow }},
		{Label: "♣ Green", Suit: logic.ColorGreen, Tone: func(t styles.Theme) color.Color { return t.UnoGreen }},
		{Label: "♠ Blue", Suit: logic.ColorBlue, Tone: func(t styles.Theme) color.Color { return t.UnoBlue }},
	},
}

// New creates a Uno TUI view bound to the session player; slug is its catalog
// slug, the game_type its metrics carry.
func New(global router.GlobalContext, engine *game.Engine, slug string) tea.Model {
	// A subscribe failure is already in Session.ActionErr for the hero band.
	session, _ := gameview.NewSession(global, engine, slug)
	m := &model{Session: session, direction: 1, color: colorPicker}
	m.syncState()
	return m
}

func (m *model) syncState() {
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
		m.color.Open = false
	}
}
