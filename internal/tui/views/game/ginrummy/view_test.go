package ginrummy

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/ginrummy"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestView_ActionBarHints(t *testing.T) {
	t.Parallel()
	m := &Model{
		Global: router.GlobalContext{
			Theme:  styles.NewTheme(true),
			Width:  80,
			Height: 40,
		},
		Base: gameview.BaseState{
			Phase: game.Playing,
			Hand:  []deck.Card{{Rank: deck.Ace, Suit: deck.Spades}},
		},
		handPhase:  logic.AwaitingDraw,
		handNumber: 1,
		seatOrder:  []string{"1", "2"},
		seatNames:  map[string]string{"1": "alice", "2": "bob"},
	}

	out := m.View().Content
	require.NotEmpty(t, out)
	assert.Contains(t, out, "draw stock")

	m.handPhase = logic.AwaitingDiscard
	out = m.View().Content
	assert.Contains(t, out, "knock")
}

func TestView_HandOverWallBanner(t *testing.T) {
	t.Parallel()
	m := &Model{
		Global: router.GlobalContext{
			Theme:  styles.NewTheme(true),
			Width:  80,
			Height: 24,
		},
		Base:             gameview.BaseState{Phase: game.Playing},
		handPhase:        logic.HandOver,
		handComplete:     true,
		handNumber:       2,
		seatOrder:        []string{"1", "2"},
		seatNames:        map[string]string{"1": "alice", "2": "bob"},
		lastHandResult:   &logic.HandResult{Wall: true},
		cumulativeScores: map[string]int{"1": 10, "2": 5},
	}

	out := m.View().Content
	require.NotEmpty(t, out)
	assert.Contains(t, out, "HAND 2 COMPLETE")
	assert.Contains(t, out, "WALL")
}

func handOf(n int) []deck.Card {
	suits := []deck.Suit{deck.Spades, deck.Hearts, deck.Diamonds, deck.Clubs}
	hand := make([]deck.Card, 0, n)
	for i := range n {
		hand = append(hand, deck.Card{Rank: deck.Rank(i%13 + 1), Suit: suits[i%len(suits)]})
	}
	return hand
}

func viewAt(width, height int) *Model {
	opponent := game.PlayerSnapshot{ID: "2", Username: "bob", HandSize: 10}
	return &Model{
		Global: router.GlobalContext{Theme: styles.NewTheme(true), Width: width, Height: height},
		Base: gameview.BaseState{
			Phase:           game.Playing,
			MyTurn:          true,
			Hand:            handOf(11),
			TopDiscard:      deck.Card{Rank: deck.Seven, Suit: deck.Clubs},
			Seats:           []game.PlayerSnapshot{{ID: "1", Username: "alice", HandSize: 11}, opponent},
			Opponents:       []game.PlayerSnapshot{opponent},
			DeckSize:        20,
			CurrentPlayer:   "alice",
			CurrentPlayerID: "1",
			TurnRemaining:   9 * time.Second,
		},
		handPhase:        logic.AwaitingDiscard,
		handNumber:       3,
		stockSize:        20,
		seatOrder:        []string{"1", "2"},
		seatNames:        map[string]string{"1": "alice", "2": "bob"},
		cumulativeScores: map[string]int{"1": 40, "2": 22},
	}
}

// Every screen this view can be in has to stay inside the terminal: the table, the
// hand-over summary with its meld boxes, and the end of the match.
func TestView_FitsTheTerminal(t *testing.T) {
	t.Parallel()

	screens := map[string]func(*Model){
		"the table":        func(*Model) {},
		"awaiting a draw":  func(m *Model) { m.handPhase = logic.AwaitingDraw },
		"the hand summary": func(m *Model) { m.handComplete = true; m.lastHandResult = knockResult() },
		"a gin":            func(m *Model) { m.handComplete = true; m.lastHandResult = ginResult() },
		"the match over": func(m *Model) {
			m.matchComplete = true
			m.Base.Phase = game.Finished
			m.Base.Winner = "alice"
			m.lastHandResult = knockResult()
		},
	}

	for _, size := range []struct{ w, h int }{
		{styles.MinWidth, styles.MinHeight},
		{80, 24},
		{120, 50},
	} {
		for name, setup := range screens {
			t.Run(fmt.Sprintf("%dx%d_%s", size.w, size.h, strings.ReplaceAll(name, " ", "_")), func(t *testing.T) {
				t.Parallel()
				m := viewAt(size.w, size.h)
				setup(m)

				out := m.View().Content
				assert.LessOrEqual(t, lg.Width(out), size.w)
				assert.LessOrEqual(t, lg.Height(out), size.h)
			})
		}
	}
}

