// Package crazyeight is the Crazy Eights table view.
package crazyeight

import (
	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/crazyeight"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/views/gameview"

	tea "charm.land/bubbletea/v2"
)

type model struct {
	gameview.ShedTable

	currentSuit deck.Suit
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

var moves = gameview.ShedMoves{
	Wild: func(c deck.Card) bool { return c.Rank == deck.Eight },
	Play: func(c deck.Card, suit deck.Suit) game.Action { return logic.ActionPlayCard{Card: c, ChosenSuit: suit} },
	Draw: logic.ActionDrawCard{},
}

// New creates a new Crazy Eights TUI view bound to the session player; slug is its catalog
// slug, the game_type its metrics carry.
func New(global router.GlobalContext, table *game.BoundEngine, slug string) tea.Model {
	// A subscribe failure is already in Session.ActionErr for the hero band.
	session, _ := gameview.NewSession(global, table, slug)
	m := &model{Session: session, Picker: suitPicker}
	m.syncState()
	return m
}

func (m *model) syncState() {
	m.Sync(func(state *game.State) {
		if s, ok := state.Extra.(*logic.State); ok {
			m.currentSuit = s.CurrentSuit
		}
	})
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	cmd := m.Handle(msg, m.syncState, moves)
	return m, cmd
}

// Close comes from the embedded Session: the router replaces this view on navigation
// and the ssh layer closes it on disconnect, so the esc/enter paths are not enough.
var _ router.Closer = (*model)(nil)
