package gameview

import (
	"github.com/Pieczasz/tty-cards/internal/tui/components"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"

	lg "charm.land/lipgloss/v2"
)

// Heights are what the bands cost, not round numbers: seat art 8, pile 11, banner 3,
// fanned hand 10, hints 2.
const (
	compactHeight      = 40
	compactWidth       = 80
	superCompactHeight = 26
	superCompactWidth  = 72
)

// IsCompact is a terminal too short or too narrow for the full table layout.
func IsCompact(width, height int) bool {
	return height < compactHeight || width < compactWidth
}

func isSuperCompact(width, height int) bool {
	return height < superCompactHeight || width < superCompactWidth
}

// RenderBands draws the three-band table frame: opponents on top, table in the middle,
// the hero's hand and key hints below. mid receives the rows the fixed bands left over.
func RenderBands(g router.GlobalContext, top, player, hints string, mid func(height int) string) string {
	topBand := renderTopBand(g, top)
	botBand := renderBottomBand(g, player, hints)
	midHeight := max(g.Height-lg.Height(topBand)-lg.Height(botBand), 0)

	return styles.Clamp(g.Width, g.Height, styles.JoinVertical(lg.Left,
		styles.PadCenter(g.Width, topBand),
		styles.Clamp(g.Width, midHeight, mid(midHeight)),
		styles.PadCenter(g.Width, botBand),
	))
}

// Each band is clamped before the frame is. Place and PadCenter hand back content
// bigger than the box they were given, so a band that overran would be trimmed out of
// the frame's total - eating the opponents rather than its own overflow.
func renderTopBand(g router.GlobalContext, top string) string {
	if isSuperCompact(g.Width, g.Height) {
		return styles.Clamp(g.Width, 0, top)
	}
	return styles.Clamp(g.Width, 0, styles.MarginTop(1, top))
}

// The key hints survive every size. A small terminal loses the margins around them,
// never the line itself: 80x24 is the commonest terminal there is, and dropping the
// one row that names "d: draw" there left the game unplayable for a new player while
// blank rows sat above it in the middle band.
func renderBottomBand(g router.GlobalContext, player, hints string) string {
	// Wrapped to the terminal, not clamped to it: the key line is longer than 64
	// columns, and cutting it would both hide the last key and drag every row of the
	// hand above it sideways, since the band is centred on its widest line. It breaks
	// between pairs: word wrapping left "esc:" on one row and what it does on the next.
	hintLine := g.Palette().Dim.Width(g.Width).Align(lg.Center).Render(styles.WrapActions(hints, g.Width))

	var band string
	if IsCompact(g.Width, g.Height) {
		band = styles.JoinVertical(lg.Center, player, hintLine)
	} else {
		band = lg.NewStyle().MarginBottom(1).Render(
			styles.JoinVertical(lg.Center, player, styles.MarginTop(1, hintLine)),
		)
	}
	return styles.Clamp(g.Width, 0, band)
}

// RenderTableRow lays out the middle band. The sides take the width they need rather
// than a third each, because the centre has to fit the pile, indicators and pickers and
// overran a fixed third; they stay capped at a third so a long username cannot squeeze
// the pile out.
func RenderTableRow(width, height int, left, center, right string) string {
	side := min(max(lg.Width(left), lg.Width(right)), width/3)
	centerWidth := max(width-2*side, 0)

	return styles.JoinHorizontal(lg.Top,
		styles.Place(side, height, lg.Left, lg.Center, styles.Clamp(side, height, left)),
		styles.Place(centerWidth, height, lg.Center, lg.Center, styles.Clamp(centerWidth, height, center)),
		styles.Place(side, height, lg.Right, lg.Center, styles.Clamp(side, height, right)),
	)
}

// HandKeyHints is the key line for the games driven entirely from the hand cursor.
const HandKeyHints = "<-/h: left | ->/l: right | enter: play/confirm | d: draw | esc: leave/cancel"

// WaitingKeyHints is the key line for a seat that is not on turn. The cursor still
// moves, but every play key does nothing until the turn comes round, and a hint for a
// key that does nothing reads as a broken key.
const WaitingKeyHints = "<-/h: left | ->/l: right | esc: leave"

// PickerKeyHints is the key line while a suit or colour picker owns the keyboard: d
// does nothing under it, and up/down are how its second row is reached. No longer
// than HandKeyHints, whose wrapped height HandRows budgets for, so opening the picker
// never pushes its frame off the top.
const PickerKeyHints = "<-/h: left | ->/l: right | up/k: up | down/j: down | enter: pick | esc: back"

// HandWidth leaves a column at each edge so a full-width fan is not flush against it.
func HandWidth(width int) int {
	return max(width-2, 0)
}

// What the hand shares a compact terminal with, in the tallest table (Uno's), once the
// middle band's margins are clamped away.
const (
	// tableRows is nine rows of card, the stock count under it, then two indicators.
	tableRows = 12
	// statusRows is the turn line and its margins.
	statusRows = 3
	// aboveHandRows is Uno's colour row, drawn over a fanned hand only.
	aboveHandRows = 1
)

// HandRows caps the hero's hand at the rows the other bands leave it; zero means
// unbounded. Below a fan's worth it is the strip, scrolled to that many rows. A fixed
// height breakpoint left the pile a row short at 26 rows, and more where the key line
// wraps, and the middle band is clamped from the top: the cut rows were the pile's
// border and its rank.
func HandRows(width, height int) int {
	if !IsCompact(width, height) {
		return 0
	}
	top := 2 // the margin over the opponents' names, and the names
	if isSuperCompact(width, height) {
		top = 1
	}
	rows := height - top - tableRows - statusRows - lg.Height(styles.WrapActions(HandKeyHints, width))
	if fan := rows - aboveHandRows; fan > components.FanRows {
		return fan
	}
	// Fewer rows than a fan and its index row, so the hand is drawn as the strip.
	return max(min(rows, components.FanRows), 1)
}
