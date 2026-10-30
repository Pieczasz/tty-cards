package ginrummy

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/ginrummy"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"
	"github.com/Pieczasz/tty-cards/internal/tui/views/gameview"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestView_ActionBarHints(t *testing.T) {
	t.Parallel()
	m := &model{
		Global: router.GlobalContext{
			Theme:  styles.NewTheme(true),
			Width:  80,
			Height: 40,
		},
		Base: gameview.BaseState{
			Phase:  game.Playing,
			MyTurn: true,
			Hand:   []deck.Card{{Rank: deck.Ace, Suit: deck.Spades}},
			Seats:  aliceAndBob,
		},
		phase:      logic.PhaseAwaitingDraw,
		handNumber: 1,
	}

	out := m.View().Content
	require.NotEmpty(t, out)
	assert.Contains(t, out, "draw stock")

	m.phase = logic.PhaseAwaitingDiscard
	out = m.View().Content
	assert.Contains(t, out, "knock")
}

func TestView_HandOverWallBanner(t *testing.T) {
	t.Parallel()
	m := &model{
		Global: router.GlobalContext{
			Theme:  styles.NewTheme(true),
			Width:  80,
			Height: 24,
		},
		Base:             gameview.BaseState{Phase: game.Playing, Seats: aliceAndBob},
		phase:            logic.PhaseHandOver,
		handNumber:       2,
		lastHandResult:   &logic.HandResult{Outcome: logic.OutcomeWall},
		cumulativeScores: map[string]int{"1": 10, "2": 5},
	}

	out := m.View().Content
	require.NotEmpty(t, out)
	assert.Contains(t, out, "HAND 2 COMPLETE")
	assert.Contains(t, out, "WALL")
}

// A wall names its cause: a hand traded on the upcard for a hundred turns walls with
// the stock still full, and "stock exhausted" there is simply false.
func TestView_HandOverWallNamesItsCause(t *testing.T) {
	t.Parallel()

	for _, cause := range []string{"stock exhausted", "turn limit"} {
		t.Run(cause, func(t *testing.T) {
			t.Parallel()
			m := viewAt(80, 24)
			m.phase = logic.PhaseHandOver
			m.lastHandResult = &logic.HandResult{Outcome: logic.OutcomeWall, WallCause: cause}

			out := tuitest.StripANSI(m.View().Content)
			assert.Contains(t, out, "WALL - "+cause+", no score")
		})
	}
}

func TestView_InterruptedMatchDoesNotNameAWinner(t *testing.T) {
	t.Parallel()
	m := &model{
		Global: router.GlobalContext{Theme: styles.NewTheme(true), Width: 80, Height: 24},
		Base: gameview.BaseState{
			Phase: game.Finished, EndReason: game.EndReasonInterrupted, WinnerName: "alice",
			Seats: aliceAndBob,
		},
		phase:            logic.PhaseHandOver,
		handNumber:       2,
		matchComplete:    true,
		leavers:          []string{"bob"},
		cumulativeScores: map[string]int{"1": 0, "2": 0},
	}

	out := tuitest.StripANSI(m.View().Content)
	assert.Contains(t, out, "MATCH ENDED EARLY - bob left the table")
	assert.NotContains(t, out, "wins")
}

var aliceAndBob = []game.PlayerSnapshot{{ID: "1", Name: "alice"}, {ID: "2", Name: "bob"}}

func handOf(n int) []deck.Card {
	suits := []deck.Suit{deck.Spades, deck.Hearts, deck.Diamonds, deck.Clubs}
	hand := make([]deck.Card, 0, n)
	for i := range n {
		hand = append(hand, deck.Card{Rank: deck.Rank(i%13 + 1), Suit: suits[i%len(suits)]})
	}
	return hand
}

