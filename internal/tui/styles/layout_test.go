package styles_test

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Clamp is the last line of defence for a frame that does not fit, so each of its
// three jobs is pinned separately: an overwide line the terminal would wrap, an
// overtall block that would push the hand off screen, and the no-op cases that must
// not quietly rewrite content that already fits.
func TestClamp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		width, height int
		in            string
		want          string
	}{
		{name: "content that already fits is untouched", width: 10, height: 3, in: "ab\ncd", want: "ab\ncd"},
		{name: "overwide lines are cut, not wrapped", width: 3, height: 0, in: "abcdef\nghijkl", want: "abc\nghi"},
		{name: "zero width leaves the columns alone", width: 0, height: 0, in: "abcdef", want: "abcdef"},
		{name: "zero height is unbounded", width: 0, height: 0, in: "a\nb\nc\nd", want: "a\nb\nc\nd"},
		{name: "negative height is unbounded too", width: 0, height: -1, in: "a\nb\nc\nd", want: "a\nb\nc\nd"},
		// Rows go from the top: the bottom of a table screen is the hero's own hand
		// and the keys they act with, which is the one part they cannot lose.
		{name: "surplus rows are dropped from the top", width: 0, height: 2, in: "table\nboard\nhand", want: "board\nhand"},
		{name: "exactly the height is kept whole", width: 0, height: 3, in: "table\nboard\nhand", want: "table\nboard\nhand"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, styles.Clamp(tt.width, tt.height, tt.in))
		})
	}
}

// Whatever else Clamp does, the survivor has to be the last line.
func TestClamp_KeepsTheHeroesHand(t *testing.T) {
	t.Parallel()

	screen := make([]string, 0, 40)
	for i := range 40 {
		screen = append(screen, fmt.Sprintf("row%02d", i))
	}
	got := styles.Clamp(80, 5, strings.Join(screen, "\n"))

	lines := strings.Split(got, "\n")
	require.Len(t, lines, 5)
	assert.Equal(t, "row39", lines[len(lines)-1], "the bottom row is the hand and must survive")
	assert.Equal(t, "row35", lines[0], "the rows that go are the ones off the top")
}

// A placed area is what RenderMainLayout stacks, so it has to be exactly the size it
// was asked for: one cell over in either direction and the box below it shifts.
func TestPlace_IsExactlyTheRequestedSize(t *testing.T) {
	t.Parallel()

	hPositions := map[string]lg.Position{"left": lg.Left, "center": lg.Center, "right": lg.Right}
	vPositions := map[string]lg.Position{"top": lg.Top, "center": lg.Center, "bottom": lg.Bottom}

	for hName, hPos := range hPositions {
		for vName, vPos := range vPositions {
			t.Run(hName+"/"+vName, func(t *testing.T) {
				t.Parallel()

				got := styles.Place(40, 9, hPos, vPos, "short\nlines")
				assert.Equal(t, 40, lg.Width(got), "the placed area is exactly as wide as asked")
				assert.Equal(t, 9, lg.Height(got), "the placed area is exactly as tall as asked")

				assert.Equal(t, 40, lg.Width(styles.PadHorizontal(40, hPos, "short\nlines")))
				assert.Equal(t, 9, lg.Height(styles.PadVertical(9, vPos, "short\nlines")))
			})
		}
	}
}

// The pad cache is 512 spaces. A wider gap has to fall back to building the run, and
// a fallback that returned the truncated cache would silently short every row.
func TestPadHorizontal_PastThePadCache(t *testing.T) {
	t.Parallel()

	for _, width := range []int{511, 512, 513, 1024} {
		t.Run(fmt.Sprintf("width=%d", width), func(t *testing.T) {
			t.Parallel()
			for _, pos := range []lg.Position{lg.Left, lg.Center, lg.Right} {
				got := styles.PadHorizontal(width, pos, "x")
				assert.Equal(t, width, lg.Width(got), "a gap wider than the cache still pads to width")
				assert.Equal(t, lg.PlaceHorizontal(width, pos, "x"), got)
			}
		})
	}
}

