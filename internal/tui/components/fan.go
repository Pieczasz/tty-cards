package components

import (
	"fmt"
	"strings"
	"sync"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	lg "charm.land/lipgloss/v2"
)

const (
	// How much of a card stays visible once the next covers it: hiding one column is
	// enough to tuck them together while every rank and pip stays readable.
	overlapWidth = FaceWidth - 1
	// The centre pip column and no less: an ace's only pip lives there, and a hand whose
	// suits are invisible is unplayable.
	minTuckWidth = centreColumn + 1
	// cardRows is a framed card: the two borders plus the face.
	cardRows = FaceHeight + 2
	// FanRows is how many rows a fan occupies, for a caller budgeting height.
	FanRows = cardRows
)

// FanTuck is how many face columns each covered card shows so a fan of n fits maxWidth,
// or 0 when no fan does and the caller falls back to RenderStrip. Zero maxWidth is
// unbounded.
func FanTuck(n, maxWidth int) int {
	if n <= 0 || maxWidth <= 0 {
		return overlapWidth
	}
	for tuck := overlapWidth; tuck >= minTuckWidth; tuck-- {
		if fanWidth(n, tuck) <= maxWidth {
			return tuck
		}
	}
	return 0
}

// fanWidth budgets for a picked-out card mid-hand, which keeps its own closing border.
// Worst case, so the hand does not change width - shifting the layout - as the cursor
// moves.
func fanWidth(n, tuck int) int {
	if n <= 0 {
		return 0
	}
	width := (n-1)*(1+tuck) + 1 + FaceWidth + 1
	if n > 1 {
		width += FaceWidth + 1 - tuck
	}
	return width
}

// Selection is the set of picked-out cards a fan is drawn with. Negative indices are
// dropped, so a single-select caller passes its cursor, or -1 for none.
func Selection(indices ...int) map[int]struct{} {
	sel := make(map[int]struct{}, len(indices))
	for _, i := range indices {
		if i >= 0 {
			sel[i] = struct{}{}
		}
	}
	return sel
}

// CardSlotWidth is the visible width of card i in a fan of n at this tuck, with the
// cards in selected picked out. A closed card is wider, so callers have to ask.
func CardSlotWidth(i, n, tuck int, selected map[int]struct{}) int {
	// Every card contributes its left border plus the columns of face it still shows;
	// a closed one adds its right border too.
	if _, picked := selected[i]; picked || i == n-1 {
		return 1 + FaceWidth + 1
	}
	return 1 + tuck
}

// RenderFan draws cards overlapping left to right, each covering all but tuck columns of
// the one before. Every card in selected keeps its own right border and the selection
// colour, so it reads as lying on top; there is no vertical lift, which would break the
// shared top edge that makes the rest read as one hand.
func RenderFan(t styles.Theme, cards []deck.Card, selected map[int]struct{}, tuck int) string {
	if len(cards) == 0 {
		return ""
	}

	rows := make([]strings.Builder, cardRows)
	for i, card := range cards {
		_, isSel := selected[i]
		// A card is closed when nothing covers it: the rightmost one, and any picked-out
		// one, which sits over its neighbour.
		closed := i == len(cards)-1 || isSel

		width := tuck
		if closed {
			width = FaceWidth
		}

		for r, line := range fanColumn(t, card, isSel, width, closed) {
			rows[r].WriteString(line)
		}
	}

	lines := make([]string, cardRows)
	for i := range rows {
		lines[i] = rows[i].String()
	}
	return strings.Join(lines, "\n")
}

// stripCellWidth is one card in the compact strip: a marker column, the rank label
// and the suit. Fixed so the cells line up in a grid however the ranks vary.
const stripCellWidth = 4

// RenderStrip is the hand as rank-and-suit cells wrapped to maxWidth, for when no fan
// fits: thirteen cards of art run past a hundred columns. selected marks staged cards,
// cursor the focused one; nil map means single selection.
func RenderStrip(t styles.Theme, cards []deck.Card, selected map[int]struct{}, cursor, maxWidth int) string {
	if len(cards) == 0 {
		return ""
	}
	perRow := len(cards)
	if maxWidth > 0 {
		perRow = max(maxWidth/stripCellWidth, 1)
	}

	rows := make([]string, 0, len(cards)/perRow+1)
	var row strings.Builder
	for i, card := range cards {
		if i > 0 && i%perRow == 0 {
			rows = append(rows, row.String())
			row.Reset()
		}
		row.WriteString(stripCell(t, card, i, selected, cursor))
	}
	return strings.Join(append(rows, row.String()), "\n")
}

func stripCell(t styles.Theme, card deck.Card, i int, selected map[int]struct{}, cursor int) string {
	suit, style := suitStyle(t, card.Suit)
	marker := " "
	if _, ok := selected[i]; ok {
		marker = "*"
		style = t.PlayerItemSelected.Bold(true)
	} else if i == cursor {
		marker = ">"
		style = t.PlayerItemSelected
	}
	return style.Render(fmt.Sprintf("%s%2s%s", marker, RankLabel(card.Rank), suit))
}

// columnKey is everything a fan column depends on.
type columnKey struct {
	rank     deck.Rank
	suit     deck.Suit
	width    int
	selected bool
	closed   bool
	dark     bool
}

// columnCache memoises fan columns: a seven-card hand is over a hundred style renders per
// frame, and a column has only a few hundred possible forms.
var columnCache sync.Map // columnKey -> []string

// fanColumn renders one card's slice of the fan: top border, face rows, bottom border.
// The returned slice is shared with the cache and must not be mutated.
func fanColumn(t styles.Theme, card deck.Card, selected bool, width int, closed bool) []string {
	key := columnKey{
		rank: card.Rank, suit: card.Suit, width: width,
		selected: selected, closed: closed, dark: t.Dark,
	}
	if cached, ok := columnCache.Load(key); ok {
		lines, _ := cached.([]string)
		return lines
	}
	lines := renderFanColumn(t, card, selected, width, closed)
	columnCache.Store(key, lines)
	return lines
}

func renderFanColumn(t styles.Theme, card deck.Card, selected bool, width int, closed bool) []string {
	border := t.CardFace
	if selected {
		border = t.Selection
	}
	edge := lg.NewStyle().Foreground(border)

	suit, suitStyle := suitStyle(t, card.Suit)
	cells := faceCells(card, suit)

	closeTop, closeMid, closeBot := "", "", ""
	if closed {
		closeTop, closeMid, closeBot = edge.Render("╮"), edge.Render("│"), edge.Render("╯")
	}

	lines := make([]string, 0, cardRows)
	lines = append(lines, edge.Render("╭"+strings.Repeat("─", width))+closeTop)
	for _, row := range cells {
		lines = append(lines, edge.Render("│")+suitStyle.Render(strings.Join(row[:width], ""))+closeMid)
	}
	lines = append(lines, edge.Render("╰"+strings.Repeat("─", width))+closeBot)
	return lines
}
