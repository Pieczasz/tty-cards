package styles_test

import (
	"strings"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/tui/styles"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"pgregory.net/rapid"
)

// A piece of a line: plain text, a colour, a reset, a double-width glyph, a tab.
var pieces = []string{"a", "bc", " ", "  ", "\x1b[31m", "\x1b[1;38;2;250;250;250m", "\x1b[m", "漢", "♥︎", "\t", "\r\n"}

func blockGen() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		lines := rapid.IntRange(0, 6).Draw(t, "lines")
		var b strings.Builder
		for i := range lines {
			if i > 0 {
				b.WriteByte('\n')
			}
			for range rapid.IntRange(0, 5).Draw(t, "pieces") {
				b.WriteString(rapid.SampledFrom(pieces).Draw(t, "piece"))
			}
		}
		return b.String()
	})
}

var positions = []lg.Position{lg.Left, lg.Center, lg.Right, 0.2, 0.73, -0.5, 1.5}

// The joins are drop-in replacements, so the proof is equality with lipgloss on
// anything a view could hand them.
func TestJoins_MatchLipgloss(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		strs := rapid.SliceOfN(blockGen(), 0, 5).Draw(t, "blocks")
		pos := rapid.SampledFrom(positions).Draw(t, "pos")

		if got, want := styles.JoinVertical(pos, strs...), lg.JoinVertical(pos, strs...); got != want {
			t.Fatalf("JoinVertical(%v, %q)\n got %q\nwant %q", pos, strs, got, want)
		}
		if got, want := styles.JoinHorizontal(pos, strs...), lg.JoinHorizontal(pos, strs...); got != want {
			t.Fatalf("JoinHorizontal(%v, %q)\n got %q\nwant %q", pos, strs, got, want)
		}
	})
}

func frameBlocks() []string {
	row := strings.Repeat("\x1b[38;2;148;148;148m│\x1b[m ", 60)
	block := strings.TrimSuffix(strings.Repeat(row+"\n", 12), "\n")
	return []string{block, block, block}
}

func BenchmarkJoinVertical(b *testing.B) {
	blocks := frameBlocks()
	b.Run("lipgloss", func(b *testing.B) {
		for b.Loop() {
			_ = lg.JoinVertical(lg.Center, blocks...)
		}
	})
	b.Run("styles", func(b *testing.B) {
		for b.Loop() {
			_ = styles.JoinVertical(lg.Center, blocks...)
		}
	})
}

func BenchmarkJoinHorizontal(b *testing.B) {
	blocks := frameBlocks()
	b.Run("lipgloss", func(b *testing.B) {
		for b.Loop() {
			_ = lg.JoinHorizontal(lg.Center, blocks...)
		}
	})
	b.Run("styles", func(b *testing.B) {
		for b.Loop() {
			_ = styles.JoinHorizontal(lg.Center, blocks...)
		}
	})
}

func TestJoins_EdgeCases(t *testing.T) {
	t.Parallel()
	assert.Empty(t, styles.JoinVertical(lg.Left))
	assert.Equal(t, "x", styles.JoinHorizontal(lg.Top, "x"))
}

func TestMarginTop_MatchesLipgloss(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := blockGen().Draw(t, "block")
		n := rapid.IntRange(0, 3).Draw(t, "n")
		if got, want := styles.MarginTop(n, s), lg.NewStyle().MarginTop(n).Render(s); got != want {
			t.Fatalf("MarginTop(%d, %q)\n got %q\nwant %q", n, s, got, want)
		}
	})
}
