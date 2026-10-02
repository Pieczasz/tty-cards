package hearts

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/hearts"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"
	"github.com/Pieczasz/terminal-card/internal/tui/views/gameview"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestView_HandOverShowsScores(t *testing.T) {
	t.Parallel()
	m := &model{
		Global: router.GlobalContext{
			Theme:  styles.NewTheme(true),
			Width:  80,
			Height: 24,
		},
		Base: gameview.BaseState{Phase: game.Playing, Seats: []game.PlayerSnapshot{
			{ID: "1", Name: "alice"}, {ID: "2", Name: "bob"},
			{ID: "3", Name: "carol"}, {ID: "4", Name: "dave"},
		}},
		phase:            logic.PhaseHandOver,
		handComplete:     true,
		handNumber:       2,
		handPoints:       map[string]int{"1": 5, "2": 8, "3": 3, "4": 10},
		cumulativeScores: map[string]int{"1": 25, "2": 8, "3": 18, "4": 17},
	}

	out := m.View().Content
	require.NotEmpty(t, out)
	assert.Contains(t, out, "HAND 2 COMPLETE")
	assert.Contains(t, out, "alice")
	assert.Contains(t, out, "25")
}

func TestView_HeartsBrokenIndicator(t *testing.T) {
	t.Parallel()
	m := &model{
		Global:       router.GlobalContext{Theme: styles.NewTheme(true)},
		heartsBroken: true,
		trickCards:   map[string]deck.Card{},
	}
	assert.Contains(t, m.renderHeartsBrokenIndicator(), "broken")
	m.heartsBroken = false
	assert.Contains(t, m.renderHeartsBrokenIndicator(), "not yet broken")
}

// Hearts deals four, but a seat stays empty for as long as it takes the engine to end
// the match after somebody leaves, and that is a frame the view still has to render.
// With three seats the art layout's third edge wrapped back onto the hero, so the view
// drew the player their own hand count as an opponent.
func TestOpponentAt_RefusesATableThatIsNotFourHanded(t *testing.T) {
	t.Parallel()

	m := &model{
		Global: router.GlobalContext{Theme: styles.NewTheme(true), Width: 100, Height: 30},
		Bound:  game.Bind(&game.Engine{}, "1"),
		Base: gameview.BaseState{
			Phase: game.Playing,
			Seats: []game.PlayerSnapshot{
				{ID: "1", Name: "alice", HandSize: 13},
				{ID: "2", Name: "bob", HandSize: 13},
				{ID: "3", Name: "carol", HandSize: 13},
			},
			Opponents: []game.PlayerSnapshot{
				{ID: "2", Name: "bob", HandSize: 13},
				{ID: "3", Name: "carol", HandSize: 13},
			},
		},
		trickCards: map[string]deck.Card{},
	}

	for rel := range 3 {
		_, ok := m.opponentAt(rel)
		assert.Falsef(t, ok, "rel %d must not resolve on a three-handed table", rel)
	}

	// Degraded, not blank: a player still holding cards has to be on screen.
	out := m.View().Content
	assert.Contains(t, out, "bob")
	assert.Contains(t, out, "carol")
	assert.NotContains(t, out, "alice", "the hero is never drawn as an opponent")
}

func TestOpponentAt_MapsTheThreeEdgesAtAFullTable(t *testing.T) {
	t.Parallel()

	// Opponents as the base state lays it out for seat 3: from the hero's left.
	m := &model{
		Bound: game.Bind(&game.Engine{}, "3"),
		Base: gameview.BaseState{
			Seats:     []game.PlayerSnapshot{{ID: "1"}, {ID: "2"}, {ID: "3"}, {ID: "4"}},
			Opponents: []game.PlayerSnapshot{{ID: "4"}, {ID: "1"}, {ID: "2"}},
		},
	}

	seen := make([]string, 0, 3)
	for rel := range 3 {
		o, ok := m.opponentAt(rel)
		require.Truef(t, ok, "rel %d", rel)
		seen = append(seen, o.ID)
	}
	assert.Equal(t, []string{"4", "1", "2"}, seen, "clockwise from the hero, hero excluded")
}

