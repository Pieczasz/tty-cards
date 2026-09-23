package poker

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seatZones splits the opponents around the table with hand-written slice
// arithmetic per seat count. Poker seats nine, so every branch is reachable, and an
// off-by-one either drops a player from the table or shows one twice.
func TestSeatZones_PlacesEveryOpponentExactlyOnce(t *testing.T) {
	t.Parallel()

	for total := 1; total <= 9; total++ {
		t.Run(fmt.Sprintf("seats=%d", total), func(t *testing.T) {
			t.Parallel()

			m := &Model{seats: make([]Seat, 0, total)}
			for i := range total {
				m.seats = append(m.seats, Seat{PlayerID: fmt.Sprintf("p%d", i), IsHero: i == 0})
			}

			z := m.seatZones()
			placed := make([]Seat, 0, total)
			placed = append(placed, z.Left...)
			placed = append(placed, z.Top...)
			placed = append(placed, z.Right...)

			wantOpponents := total - 1
			require.Len(t, placed, wantOpponents, "every opponent gets exactly one zone")

			seen := map[string]bool{}
			for _, s := range placed {
				assert.False(t, seen[s.PlayerID], "%s placed twice", s.PlayerID)
				seen[s.PlayerID] = true
				assert.False(t, s.IsHero, "the hero is never an opponent")
			}
		})
	}
}

// With no hero seated - a spectator, or state that has not synced yet - everyone is
// an opponent and nobody may be dropped.
func TestSeatZones_WithoutAHeroPlacesEverybody(t *testing.T) {
	t.Parallel()

	m := &Model{seats: []Seat{{PlayerID: "a"}, {PlayerID: "b"}, {PlayerID: "c"}}}
	z := m.seatZones()

	// ElementsMatch, not Len: a duplicated seat standing in for a dropped one keeps
	// the count right while losing a player off the table.
	placed := slices.Concat(z.Left, z.Top, z.Right)
	assert.ElementsMatch(t, m.seats, placed)
}

// The mini card used to derive a numeric rank as int(rank)+1, which printed every
// pip card one higher than it was and turned the ace of the deck's 1-based ranks
// into a "2". The label now comes from the table the full card faces use.
func TestRenderMiniCard_PrintsTheRankOnTheCard(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	tests := []struct {
		rank deck.Rank
		want string
	}{
		{deck.Ace, "A"},
		{deck.Two, "2"},
		{deck.Three, "3"},
		{deck.Four, "4"},
		{deck.Five, "5"},
		{deck.Six, "6"},
		{deck.Seven, "7"},
		{deck.Eight, "8"},
		{deck.Nine, "9"},
		{deck.Ten, "10"},
		{deck.Jack, "J"},
		{deck.Queen, "Q"},
		{deck.King, "K"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			got := stripANSI(renderMiniCard(theme, deck.Card{Rank: tt.rank, Suit: deck.Hearts}))
			assert.Equal(t, fmt.Sprintf("[%2s♥]", tt.want), got)
		})
	}
}

// Every mini card is the same width, so a ten on the board does not shift the
// cards beside it by a column.
func TestRenderMiniCard_IsAFixedWidth(t *testing.T) {
	t.Parallel()
	theme := styles.NewTheme(true)

	want := lg.Width(stripANSI(renderMiniCard(theme, deck.Card{Rank: deck.Ace, Suit: deck.Spades})))
	for _, rank := range []deck.Rank{deck.Ten, deck.King, deck.Two} {
		got := lg.Width(stripANSI(renderMiniCard(theme, deck.Card{Rank: rank, Suit: deck.Spades})))
		assert.Equal(t, want, got, "rank %d changes the card width", rank)
	}
}

// A busted seat is dealt no cards. Drawing a fixed pair of backs for it showed a
// hand at a seat that is out of the match.
func TestRenderSeatCards_ShowsNoBacksForASeatWithNoCards(t *testing.T) {
	t.Parallel()

	m := &Model{Global: router.GlobalContext{Theme: styles.NewTheme(true)}}
	busted := Seat{Name: "broke", HandSize: 0}

	for _, compact := range []bool{false, true} {
		t.Run(fmt.Sprintf("compact=%v", compact), func(t *testing.T) {
			t.Parallel()
			assert.Empty(t, stripANSI(m.renderSeatCards(busted, compact)))
		})
	}

	dealt := Seat{Name: "live", HandSize: 2}
	assert.NotEmpty(t, stripANSI(m.renderSeatCards(dealt, true)),
		"a seat holding cards still shows them face down")
}