func viewAt(width, height int) *model {
	opponent := game.PlayerSnapshot{ID: "2", Name: "bob", HandSize: 10}
	return &model{
		Global: router.GlobalContext{Theme: styles.NewTheme(true), Width: width, Height: height},
		Base: gameview.BaseState{
			Phase:             game.Playing,
			MyTurn:            true,
			Hand:              handOf(11),
			TopDiscard:        deck.Card{Rank: deck.Seven, Suit: deck.Clubs},
			Seats:             []game.PlayerSnapshot{{ID: "1", Name: "alice", HandSize: 11}, opponent},
			Opponents:         []game.PlayerSnapshot{opponent},
			DeckSize:          20,
			CurrentPlayerName: "alice",
			CurrentPlayerID:   "1",
			TurnRemaining:     9 * time.Second,
		},
		phase:            logic.PhaseAwaitingDiscard,
		handNumber:       3,
		cumulativeScores: map[string]int{"1": 40, "2": 22},
	}
}

// Every screen this view can be in has to stay inside the terminal: the table, the
// hand-over summary with its meld boxes, and the end of the match.
func TestView_FitsTheTerminal(t *testing.T) {
	t.Parallel()

	screens := map[string]func(*model){
		"the table":        func(*model) {},
		"awaiting a draw":  func(m *model) { m.phase = logic.PhaseAwaitingDraw },
		"the hand summary": func(m *model) { m.phase = logic.PhaseHandOver; m.lastHandResult = knockResult() },
		"a gin":            func(m *model) { m.phase = logic.PhaseHandOver; m.lastHandResult = ginResult() },
		"the match over": func(m *model) {
			m.matchComplete = true
			m.Base.Phase = game.Finished
			m.Base.WinnerName = "alice"
			m.lastHandResult = knockResult()
		},
	}

	for _, size := range tuitest.FitSizes {
		for name, setup := range screens {
			t.Run(fmt.Sprintf("%dx%d_%s", size.Width, size.Height, strings.ReplaceAll(name, " ", "_")), func(t *testing.T) {
				t.Parallel()
				m := viewAt(size.Width, size.Height)
				setup(m)

				out := m.View().Content
				assert.LessOrEqual(t, lg.Width(out), size.Width)
				assert.LessOrEqual(t, lg.Height(out), size.Height)
			})
		}
	}
}

func knockResult() *logic.HandResult {
	return &logic.HandResult{
		Outcome: logic.OutcomeKnock,
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
	r.Outcome = logic.OutcomeGin
	r.LaidOffCards = nil
	return r
}

// The summary is how a player learns why they scored what they did, so each of the
// three outcomes has to name itself and the melds have to be labelled set or run.
func TestRenderOutcome_NamesEveryOutcome(t *testing.T) {
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
			r.Outcome = logic.OutcomeUndercut
			return r
		}(), want: "UNDERCUT"},
		{name: "a wall", result: &logic.HandResult{Outcome: logic.OutcomeWall}, want: "WALL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := viewAt(120, 50)
			m.lastHandResult = tc.result
			assert.Contains(t, m.renderOutcome(), tc.want)
		})
	}

	t.Run("no result yet draws nothing", func(t *testing.T) {
		t.Parallel()
		m := viewAt(120, 50)
		assert.Empty(t, m.renderOutcome())
	})
}

// A set is three of a rank and a run is three in a suit; mislabelling them would
// teach a player the wrong rule for what they may lay off.
func TestRenderMeldGroups_LabelsSetsAndRuns(t *testing.T) {
	t.Parallel()
	m := viewAt(120, 50)

	out := m.renderMeldGroups("knocker melds", knockResult().KnockerMelds)
	assert.Contains(t, out, "SET")
	assert.Contains(t, out, "RUN")

	assert.Contains(t, m.renderMeldGroups("knocker melds", nil), "knocker melds: -",
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
		{name: "awaiting a draw", phase: logic.PhaseAwaitingDraw, want: "draw stock"},
		{name: "awaiting a discard", phase: logic.PhaseAwaitingDiscard, want: "knock"},
		{name: "the hand is over", phase: logic.PhaseHandOver, want: "esc: leave"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := viewAt(100, 40)
			m.phase = tc.phase
			assert.Contains(t, m.keyHints(), tc.want)
		})
	}
}

