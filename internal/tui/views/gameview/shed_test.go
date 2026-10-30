package gameview

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/tuitest"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func shedTableAt(width, height int) *ShedTable {
	hand := []deck.Card{
		{Rank: deck.Two, Suit: deck.Spades}, {Rank: deck.Three, Suit: deck.Hearts},
		{Rank: deck.Four, Suit: deck.Clubs}, {Rank: deck.Five, Suit: deck.Diamonds},
	}
	return &ShedTable{
		Global: router.GlobalContext{Theme: styles.NewTheme(true), Width: width, Height: height},
		Base:   BaseState{Phase: game.Playing, MyTurn: true, Hand: hand},
		Picker: ChoicePicker{Title: "Pick:", Choices: []Choice{
			{Label: "Spades", Suit: deck.Spades}, {Label: "Hearts", Suit: deck.Hearts},
			{Label: "Diamonds", Suit: deck.Diamonds}, {Label: "Clubs", Suit: deck.Clubs},
		}},
	}
}

// Selection is suppressed, not just unhighlighted: while the picker is open the hand
// cursor is frozen, and a highlight on a card the player cannot move reads as live.
func TestShedTable_ThePickerDropsTheHandHighlight(t *testing.T) {
	t.Parallel()

	tbl := shedTableAt(120, 50)
	tbl.Selected = 3
	tbl.Picker.Open = true
	picking := tbl.renderHero("")

	tbl.Picker.Open = false
	tbl.Selected = -1
	assert.Equal(t, tbl.renderHero(""), picking)
}

// Anything that is neither the frame's nor a key - a mouse event, a paste - is not a
// move, and must not reach the rules.
func TestShedTable_HandleIgnoresWhatIsNotAKey(t *testing.T) {
	t.Parallel()

	tbl := shedTableAt(80, 40)
	synced := false
	cmd := tbl.Handle(struct{}{}, func() { synced = true }, ShedMoves{})

	assert.Nil(t, cmd)
	assert.False(t, synced)
	assert.NoError(t, tbl.ActionErr)
}

func TestChoicePicker_LookupNamesOnlyItsOwnChoices(t *testing.T) {
	t.Parallel()

	p := shedTableAt(80, 40).Picker
	c, ok := p.Lookup(deck.Spades)
	assert.True(t, ok)
	assert.Equal(t, "Spades", c.Label)

	_, ok = p.Lookup(deck.NoSuit)
	assert.False(t, ok, "an unset suit names nothing")
}

type playedMove struct {
	card   deck.Card
	choice deck.Suit
}

// recordingMoves treats a Five as the wild, and records every play it is asked for. The
// table is unseated, so the engine never sees the move: what reached Play is the test.
func recordingMoves(played *[]playedMove) ShedMoves {
	return ShedMoves{
		Wild: func(c deck.Card) bool { return c.Rank == deck.Five },
		Play: func(c deck.Card, choice deck.Suit) game.Action {
			*played = append(*played, playedMove{c, choice})
			return nil
		},
	}
}

func pressKeys(tbl *ShedTable, moves ShedMoves, keys ...string) {
	for _, k := range keys {
		_ = tbl.Handle(tuitest.Key(k), func() {}, moves)
	}
}

func TestShedTable_EnterPlaysTheCardUnderTheCursor(t *testing.T) {
	t.Parallel()

	t.Run("an ordinary card goes straight to the pile", func(t *testing.T) {
		t.Parallel()
		tbl := shedTableAt(80, 40)
		var played []playedMove
		pressKeys(tbl, recordingMoves(&played), "l", "enter")

		assert.Equal(t, []playedMove{{tbl.Base.Hand[1], deck.NoSuit}}, played)
		assert.False(t, tbl.Picker.Open)
	})

	t.Run("a wild asks first, and the picker steers while it is open", func(t *testing.T) {
		t.Parallel()
		tbl := shedTableAt(80, 40)
		var played []playedMove
		moves := recordingMoves(&played)

		pressKeys(tbl, moves, "3", "enter")
		require.True(t, tbl.Picker.Open, "the wild opens the picker instead of playing")
		require.Empty(t, played)

		// right then down on the two-by-two grid is the fourth choice; the digit and the
		// arrows leave the hand cursor where it was.
		pressKeys(tbl, moves, "l", "j", "0", "enter")
		assert.Equal(t, 3, tbl.Selected, "the hand cursor is frozen behind the picker")
		assert.Equal(t, []playedMove{{tbl.Base.Hand[3], deck.Clubs}}, played)
		assert.False(t, tbl.Picker.Open)
	})

	t.Run("off turn nothing is played", func(t *testing.T) {
		t.Parallel()
		tbl := shedTableAt(80, 40)
		tbl.Base.MyTurn = false
		var played []playedMove
		pressKeys(tbl, recordingMoves(&played), "enter")

		assert.Empty(t, played)
	})
}

