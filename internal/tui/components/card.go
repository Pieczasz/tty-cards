// Package components draws the pieces screens are built from: card faces and fans,
// mini cards, the rank-and-suit strip, fixed-width tables, the grid picker and cursor
// arithmetic.
package components

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	lg "charm.land/lipgloss/v2"
)

// A face is nine columns by seven rows inside its border: a rank line, five rows of
// pips, and the rank again mirrored. Those are the proportions of a real card, and the
// reason a fanned hand reads as a hand rather than a row of boxes.
const (
	FaceWidth  = 9
	FaceHeight = 7
	pipRows    = 5
)

// pipLayout says which of the three columns carry a pip on each of the five rows.
// This is how cards are actually printed: the eye counts a seven by its shape long
// before it reads the corner, so the pips go where a player expects them.
var pipLayout = map[deck.Rank][pipRows]string{
	deck.Ace:   {"", "", "C", "", ""},
	deck.Two:   {"C", "", "", "", "C"},
	deck.Three: {"C", "", "C", "", "C"},
	deck.Four:  {"LR", "", "", "", "LR"},
	deck.Five:  {"LR", "", "C", "", "LR"},
	deck.Six:   {"LR", "", "LR", "", "LR"},
	deck.Seven: {"LR", "", "LR", "C", "LR"},
	deck.Eight: {"LR", "C", "LR", "C", "LR"},
	deck.Nine:  {"LR", "LR", "C", "LR", "LR"},
	deck.Ten:   {"LCR", "C", "LR", "C", "LCR"},
}

// suitMark is where the suit pip belongs inside a court card's art. It sits on the
// centre column, the same one a numbered card puts its middle pip on.
const suitMark = '@'

// courtArt gives the face cards a portrait rather than a letter in a box. Every row is
// exactly FaceWidth columns wide and suitMark is replaced by the suit.
var courtArt = map[deck.Rank][pipRows]string{
	deck.King: {
		"      WW ",
		"      {) ",
		"    @ %% ",
		"     %%% ",
		"   _%%%> ",
	},
	deck.Queen: {
		"      ww ",
		"      {( ",
		"    @ %% ",
		"     %%% ",
		"   _%%%O ",
	},
	deck.Jack: {
		"      ww ",
		"      {) ",
		"    @  % ",
		"       % ",
		"   __%%[ ",
	},
	deck.Joker: {
		"     \\o/ ",
		"      |  ",
		"    @/^\\ ",
		"     %%% ",
		"   _%%%* ",
	},
}

// cardKey is everything a rendered card depends on. The palette collapses to one bool
// because NewTheme is the only way to build a Theme and every colour in it is a pure
// function of Dark, which TestRenderCard_CacheKeyCoversTheWholePalette pins.
type cardKey struct {
	rank     deck.Rank
	suit     deck.Suit
	selected bool
	dark     bool
}

// cardCache memoises rendered cards. A face is a bordered box of placed lines, which
// is the lipgloss whitespace work that dominates a frame, and there are only a few
// hundred distinct faces.
var cardCache sync.Map // cardKey -> string

func RenderCard(t styles.Theme, card deck.Card, selected bool) string {
	key := cardKey{rank: card.Rank, suit: card.Suit, selected: selected, dark: t.Dark}
	if cached, ok := cardCache.Load(key); ok {
		rendered, _ := cached.(string)
		return rendered
	}
	rendered := renderCard(t, card, selected)
	cardCache.Store(key, rendered)
	return rendered
}

func renderCard(t styles.Theme, card deck.Card, selected bool) string {
	style := t.CardFrame
	if selected {
		// A selected card is lifted out of the hand as well as recoloured, so the
		// selection survives a viewer who cannot tell the border colours apart.
		style = style.BorderForeground(t.Selection).MarginBottom(1)
	} else {
		style = style.MarginTop(1)
	}
	return style.Render(strings.Join(faceLines(t, card), "\n"))
}

// faceLines renders the seven inner rows of a face, styled but unbordered, so a caller
// can frame one card or overlap several into a fan.
func faceLines(t styles.Theme, card deck.Card) []string {
	suit, suitStyle := suitStyle(t, card.Suit)
	lines := make([]string, 0, FaceHeight)
	for _, row := range faceCells(card, suit) {
		lines = append(lines, suitStyle.Render(strings.Join(row, "")))
	}
	return lines
}

// faceCells builds the face as single-column cells. Cells rather than one string per
// row because a fan has to cut a card off mid-face, and slicing a styled string by
// display column is a good way to cut an escape sequence in half.
func faceCells(card deck.Card, suit string) [][]string {
	rank := RankLabel(card.Rank)
	rows := make([][]string, 0, FaceHeight)
	rows = append(rows, rankRow(rank, false))
	rows = append(rows, artRows(card.Rank, suit)...)
	rows = append(rows, rankRow(rank, true))
	return rows
}

// rankRow puts the rank in a corner: top-left, or bottom-right when mirrored, the way
// a real card reads the same upside down.
func rankRow(rank string, mirrored bool) []string {
	cells := blankRow()
	glyphs := strings.Split(rank, "")
	for i, g := range glyphs {
		if mirrored {
			cells[FaceWidth-len(glyphs)+i] = g
			continue
		}
		cells[i] = g
	}
	return cells
}

