package crazyeight

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"
	"github.com/Pieczasz/tty-cards/internal/tui/views/gameview"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
)

// handOf builds n distinct cards, which is what a hand has to be for the fan to lay
// out the way a real one does.
func handOf(n int) []deck.Card {
	hand := make([]deck.Card, 0, n)
	suits := []deck.Suit{deck.Spades, deck.Hearts, deck.Diamonds, deck.Clubs}
	for i := range n {
		hand = append(hand, deck.Card{Rank: deck.Rank(i%13 + 1), Suit: suits[i%len(suits)]})
	}
	return hand
}

func seatsOf(names ...string) []game.PlayerSnapshot {
	seats := make([]game.PlayerSnapshot, 0, len(names))
	for i, n := range names {
		seats = append(seats, game.PlayerSnapshot{ID: n, Name: n, HandSize: 3 + i})
	}
	return seats
}

func viewAt(width, height int, opponents ...string) *model {
	opps := seatsOf(opponents...)
	return &model{
		Global: router.GlobalContext{Theme: styles.NewTheme(true), Width: width, Height: height},
		Base: gameview.BaseState{
			Phase:             game.Playing,
			MyTurn:            true,
			Hand:              handOf(12),
			TopDiscard:        deck.Card{Rank: deck.Nine, Suit: deck.Hearts},
			Seats:             append(seatsOf("hero"), opps...),
			Opponents:         opps,
			DeckSize:          20,
			CurrentPlayerName: "hero",
			CurrentPlayerID:   "hero",
			TurnRemaining:     12 * time.Second,
		},
		currentSuit: deck.Hearts,
		Picker:      suitPicker,
	}
}

// Every band is laid out against the terminal, so whatever the table holds the frame
// stays inside it. A band that overran used to be handed to the terminal to wrap, and
// one wrapped row shifts every row under it into confetti.
func TestView_FitsTheTerminal(t *testing.T) {
	t.Parallel()

	sizes := tuitest.FitSizes
	for _, size := range sizes {
		for _, opponents := range [][]string{
			{"bob"},
			{"bob", "carol", "dave"},
			{"bob", "carol", "dave", "erin", "frank", "grace"},
		} {
			name := fmt.Sprintf("%dx%d_with_%d_opponents", size.Width, size.Height, len(opponents))
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				m := viewAt(size.Width, size.Height, opponents...)

				for _, picking := range []bool{false, true} {
					m.Picker.Open = picking
					out := m.View().Content
					assert.LessOrEqual(t, lg.Width(out), size.Width, "picker=%v overran the width", picking)
					assert.LessOrEqual(t, lg.Height(out), size.Height, "picker=%v overran the height", picking)
				}
			})
		}
	}
}

// The middle band is clamped from the top, so a hand that takes one row too many cuts
// the pile's top border and rank, or the picker's frame: a fanned hand did it at 26
// rows, and a long strip at 64x20.
func TestView_KeepsThePileAndThePickerWhole(t *testing.T) {
	t.Parallel()

	for _, width := range []int{styles.MinWidth, 72, 80, 100, 120} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			t.Parallel()
			for height := styles.MinHeight; height <= 50; height++ {
				for _, cards := range []int{7, 10, 12, 46} {
					at := fmt.Sprintf("%dx%d, %d cards", width, height, cards)
					m := viewAt(width, height, "bob")
					m.Base.Hand = handOf(cards)

					for _, myTurn := range []bool{true, false} {
						m.Base.MyTurn = myTurn
						out := tuitest.StripANSI(m.View().Content)
						assert.Containsf(t, out, "╭─────────╮   ╭─────────╮", "%s, on turn %v: the pile is cut", at, myTurn)
					}

					m.Base.MyTurn, m.Picker.Open = true, true
					picker := tuitest.StripANSI(m.Picker.Render(m.Global.Palette()))
					topBorder, _, _ := strings.Cut(picker, "\n")
					assert.Containsf(t, tuitest.StripANSI(m.View().Content), topBorder, "%s: the picker is cut", at)
				}
			}
		})
	}
}

// The error line is part of the hero band, so a rejected move has to reach the screen
// without pushing the frame past the terminal.
func TestView_ShowsTheLastRejectedActionAndStillFits(t *testing.T) {
	t.Parallel()

	m := viewAt(styles.MinWidth, styles.MinHeight, "bob")
	m.ActionErr = errors.New("that card does not match the suit")

	out := m.View().Content
	assert.Contains(t, out, "does not match")
	assert.LessOrEqual(t, lg.Height(out), styles.MinHeight)
}

func TestView_WaitingAndFinishedScreens(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		phase game.Phase
		want  string
	}{
		{name: "waiting for the table to start", phase: game.Waiting, want: "Waiting for game to start"},
		{name: "the game is over", phase: game.Finished, want: "Game Over"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := viewAt(80, 24, "bob")
			m.Base.Phase = tc.phase
			m.Base.WinnerName = "bob"

			out := m.View().Content
			assert.Contains(t, out, tc.want)
			assert.LessOrEqual(t, lg.Height(out), 24)
		})
	}
}

// The indicator is the only thing telling a player which suit an eight switched the
// pile to, so every suit has to name itself and an unset one has to draw nothing
// rather than a stray label.
func TestRenderCurrentSuitIndicator_NamesEverySuit(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		suit deck.Suit
		want string
	}{
		{name: "spades", suit: deck.Spades, want: "Spades"},
		{name: "hearts", suit: deck.Hearts, want: "Hearts"},
		{name: "diamonds", suit: deck.Diamonds, want: "Diamonds"},
		{name: "clubs", suit: deck.Clubs, want: "Clubs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := viewAt(80, 40, "bob")
			m.currentSuit = tc.suit
			assert.Contains(t, m.renderCurrentSuitIndicator(), tc.want)
		})
	}

	t.Run("no suit draws nothing", func(t *testing.T) {
		t.Parallel()
		m := viewAt(80, 40, "bob")
		m.currentSuit = deck.NoSuit
		assert.Empty(t, m.renderCurrentSuitIndicator())
	})
}

// The picker replaces the pile in the centre of the table, so it has to name all four
// suits and mark the one under the cursor.
func TestRenderSuitPicker_OffersEverySuit(t *testing.T) {
	t.Parallel()

	m := viewAt(100, 40, "bob")
	assert.Empty(t, m.Picker.Render(m.Global.Palette()), "closed, the picker draws nothing")

	m.Picker.Open = true
	m.Picker.Cursor = 2
	out := m.Picker.Render(m.Global.Palette())
	for _, c := range suitPicker.Choices {
		assert.Contains(t, out, c.Label)
	}
	assert.Contains(t, out, "Pick a suit")
}