func TestBoardAndHolePlaceholdersMatchCardFootprint(t *testing.T) {
	t.Parallel()
	th := styles.NewTheme(true)
	card := components.RenderCard(th, deck.Card{Rank: deck.Ace, Suit: deck.Spades}, false)
	empty := renderEmptySlot(th)
	back := renderFacedownCard(th)

	assert.Equal(t, lg.Width(card), lg.Width(empty), "board placeholder width")
	assert.Equal(t, lg.Height(card), lg.Height(empty), "board placeholder height")
	assert.Equal(t, lg.Width(card), lg.Width(back), "hole back width")
	assert.Equal(t, lg.Height(card), lg.Height(back), "hole back height")
}

func TestSeatZones_EmptyTable(t *testing.T) {
	t.Parallel()

	z := (&Model{}).seatZones()
	assert.Empty(t, z.Left)
	assert.Empty(t, z.Top)
	assert.Empty(t, z.Right)
}

// tableOf builds a rendered table of n seats with the hero first, mid-hand on the
// flop. Hole cards are dealt to the hero only, which is what the table looks like for
// all but the last frame of a hand.
func tableOf(width, height, n int) *Model {
	seats := make([]Seat, 0, n)
	for i := range n {
		s := Seat{
			PlayerID: fmt.Sprintf("p%d", i),
			Name:     fmt.Sprintf("player_%d", i),
			Chips:    uint(500 - 40*i),
			Bet:      uint(25 * i),
			IsHero:   i == 0,
			IsDealer: i == 0,
			IsSB:     i == 1%n,
			IsBB:     i == 2%n,
			IsTurn:   i == 0,
			HandSize: 2,
		}
		if s.IsHero {
			s.Hole = []deck.Card{{Rank: deck.Ace, Suit: deck.Spades}, {Rank: deck.King, Suit: deck.Hearts}}
		}
		seats = append(seats, s)
	}
	m := &Model{
		seats: seats,
		board: []deck.Card{
			{Rank: deck.Two, Suit: deck.Clubs},
			{Rank: deck.Seven, Suit: deck.Diamonds},
			{Rank: deck.Ten, Suit: deck.Spades},
			{Rank: deck.Jack, Suit: deck.Hearts},
			{Rank: deck.Queen, Suit: deck.Clubs},
		},
		pot: 480, sidePots: 2, street: "RIVER",
		currentBet: 50, toCall: 50, myChips: 500,
		raiseMin: 100, raiseMax: 500, raiseOK: true,
		handNumber: 3, handsTotal: 10, winnerName: "player_1",
	}
	m.Global = router.GlobalContext{Theme: styles.NewTheme(true), Width: width, Height: height}
	m.Base = gameview.BaseState{
		Phase: game.Playing, MyTurn: true,
		CurrentPlayer: "player_0", CurrentPlayerID: "p0",
		TurnRemaining: 14 * time.Second,
	}
	return m
}

// Nine seats of card art is wider than any terminal, so the frame clamps rather than
// letting the terminal wrap it - one wrapped row shifts every row under it.
func TestView_FitsTheTerminal(t *testing.T) {
	t.Parallel()

	screens := map[string]func(*Model){
		"mid hand":       func(*Model) {},
		"an empty board": func(m *Model) { m.board = nil },
		"the raise prompt": func(m *Model) {
			m.raising = true
			m.raiseAmount = 200
		},
		"the hand over":  func(m *Model) { m.handComplete = true },
		"the match over": func(m *Model) { m.handComplete, m.matchComplete = true, true },
	}

	for _, size := range []struct{ w, h int }{
		{styles.MinWidth, styles.MinHeight},
		{80, 24},
		{120, 50},
	} {
		for _, n := range []int{2, 6, 9} {
			for name, setup := range screens {
				sub := fmt.Sprintf("%dx%d_%dseats_%s", size.w, size.h, n, strings.ReplaceAll(name, " ", "_"))
				t.Run(sub, func(t *testing.T) {
					t.Parallel()
					m := tableOf(size.w, size.h, n)
					setup(m)

					out := m.View().Content
					assert.LessOrEqual(t, lg.Width(out), size.w)
					assert.LessOrEqual(t, lg.Height(out), size.h)
				})
			}
		}
	}
}

