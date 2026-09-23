package components

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Poker's board and Hearts' trick line mini cards up in a row, so every one of them -
// face up, face down or an empty seat - has to occupy the same footprint or the
// column under it shifts as the hand is played out.
func TestMiniCards_ShareOneFootprint(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	want := lg.Width(MiniCardSlot(theme))
	assert.Equal(t, want, lg.Width(MiniCardBack(theme)), "a face-down card is the same width as an empty slot")

	for _, card := range deck.StandardDeck() {
		assert.Equalf(t, want, lg.Width(RenderMiniCard(theme, card)),
			"%v is a different width from the slot it lands in", card)
	}
}

// A mini card is all a player has to read the board from, so it has to name the card.
func TestRenderMiniCard_NamesTheCard(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	tests := []struct {
		name string
		card deck.Card
		want string
	}{
		{name: "ace of hearts", card: deck.Card{Rank: deck.Ace, Suit: deck.Hearts}, want: "[ A♥]"},
		{name: "a ten keeps both digits", card: deck.Card{Rank: deck.Ten, Suit: deck.Spades}, want: "[10♠]"},
		{name: "king of diamonds", card: deck.Card{Rank: deck.King, Suit: deck.Diamonds}, want: "[ K♦]"},
		{name: "queen of clubs", card: deck.Card{Rank: deck.Queen, Suit: deck.Clubs}, want: "[ Q♣]"},
		// A card with no suit must still be visible rather than render as blank: a
		// blank cell reads as "no card played", which is a different table.
		{name: "an unknown suit is still drawn", card: deck.Card{Rank: deck.Two}, want: "[ 2?]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tuitest.StripANSI(RenderMiniCard(theme, tt.card)))
		})
	}
}

// The back and the empty slot have to be distinguishable: one means a card the player
// cannot see, the other means no card at all.
func TestMiniCardBack_IsNotAnEmptySlot(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	assert.Equal(t, "[???]", tuitest.StripANSI(MiniCardBack(theme)))
	assert.Equal(t, "[   ]", tuitest.StripANSI(MiniCardSlot(theme)))
}

// The uno view paints a colour glyph per slot above the fan by summing CardSlotWidth,
// so the sum and the rendered width must not drift: one column off and every glyph
// sits over the wrong card.
func TestCardSlotWidth_SumsToTheRenderedFan(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	for _, n := range []int{1, 2, 3, 5, 7, 13} {
		for _, maxWidth := range []int{0, 40, 64, 80, 100, 120, 200} {
			tuck := FanTuck(n, maxWidth)
			if tuck == 0 {
				continue // no fan fits; the caller falls back to RenderStrip
			}
			t.Run(fmt.Sprintf("cards=%d/width=%d/tuck=%d", n, maxWidth, tuck), func(t *testing.T) {
				t.Parallel()

				hand := testHand(n)
				for _, selected := range []int{-1, 0, n / 2, n - 1} {
					sum := 0
					for i := range n {
						sum += CardSlotWidth(i, n, selected, tuck)
					}
					assert.Equalf(t, sum, lg.Width(RenderFan(theme, hand, selected, tuck)),
						"the slot widths and the fan disagree with card %d picked out", selected)
				}
			})
		}
	}
}

// Hearts' pass phase picks three cards at once, so the multi-select fan needs the
// same agreement between what it claims and what it draws.
func TestCardSlotWidthMulti_SumsToTheRenderedFan(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	selections := []map[int]struct{}{
		{},
		{0: {}},
		{0: {}, 2: {}, 4: {}},
		{6: {}}, // the last card, which already closes its own edge
	}

	for _, n := range []int{1, 5, 7, 13} {
		for _, maxWidth := range []int{0, 64, 100, 200} {
			tuck := FanTuck(n, maxWidth)
			if tuck == 0 {
				continue
			}
			t.Run(fmt.Sprintf("cards=%d/width=%d/tuck=%d", n, maxWidth, tuck), func(t *testing.T) {
				t.Parallel()

				hand := testHand(n)
				for i, selected := range selections {
					sum := 0
					for card := range n {
						sum += CardSlotWidthMulti(card, n, tuck, selected)
					}
					assert.Equalf(t, sum, lg.Width(RenderFanMulti(theme, hand, selected, tuck)),
						"selection %d disagrees with what was drawn", i)
				}
			})
		}
	}
}

