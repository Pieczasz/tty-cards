package poker

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
)

// A chip stack has to add up to the stack it draws, or the table lies about who
// is winning. Change below the smallest chip is deliberately not drawn.
func TestRenderChipStack_BreaksAmountIntoWholeChips(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		amount uint
		want   string
	}{
		{name: "nothing to draw", amount: 0, want: ""},
		{name: "one of the smallest chip", amount: 10, want: "■1"},
		{name: "one of each", amount: 185, want: "●1 ◆1 ▲1 ■1"},
		{name: "hundreds only", amount: 1000, want: "●10"},
		{name: "change below the smallest chip is dropped", amount: 7, want: ""},
		{name: "a full stack with change", amount: 1287, want: "●12 ◆1 ▲1 ■1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tuitest.StripANSI(renderChipStack(styles.NewTheme(true), tt.amount)))
		})
	}
}

func TestChipForKey(t *testing.T) {
	t.Parallel()

	value, ok := chipForKey("1")
	assert.True(t, ok)
	assert.Equal(t, uint(100), value, "key 1 is the largest chip")

	value, ok = chipForKey("4")
	assert.True(t, ok)
	assert.Equal(t, smallestChip(), value)

	_, ok = chipForKey("5")
	assert.False(t, ok, "there is no fifth chip")
}

// Denomination is carried by shape, not just hue, so a colour-blind player - or
// anyone on a terminal with a mangled palette - can still count a stack. If two
// denominations ever share a glyph, the stack becomes ambiguous again.
func TestChipGlyphs_AreDistinctPerDenomination(t *testing.T) {
	t.Parallel()

	seen := make(map[string]uint, len(chipDenoms))
	for _, d := range chipDenoms {
		assert.NotContains(t, seen, d.Glyph, "chip %d reuses the glyph of chip %d", d.Value, seen[d.Glyph])
		seen[d.Glyph] = d.Value
		assert.Equal(t, 1, lg.Width(d.Glyph), "chip %d is not one cell wide, so it shifts the layout", d.Value)
	}
}