// The results screen is where the hand is settled, so it has to name the winner, list
// every stack and show the board the pot was won on.
func TestRenderHandOver_ShowsTheSettlement(t *testing.T) {
	t.Parallel()

	m := tableOf(120, 50, 4)
	m.handComplete = true
	out := m.View().Content
	assert.Contains(t, out, "HAND 3/10 COMPLETE")
	assert.Contains(t, out, "player_1 wins the hand")
	assert.Contains(t, out, "player_3")

	m.matchComplete = true
	assert.Contains(t, m.View().Content, "MATCH COMPLETE")
}

// A folded seat and a shown-down pair are both drawn on the results screen, and a
// folded one must say so rather than look like a seat that never played.
func TestRenderHandOver_MarksFoldedSeatsAndShowsRevealedHands(t *testing.T) {
	t.Parallel()

	m := tableOf(120, 50, 3)
	m.handComplete = true
	m.seats[1].Folded = true
	m.seats[2].Hole = []deck.Card{{Rank: deck.Nine, Suit: deck.Clubs}, {Rank: deck.Nine, Suit: deck.Spades}}

	out := m.View().Content
	assert.Contains(t, out, "folded")
	assert.LessOrEqual(t, lg.Width(out), 120)
}

// The action bar is the whole control surface: an option it does not offer is a move
// the player cannot make, and one it offers that the engine rejects is worse.
func TestRenderActionBar_OffersOnlyTheLegalMoves(t *testing.T) {
	t.Parallel()

	t.Run("facing a bet", func(t *testing.T) {
		t.Parallel()
		m := tableOf(120, 50, 3)
		bar := m.renderActionBar()
		assert.Contains(t, bar, "f fold")
		assert.Contains(t, bar, "c call 50")
		assert.NotContains(t, bar, "c check")
	})

	t.Run("checked to", func(t *testing.T) {
		t.Parallel()
		m := tableOf(120, 50, 3)
		m.toCall, m.currentBet = 0, 0
		bar := m.renderActionBar()
		assert.Contains(t, bar, "c check")
		assert.Contains(t, bar, "r raise")
		assert.Contains(t, bar, "a all-in")
	})

	t.Run("off turn", func(t *testing.T) {
		t.Parallel()
		m := tableOf(120, 50, 3)
		m.Base.MyTurn = false
		assert.Contains(t, m.renderActionBar(), "waiting")
	})

	t.Run("busted", func(t *testing.T) {
		t.Parallel()
		m := tableOf(120, 50, 3)
		m.seats[0].Chips, m.seats[0].Bet = 0, 0
		m.raiseOK = false // what logic.RaiseBounds reports for an empty stack
		bar := m.renderActionBar()
		assert.NotContains(t, bar, "r raise")
		assert.NotContains(t, bar, "a all-in")
	})

	t.Run("the hand is over", func(t *testing.T) {
		t.Parallel()
		m := tableOf(120, 50, 3)
		m.Base.MyTurn = false
		m.handComplete = true
		assert.Contains(t, m.renderActionBar(), "esc -> lobby")
	})
}

// The prompt has to show the running total, the bounds it moves between and the keys
// that move it - a raise built blind is a raise the engine rejects.
func TestRenderRaisePrompt_ShowsTheAmountItsBoundsAndTheRack(t *testing.T) {
	t.Parallel()

	m := tableOf(120, 50, 3)
	m.raising = true
	m.raiseAmount = 250

	out := m.renderActionBar()
	assert.Contains(t, out, "RAISE TO 250")
	assert.Contains(t, out, "min 100")
	assert.Contains(t, out, "enter confirm")
	for _, d := range chipDenoms {
		assert.Contains(t, out, d.Glyph, "the rack doubles as the legend for the seat stacks")
	}
}

