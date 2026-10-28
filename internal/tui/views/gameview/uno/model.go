// Package uno is the Uno table view.
package uno

import (
	"image/color"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/uno"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/views/gameview"

	tea "charm.land/bubbletea/v2"
)

type model struct {
	gameview.ShedTable

	currentColor deck.Suit
	direction    int8
}

// colorPicker is the picker a wild opens, closed. Each model takes its own copy. Every
// cell is drawn in the colour it plays, and it is the one table of Uno's colours: the
// indicator and the hand's colour row read their tones from it too.
var colorPicker = gameview.ChoicePicker{
	Title: "Pick a color:",
	Choices: []gameview.Choice{
		{Label: "♥ Red", Suit: logic.ColorRed, Tone: func(t *styles.Theme) color.Color { return t.UnoRed }},
		{Label: "♦ Yellow", Suit: logic.ColorYellow, Tone: func(t *styles.Theme) color.Color { return t.UnoYellow }},
		{Label: "♣ Green", Suit: logic.ColorGreen, Tone: func(t *styles.Theme) color.Color { return t.UnoGreen }},
		{Label: "♠ Blue", Suit: logic.ColorBlue, Tone: func(t *styles.Theme) color.Color { return t.UnoBlue }},
	},
}

var moves = gameview.ShedMoves{
	Wild: logic.IsWild,
	Play: func(c deck.Card, chosen deck.Suit) game.Action {
		return logic.ActionPlayCard{Card: c, ChosenSuit: chosen}
	},
	Draw: logic.ActionDrawCard{},
}

// New creates a Uno TUI view bound to the session player; slug is its catalog
// slug, the game_type its metrics carry.
func New(global router.GlobalContext, table *game.BoundEngine, slug string) tea.Model {
	// A subscribe failure is already in Session.ActionErr for the hero band.
	session, _ := gameview.NewSession(global, table, slug)
	m := &model{Session: session, Picker: colorPicker, direction: 1}
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
}
