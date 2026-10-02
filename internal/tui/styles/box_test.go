package styles_test

import (
	"strings"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/tui/styles"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"pgregory.net/rapid"
)

// Line pieces renderBox must either reproduce or hand to lipgloss. The first seven
// are what a view renders (text, wide glyphs, closed styles) and take the fast path;
// the rest - open styles, a bare reset, a non-SGR sequence, a hyperlink, controls -
// must reach lipgloss.
var boxPieces = []string{
	"a", "bc", " ", "漢", "♥︎", "\x1b[31mx\x1b[m", "\x1b[1;38;2;250;250;250mK\x1b[0m",
	"\x1b[31m", "\x1b[m", "\x1b[39m", "\x1b[2K", "\x1b]8;;http://x\x1b\\l\x1b]8;;\x1b\\", "\t", "\r", "\a", "\b", "\x7f",
}

// boxFrameGen draws half its frames from the view pieces alone, or almost none would
// take the fast path.
func boxFrameGen() *rapid.Generator[string] {
	return rapid.Custom(func(t *rapid.T) string {
		pieces := boxPieces
		if rapid.Bool().Draw(t, "well-formed") {
			pieces = boxPieces[:7]
		}
		lines := rapid.IntRange(0, 12).Draw(t, "lines")
		var b strings.Builder
		for i := range lines {
			if i > 0 {
				b.WriteByte('\n')
			}
			for range rapid.IntRange(0, 14).Draw(t, "pieces") {
				b.WriteString(rapid.SampledFrom(pieces).Draw(t, "piece"))
			}
		}
		return b.String()
	})
}

// renderBox replaces lipgloss's render of the frame's box, so the proof is equality
// with lipgloss on any frame and size, in both themes, down to boxes too small to
// hold their own padding.
func TestRenderBox_MatchesLipgloss(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		box := styles.NewTheme(rapid.Bool().Draw(t, "dark")).Box
		width := rapid.IntRange(0, 40).Draw(t, "width")
		height := rapid.IntRange(0, 20).Draw(t, "height")
		s := boxFrameGen().Draw(t, "frame")

		want := box.Width(width).Height(height).Render(s)
		if got := styles.RenderBox(box, width, height, s); got != want {
			t.Fatalf("RenderBox(%d, %d, %q)\n got %q\nwant %q", width, height, s, got, want)
		}
	})
}

func TestCentreWrapped_MatchesLipgloss(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		width := rapid.IntRange(-1, 40).Draw(t, "width")
		s := boxFrameGen().Draw(t, "text")

		want := lg.NewStyle().Width(width).Align(lg.Center).Render(s)
		if got := styles.CentreWrapped(width, s); got != want {
			t.Fatalf("CentreWrapped(%d, %q)\n got %q\nwant %q", width, s, got, want)
		}
	})
}

// Each frame lipgloss would rewrite, pinned rather than left to rapid's draw: a colour
// left open over a newline is closed and reopened by lipgloss's wrap, a trailing
// carriage return is dropped, and the rest wrap, expand or cannot fit.
func TestRenderBox_HandsLipglossWhatItRewrites(t *testing.T) {
	t.Parallel()
	box := styles.NewTheme(true).Box
	for name, frame := range map[string]string{
		"open colour":   "\x1b[31mred\nstill red\x1b[m",
		"open bold":     "\x1b[1mbold",
		"partial reset": "\x1b[1;31mx\x1b[39m\nnext",
		"hyperlink":     "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\",
		"tab":           "a\tb",
		"carriage":      "a\r\nb\r",
		"bell":          "a\ab",
		"overwide":      strings.Repeat("w", 40),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, size := range [][2]int{{20, 8}, {8, 3}, {0, 0}} {
				want := box.Width(size[0]).Height(size[1]).Render(frame)
				assert.Equal(t, want, styles.RenderBox(box, size[0], size[1], frame), "%dx%d", size[0], size[1])
			}
		})
	}
}

// The fast path is the point: a frame sized to fit must not reach lipgloss's wrap.
// The fast path allocates a fixed ~40 times whatever the frame; lipgloss's path
// allocates per rune, thousands of times even for this small one.
//
//nolint:paralleltest // AllocsPerRun refuses to run beside other tests
func TestRenderBox_FitsWithoutWrapping(t *testing.T) {
	box := styles.NewTheme(true).Box
	frame := strings.Repeat("\x1b[31m♠ A\x1b[m"+strings.Repeat(" ", 20)+"\n", 9) + "end"

	allocs := testing.AllocsPerRun(20, func() { _ = styles.RenderBox(box, 40, 16, frame) })
	assert.LessOrEqual(t, allocs, 64.0, "a fitting frame took the lipgloss path")
}

func benchFrame(width, height int) string {
	row := strings.Repeat("\x1b[38;2;148;148;148m♠ A\x1b[m ", (width-10)/4)
	return strings.TrimSuffix(strings.Repeat(row+"\n", height-6), "\n")
}

func BenchmarkRenderBox(b *testing.B) {
	box := styles.NewTheme(true).Box
	frame := benchFrame(116, 38)
	b.Run("lipgloss", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = box.Width(116).Height(38).Render(frame)
		}
	})
	b.Run("styles", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = styles.RenderBox(box, 116, 38, frame)
		}
	})
}