func TestShedTable_DrawOnlyOnYourTurnAndNotBehindThePicker(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		myTurn     bool
		picking    bool
		wantSubmit bool
	}{
		{name: "on turn", myTurn: true, wantSubmit: true},
		{name: "off turn", myTurn: false},
		{name: "with the picker open", myTurn: true, picking: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tbl := shedTableAt(80, 40)
			tbl.Base.MyTurn, tbl.Picker.Open = tc.myTurn, tc.picking
			pressKeys(tbl, ShedMoves{}, "d")

			// Unseated, a submitted draw comes back refused; one never sent leaves no error.
			assert.Equal(t, tc.wantSubmit, tbl.ActionErr != nil)
		})
	}
}

// Esc is overloaded: it cancels the picker while one is open and asks to leave the
// table otherwise. Collapsing the two would forfeit a seat on a mistyped cancel.
func TestShedTable_EscClosesThePickerBeforeItMeansLeaving(t *testing.T) {
	t.Parallel()
	tbl := shedTableAt(80, 40)
	tbl.Picker.Show()

	pressKeys(tbl, ShedMoves{}, "esc")
	assert.False(t, tbl.Picker.Open)
	_, confirming := tbl.LeaveConfirmScreen()
	assert.False(t, confirming, "the first esc only closes the picker")

	pressKeys(tbl, ShedMoves{}, "esc")
	_, confirming = tbl.LeaveConfirmScreen()
	assert.True(t, confirming, "the second asks before forfeiting")
}

// The picker belongs to the hero's turn: a turn lost to the clock takes it down.
func TestShedTable_SyncClosesThePickerOffTurn(t *testing.T) {
	t.Parallel()
	tbl := shedTableAt(80, 40)
	tbl.Picker.Show()

	// Unbound, a sync reads an empty table, which is nobody's turn.
	tbl.Sync(nil)
	assert.False(t, tbl.Picker.Open)
}

func TestShedTable_RenderTableShowsThePickerInPlaceOfThePile(t *testing.T) {
	t.Parallel()
	tbl := shedTableAt(100, 40)

	out := tbl.RenderTable("THE PILE", "").Content
	assert.Contains(t, out, "THE PILE")

	tbl.Picker.Show()
	out = tbl.RenderTable("THE PILE", "").Content
	assert.NotContains(t, out, "THE PILE")
	assert.Contains(t, out, "Pick:")

	tbl.Base.Phase = game.Finished
	assert.Contains(t, tbl.RenderTable("THE PILE", "").Content, "Game Over")
}

// enter and d do nothing off turn, so a waiting seat is not offered them: a hint for a
// key that does nothing reads as a broken key. Hearts and gin already worked this way.
func TestShedTable_OffTurnHintsOfferNoPlayKeys(t *testing.T) {
	t.Parallel()
	tbl := shedTableAt(120, 50)

	assert.Contains(t, tuitest.StripANSI(tbl.RenderTable("", "").Content), HandKeyHints)

	tbl.Base.MyTurn = false
	out := tuitest.StripANSI(tbl.RenderTable("", "").Content)
	assert.Contains(t, out, WaitingKeyHints)
	assert.NotContains(t, out, "d: draw")
	assert.NotContains(t, out, "enter:")
}

// The frame's messages are the frame's: a resize reaches the table without touching
// the hand or the picker.
func TestShedTable_HandleLeavesTheFrameToSession(t *testing.T) {
	t.Parallel()
	tbl := shedTableAt(80, 40)

	_ = tbl.Handle(tea.WindowSizeMsg{Width: 120, Height: 50}, func() {}, ShedMoves{})
	assert.Equal(t, 120, tbl.Global.Width)
	assert.Equal(t, 0, tbl.Selected)
}