func artRows(rank deck.Rank, suit string) [][]string {
	if art, ok := courtArt[rank]; ok {
		rows := make([][]string, 0, pipRows)
		for _, line := range art {
			rows = append(rows, artRow(line, suit))
		}
		return rows
	}

	rows := make([][]string, 0, pipRows)
	for _, cols := range pipLayout[rank] {
		rows = append(rows, pipRow(cols, suit))
	}
	return rows
}

func artRow(line, suit string) []string {
	cells := blankRow()
	for i, r := range []rune(line) {
		if i >= FaceWidth {
			break
		}
		if r == suitMark {
			cells[i] = suit
			continue
		}
		cells[i] = string(r)
	}
	return cells
}

// The three pip columns. A court card's suit shares the centre one, so a face card and
// a numbered card put their middle mark in the same place.
const (
	leftColumn = 2
	// centreColumn is the one the court art puts its suit on (see its test).
	centreColumn = 4
	rightColumn  = 6
)

// pipRow places pips at the three columns, so a pip lands in the same place on every
// card in the hand.
func pipRow(cols, suit string) []string {
	cells := blankRow()
	for _, c := range cols {
		switch c {
		case 'L':
			cells[leftColumn] = suit
		case 'C':
			cells[centreColumn] = suit
		case 'R':
			cells[rightColumn] = suit
		}
	}
	return cells
}

func blankRow() []string {
	return slices.Repeat([]string{" "}, FaceWidth)
}

// suitStyle is the one-column symbol for a suit, and the style it is drawn in.
func suitStyle(t styles.Theme, suit deck.Suit) (string, lg.Style) {
	switch suit {
	case deck.Hearts:
		// The text-presentation selector keeps the heart one column wide; without it
		// some terminals draw it as a double-width emoji and the whole grid shifts.
		return "♥\ufe0e", lg.NewStyle().Foreground(t.SuitRed)
	case deck.Diamonds:
		return "♦", lg.NewStyle().Foreground(t.SuitRed)
	case deck.Clubs:
		return "♣", lg.NewStyle().Foreground(t.SuitDark)
	case deck.Spades:
		return "♠", lg.NewStyle().Foreground(t.SuitDark)
	default:
		return " ", lg.NewStyle()
	}
}

var rankLabels = map[deck.Rank]string{
	deck.Ace: "A", deck.Two: "2", deck.Three: "3", deck.Four: "4",
	deck.Five: "5", deck.Six: "6", deck.Seven: "7", deck.Eight: "8",
	deck.Nine: "9", deck.Ten: "10", deck.Jack: "J", deck.Queen: "Q",
	deck.King: "K", deck.Joker: "Jk",
	deck.Zero: "0", deck.One: "1", deck.Skip: "SK", deck.Reverse: "RV",
	deck.DrawTwo: "+2", deck.Wild: "W", deck.WildDrawFour: "+4",
}

// RankLabel is the rank as it is printed on a card, for callers drawing a card too
// small to hold a face. It is the same table the faces use, so a mini card and the
// card it stands for can never disagree about what rank they are.
func RankLabel(rank deck.Rank) string {
	return rankLabels[rank]
}

// miniRankWidth is the width of the widest rank label, so a ten landing on the table
// does not shift the cards beside it by a column.
const miniRankWidth = 2

// RenderMiniCard is a card in four columns, "[ A♥]", for a table with no room for a
// face: poker's board on a short terminal, and Hearts' trick when the middle band
// cannot hold three rows of card art. Nine rows per played card is what a trick costs,
// and a trick a player cannot see all of is a trick they cannot play into.
func RenderMiniCard(t styles.Theme, c deck.Card) string {
	suit, style := miniSuit(t, c.Suit)
	return style.Render(fmt.Sprintf("[%*s%s]", miniRankWidth, RankLabel(c.Rank), suit))
}

// MiniCardBack is a face-down card at mini size, the same footprint as RenderMiniCard.
func MiniCardBack(t styles.Theme) string {
	return t.Dim.Render("[" + strings.Repeat("?", miniRankWidth+1) + "]")
}

func MiniCardSlot(t styles.Theme) string {
	return t.Dim.Render("[" + strings.Repeat(" ", miniRankWidth+1) + "]")
}

// miniSuit is the suit symbol for a mini card. It has its own table rather than
// sharing suitStyle: a mini card is one cell of text, so the heart needs no
// text-presentation selector to hold the grid open, and an unknown suit has to be
// visible rather than blank.
func miniSuit(t styles.Theme, s deck.Suit) (string, lg.Style) {
	switch s {
	case deck.Hearts:
		return "♥", lg.NewStyle().Foreground(t.SuitRed)
	case deck.Diamonds:
		return "♦", lg.NewStyle().Foreground(t.SuitRed)
	case deck.Clubs:
		return "♣", lg.NewStyle().Foreground(t.SuitDark)
	case deck.Spades:
		return "♠", lg.NewStyle().Foreground(t.SuitDark)
	default:
		return "?", t.Dim
	}
}