// The four cards of a trick are the one thing a player cannot play Hearts without
// seeing. The trick drawn with faces is three rows of card art - taller than the
// middle band on any normal terminal - so it used to be cut off at the band's edge,
// which silently hid whichever card was played from the far seat.
func TestView_TheWholeTrickIsVisibleAtEverySize(t *testing.T) {
	t.Parallel()

	seats := []game.PlayerSnapshot{
		{ID: "1", Name: "alice", HandSize: 10},
		{ID: "2", Name: "bob", HandSize: 10},
		{ID: "3", Name: "carol", HandSize: 10},
		{ID: "4", Name: "dave", HandSize: 10},
	}
	trick := map[string]deck.Card{
		"1": {Rank: deck.Ace, Suit: deck.Spades},
		"2": {Rank: deck.King, Suit: deck.Hearts},
		"3": {Rank: deck.Queen, Suit: deck.Diamonds},
		"4": {Rank: deck.Jack, Suit: deck.Clubs},
	}

	for _, size := range []struct{ Width, Height int }{{64, 20}, {80, 24}, {100, 30}, {120, 40}} {
		t.Run(fmt.Sprintf("%dx%d", size.Width, size.Height), func(t *testing.T) {
			t.Parallel()

			m := &model{
				Global: router.GlobalContext{Theme: styles.NewTheme(true), Width: size.Width, Height: size.Height},
				Bound:  game.Bind(&game.Engine{}, "1"),
				Base: gameview.BaseState{
					Phase: game.Playing, Seats: seats, Opponents: seats[1:],
					Hand: []deck.Card{{Rank: deck.Two, Suit: deck.Clubs}},
				},
				trickCards: trick,
			}

			out := tuitest.StripANSI(m.View().Content)
			labels := map[deck.Rank]string{deck.Ace: "A", deck.King: "K", deck.Queen: "Q", deck.Jack: "J"}
			for id, card := range trick {
				assert.Containsf(t, out, labels[card.Rank],
					"the card played from seat %s is not on screen", id)
			}
		})
	}
}

func fourHanded(width, height int) *model {
	seats := []game.PlayerSnapshot{
		{ID: "1", Name: "alice", HandSize: 13},
		{ID: "2", Name: "bob", HandSize: 13},
		{ID: "3", Name: "carol", HandSize: 13},
		{ID: "4", Name: "dave", HandSize: 13},
	}
	hand := make([]deck.Card, 0, 13)
	for i := range 13 {
		hand = append(hand, deck.Card{Rank: deck.Rank(i + 1), Suit: deck.Hearts})
	}
	return &model{
		Global: router.GlobalContext{Theme: styles.NewTheme(true), Width: width, Height: height},
		Bound:  game.Bind(&game.Engine{}, "1"),
		Base: gameview.BaseState{
			Phase:             game.Playing,
			MyTurn:            true,
			Hand:              hand,
			Seats:             seats,
			Opponents:         seats[1:],
			CurrentPlayerName: "alice",
			CurrentPlayerID:   "1",
			TurnRemaining:     11 * time.Second,
		},
		phase:            logic.PhaseTrickPlay,
		trickCards:       map[string]deck.Card{"2": {Rank: deck.Queen, Suit: deck.Spades}},
		handPoints:       map[string]int{"1": 0, "2": 13, "3": 0, "4": 0},
		cumulativeScores: map[string]int{"1": 12, "2": 40, "3": 7, "4": 0},
		handNumber:       4,
		passDirection:    logic.PassLeft,
		passSelected:     map[deck.Card]struct{}{},
	}
}