func knockResult() *logic.HandResult {
	return &logic.HandResult{
		KnockerMelds: [][]deck.Card{
			{{Rank: deck.Four, Suit: deck.Spades}, {Rank: deck.Four, Suit: deck.Hearts}, {Rank: deck.Four, Suit: deck.Clubs}},
			{{Rank: deck.Five, Suit: deck.Hearts}, {Rank: deck.Six, Suit: deck.Hearts}, {Rank: deck.Seven, Suit: deck.Hearts}},
		},
		OpponentDeadwood: []deck.Card{{Rank: deck.King, Suit: deck.Clubs}, {Rank: deck.Nine, Suit: deck.Spades}},
		LaidOffCards:     []deck.Card{{Rank: deck.Eight, Suit: deck.Hearts}},
		ScoreDelta:       14,
		Winner:           "1",
	}
}

func ginResult() *logic.HandResult {
	r := knockResult()
	r.Gin = true
	r.LaidOffCards = nil
	return r
}

// The summary is how a player learns why they scored what they did, so each of the
// three outcomes has to name itself and the melds have to be labelled set or run.
func TestRenderHandResult_NamesEveryOutcome(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		result *logic.HandResult
		want   string
	}{
		{name: "a knock", result: knockResult(), want: "KNOCK"},
		{name: "a gin", result: ginResult(), want: "GIN!"},
		{name: "an undercut", result: func() *logic.HandResult {
			r := knockResult()
			r.Undercut = true
			return r
		}(), want: "UNDERCUT"},
		{name: "a wall", result: &logic.HandResult{Wall: true}, want: "WALL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := viewAt(120, 50)
			m.lastHandResult = tc.result
			assert.Contains(t, m.renderHandResult(), tc.want)
		})
	}

	t.Run("no result yet draws nothing", func(t *testing.T) {
		t.Parallel()
		m := viewAt(120, 50)
		assert.Empty(t, m.renderHandResult())
	})
}

// A set is three of a rank and a run is three in a suit; mislabelling them would
// teach a player the wrong rule for what they may lay off.
func TestRenderMeldGroups_LabelsSetsAndRuns(t *testing.T) {
	t.Parallel()
	m := viewAt(120, 50)

	out := m.renderMeldGroups("knocker melds", knockResult().KnockerMelds, false)
	assert.Contains(t, out, "SET")
	assert.Contains(t, out, "RUN")

	assert.Contains(t, m.renderMeldGroups("knocker melds", nil, false), "knocker melds: -",
		"an empty group says so rather than leaving a gap")
}

func TestRenderCardRow_SaysWhenThereIsNothingToShow(t *testing.T) {
	t.Parallel()
	m := viewAt(120, 50)

	assert.Contains(t, m.renderCardRow("laid off", nil, true), "laid off: -")
	assert.NotEmpty(t, m.renderCardRow("laid off", []deck.Card{{Rank: deck.Ace, Suit: deck.Spades}}, true))
}

// The key line is the only prompt a player gets, so it has to follow the phase: the
// discard keys mean nothing before a card has been drawn.
func TestKeyHints_FollowTheHandPhase(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		phase logic.Phase
		want  string
	}{
		{name: "awaiting a draw", phase: logic.AwaitingDraw, want: "draw stock"},
		{name: "awaiting a discard", phase: logic.AwaitingDiscard, want: "knock"},
		{name: "the hand is over", phase: logic.HandOver, want: "esc: leave"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := viewAt(100, 40)
			m.handPhase = tc.phase
			assert.Contains(t, m.keyHints(), tc.want)
		})
	}
}

// The running total is what a player checks against the match target, so both seats
// have to appear with the score the engine credited them.
func TestRenderScoreLine_NamesBothSeatsAndTheirTotals(t *testing.T) {
	t.Parallel()
	m := viewAt(100, 40)

	line := m.renderScoreLine()
	assert.Contains(t, line, "alice")
	assert.Contains(t, line, "40")
	assert.Contains(t, line, "bob")
	assert.Contains(t, line, "22")
	assert.Contains(t, line, "hand 3")
}

func TestView_ShowsTheLastRejectedActionAndStillFits(t *testing.T) {
	t.Parallel()

	m := viewAt(styles.MinWidth, styles.MinHeight)
	m.ActionErr = errors.New("you have to draw before you discard")

	out := m.View().Content
	assert.Contains(t, out, "draw before")
	assert.LessOrEqual(t, lg.Height(out), styles.MinHeight)
}

func TestView_WaitingScreen(t *testing.T) {
	t.Parallel()

	m := viewAt(80, 24)
	m.Base.Phase = game.Waiting
	assert.Contains(t, m.View().Content, "Waiting for game to start")
}
