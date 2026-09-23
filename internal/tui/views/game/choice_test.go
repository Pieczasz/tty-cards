package game

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPicker() ChoicePicker {
	return ChoicePicker{Title: "Pick a suit:", Choices: []Choice{
		{Label: "Spades", Suit: deck.Spades},
		{Label: "Hearts", Suit: deck.Hearts},
		{Label: "Diamonds", Suit: deck.Diamonds},
		{Label: "Clubs", Suit: deck.Clubs},
	}}
}

// A closed picker must leave the arrow keys to the hand cursor, and draw nothing over
// the table.
func TestChoicePicker_ClosedTakesNoKeysAndDrawsNothing(t *testing.T) {
	t.Parallel()
	p := testPicker()

	assert.False(t, p.Step(1, 0))
	assert.Zero(t, p.Cursor)
	assert.Empty(t, p.Render(styles.NewTheme(true)))
}

func TestChoicePicker_PickCommitsTheCellUnderTheCursor(t *testing.T) {
	t.Parallel()
	p := testPicker()
	p.Show()

	require.True(t, p.Step(1, 0))
	require.True(t, p.Step(0, 1))
	out := p.Render(styles.NewTheme(true))
	for _, c := range p.Choices {
		assert.Contains(t, out, c.Label)
	}

	suit, ok := p.Pick()
	require.True(t, ok)
	assert.Equal(t, deck.Clubs, suit, "right then down lands on the fourth cell")
	assert.False(t, p.Open, "a pick closes the picker")
}

// GridStep keeps the cursor on the grid, but Pick checks again: a cursor off it must
// commit nothing rather than index past the choices.
func TestChoicePicker_PickIgnoresACursorOffTheGrid(t *testing.T) {
	t.Parallel()
	p := testPicker()
	p.Show()
	p.Cursor = len(p.Choices)

	_, ok := p.Pick()
	assert.False(t, ok)
	assert.True(t, p.Open, "nothing was committed, so the picker stays open")
}
