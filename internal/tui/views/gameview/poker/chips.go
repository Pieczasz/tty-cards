package poker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	lg "charm.land/lipgloss/v2"
)

// chipDenoms are the chips a player can push forward, largest first. Values map
// onto the 25/50 blinds so a raise can always be built out of whole chips. The
// index into this slice is also the index into Theme.Chips.
var chipDenoms = []struct {
	Value uint
	Glyph string
}{
	// Each denomination gets its own shape as well as its own colour: a stack read
	// by hue alone is unreadable to a colour-blind player and to anyone on a
	// terminal with a mangled palette. All four glyphs measure one cell wide, so
	// swapping shapes never shifts the layout.
	{Value: 100, Glyph: "●"},
	{Value: 50, Glyph: "◆"},
	{Value: 25, Glyph: "▲"},
	{Value: 10, Glyph: "■"},
}

// renderChipStack draws amount as coloured chips, largest denomination first.
// Change below the smallest chip is left out: the exact number is always printed
// next to the stack anyway.
func renderChipStack(t styles.Theme, amount uint) string {
	parts := make([]string, 0, len(chipDenoms))
	for i, d := range chipDenoms {
		count := amount / d.Value
		if count == 0 {
			continue
		}
		amount %= d.Value
		parts = append(parts, chipStyle(t, i).Render(fmt.Sprintf("%s%d", d.Glyph, count)))
	}
	return strings.Join(parts, " ")
}

// renderChipRack is the raise prompt's keypad: which key pushes which chip. It
// doubles as the legend for the shapes drawn beside each seat.
func renderChipRack(t styles.Theme) string {
	parts := make([]string, 0, len(chipDenoms))
	for i, d := range chipDenoms {
		parts = append(parts, fmt.Sprintf("%s %s",
			chipStyle(t, i).Render(fmt.Sprintf("%s%d", d.Glyph, d.Value)),
			t.Dim.Render(fmt.Sprintf("[%d]", i+1)),
		))
	}
	return strings.Join(parts, "  ")
}

func chipStyle(t styles.Theme, denomIndex int) lg.Style {
	return lg.NewStyle().Foreground(t.Chips[denomIndex])
}

// chipForKey maps a rack key to its denomination. Keys run largest to smallest,
// matching the order the chips are drawn in.
func chipForKey(key string) (uint, bool) {
	for i, d := range chipDenoms {
		if key == strconv.Itoa(i+1) {
			return d.Value, true
		}
	}
	return 0, false
}

// smallestChip is the step for nudging a raise up or down by hand.
func smallestChip() uint {
	return chipDenoms[len(chipDenoms)-1].Value
}
