package styles_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode"

	"github.com/Pieczasz/tty-cards/internal/tui/styles"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"pgregory.net/rapid"
)

func benchBlock() string {
	lines := make([]string, 0, 12)
	for i := range 12 {
		lines = append(lines, lg.NewStyle().Bold(true).Render(strings.Repeat("x", 20+i)))
	}
	return strings.Join(lines, "\n")
}

func TestPadCenter_MatchesLipgloss(t *testing.T) {
	t.Parallel()

	for _, s := range []string{
		"",
		"one line",
		"two\nlines",
		benchBlock(),
		lg.NewStyle().Foreground(lg.Color("#FF0000")).Render("styled"),
		"ragged\nlines of\ndiffering width",
		"♥︎ wide runes ♦",
	} {
		for _, width := range []int{0, 1, 10, 40, 120} {
			assert.Equalf(t, lg.PlaceHorizontal(width, lg.Center, s), styles.PadCenter(width, s),
				"width=%d input=%q", width, s)
		}
	}
}

func BenchmarkPadCenter(b *testing.B) {
	block := benchBlock()

	b.Run("lipgloss", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = lg.PlaceHorizontal(120, lg.Center, block)
		}
	})

	b.Run("PadCenter", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = styles.PadCenter(120, block)
		}
	})
}

func TestPadCenter_PropertyMatchesLipgloss(t *testing.T) {
	t.Parallel()

	rapid.Check(t, func(rt *rapid.T) {
		lineCount := rapid.IntRange(1, 8).Draw(rt, "lines")
		lines := make([]string, 0, lineCount)
		for i := range lineCount {
			body := rapid.StringOfN(rapid.RuneFrom([]rune("ab ♥︎♦x"), unicode.Latin), 0, 30, -1).
				Draw(rt, fmt.Sprintf("line%d", i))
			if rapid.Bool().Draw(rt, fmt.Sprintf("styled%d", i)) {
				body = lg.NewStyle().Bold(true).Foreground(lg.Color("#FF00FF")).Render(body)
			}
			lines = append(lines, body)
		}
		block := strings.Join(lines, "\n")
		width := rapid.IntRange(0, 200).Draw(rt, "width")
		height := rapid.IntRange(0, 40).Draw(rt, "height")

		positions := []lg.Position{lg.Left, lg.Center, lg.Right}
		hPos := positions[rapid.IntRange(0, 2).Draw(rt, "hPos")]
		vPositions := []lg.Position{lg.Top, lg.Center, lg.Bottom}
		vPos := vPositions[rapid.IntRange(0, 2).Draw(rt, "vPos")]

		assert.Equal(rt, lg.PlaceHorizontal(width, hPos, block), styles.PadHorizontal(width, hPos, block),
			"horizontal")
		assert.Equal(rt, lg.PlaceVertical(height, vPos, block), styles.PadVertical(height, vPos, block),
			"vertical")
		assert.Equal(rt, lg.Place(width, height, hPos, vPos, block), styles.Place(width, height, hPos, vPos, block),
			"both")
	})
}
