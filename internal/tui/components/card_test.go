package components

import (
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The cache key collapses the whole palette to Theme.Dark.
func TestRenderCard_CacheKeyCoversTheWholePalette(t *testing.T) {
	t.Parallel()

	card := deck.Card{Rank: deck.Queen, Suit: deck.Hearts}

	dark := RenderCard(styles.NewTheme(true), card, false)
	light := RenderCard(styles.NewTheme(false), card, false)
	assert.NotEqual(t, dark, light, "the two modes must not share a cache entry")

	// A second Theme of the same mode has to be interchangeable with the first, which
	// is exactly what the cache assumes when it serves an entry built from another.
	assert.Equal(t, dark, RenderCard(styles.NewTheme(true), card, false))
	assert.Equal(t, light, RenderCard(styles.NewTheme(false), card, false))
}

// Selection is part of the key because it changes the border and the margins.
func TestRenderCard_SelectionIsNotCachedOverTheUnselectedCard(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)
	card := deck.Card{Rank: deck.Ace, Suit: deck.Spades}

	assert.NotEqual(t, RenderCard(theme, card, false), RenderCard(theme, card, true),
		"a selected card is lifted and recoloured, so it cannot reuse the plain one")
}

// Every rank and suit has to survive the cache: a key collision would silently draw the
// wrong card, which at a poker table is the worst possible bug.
func TestRenderCard_EveryCardIsDistinct(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	seen := make(map[string]deck.Card)
	for _, c := range deck.StandardDeck() {
		rendered := RenderCard(theme, c, false)
		if previous, clash := seen[rendered]; clash {
			t.Fatalf("%v and %v render identically", previous, c)
		}
		seen[rendered] = c
	}
	require.Len(t, seen, len(deck.StandardDeck()))
}

// The cache must return the same string on the miss that fills it and every hit after, or
// the first player to see a card would see a different one from everyone who follows.
func TestRenderCard_HitMatchesMiss(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)
	card := deck.Card{Rank: deck.Seven, Suit: deck.Clubs}

	first := RenderCard(theme, card, false)
	for range 5 {
		assert.Equal(t, first, RenderCard(theme, card, false))
	}
	assert.Equal(t, renderCard(theme, card, false), first, "the cached value is what the renderer produced")
}

// The pip count is the whole point of the layout: a player reads a seven by its shape
// before the corner, so a grid that draws the wrong number of pips is a misread card.
func TestFaceCells_PipCountMatchesTheRank(t *testing.T) {
	t.Parallel()

	counts := map[deck.Rank]int{
		deck.Ace: 1, deck.Two: 2, deck.Three: 3, deck.Four: 4, deck.Five: 5,
		deck.Six: 6, deck.Seven: 7, deck.Eight: 8, deck.Nine: 9, deck.Ten: 10,
	}
	for rank, want := range counts {
		rows := FaceCells(deck.Card{Rank: rank, Suit: deck.Spades}, "♠")
		pips := 0
		// The rank corners are the first and last rows; only the art rows carry pips.
		for _, row := range rows[1 : len(rows)-1] {
			for _, cell := range row {
				if cell == "♠" {
					pips++
				}
			}
		}
		assert.Equalf(t, want, pips, "rank %v", rank)
	}
}

// Every row has to be exactly FaceWidth cells or the fan cannot slice a card at a
// column boundary, and the hand shears.
func TestFaceCells_EveryRowIsFaceWidth(t *testing.T) {
	t.Parallel()

	for _, c := range deck.StandardDeck() {
		suit, _ := suitStyle(styles.NewTheme(true), c.Suit)
		rows := FaceCells(c, suit)
		require.Lenf(t, rows, FaceHeight, "%v row count", c)
		for i, row := range rows {
			assert.Lenf(t, row, FaceWidth, "%v row %d", c, i)
		}
	}
}

// Court cards get a portrait rather than a letter in a box.
func TestFaceCells_CourtCardsCarryArtAndTheirSuit(t *testing.T) {
	t.Parallel()

	for _, rank := range []deck.Rank{deck.Jack, deck.Queen, deck.King} {
		rows := FaceCells(deck.Card{Rank: rank, Suit: deck.Hearts}, "H")
		var art strings.Builder
		for _, row := range rows[1 : len(rows)-1] {
			art.WriteString(strings.Join(row, ""))
		}
		assert.Containsf(t, art.String(), "%", "%v should be drawn with art", rank)
		assert.Containsf(t, art.String(), "H", "%v should still show its suit", rank)

		// The suit belongs on the centre column, the same one a numbered card puts its
		// middle pip on, or the courts read as sitting off to one side.
		suitCol := -1
		for _, row := range rows[1 : len(rows)-1] {
			for col, cell := range row {
				if cell == "H" {
					suitCol = col
				}
			}
		}
		assert.Equalf(t, CentreColumn, suitCol, "%v suit column", rank)
	}
}

// The fan overlaps every card except the one on top, so only that one closes.
func TestRenderFan_OnlyTheTopCardClosesItsEdge(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)
	hand := []deck.Card{
		{Rank: deck.Five, Suit: deck.Diamonds},
		{Rank: deck.Seven, Suit: deck.Clubs},
		{Rank: deck.Three, Suit: deck.Hearts},
	}

	flat := tuitest.StripANSI(RenderFan(theme, hand, -1, overlapWidth))
	assert.Equal(t, 1, strings.Count(flat, "╮"), "only the rightmost card closes")
	wantWidth := 0
	for i := range hand {
		wantWidth += CardSlotWidth(i, len(hand), -1, overlapWidth)
	}
	assert.Equal(t, wantWidth, lg.Width(flat), "the fan is as wide as it claims")

	picked := tuitest.StripANSI(RenderFan(theme, hand, 1, overlapWidth))
	assert.Equal(t, 2, strings.Count(picked, "╮"), "the picked card closes over its neighbour")

	assert.Empty(t, RenderFan(theme, nil, -1, overlapWidth), "no cards, nothing to draw")
}

// rankLabels replaced an exhaustive switch, so the compiler no longer catches a
// missing rank - a new one would render as a blank label instead. deck.AllRanks is
// the substitute check, and deck's own test keeps that list honest.
func TestRankLabels_CoversAllDeckRanks(t *testing.T) {
	t.Parallel()

	require.Len(t, rankLabels, len(deck.AllRanks), "rankLabels has entries deck.AllRanks does not")
	seen := make(map[string]deck.Rank, len(deck.AllRanks))
	for _, rank := range deck.AllRanks {
		label, ok := rankLabels[rank]
		require.Truef(t, ok, "rankLabels missing deck.Rank %d", rank)
		require.NotEmptyf(t, label, "rankLabels[%d] is empty", rank)

		prev, dup := seen[label]
		assert.Falsef(t, dup, "ranks %d and %d both render as %q", prev, rank, label)
		seen[label] = rank
	}
}