// Every staged card is lifted out, so the pass phase shows three closed edges plus the
// one the rightmost card always has.
func TestRenderFanMulti_ClosesEveryStagedCard(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	hand := testHand(7)
	flat := tuitest.StripANSI(RenderFanMulti(theme, hand, map[int]struct{}{1: {}, 3: {}}, overlapWidth))
	assert.Equal(t, 3, strings.Count(flat, "╮"), "two staged cards plus the rightmost one")

	assert.Empty(t, RenderFanMulti(theme, nil, nil, overlapWidth), "no cards, nothing to draw")
}

// A hand of thirteen cannot fan at any tuck inside 64 columns, which is the size the
// server admits. FanTuck has to say so rather than return a tuck that overruns.
func TestFanTuck_GivesUpWhenNoFanFits(t *testing.T) {
	t.Parallel()

	assert.Zero(t, FanTuck(13, 40), "no tuck fits thirteen cards in forty columns")
	assert.Equal(t, overlapWidth, FanTuck(5, 0), "a zero budget is unbounded, so nothing is tucked")
	assert.Equal(t, overlapWidth, FanTuck(0, 80), "an empty hand tucks nothing")
	assert.Equal(t, overlapWidth, FanTuck(-1, 80), "neither does a nonsensical one")
}

// The strip is the fallback, and it is what a player reads their hand from when no fan
// fits, so staged cards and the cursor both have to be marked - and differently.
func TestRenderStrip_MarksStagedCardsApartFromTheCursor(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	hand := testHand(4)
	out := tuitest.StripANSI(RenderStrip(theme, hand, map[int]struct{}{0: {}}, 1, 0))

	assert.Contains(t, out, "*", "a staged card is starred")
	assert.Contains(t, out, ">", "the cursor is an arrow")
	assert.Equal(t, 1, strings.Count(out, "*"))
	assert.Equal(t, 1, strings.Count(out, ">"))
	assert.NotContains(t, out, "\n", "an unbounded width keeps the hand on one row")

	assert.Empty(t, RenderStrip(theme, nil, nil, 0, 20), "no cards, nothing to draw")
}

// A budget narrower than a single cell must still put one card per row rather than
// dividing by zero or emitting an empty grid.
func TestRenderStrip_AlwaysFitsAtLeastOneCard(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	out := tuitest.StripANSI(RenderStrip(theme, testHand(3), nil, 0, 1))
	assert.Len(t, strings.Split(out, "\n"), 3, "one card per row when nothing else fits")
}

// A short page must not make the rest of the layout jump, which is the only reason
// PadTo exists.
func TestTable_PadsShortPagesToAFixedHeight(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	tbl := Table{Cols: []Column{{Title: "Player", Width: 10}, {Title: "Elo", Width: 5}}, PadTo: 6}

	for _, rowCount := range []int{0, 1, 5, 6} {
		t.Run(fmt.Sprintf("rows=%d", rowCount), func(t *testing.T) {
			t.Parallel()

			rows := make([]string, 0, rowCount)
			for i := range rowCount {
				rows = append(rows, tbl.Cells(fmt.Sprintf("p%d", i), "1500"))
			}
			lines := strings.Split(tuitest.StripANSI(tbl.Render(theme, rows)), "\n")
			require.Len(t, lines, 2+tbl.PadTo, "header, rule and a fixed number of data rows")
			for i, line := range lines {
				assert.Equalf(t, tbl.Width(), lg.Width(line), "row %d is a different width", i)
			}
		})
	}

	// A full page is not padded away, and an over-full one is the caller's business.
	over := make([]string, 0, 9)
	for i := range 9 {
		over = append(over, tbl.Cells(fmt.Sprintf("p%d", i)))
	}
	assert.Len(t, strings.Split(tuitest.StripANSI(tbl.Render(theme, over)), "\n"), 2+9,
		"more rows than PadTo are all shown")
}

// The header is two lines - titles and the rule under them - and the rule has to span
// the data columns exactly, or it reads as belonging to a narrower table.
func TestTable_HeaderIsTitlesAndARule(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	tbl := Table{Cols: []Column{{Title: "Game", Width: 8}, {Title: "Mode", Width: 6}}, Lead: "> "}
	lines := strings.Split(tuitest.StripANSI(tbl.Header(theme)), "\n")

	require.Len(t, lines, 2)
	assert.Equal(t, "> Game     | Mode  ", lines[0], "titles are padded into their own columns")
	assert.Equal(t, "> "+strings.Repeat("-", tbl.Width()-2), lines[1], "the rule spans the columns, not the lead")
	assert.Equal(t, tbl.Width(), lg.Width(lines[0]))
	assert.Equal(t, tbl.Width(), lg.Width(lines[1]))
}

