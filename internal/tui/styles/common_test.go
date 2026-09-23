package styles_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Router.Global.Width/Height are zero until the first WindowSizeMsg, so every
// session renders its opening frame through these with a zero size. None of them may
// return a negative dimension.
func TestSizing_NeverNegative(t *testing.T) {
	t.Parallel()

	for _, size := range []int{0, 1, 2, 5, 10, 29, 30, 99, 100, 400} {
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			t.Parallel()
			assert.GreaterOrEqual(t, styles.BoxWidth(size), 0, "BoxWidth")
			assert.GreaterOrEqual(t, styles.BoxHeight(size), 0, "BoxHeight")
			assert.GreaterOrEqual(t, styles.InnerWidth(size), 0, "InnerWidth")
			assert.GreaterOrEqual(t, styles.AvailableContentHeight(size, size, "hdr", "ftr"), 0, "AvailableContentHeight")
		})
	}
}

// A box must never be wider than the terminal that holds it.
func TestSizing_FitsWithinScreen(t *testing.T) {
	t.Parallel()

	for size := range 400 {
		assert.LessOrEqual(t, styles.BoxWidth(size), max(size, 0), "BoxWidth(%d) overflows the screen", size)
		assert.LessOrEqual(t, styles.BoxHeight(size), max(size, 0), "BoxHeight(%d) overflows the screen", size)
		assert.LessOrEqual(t, styles.InnerWidth(size), max(size, 0), "InnerWidth(%d) overflows the screen", size)
	}
}

// Growing the terminal must never shrink the box. A previous form switched to a 5/6
// proportion at 100 columns, so widening from 99 to 100 lost 12 columns.
func TestSizing_IsMonotonic(t *testing.T) {
	t.Parallel()

	for size := 1; size < 400; size++ {
		assert.GreaterOrEqual(t, styles.BoxWidth(size), styles.BoxWidth(size-1),
			"BoxWidth shrank going from %d to %d columns", size-1, size)
		assert.GreaterOrEqual(t, styles.BoxHeight(size), styles.BoxHeight(size-1),
			"BoxHeight shrank going from %d to %d rows", size-1, size)
		assert.GreaterOrEqual(t, styles.InnerWidth(size), styles.InnerWidth(size-1),
			"InnerWidth shrank going from %d to %d columns", size-1, size)
	}
}

// The box stops growing so text stays readable on an ultra-wide terminal.
func TestSizing_StopsGrowing(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 120, styles.BoxWidth(400), "width settles on the configured cap")
	assert.Equal(t, 40, styles.BoxHeight(400), "height settles on the configured cap")
	assert.Equal(t, styles.BoxWidth(400), styles.BoxWidth(4000), "width is capped")
	assert.Equal(t, styles.BoxHeight(400), styles.BoxHeight(4000), "height is capped")
}

func TestTooSmall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		w, h   int
		expect bool
	}{
		{name: "comfortable terminal fits", w: 120, h: 40, expect: false},
		{name: "exactly the minimum fits", w: styles.MinWidth, h: styles.MinHeight, expect: false},
		{name: "one column short", w: styles.MinWidth - 1, h: styles.MinHeight, expect: true},
		{name: "one row short", w: styles.MinWidth, h: styles.MinHeight - 1, expect: true},
		// Width and height are both zero until the first WindowSizeMsg lands.
		// Treating that as "small" would flash the resize prompt on every connect.
		{name: "unknown size is not small", w: 0, h: 0, expect: false},
		{name: "unknown height is not small", w: 100, h: 0, expect: false},
		{name: "unknown width is not small either", w: 0, h: 40, expect: false},
		{name: "negative size is not small", w: -1, h: -1, expect: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expect, styles.TooSmall(tt.w, tt.h))
		})
	}
}

func TestPadTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    string
		width int
		want  string
	}{
		{name: "short values are padded to width", in: "bob", width: 6, want: "bob   "},
		{name: "exact fit is untouched", in: "sixchr", width: 6, want: "sixchr"},
		{name: "long values are elided", in: "averylongusername", width: 8, want: "avery..."},
		{name: "multi-byte runes are not split", in: "źdźbło_długie", width: 8, want: "źdźbł..."},
		{name: "no room for an ellipsis just cuts", in: "abcdef", width: 2, want: "ab"},
		// At three cells an ellipsis would be the entire column, so the value wins.
		{name: "exactly three cells still shows the value", in: "abcdef", width: 3, want: "abc"},
		{name: "four cells is the narrowest ellipsis", in: "abcdef", width: 4, want: "a..."},
		{name: "zero width renders nothing", in: "abc", width: 0, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := styles.PadTruncate(tt.in, tt.width)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.width, lg.Width(got), "the column must be exactly width cells wide")
		})
	}
}

