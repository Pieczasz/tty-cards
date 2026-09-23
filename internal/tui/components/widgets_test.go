package components

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A thirteen-card hand is over a hundred columns of card art and a terminal is
// admitted at 64, so the fan has to tighten and then give up. Whatever it settles on
// has to fit the budget it was given.
func TestFanTuck_FitsTheWidthItIsGiven(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	for _, n := range []int{1, 5, 7, 13, 25, 52} {
		for _, width := range []int{62, 78, 98, 118} {
			t.Run(fmt.Sprintf("cards=%d/width=%d", n, width), func(t *testing.T) {
				t.Parallel()

				hand := make([]deck.Card, 0, n)
				for i := range n {
					hand = append(hand, deck.Card{
						Rank: deck.AllRanks[i%len(deck.AllRanks)],
						Suit: deck.Spades,
					})
				}

				tuck := FanTuck(n, width)
				if tuck == 0 {
					strip := RenderStrip(theme, hand, nil, 0, width)
					assert.LessOrEqual(t, lg.Width(strip), width, "the strip is the last resort and must fit")
					return
				}

				// Worst case: the picked-out card is in the middle of the hand, where
				// it keeps its own closing border.
				for _, selected := range []int{-1, 0, n / 2, n - 1} {
					fan := RenderFan(theme, hand, Selection(selected), tuck)
					assert.LessOrEqualf(t, lg.Width(fan), width,
						"a fan with card %d picked out overran its budget", selected)
				}
			})
		}
	}
}

// A covered card has to keep its suit, or a hand of tucked cards cannot be played
// from. The suit of an ace is a single pip on the centre column.
func TestFanTuck_NeverHidesTheSuit(t *testing.T) {
	t.Parallel()

	assert.GreaterOrEqual(t, minTuckWidth, centreColumn+1,
		"the tightest tuck still has to show the centre pip column")

	theme := styles.NewTheme(true)
	ace := deck.Card{Rank: deck.Ace, Suit: deck.Spades}
	fan := tuitest.StripANSI(RenderFan(theme, []deck.Card{ace, ace, ace}, nil, minTuckWidth))
	assert.Equal(t, 3, strings.Count(fan, "♠"), "every card in the fan shows its suit")
}

// The strip is what the layout falls back to, so it has to be readable: rank and suit
// for every card, and the cursor visible.
func TestRenderStrip_NamesEveryCardAndWrapsToWidth(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	hand := []deck.Card{
		{Rank: deck.Ten, Suit: deck.Hearts},
		{Rank: deck.Ace, Suit: deck.Spades},
		{Rank: deck.King, Suit: deck.Clubs},
	}
	out := tuitest.StripANSI(RenderStrip(theme, hand, nil, 1, 8))

	assert.Contains(t, out, "10")
	assert.Contains(t, out, "A")
	assert.Contains(t, out, "K")
	assert.Contains(t, out, ">", "the cursor has to be visible")
	assert.LessOrEqual(t, lg.Width(out), 8)
	assert.Equal(t, 1, strings.Count(out, "\n"), "two cells fit eight columns, so three wrap once")
}

func TestGridStep_StaysInsideTheGrid(t *testing.T) {
	t.Parallel()

	// A 2x2 grid: 0 1 / 2 3.
	tests := []struct{ from, dx, dy, want int }{
		{from: 0, dx: 1, want: 1},
		{from: 1, dx: 1, want: 1},  // off the right edge
		{from: 1, dx: -1, want: 0}, //
		{from: 0, dx: -1, want: 0}, // off the left edge
		{from: 0, dy: 1, want: 2},
		{from: 2, dy: 1, want: 2}, // off the bottom
		{from: 3, dy: -1, want: 1},
		{from: 1, dy: -1, want: 1}, // off the top
	}
	for _, tt := range tests {
		assert.Equalf(t, tt.want, GridStep(tt.from, 4, tt.dx, tt.dy),
			"from %d by (%d,%d)", tt.from, tt.dx, tt.dy)
	}

	assert.Equal(t, 5, GridStep(5, 4, 1, 0), "a cursor already off the grid is left alone")
}

// An odd number of choices leaves a hole in the last row, and stepping into it must
// not land on a cell that is not there.
func TestGridStep_SkipsAMissingCell(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 1, GridStep(1, 3, 0, 1), "row two has only one cell")
	assert.Equal(t, 2, GridStep(0, 3, 0, 1))
}

func TestGridPicker_RendersEveryChoiceAndMarksTheCursor(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	p := GridPicker{Title: "Pick a suit:", Labels: []string{"♠ Spades", "♥ Hearts", "♦ Diamonds", "♣ Clubs"}, Cursor: 2}
	out := p.Render(theme)

	for _, label := range p.Labels {
		assert.Contains(t, tuitest.StripANSI(out), label)
	}
	// Moving the cursor has to change what is drawn, or the highlight is not there.
	p.Cursor = 0
	assert.NotEqual(t, out, p.Render(theme))

	assert.Empty(t, GridPicker{}.Render(theme), "nothing to pick, nothing to draw")
}

// The whole point of fixed cells is that the rows line up whatever is in them.
func TestTable_RowsAreAllTheSameWidth(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	tbl := Table{
		Cols:  []Column{{Title: "Game", Width: 12}, {Title: "Elo", Width: 4}},
		Lead:  " ",
		PadTo: 4,
	}

	out := tbl.Render(theme, []string{
		tbl.Cells("Crazy Eights", "1600"),
		tbl.Cells("a name far too long to fit", "42"),
	})

	lines := strings.Split(tuitest.StripANSI(out), "\n")
	require.Len(t, lines, 1+1+4, "header, rule, and PadTo data rows")
	for i, line := range lines {
		assert.Equalf(t, tbl.Width(), lg.Width(line), "row %d is a different width", i)
	}
	assert.Contains(t, lines[2], "1600")
	assert.Contains(t, lines[3], "...", "a long value is elided, not allowed to push the column")
}

func TestStepCursor_StopsAtBothEnds(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0, StepCursor(0, -1, 3), "left of the first row stays put")
	assert.Equal(t, 3, StepCursor(3, +1, 3), "past the last row stays put")
	assert.Equal(t, 2, StepCursor(1, +1, 3))
	assert.Equal(t, 0, StepCursor(5, 0, -1), "an empty list has nowhere to be")
}

func TestCycleIndex_Wraps(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 0, CycleIndex(2, 1, 3))
	assert.Equal(t, 2, CycleIndex(0, -1, 3))
	assert.Equal(t, 1, CycleIndex(0, 7, 3))
	assert.Equal(t, 0, CycleIndex(3, 1, 0), "no filters, no index")
}