// Cells is what the plain-text screens build rows with, so a caller that passes fewer
// values than there are columns has to get a blank column rather than a short row -
// a short row shifts every column to its right.
func TestTable_CellsPadsMissingAndElidesOversizedValues(t *testing.T) {
	t.Parallel()

	tbl := Table{Cols: []Column{{Title: "Game", Width: 8}, {Title: "Mode", Width: 6}}, Lead: "| "}

	tests := []struct {
		name   string
		values []string
		want   string
	}{
		{name: "one value per column", values: []string{"Poker", "Casual"}, want: "| Poker    | Casual"},
		{name: "a missing value leaves its column blank", values: []string{"Poker"}, want: "| Poker    |       "},
		{name: "no values at all", want: "|          |       "},
		{name: "an oversized value is elided", values: []string{"Crazy Eights", "Ranked"}, want: "| Crazy... | Ranked"},
		{name: "extra values are ignored", values: []string{"Uno", "Ranked", "spare"}, want: "| Uno      | Ranked"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tbl.Cells(tt.values...)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tbl.Width(), lg.Width(got), "every row is exactly the table's width")
		})
	}
}

// StepCursor clamps and CycleIndex wraps. The two are one keystroke apart at every
// call site, so the difference is pinned rather than assumed.
func TestStepCursor_ClampsWhereCycleIndexWraps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		index, delta, maxOrN    int
		wantStepped, wantCycled int
	}{
		{name: "forward inside the list", index: 0, delta: 1, maxOrN: 3, wantStepped: 1, wantCycled: 1},
		{name: "off the end clamps but cycles", index: 2, delta: 1, maxOrN: 3, wantStepped: 3, wantCycled: 0},
		{name: "off the front clamps but cycles", index: 0, delta: -1, maxOrN: 3, wantStepped: 0, wantCycled: 2},
		{name: "a delta larger than the list", index: 0, delta: 7, maxOrN: 3, wantStepped: 3, wantCycled: 1},
		{name: "a large negative delta", index: 0, delta: -7, maxOrN: 3, wantStepped: 0, wantCycled: 2},
		{name: "an empty list has nowhere to be", index: 0, delta: 1, maxOrN: 0, wantStepped: 0, wantCycled: 0},
		// A negative bound is what an empty slice's len()-1 gives, and neither may
		// answer with a negative index: every caller uses it to subscript.
		{name: "a negative bound is still not a negative index", index: 4, delta: -1, maxOrN: -1, wantStepped: 0, wantCycled: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantStepped, StepCursor(tt.index, tt.delta, tt.maxOrN), "StepCursor clamps")
			assert.Equal(t, tt.wantCycled, CycleIndex(tt.index, tt.delta, tt.maxOrN), "CycleIndex wraps")
		})
	}
}

// Whatever it is handed, neither may hand back an index a caller cannot subscript.
func TestCursorHelpers_NeverLeaveTheList(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, 1, 3, 8} {
		for _, index := range []int{-4, 0, 1, 7, 99} {
			for _, delta := range []int{-9, -1, 0, 1, 9} {
				stepped := StepCursor(index, delta, n-1)
				assert.GreaterOrEqual(t, stepped, 0, "StepCursor(%d,%d,%d)", index, delta, n-1)
				assert.LessOrEqual(t, stepped, max(n-1, 0), "StepCursor(%d,%d,%d)", index, delta, n-1)

				cycled := CycleIndex(index, delta, n)
				assert.GreaterOrEqual(t, cycled, 0, "CycleIndex(%d,%d,%d)", index, delta, n)
				assert.Less(t, cycled, max(n, 1), "CycleIndex(%d,%d,%d)", index, delta, n)
			}
		}
	}
}

// testHand is n distinct cards, so a rendering bug shows up as the wrong card rather
// than hiding behind a repeated one.
func testHand(n int) []deck.Card {
	hand := make([]deck.Card, 0, max(n, 0))
	suits := []deck.Suit{deck.Spades, deck.Hearts, deck.Diamonds, deck.Clubs}
	for i := range n {
		hand = append(hand, deck.Card{
			Rank: deck.AllRanks[i%len(deck.AllRanks)],
			Suit: suits[(i/len(deck.AllRanks))%len(suits)],
		})
	}
	return hand
}
