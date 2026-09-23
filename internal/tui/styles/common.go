package styles

import (
	"fmt"
	"strings"
	"sync"

	lg "charm.land/lipgloss/v2"
	"github.com/common-nighthawk/go-figure"
)

// Box dimensions stop growing past these so content stays readable on an ultra-wide
// or very tall terminal instead of stretching across the whole screen.
const (
	maxBoxWidth  = 120
	maxBoxHeight = 40
)

// The framed layout needs this much terminal to draw without overflowing: the
// poker board alone is five cards at 11 columns each. Below it, views are told to
// render the resize prompt instead of a broken frame - lipgloss will happily wrap
// a table into unreadable confetti rather than complain.
const (
	MinWidth  = 64
	MinHeight = 20
)

// TooSmall reports whether the terminal cannot fit the layout. A zero dimension
// means the first WindowSizeMsg has not arrived yet, which is not the same as
// small - answering true there would flash the resize prompt on every connection.
func TooSmall(screenWidth, screenHeight int) bool {
	if screenWidth <= 0 || screenHeight <= 0 {
		return false
	}
	return screenWidth < MinWidth || screenHeight < MinHeight
}

// RenderTooSmall fills the terminal with the resize prompt. It reports the current
// size as well as the required one, so the player can see which way to drag.
func (t Theme) RenderTooSmall(screenWidth, screenHeight int) string {
	msg := lg.JoinVertical(lg.Center,
		t.Accented.Render("Terminal too small"),
		"",
		t.Muted.Render(fmt.Sprintf("need %d x %d", MinWidth, MinHeight)),
		t.Muted.Render(fmt.Sprintf("have %d x %d", screenWidth, screenHeight)),
	)
	return lg.Place(max(screenWidth, 1), max(screenHeight, 1), lg.Center, lg.Center, msg)
}

// BoxWidth is the outer width of the framed layout. It must never shrink as the
// terminal grows, and must never go negative: Router.Global.Width is 0 until the
// first WindowSizeMsg, so every session's opening frame renders at zero.
func BoxWidth(screenWidth int) int {
	return max(min(screenWidth-4, maxBoxWidth), 0)
}

func BoxHeight(screenHeight int) int {
	return max(min(screenHeight-2, maxBoxHeight), 0)
}

func InnerWidth(screenWidth int) int {
	return max(BoxWidth(screenWidth)-6, 0)
}

// opticalPadding is the two blank lines RenderMainLayout appends to content. They are
// part of the content area, so a view that is told it may use the whole area and then
// fills it overflows the box by exactly this much.
const opticalPadding = 2

// layoutHeights is the arithmetic RenderMainLayout and AvailableContentHeight must
// agree on. They used to disagree twice over: the header and footer were measured
// unwrapped here but wrapped there - a footer that wraps to two lines is a line the
// content cannot have - and opticalPadding was never deducted. Either one alone makes
// a full-screen view a row taller than the terminal, which the frame then hands to
// the terminal to wrap, shifting every row under it.
func layoutHeights(screenWidth, screenHeight int, header, footer string) (
	innerWidth, hHeader, hFooter, hContent int,
) {
	innerWidth = max(BoxWidth(screenWidth)-6, 0)
	innerHeight := max(BoxHeight(screenHeight)-4, 0)

	// go-figure leaves trailing newlines that inflate the measured height.
	header = strings.TrimRight(header, "\r\n")
	footer = strings.TrimRight(footer, "\r\n")

	// Wrap before measuring, or lg.Height reports the unwrapped height.
	header = lg.NewStyle().Width(innerWidth).Align(lg.Center).Render(header)
	footer = lg.NewStyle().Width(innerWidth).Align(lg.Center).Render(footer)

	hHeader, hFooter = lg.Height(header), lg.Height(footer)
	return innerWidth, hHeader, hFooter, max(innerHeight-hHeader-hFooter, 0)
}

// AvailableContentHeight is how many lines of content a full-screen view may render
// at this size without the frame outgrowing the terminal.
func AvailableContentHeight(screenWidth, screenHeight int, header, footer string) int {
	_, _, _, hContent := layoutHeights(screenWidth, screenHeight, header, footer)
	return max(hContent-opticalPadding, 0)
}