// Every screen this view can be in has to stay inside the terminal, including the
// pass phase (whose hand is drawn by the multi-select renderer) and the summaries.
func TestView_EveryScreenFitsTheTerminal(t *testing.T) {
	t.Parallel()

	screens := map[string]func(*model){
		"trick play": func(*model) {},
		"the pass phase": func(m *model) {
			m.phase = logic.PhasePassing
			m.passSelected = map[deck.Card]struct{}{m.Base.Hand[0]: {}, m.Base.Hand[3]: {}}
		},
		"the hand summary": func(m *model) { m.phase = logic.PhaseHandOver; m.handComplete = true },
		"the match over": func(m *model) {
			m.matchComplete = true
			m.Base.Phase = game.Finished
			m.Base.WinnerName = "carol"
		},
		"a seat lost mid-hand": func(m *model) {
			m.Base.Seats = m.Base.Seats[:3]
			m.Base.Opponents = m.Base.Seats[1:]
		},
	}

	for _, size := range tuitest.FitSizes {
		for name, setup := range screens {
			sub := fmt.Sprintf("%dx%d_%s", size.Width, size.Height, strings.ReplaceAll(name, " ", "_"))
			t.Run(sub, func(t *testing.T) {
				t.Parallel()
				m := fourHanded(size.Width, size.Height)
				setup(m)

				out := m.View().Content
				assert.LessOrEqual(t, lg.Width(out), size.Width)
				assert.LessOrEqual(t, lg.Height(out), size.Height)
			})
		}
	}
}

// The key line is the only prompt: showing the play keys during the pass would tell a
// player to press enter with one card selected, which the pass refuses.
func TestKeyHints_FollowTheStage(t *testing.T) {
	t.Parallel()

	m := fourHanded(100, 40)
	assert.Equal(t, keyHintsPlay, m.keyHints())

	m.phase = logic.PhasePassing
	assert.Equal(t, keyHintsPass, m.keyHints())
}

// Which way the three cards travel changes what a player keeps, so the direction has
// to be on screen during the pass and gone once it is over.
func TestRenderPassDirection_ShowsOnlyDuringThePass(t *testing.T) {
	t.Parallel()

	m := fourHanded(100, 40)
	assert.Empty(t, m.renderPassDirection(), "trick play has no pass direction")

	m.phase = logic.PhasePassing
	assert.Contains(t, m.renderPassDirection(), "Pass: ")
}

// An empty slot has to keep the cross the same size as a played one, or the trick
// jumps around the table as each card lands.
func TestRenderTrickSlot_DrawsAPlaceholderForASeatYetToPlay(t *testing.T) {
	t.Parallel()
	m := fourHanded(100, 40)

	for _, mini := range []bool{false, true} {
		t.Run(fmt.Sprintf("mini=%v", mini), func(t *testing.T) {
			t.Parallel()
			assert.NotEmpty(t, m.renderTrickSlot("2", mini), "bob has played")
			assert.NotEmpty(t, m.renderTrickSlot("3", mini), "carol has not, but keeps her slot")
			assert.NotEmpty(t, m.renderTrickSlot("", mini), "and so does a seat that is not there")
		})
	}
}

// The summary is the running score, so it has to name every seat and both its numbers.
func TestRenderHandOver_ShowsEverySeatsHandAndTotal(t *testing.T) {
	t.Parallel()

	m := fourHanded(120, 50)
	m.phase = logic.PhaseHandOver
	m.handComplete = true

	out := m.View().Content
	for _, name := range []string{"alice", "bob", "carol", "dave"} {
		assert.Contains(t, out, name)
	}
	assert.Contains(t, out, "HAND 4 COMPLETE")

	m.matchComplete = true
	m.Base.WinnerName = "bob"
	assert.Contains(t, m.View().Content, "MATCH COMPLETE - bob wins")
}

func TestView_ShowsTheLastRejectedActionAndStillFits(t *testing.T) {
	t.Parallel()

	m := fourHanded(styles.MinWidth, styles.MinHeight)
	m.ActionErr = errNeedThreeCards

	out := m.View().Content
	assert.Contains(t, out, "exactly 3 cards")
	assert.LessOrEqual(t, lg.Height(out), styles.MinHeight)
}

func TestView_WaitingScreen(t *testing.T) {
	t.Parallel()

	m := fourHanded(80, 24)
	m.Base.Phase = game.Waiting
	assert.Contains(t, m.View().Content, "Waiting for game to start")
}
