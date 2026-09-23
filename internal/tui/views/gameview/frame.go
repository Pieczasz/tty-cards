package gameview

import (
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

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

	return styles.Clamp(g.Width, g.Height, lg.JoinVertical(lg.Left,
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
	return styles.Clamp(g.Width, 0, lg.NewStyle().MarginTop(1).Render(top))
}

// The key hints survive every size. A small terminal loses the margins around them,
// never the line itself: 80x24 is the commonest terminal there is, and dropping the
// one row that names "d: draw" there left the game unplayable for a new player while
// blank rows sat above it in the middle band.
func renderBottomBand(g router.GlobalContext, player, hints string) string {
	// Wrapped to the terminal, not clamped to it: the key line is longer than 64
	// columns, and cutting it would both hide the last key and drag every row of the
	// hand above it sideways, since the band is centred on its widest line.
	hintLine := g.Theme.Dim.Width(g.Width).Align(lg.Center).Render(hints)

	var band string
	if IsCompact(g.Width, g.Height) {
		band = lg.JoinVertical(lg.Center, player, hintLine)
	} else {
		band = lg.NewStyle().MarginBottom(1).Render(
			lg.JoinVertical(lg.Center, player, lg.NewStyle().MarginTop(1).Render(hintLine)),
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

	return lg.JoinHorizontal(lg.Top,
		styles.Place(side, height, lg.Left, lg.Center, styles.Clamp(side, height, left)),
		styles.Place(centerWidth, height, lg.Center, lg.Center, styles.Clamp(centerWidth, height, center)),
		styles.Place(side, height, lg.Right, lg.Center, styles.Clamp(side, height, right)),
	)
}

// HandKeyHints is the key line for the games driven entirely from the hand cursor.
const HandKeyHints = "<-/h: left | ->/l: right | enter: play/confirm | d: draw | esc: leave/cancel"

// HandWidth leaves a column at each edge so a full-width fan is not flush against it.
func HandWidth(width int) int {
	return max(width-2, 0)
}

const stripHandRows = 3

// HandRows caps the hero's hand; zero means unbounded. A terminal too short to spare
// ten rows for card art gets the compact strip instead.
func HandRows(height int) int {
	if height < superCompactHeight {
		return stripHandRows
	}
	return 0
}