// PadTruncate counts runes, not bytes: a name cut mid-character renders as a
// replacement glyph and takes the column alignment with it.
func TestPadTruncate_NeverSplitsAMultibyteRune(t *testing.T) {
	t.Parallel()

	const name = "日本語テスト" // six runes, eighteen bytes

	for _, width := range []int{1, 2, 3, 4, 5, 6, 7, 12} {
		t.Run(fmt.Sprintf("width=%d", width), func(t *testing.T) {
			t.Parallel()

			got := styles.PadTruncate(name, width)
			assert.True(t, utf8.ValidString(got), "a truncated name must still be valid UTF-8")
			assert.Len(t, []rune(got), width, "the column is measured in whole runes")
		})
	}

	assert.Equal(t, name, styles.PadTruncate(name, 6), "an exactly-fitting value is returned unchanged")
	assert.Empty(t, styles.PadTruncate(name, 0), "no column, no content")
	assert.Empty(t, styles.PadTruncate(name, -5), "a negative width is not a wider column")
	assert.Equal(t, "日本語", styles.PadTruncate(name, 3),
		"three cells is all ellipsis, so the value wins instead")
}

// The resize prompt is the only thing a player sees on an undersized terminal, so it
// has to say which way to drag - "too small" alone leaves them guessing.
func TestRenderTooSmall_NamesBothSizes(t *testing.T) {
	t.Parallel()

	out := styles.NewTheme(true).RenderTooSmall(40, 12)

	assert.Contains(t, out, "Terminal too small")
	assert.Contains(t, out, fmt.Sprintf("need %d x %d", styles.MinWidth, styles.MinHeight))
	assert.Contains(t, out, "have 40 x 12", "the current size is what tells the player how far off they are")
}

// It is rendered as the whole frame, so it has to cover the whole terminal: a short
// prompt over an unpainted alt screen leaves the previous frame's confetti visible.
func TestRenderTooSmall_FillsTheTerminal(t *testing.T) {
	t.Parallel()

	for _, size := range []struct{ w, h int }{{w: 63, h: 19}, {w: 40, h: 12}, {w: 200, h: 60}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			t.Parallel()
			out := styles.NewTheme(true).RenderTooSmall(size.w, size.h)
			assert.Equal(t, size.w, lg.Width(out), "the prompt is exactly as wide as the terminal")
			assert.Equal(t, size.h, lg.Height(out), "the prompt is exactly as tall as the terminal")
		})
	}
}

// Width and height are zero until the first WindowSizeMsg, and lipgloss panics on a
// negative dimension, so the zero frame has to be clamped rather than passed through.
func TestRenderTooSmall_SurvivesAnUnknownSize(t *testing.T) {
	t.Parallel()

	for _, size := range []struct{ w, h int }{{w: 0, h: 0}, {w: -1, h: -1}, {w: 0, h: 40}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			t.Parallel()
			out := styles.NewTheme(true).RenderTooSmall(size.w, size.h)
			assert.NotEmpty(t, out, "an unknown size still renders the prompt rather than nothing")
			assert.GreaterOrEqual(t, lg.Width(out), max(size.w, 1))
			assert.GreaterOrEqual(t, lg.Height(out), max(size.h, 1))
		})
	}
}

// The footer is styled per action, so a ten-item footer is ten colour sequences on a
// frame that never changes. The cache is what keeps that off every render.
func TestRenderActionFooter_IsStableAndPerPalette(t *testing.T) {
	t.Parallel()

	dark := styles.NewTheme(true)
	light := styles.NewTheme(false)
	actions := []string{"q - Quit", "enter - Play"}

	first := dark.RenderActionFooter(actions)
	assert.Equal(t, first, dark.RenderActionFooter(actions), "a cache hit renders what the miss did")
	assert.Equal(t, first, styles.NewTheme(true).RenderActionFooter(actions),
		"a second Theme of the same mode is interchangeable with the first")

	// Two players can have opposite terminal backgrounds; sharing one entry would
	// leave one of them reading the other's palette.
	assert.NotEqual(t, first, light.RenderActionFooter(actions), "the two modes get their own entry")

	for _, action := range actions {
		assert.Contains(t, first, action)
	}
	assert.Contains(t, first, " | ", "actions are separated so they do not read as one key")
	assert.Empty(t, dark.RenderActionFooter(nil), "no actions, no footer")
}