// The content area is what every view lays out inside, so its arithmetic is pinned to
// exact numbers: an off-by-four here silently clips a row off every screen.
func TestAvailableContentHeight_ExactSizes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		screenHeight int
		header       string
		footer       string
		want         int
	}{
		// BoxHeight(30) is 28, less the 4 cells of frame leaves 24 for content, less
		// the two blank lines RenderMainLayout appends for optical centering.
		{name: "one-line header and footer", screenHeight: 30, header: "h", footer: "f", want: 20},
		{name: "a two-line header costs a row", screenHeight: 30, header: "h\nh", footer: "f", want: 19},
		{name: "a two-line footer costs a row too", screenHeight: 30, header: "h", footer: "f\nf", want: 19},
		{name: "no header or footer", screenHeight: 30, want: 20},
		{name: "a header taller than the screen cannot go negative", screenHeight: 24, header: strings.Repeat("h\n", 40), want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, styles.AvailableContentHeight(100, tt.screenHeight, tt.header, tt.footer))
		})
	}
}

// The frame is a fixed inset on each side, so these are exact too.
func TestBoxSizes_ExactInsets(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 46, styles.BoxWidth(50), "two cells of border and one of padding each side")
	assert.Equal(t, 28, styles.BoxHeight(30))
	assert.Equal(t, 40, styles.InnerWidth(50))
}

// The cache is keyed by text and font, never by size: a banner's size does not depend
// on the box it has to fit, so one title across every terminal size is at most one
// entry per font. Keying on the size made every resize mint entries until a cap hid it.
//
//nolint:paralleltest // mutates the package-wide banner cache
func TestRenderFigureASCII_CachesPerFontNotPerSize(t *testing.T) {
	styles.ResetFigureCacheForTest()

	for w := 60; w <= 120; w++ {
		for h := 5; h <= 10; h++ {
			require.NotEmpty(t, styles.RenderFigureASCII("Terminal Cards", w, h))
		}
	}

	assert.LessOrEqual(t, styles.FigureCacheLenForTest(), 3,
		"one title at 61x6 sizes is at most one entry per font")
}

// A cached banner must pick the same font a fresh render would: a smaller box has to
// fall through to a smaller font even when the bigger one is already cached.
//
//nolint:paralleltest // mutates the package-wide banner cache
func TestRenderFigureASCII_CachedAnswerMatchesAFreshOne(t *testing.T) {
	sizes := [][2]int{{120, 10}, {40, 10}, {120, 3}, {120, 1}, {80, 5}}
	fresh := make([]string, 0, len(sizes))
	for _, size := range sizes {
		styles.ResetFigureCacheForTest()
		fresh = append(fresh, styles.RenderFigureASCII("Join Game", size[0], size[1]))
	}

	styles.ResetFigureCacheForTest()
	for i, size := range sizes {
		assert.Equal(t, fresh[i], styles.RenderFigureASCII("Join Game", size[0], size[1]),
			fmt.Sprintf("%dx%d", size[0], size[1]))
	}
}

// The banner is decoration. On a short terminal it has to give way to plain text so
// the content gets the lines back; on a tall one the biggest fitting font still wins.
func TestRenderFigureASCII_FallsBackByHeightToo(t *testing.T) {
	t.Parallel()
	plain := styles.RenderFigureASCII("Join Game", 120, 1)
	assert.Equal(t, "Join Game", plain, "a one-line budget is plain text")

	short := styles.RenderFigureASCII("Join Game", 120, 3)
	assert.Equal(t, 3, lg.Height(short), "the mini font is the tallest that fits three lines")

	tall := styles.RenderFigureASCII("Join Game", 120, 10)
	assert.Equal(t, 5, lg.Height(tall), "with room, the slant font")

	assert.Equal(t, 2, styles.TitleHeightBudget(styles.MinHeight),
		"the declared minimum terminal gets a plain title")
}
