package gameview

import (
	"image/color"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/tui/components"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"

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

// Tone is optional per choice, and the grid's colours are positional: a choice with
// no tone must not shift the next one's colour onto its own cell. In uno the colour
// is the whole information.
func TestChoicePicker_RenderKeepsEachToneOnItsOwnCell(t *testing.T) {
	t.Parallel()
	th := styles.NewTheme(true)
	red := func(t *styles.Theme) color.Color { return t.SuitRed }
	gold := func(t *styles.Theme) color.Color { return t.Selection }

	p := ChoicePicker{Choices: []Choice{{Label: "A"}, {Label: "B", Tone: red}, {Label: "C"}, {Label: "D", Tone: gold}}}
	p.Show()

	want := components.GridPicker{
		Labels: []string{"A", "B", "C", "D"},
		Colors: []color.Color{th.TextMuted, th.SuitRed, th.TextMuted, th.Selection},
	}.Render(th)
	assert.Equal(t, want, p.Render(th))
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
