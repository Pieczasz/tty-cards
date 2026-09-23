package game

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/game/crazyeight"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"

	lg "charm.land/lipgloss/v2"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testHand(n int) []deck.Card {
	suits := []deck.Suit{deck.Spades, deck.Hearts, deck.Diamonds, deck.Clubs}
	hand := make([]deck.Card, 0, n)
	for i := range n {
		hand = append(hand, deck.Card{Rank: deck.Rank(i%13 + 1), Suit: suits[i%len(suits)]})
	}
	return hand
}

// The hand is the one thing a player must always be able to read, so it may never
// outgrow the columns or the rows it was budgeted - a hand that overflowed would be
// wrapped by the terminal and shift the key hints off the bottom of the screen.
func TestRenderHand_StaysInsideItsBudget(t *testing.T) {
	t.Parallel()

	theme := styles.NewTheme(true)
	for _, size := range []int{1, 5, 13, 27, 40} {
		for _, width := range []int{20, HandWidth(styles.MinWidth), HandWidth(80), HandWidth(200)} {
			for _, rows := range []int{HandRows(styles.MinHeight), HandRows(24), HandRows(50)} {
				name := fmt.Sprintf("hand=%d_width=%d_rows=%d", size, width, rows)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					hand := testHand(size)

					out := RenderHand(theme, hand, 2, false, width, rows)
					multi := RenderHandMulti(theme, hand, map[int]struct{}{0: {}}, 1, width, rows)

					assert.LessOrEqual(t, lg.Width(out), width)
					assert.LessOrEqual(t, lg.Width(multi), width)

					// maxRows bounds the fan; the strip wraps to what the hand needs
					// rather than hiding cards the player has to choose between.
					if rows > 0 && FansHand(size, width, rows) {
						assert.LessOrEqual(t, lg.Height(out), rows)
						assert.LessOrEqual(t, lg.Height(multi), rows)
					}
				})
			}
		}
	}
}

func TestRenderHand_AnEmptyHandDrawsNothing(t *testing.T) {
	t.Parallel()

	theme := styles.NewTheme(true)
	assert.Empty(t, RenderHand(theme, nil, 0, false, 80, 0))
	assert.Empty(t, RenderHandMulti(theme, nil, nil, 0, 80, 0))
}

// FansHand is what a decorator (Uno's colour row) asks instead of guessing, so it has
// to give exactly the answer the renderer acts on. A disagreement paints slot-spaced
// glyphs over a strip that has no slots.
func TestFansHand_AgreesWithWhatRenderHandDraws(t *testing.T) {
	t.Parallel()

	theme := styles.NewTheme(true)
	for _, size := range []int{0, 1, 7, 20, 40} {
		for _, width := range []int{12, 40, 78, 198} {
			for _, rows := range []int{0, components.FanRows, components.FanRows + 1, stripHandRows} {
				name := fmt.Sprintf("hand=%d_width=%d_rows=%d", size, width, rows)
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					hand := testHand(size)
					// Whether the renderer fanned is not a guess: it either drew the
					// fan or fell back to the very strip components exposes.
					drewTheStrip := RenderHand(theme, hand, 0, false, width, rows) ==
						components.RenderStrip(theme, hand, nil, 0, width)
					fanned := size > 0 && !drewTheStrip

					assert.Equal(t, fanned, FansHand(size, width, rows),
						"the predicate and the renderer must make the same choice")
				})
			}
		}
	}
}

// Selection is suppressed, not just unhighlighted: while a picker is open the cursor
// is frozen, and a highlight on a card the player cannot move reads as a live cursor.
func TestRenderHand_DisablingSelectionDropsTheHighlight(t *testing.T) {
	t.Parallel()

	theme := styles.NewTheme(true)
	hand := testHand(6)

	disabled := RenderHand(theme, hand, 3, true, HandWidth(120), 0)
	none := RenderHand(theme, hand, -1, false, HandWidth(120), 0)
	assert.Equal(t, none, disabled)
}

// The three breakpoints are read by every game view to decide whether to draw card art
// at all, so they have to answer on either dimension: a 200x20 terminal is short, not
// roomy, and a 40x60 one is narrow.
func TestCompactBreakpoints_AnswerOnEitherDimension(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name                   string
		width, height          int
		compact, superCompact  bool
		handRowsAreTheStripCap bool
	}{
		{name: "a roomy terminal", width: 120, height: 50},
		{name: "short but wide", width: 200, height: 20, compact: true, superCompact: true, handRowsAreTheStripCap: true},
		{name: "tall but narrow", width: 40, height: 60, compact: true, superCompact: true},
		{name: "just inside the compact breakpoint", width: compactWidth, height: compactHeight},
		{name: "one row under it", width: compactWidth, height: compactHeight - 1, compact: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.compact, IsCompact(tc.width, tc.height))
			assert.Equal(t, tc.superCompact, isSuperCompact(tc.width, tc.height))

			want := 0
			if tc.handRowsAreTheStripCap {
				want = stripHandRows
			}
			assert.Equal(t, want, HandRows(tc.height))
		})
	}
}

// HandWidth leaves a column at each edge so a full-width fan is not flush against it,
// and never goes negative - Global.Width is 0 until the first WindowSizeMsg.
func TestHandWidth_LeavesAnEdgeAndNeverGoesNegative(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 78, HandWidth(80))
	assert.Equal(t, 0, HandWidth(1))
	assert.Equal(t, 0, HandWidth(0))
}