// r opens the prompt at the smallest legal raise, esc takes it back down, and neither
// may reach the engine on its own.
func TestRaisePrompt_OpensAndCancelsWithoutSubmitting(t *testing.T) {
	t.Parallel()

	m := tableOf(120, 50, 3)
	_, _ = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	require.True(t, m.raising)
	assert.Equal(t, m.raiseMin, m.raiseAmount, "the prompt opens on a legal amount")

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	assert.Nil(t, cmd, "esc cancels the prompt rather than leaving the table")
	assert.False(t, m.raising)
}

func TestBeginRaise_IsANoOpWhenARaiseIsIllegal(t *testing.T) {
	t.Parallel()

	m := tableOf(120, 50, 3)
	m.seats[0].Chips = 0 // busted: nothing left to raise with
	m.raiseOK = false

	_, _ = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	assert.False(t, m.raising)
}

// Every key the action bar advertises has to reach the engine on the hero's turn and
// be swallowed off it. A key that fires off turn is a rejected action the player has
// to read an error for; one that never fires is a move they cannot make at all.
func TestHandleKey_DispatchesEveryAdvertisedAction(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		key    string
		toCall uint
	}{
		{name: "fold", key: "f", toCall: 50},
		{name: "call", key: "c", toCall: 50},
		{name: "check", key: "c"},
		{name: "all in", key: "a", toCall: 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := tableOf(120, 50, 3)
			m.toCall = tc.toCall
			m.Base.MyTurn = false

			_, _ = m.Update(tea.KeyPressMsg{Code: rune(tc.key[0]), Text: tc.key})
			require.NoError(t, m.lastActionErr, "off turn the key never reaches the engine")

			// With no engine bound, submit stops at the nil check - which is the same
			// guard that keeps a key from acting for a seat this session does not hold.
			m.Base.MyTurn = true
			_, cmd := m.Update(tea.KeyPressMsg{Code: rune(tc.key[0]), Text: tc.key})
			assert.Nil(t, cmd, "an action key never navigates away on its own")
		})
	}
}

// [ and ] nudge the raise, and the digits push whole chips onto it. Both are inert
// until the prompt is open, or a stray keystroke would build a raise nobody asked for.
func TestHandleKey_RaiseNudgesAreInertUntilThePromptIsOpen(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"[", "]", "h", "l", "1", "4"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			m := tableOf(120, 50, 3)

			_, _ = m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
			assert.Zero(t, m.raiseAmount, "nothing is staged while the prompt is shut")
			assert.False(t, m.raising)
		})
	}

	t.Run("with the prompt open they move the amount", func(t *testing.T) {
		t.Parallel()
		m := tableOf(120, 50, 3)
		_, _ = m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
		opened := m.raiseAmount

		_, _ = m.Update(tea.KeyPressMsg{Code: ']', Text: "]"})
		assert.Greater(t, m.raiseAmount, opened)

		_, _ = m.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
		assert.Greater(t, m.raiseAmount, opened)
	})
}

// Enter means "leave" only once the match is over; with hands still to play it deals.
// Confusing the two forfeits the stack the player just spent the match building.
func TestConfirm_LeavesOnlyAFinishedMatch(t *testing.T) {
	t.Parallel()

	m := tableOf(120, 50, 3)
	m.handComplete = true
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.Nil(t, cmd, "between hands enter deals rather than leaving")

	m.matchComplete = true
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.NotNil(t, cmd, "once the match is over enter is the way out")
}

func TestHandleEscape_LeavesTheTableWhenNoPromptIsOpen(t *testing.T) {
	t.Parallel()

	m := tableOf(120, 50, 3)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	assert.NotNil(t, cmd)
}

func TestHandleKey_AnUnboundKeyChangesNothing(t *testing.T) {
	t.Parallel()

	m := tableOf(120, 50, 3)
	before := *m
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'z', Text: "z"})
	assert.Nil(t, cmd)
	assert.Equal(t, before.raiseAmount, m.raiseAmount)
	assert.Equal(t, before.raising, m.raising)
}

func TestHandOverHint_SaysNothingIsLeftForABustedHero(t *testing.T) {
	t.Parallel()

	m := tableOf(120, 50, 3)
	m.handComplete = true
	m.seats[0].Chips = 0
	assert.Contains(t, m.handOverHint(), "out of chips")

	m.Base.CurrentPlayer = ""
	m.seats[0].Chips = 100
	m.Base.MyTurn = false
	assert.Contains(t, m.handOverHint(), "waiting for the next hand")
}