// A distinct action list must not be served the previous one's footer.
func TestRenderActionFooter_DifferentActionsDifferentFooter(t *testing.T) {
	t.Parallel()

	theme := styles.NewTheme(true)
	assert.NotEqual(t,
		theme.RenderActionFooter([]string{"a", "b"}),
		theme.RenderActionFooter([]string{"a", "c"}))
	// Joining on a separator that could appear in an action would collide these two.
	assert.NotEqual(t,
		theme.RenderActionFooter([]string{"a b"}),
		theme.RenderActionFooter([]string{"a", "b"}))
}

// The banner is decoration and the content is not, so the budget has to stay at least
// one line (the plain-text fallback) and grow with the terminal rather than jumping.
func TestTitleHeightBudget(t *testing.T) {
	t.Parallel()

	for h := range 400 {
		require.GreaterOrEqualf(t, styles.TitleHeightBudget(h), 1,
			"TitleHeightBudget(%d) left no room for even a plain title", h)
		if h > 0 {
			require.GreaterOrEqualf(t, styles.TitleHeightBudget(h), styles.TitleHeightBudget(h-1),
				"the title budget shrank going from %d to %d rows", h-1, h)
		}
	}

	assert.Greater(t, styles.TitleHeightBudget(60), styles.TitleHeightBudget(styles.MinHeight),
		"a taller terminal earns a taller banner")
}

// AvailableContentHeight is the promise a full-screen view lays out against, and
// RenderMainLayout is what has to honour it. They disagreed twice before: the header
// was measured unwrapped here and wrapped there, and the two optical-padding lines
// were never deducted. Either one alone puts the frame a row past the terminal, which
// the terminal then wraps, shifting every row under it.
func TestRenderMainLayout_HonoursAvailableContentHeight(t *testing.T) {
	t.Parallel()

	theme := styles.NewTheme(true)
	// A long single line that fits 200 columns and wraps at 64: the wrapping header is
	// the regression this protects.
	header := "Terminal Cards - the lobby you are in, the game you picked and the seat you hold"
	footer := "n - New Game | f - Join Game | p - Profile | t - Leaderboard | ctrl+c - Quit"

	sizes := []struct{ w, h int }{{w: 64, h: 20}, {w: 80, h: 24}, {w: 120, h: 50}, {w: 200, h: 60}}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			t.Parallel()

			budget := styles.AvailableContentHeight(size.w, size.h, header, footer)
			lines := make([]string, 0, budget)
			for i := range budget {
				lines = append(lines, fmt.Sprintf("content row %d", i))
			}

			out := theme.RenderMainLayout(size.w, size.h, header, strings.Join(lines, "\n"), footer)
			assert.LessOrEqual(t, lg.Height(out), size.h, "the frame is taller than the terminal")
			assert.LessOrEqual(t, lg.Width(out), size.w, "the frame is wider than the terminal")
		})
	}
}

// The frame still has to render at the sizes that arrive before the first
// WindowSizeMsg, and with the content a view has when it has nothing to say.
func TestRenderMainLayout_SurvivesDegenerateSizes(t *testing.T) {
	t.Parallel()

	theme := styles.NewTheme(true)
	for _, size := range []struct{ w, h int }{{w: 0, h: 0}, {w: 1, h: 1}, {w: 10, h: 3}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			t.Parallel()
			assert.NotPanics(t, func() {
				theme.RenderMainLayout(size.w, size.h, "", "", "")
			})
		})
	}
}