// The hero band's height comes straight off the middle band's, so an empty row is a
// row of the table lost. Dropping them is also what keeps a game that has no error
// line and no decoration from paying for either.
func TestRenderHeroBand_DropsEmptyRowsAndAppendsTheError(t *testing.T) {
	t.Parallel()

	theme := styles.NewTheme(true)

	withGaps := RenderHeroBand(theme, nil, "status", "", "hand", "")
	assert.Equal(t, lg.JoinVertical(lg.Center, "status", "hand"), withGaps)

	withErr := RenderHeroBand(theme, errors.New("not your turn"), "status", "hand")
	assert.Contains(t, withErr, "not your turn")
	assert.Equal(t, lg.Height(withGaps)+1, lg.Height(withErr), "the error costs exactly one row")

	assert.Empty(t, RenderHeroBand(theme, nil, "", ""))
}

func TestRenderWaitingScreen_FitsTheTerminal(t *testing.T) {
	t.Parallel()

	for _, size := range tuitest.FitSizes {
		for _, tc := range []struct {
			name  string
			phase game.Phase
			want  string
		}{
			{name: "waiting", phase: game.Waiting, want: "Waiting for game to start"},
			{name: "finished", phase: game.Finished, want: "Game Over"},
		} {
			t.Run(fmt.Sprintf("%dx%d_%s", size.Width, size.Height, tc.name), func(t *testing.T) {
				t.Parallel()
				out := RenderWaitingScreen(testContext(size.Width, size.Height), tc.phase, "alice")

				assert.Contains(t, out, tc.want)
				assert.LessOrEqual(t, lg.Width(out), size.Width)
				assert.LessOrEqual(t, lg.Height(out), size.Height)
			})
		}
	}
}

func TestClockTick_StartsBeforeADeadlineIsKnown(t *testing.T) {
	t.Parallel()
	s := &Session{}
	assert.NotNil(t, s.ClockTick())
}

// A username is the one string on a table that a player chooses, so the belt-and-
// braces check is that nothing which could carry an escape sequence can be one. The
// braces are the DB CHECK constraint; this is the belt, kept next to the code that
// paints seat names.
func TestSeatNames_CannotCarryATerminalEscape(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		username string
	}{
		{name: "an OSC title change", username: "\x1b]0;pwned\x07"},
		{name: "a CSI colour", username: "\x1b[31mred"},
		{name: "a bare escape", username: "bob\x1b"},
		{name: "a bell", username: "bob\x07"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Error(t, db.ValidateUsername(tc.username),
				"a name that reaches a seat has to be rejected before it is stored")
		})
	}

	// And the ID a missing username falls back to is the engine's own seat key, never
	// anything a player typed.
	base := BaseState{Seats: []game.PlayerSnapshot{{ID: "17"}}}
	assert.Equal(t, map[string]string{"17": ""}, base.SeatNames())
}

// Submit is the one path from a keystroke to the engine. Without a seat it refuses
// rather than panicking on a nil handle, which is the frame after a disconnect.
func TestSession_SubmitCountsARejection(t *testing.T) {
	t.Parallel()

	players := []*game.Player{{ID: testutil.SeatID(1), UserID: testutil.UID(1), Name: "alice"}, {ID: testutil.SeatID(2), UserID: testutil.UID(2), Name: "bob"}}
	engine := game.NewEngine(&crazyeight.Rules{}, players, deck.Standard())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	// The seat that is not on turn: submitting from it is the rejection path, and the
	// assertion is that it comes back as an error rather than reaching the rules.
	offTurn := testutil.SeatID(1)
	if engine.CurrentPlayerID() == offTurn {
		offTurn = testutil.SeatID(2)
	}
	s := Session{Bound: game.Bind(engine, offTurn), gameName: "crazy eights"}
	require.Error(t, s.Submit(crazyeight.ActionDrawCard{}))

	var unseated Session
	assert.ErrorIs(t, unseated.Submit(crazyeight.ActionDrawCard{}), errNotSeated)
}

// The key line is how a player learns which keys play a card, so it survives every
// size the server admits - a small terminal loses the margins around it, not the line.
// A key line longer than the terminal wraps rather than being cut, because the band is
// centred on its widest row and a clipped line drags the hand above it sideways.
func TestRenderBands_KeepsTheKeyLineAtEverySize(t *testing.T) {
	t.Parallel()

	for _, size := range []struct{ w, h int }{
		{styles.MinWidth, styles.MinHeight},
		{70, 22},
		{80, 24},
		{120, 50},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			t.Parallel()

			g := testContext(size.w, size.h)
			out := RenderBands(g, "TOP", "MYHAND", HandKeyHints, func(int) string { return "MID" })

			assert.Contains(t, out, "MYHAND")
			// Wrapped, not clipped: the last key is still on screen, on a second row
			// when the line is longer than the terminal.
			assert.Contains(t, out, "leave/cancel", "the last key of the line is not cut off")
			assert.LessOrEqual(t, lg.Width(out), size.w)
			assert.LessOrEqual(t, lg.Height(out), size.h)
		})
	}
}

func BenchmarkRenderBands(b *testing.B) {
	g := testContext(120, 50)
	theme := g.Theme
	hand := RenderHand(theme, testHand(13), 4, false, HandWidth(120), 0)
	top := strings.Repeat("seat  ", 4)
	mid := strings.Repeat("table\n", 10)

	b.ReportAllocs()
	for b.Loop() {
		_ = RenderBands(g, top, hand, HandKeyHints, func(int) string { return mid })
	}
}
