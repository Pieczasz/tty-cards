package ginrummy

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/ginrummy"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"
	"github.com/Pieczasz/terminal-card/internal/tui/views/gameview"

	"github.com/stretchr/testify/assert"
)

func TestUpdate_Navigation(t *testing.T) {
	t.Parallel()
	m := model{
		Base: gameview.BaseState{
			Hand: []deck.Card{
				{Rank: deck.Two, Suit: deck.Hearts},
				{Rank: deck.Three, Suit: deck.Clubs},
				{Rank: deck.Four, Suit: deck.Spades},
			},
			MyTurn: true,
			Phase:  game.Playing,
		},
		phase: logic.PhaseAwaitingDiscard,
	}

	msg := tuitest.Key("l")
	newM, _ := m.Update(msg)
	assert.Equal(t, 1, newM.(*model).Selected)

	msg = tuitest.Key("h")
	newM, _ = newM.Update(msg)
	assert.Equal(t, 0, newM.(*model).Selected)
}

func TestUpdate_NumberSelection(t *testing.T) {
	t.Parallel()
	m := model{
		Base: gameview.BaseState{Hand: make([]deck.Card, 10)}}
	msg := tuitest.Key("5")
	newM, _ := m.Update(msg)
	assert.Equal(t, 5, newM.(*model).Selected)
}