// Drawing, discarding and knocking all wait for the turn, so the seat that is not on
// turn is offered only the cursor and leaving.
func TestView_OffTurnHintsOfferNoPlayKeys(t *testing.T) {
	t.Parallel()

	for _, phase := range []logic.Phase{logic.PhaseAwaitingDraw, logic.PhaseAwaitingDiscard} {
		m := viewAt(80, 40)
		m.phase = phase
		m.Base.MyTurn = false

		out := tuitest.StripANSI(m.View().Content)
		for _, key := range []string{"draw stock", "take discard", "enter: discard", "knock"} {
			assert.NotContains(t, out, key, "phase %v", phase)
		}
		assert.Contains(t, out, "->/l: right", "phase %v: the cursor still moves", phase)
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

func TestView_NextHandHintOnlyWhenOnTurn(t *testing.T) {
	t.Parallel()
	m := viewAt(80, 24)
	m.phase = logic.PhaseHandOver
	m.Base.MyTurn = false

	assert.NotContains(t, m.View().Content, "enter: deal next hand")
	assert.Contains(t, m.View().Content, "waiting for the next hand")

	m.Base.MyTurn = true
	assert.Contains(t, m.View().Content, "enter: deal next hand")
}

func TestView_ForfeitShowsTheScoreboard(t *testing.T) {
	t.Parallel()
	m := viewAt(80, 24)
	m.Base.Phase = game.Finished
	m.Base.WinnerName = "alice"
	m.phase = logic.PhaseAwaitingDiscard
	m.matchComplete = false

	out := m.View().Content
	assert.NotContains(t, out, "Press Esc to go back")
	assert.Contains(t, out, "alice")
	assert.Contains(t, out, "40")
}

// A rules error ends the match unrated and without a winner, so it must not read as
// a completed match.
func TestView_ARulesErrorSaysTheTableFailed(t *testing.T) {
	t.Parallel()
	m := viewAt(80, 24)
	m.Base.Phase = game.Finished
	m.Base.EndReason = game.EndReasonRulesError

	out := tuitest.StripANSI(m.View().Content)
	assert.Contains(t, out, "table error")
	assert.NotContains(t, out, "MATCH COMPLETE")
}

// The hand-over screen is how a player learns who won the hand and why, so the
// headline, the outcome and the hint have to survive on every terminal; only the
// card art may give way.
func TestView_HandOverKeepsTheHeadlineAtEverySize(t *testing.T) {
	t.Parallel()

	for _, size := range tuitest.FitSizes {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			t.Parallel()
			m := viewAt(size.Width, size.Height)
			m.phase = logic.PhaseHandOver
			m.lastHandResult = knockResult()

			out := tuitest.StripANSI(m.View().Content)
			assert.Contains(t, out, "HAND 3 COMPLETE")
			assert.Contains(t, out, "KNOCK")
			assert.Contains(t, out, "+14 -> alice")
			assert.Contains(t, out, "enter: deal next hand")
		})
	}
}

// The key line is wrapped to the terminal, so one longer than 64 columns breaks inside
// a binding at the minimum size and leaves "leave" alone under "esc:".
func TestView_KeyHintsFitOneLineAtTheMinimumSize(t *testing.T) {
	t.Parallel()

	for _, phase := range []logic.Phase{logic.PhaseAwaitingDraw, logic.PhaseAwaitingDiscard} {
		m := viewAt(styles.MinWidth, styles.MinHeight)
		m.phase = phase

		hints := m.keyHints()
		assert.LessOrEqual(t, lg.Width(hints), styles.MinWidth, "phase %v: %q", phase, hints)
		out := tuitest.StripANSI(m.View().Content)
		assert.Contains(t, out, hints, "phase %v: the key line is drawn whole on one row", phase)
	}
}
