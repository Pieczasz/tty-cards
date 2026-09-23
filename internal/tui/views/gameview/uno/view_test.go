package uno

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/uno"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"
	"github.com/Pieczasz/terminal-card/internal/tui/views/gameview"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// handOf cycles the four Uno colours and slips in the two wilds, so the colour row
// under the hand has every glyph it can draw to exercise.
func handOf(n int) []deck.Card {
	colors := []deck.Suit{logic.ColorRed, logic.ColorYellow, logic.ColorGreen, logic.ColorBlue}
	hand := make([]deck.Card, 0, n)
	for i := range n {
		switch i {
		case 3:
			hand = append(hand, deck.Card{Rank: logic.Wild})
		case 7:
			hand = append(hand, deck.Card{Rank: logic.WildDrawFour})
		default:
			hand = append(hand, deck.Card{Rank: deck.Rank(i%9 + 1), Suit: colors[i%len(colors)]})
		}
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
			TopDiscard:        deck.Card{Rank: deck.Five, Suit: logic.ColorRed},
			Seats:             append(seatsOf("hero"), opps...),
			Opponents:         opps,
			DeckSize:          20,
			CurrentPlayerName: "hero",
			CurrentPlayerID:   "hero",
			TurnRemaining:     12 * time.Second,
		},
		currentColor: logic.ColorRed,
		direction:    1,
		color:        colorPicker,
	}
}

// Every band is laid out against the terminal, so whatever the table holds the frame
// stays inside it. A band that overran used to be handed to the terminal to wrap, and
// one wrapped row shifts every row under it into confetti.
func TestView_FitsTheTerminal(t *testing.T) {
	t.Parallel()

	for _, size := range tuitest.FitSizes {
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
					m.color.Open = picking
					out := m.View().Content
					assert.LessOrEqual(t, lg.Width(out), size.Width, "picker=%v overran the width", picking)
					assert.LessOrEqual(t, lg.Height(out), size.Height, "picker=%v overran the height", picking)
				}
			})
		}
	}
}

// The colour row is a second walk over the same card slots RenderHand lays out, in a
// different file. Nothing but this test stops the two drifting apart - and a drifted
// row puts every glyph over the wrong card, which in Uno is the whole game.
func TestRenderHandColorRow_LinesUpWithTheHandBelowIt(t *testing.T) {
	t.Parallel()

	for _, handSize := range []int{1, 3, 7, 12, 25, 40} {
		for _, width := range []int{styles.MinWidth, 80, 120, 200} {
			for _, height := range []int{styles.MinHeight, 24, 50} {
				name := fmt.Sprintf("hand=%d_at_%dx%d", handSize, width, height)
				t.Run(name, func(t *testing.T) {
					t.Parallel()

					m := viewAt(width, height, "bob")
					m.Base.Hand = handOf(handSize)
					m.Selected = min(2, handSize-1)

					handWidth, handRows := gameview.HandWidth(width), gameview.HandRows(height)
					row := m.renderHandColorRow(handWidth, handRows)
					hand := gameview.RenderHand(m.Global.Theme, m.Base.Hand, m.Selected, false, handWidth, handRows)

					if !gameview.FansHand(handSize, handWidth, handRows) {
						assert.Empty(t, row, "the strip has no card columns for the row to sit over")
						return
					}
					require.NotEmpty(t, row)
					assert.Equal(t, lg.Width(hand), lg.Width(row),
						"the row and the fan must agree on every slot width")
					assert.LessOrEqual(t, lg.Width(row), handWidth, "the row stays inside the hand's budget")
				})
			}
		}
	}
}

// While the picker is open the hand cursor is frozen, so the colour row must not keep
// highlighting a slot the player can no longer move.
func TestRenderHandColorRow_DropsTheSelectionBehindThePicker(t *testing.T) {
	t.Parallel()

	m := viewAt(120, 50, "bob")
	m.Selected = 4
	handWidth, handRows := gameview.HandWidth(120), gameview.HandRows(50)

	m.color.Open = true
	withPicker := m.renderHandColorRow(handWidth, handRows)
	unselected := gameview.RenderHand(m.Global.Theme, m.Base.Hand, -1, true, handWidth, handRows)

	assert.Equal(t, lg.Width(unselected), lg.Width(withPicker),
		"with nothing selected the row has to match the unselected fan's slots")
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

// The colour indicator is what a player reads to know what a wild switched the pile
// to; the direction arrow is what tells them who plays next.
func TestRenderCenterTable_NamesTheColourAndTheDirection(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		color deck.Suit
		want  string
	}{
		{name: "red", color: logic.ColorRed, want: "Red"},
		{name: "yellow", color: logic.ColorYellow, want: "Yellow"},
		{name: "green", color: logic.ColorGreen, want: "Green"},
		{name: "blue", color: logic.ColorBlue, want: "Blue"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := viewAt(100, 50, "bob")
			m.currentColor = tc.color
			assert.Contains(t, m.renderCenterTable(), tc.want)
		})
	}

	t.Run("an unset colour draws no label", func(t *testing.T) {
		t.Parallel()
		m := viewAt(100, 50, "bob")
		m.currentColor = deck.NoSuit
		assert.NotContains(t, m.renderCenterTable(), "Color:")
	})

	t.Run("the direction flips with the play order", func(t *testing.T) {
		t.Parallel()
		m := viewAt(100, 50, "bob")
		assert.Contains(t, m.renderCenterTable(), "Clockwise")
		m.direction = -1
		assert.Contains(t, m.renderCenterTable(), "Counterclockwise")
	})
}

func TestRenderColorPicker_OffersEveryColour(t *testing.T) {
	t.Parallel()

	m := viewAt(100, 50, "bob")
	assert.Empty(t, m.color.Render(m.Global.Theme), "closed, the picker draws nothing")

	m.color.Open = true
	out := m.color.Render(m.Global.Theme)
	for _, c := range colorPicker.Choices {
		assert.Contains(t, out, c.Label)
	}
	assert.Contains(t, out, "Pick a color")
}

func TestView_ShowsTheLastRejectedActionAndStillFits(t *testing.T) {
	t.Parallel()

	m := viewAt(styles.MinWidth, styles.MinHeight, "bob")
	m.ActionErr = errors.New("that card does not match the colour")

	out := m.View().Content
	assert.Contains(t, out, "does not match")
	assert.LessOrEqual(t, lg.Height(out), styles.MinHeight)
}