// PadTruncate fits s into exactly width cells, padding short values and eliding
// long ones. It counts runes rather than bytes so a multi-byte username is never
// cut mid-character, and it is what keeps aligned columns aligned once a player
// uses all 16 characters of their name.
func PadTruncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= width {
		return s + strings.Repeat(" ", width-len(runes))
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

// figureKey is a title in one font. The size it has to fit is not part of it: a
// banner's measurements do not depend on the box, so keying on the box stored the same
// three banners once per terminal size.
type figureKey struct {
	text string
	font string
}

type figureBanner struct {
	art           string
	width, height int
}

// TitleHeightBudget is how many lines a screen may spend on its figlet title: a fifth
// of the box interior. On a 20-row terminal that is two, so the title falls back to
// plain text and the content gets those lines back - the banner is decoration, and
// three lines of it in a fourteen-line box left some screens no room for a single row.
func TitleHeightBudget(screenHeight int) int {
	return max((BoxHeight(screenHeight)-4)/5, 1)
}

// figureCache memoises rendered banners. go-figure re-reads and re-parses the whole
// figlet font on every call, which measured as 85% of the allocations in a menu frame.
//
// It holds at most three entries per title and has no cap, which is safe only because
// every banner text is a fixed string in the source. Nothing player-controlled may be
// banner text: the home screen used to banner the username, which let any account
// mint entries.
var figureCache sync.Map // figureKey -> figureBanner

var figureFonts = []string{"slant", "small", "mini"}

// RenderFigureASCII is text as the largest figlet banner that fits both bounds, or
// the text itself when none does. Fonts are tried largest first and only rendered
// when reached, so a roomy terminal never pays for the smaller two.
func RenderFigureASCII(text string, maxWidth, maxHeight int) string {
	for _, font := range figureFonts {
		if b := figureFor(text, font); b.width <= maxWidth && b.height <= maxHeight {
			return b.art
		}
	}
	return text // no font fits; plain text always does
}

func figureFor(text, font string) figureBanner {
	key := figureKey{text: text, font: font}
	if cached, ok := figureCache.Load(key); ok {
		b, _ := cached.(figureBanner)
		return b
	}
	art := strings.TrimRight(figure.NewFigure(text, font, true).String(), "\r\n")
	b := figureBanner{art: art, width: lg.Width(art), height: lg.Height(art)}
	figureCache.Store(key, b)
	return b
}

func (t Theme) RenderMainLayout(width, height int, header, content, footer string) string {
	boxWidth := BoxWidth(width)
	boxHeight := BoxHeight(height)

	innerWidth, hHeader, hFooter, hContent := layoutHeights(width, height, header, footer)

	// Re-render at the measured width so the placed areas match what was measured.
	header = lg.NewStyle().Width(innerWidth).Align(lg.Center).
		Render(strings.TrimRight(header, "\r\n"))
	footer = lg.NewStyle().Width(innerWidth).Align(lg.Center).
		Render(strings.TrimRight(footer, "\r\n"))

	// Optical centering: two trailing blank lines push the visible text one line up,
	// which reads as centered where true centering reads as slightly low.
	content = strings.TrimRight(content, "\r\n") + "\n\n"

	headerArea := Place(innerWidth, hHeader, lg.Center, lg.Top, header)
	footerArea := Place(innerWidth, hFooter, lg.Center, lg.Bottom, footer)
	contentArea := Place(innerWidth, hContent, lg.Center, lg.Center, content)

	stacked := lg.JoinVertical(lg.Center, headerArea, contentArea, footerArea)
	return t.Box.Width(boxWidth).Height(boxHeight).Render(stacked)
}

// footerKey is the action list a view offers, at one palette. Each view has a fixed
// set, so the cache holds one entry per screen rather than growing with use.
type footerKey struct {
	actions string
	dark    bool
}

// footerCache memoises the action footer. Every action is styled separately, so a
// ten-item footer emits ten colour sequences on a frame that never changes.
var footerCache sync.Map // footerKey -> string

func (t Theme) RenderActionFooter(actions []string) string {
	key := footerKey{actions: strings.Join(actions, "\x00"), dark: t.Dark}
	if cached, ok := footerCache.Load(key); ok {
		footer, _ := cached.(string)
		return footer
	}

	renderedActions := make([]string, 0, len(actions))
	for _, action := range actions {
		renderedActions = append(renderedActions, t.ActionsText.Render(action))
	}
	footer := strings.Join(renderedActions, " | ")
	footerCache.Store(key, footer)
	return footer
}

// ResetFigureCacheForTest empties the banner cache. Exported for the cache-size tests,
// which cannot observe a count they share with every other test in the package.
func ResetFigureCacheForTest() {
	figureCache.Clear()
}

func FigureCacheLenForTest() int {
	n := 0
	figureCache.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}
