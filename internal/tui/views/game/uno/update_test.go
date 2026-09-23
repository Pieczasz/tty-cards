package uno

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/uno"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	"uuid"

	tea "charm.land/bubbletea/v2"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdate_Navigation(t *testing.T) {
	t.Parallel()
	m := Model{
		Base: gameview.BaseState{
			Hand: []deck.Card{
				{Rank: deck.Two, Suit: logic.ColorRed},
				{Rank: deck.Three, Suit: logic.ColorBlue},
				{Rank: deck.Four, Suit: logic.ColorGreen},
			},
		}}

	msg := tea.KeyPressMsg{Code: rune("l"[0]), Text: "l"}
	newM, _ := m.Update(msg)
	assert.Equal(t, 1, newM.(*Model).Selected)

	msg = tea.KeyPressMsg{Code: rune("h"[0]), Text: "h"}
	newM, _ = newM.Update(msg)
	assert.Equal(t, 0, newM.(*Model).Selected)
}

func TestUpdate_ColorPicking(t *testing.T) {
	t.Parallel()
	m := Model{pickingColor: true, colorCursor: 0}

	msg := tea.KeyPressMsg{Code: rune("l"[0]), Text: "l"}
	newM, _ := m.Update(msg)
	assert.Equal(t, 1, newM.(*Model).colorCursor)

	msg = tea.KeyPressMsg{Code: rune("j"[0]), Text: "j"}
	newM, _ = newM.Update(msg)
	assert.Equal(t, 3, newM.(*Model).colorCursor)
}

// tableOnTurn seats the view as whichever player the engine put on turn, so the
// test does not depend on where the deal landed.
func tableOnTurn(t *testing.T) (*game.Engine, *Model) {
	t.Helper()
	players := []*game.Player{
		{ID: testutil.SeatID(1), UserID: testutil.UID(1), Name: "alice"},
		{ID: testutil.SeatID(2), UserID: testutil.UID(2), Name: "bob"},
		{ID: testutil.SeatID(3), UserID: testutil.UID(3), Name: "carol"},
	}
	engine := game.NewEngine(&logic.Rules{}, players, (&logic.Rules{}).InitialDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	id, err := uuid.Parse(engine.CurrentPlayerID())
	require.NoError(t, err)

	// A real manager, because leaving the table goes through it: the view is
	// constructed exactly as app.go builds it.
	global := router.GlobalContext{
		User:         &db.User{ID: id, Username: "hero"},
		LobbyManager: lobby.NewManager(context.Background(), nil),
	}
	m, ok := New(global, engine).(*Model)
	require.True(t, ok)
	require.True(t, m.Base.MyTurn, "the view has to be bound to the seat on turn")
	return engine, m
}

// The colour picker is a modal over the hero's own turn. It used to survive the
// turn being taken by the clock, leaving a picker on screen with nothing left to
// confirm and the table hidden behind it.
func TestSyncState_ClosesTheColourPickerWhenTheTurnIsLost(t *testing.T) {
	t.Parallel()
	engine, m := tableOnTurn(t)

	m.pickingColor = true
	m.colorCursor = 2
	require.NoError(t, engine.SubmitAction(m.Bound.PlayerID(), logic.ActionDrawCard{}))
	m.syncState()

	require.False(t, m.Base.MyTurn, "drawing passes the turn on")
	assert.False(t, m.pickingColor, "the picker cannot outlive the turn it belongs to")
}

func TestSyncState_KeepsTheColourPickerWhileTheTurnIsStillYours(t *testing.T) {
	t.Parallel()
	_, m := tableOnTurn(t)

	m.pickingColor = true
	m.syncState()

	assert.True(t, m.pickingColor, "a refresh mid-turn must not close the picker")
}

func TestClose_ReleasesEngineSubscription(t *testing.T) {
	t.Parallel()

	players := []*game.Player{
		{ID: testutil.SeatID(1), UserID: testutil.UID(1), Name: "alice"},
		{ID: testutil.SeatID(2), UserID: testutil.UID(2), Name: "bob"},
	}
	engine := game.NewEngine(&logic.Rules{}, players, (&logic.Rules{}).InitialDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	global := router.GlobalContext{User: &db.User{ID: testutil.UID(1), Username: "alice"}}
	m, ok := New(global, engine).(*Model)
	require.True(t, ok)
	require.Equal(t, 1, engine.Broadcaster().Len())

	listen := m.Listen()
	done := make(chan tea.Msg, 1)
	go func() { done <- listen() }()

	m.Close()
	assert.Zero(t, engine.Broadcaster().Len())

	select {
	case msg := <-done:
		assert.Nil(t, msg)
	case <-time.After(2 * time.Second):
		t.Fatal("listener goroutine did not return after Close")
	}

	m.Close()
}

// The colour glyphs sit at card-slot spacing, so they only make sense over a fan.
// A default 80x24 terminal is too short for one, and the hand falls back to the
// compact strip - a row drawn there lines up with nothing, which loses the one thing
// colour conveys in Uno.
func TestRenderHandColorRow_FollowsTheHandItSitsOver(t *testing.T) {
	t.Parallel()

	hand := make([]deck.Card, 7)
	for i := range hand {
		hand[i] = deck.Card{Rank: logic.Zero + deck.Rank(i), Suit: logic.ColorRed}
	}

	tests := []struct {
		name          string
		width, height int
		wantRow       bool
	}{
		{name: "a terminal tall enough to fan", width: 100, height: 40, wantRow: true},
		{name: "the default 80x24, which strips the hand", width: 80, height: 24, wantRow: false},
		{name: "the shortest admitted height", width: 80, height: 20, wantRow: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &Model{
				Global: router.GlobalContext{Width: tt.width, Height: tt.height},
				Base:   gameview.BaseState{Hand: hand},
			}

			handWidth := gameview.HandWidth(tt.width)
			handRows := gameview.HandRows(tt.height)
			row := m.renderHandColorRow(handWidth, handRows)

			fanned := gameview.FansHand(len(hand), handWidth, handRows)
			require.Equal(t, tt.wantRow, fanned, "test expectation must match the renderer's own choice")
			assert.Equal(t, tt.wantRow, row != "", "the colour row appears exactly when the hand fans")
		})
	}
}

func TestInit_ArmsBothTheFeedAndTheClock(t *testing.T) {
	t.Parallel()
	_, m := tableOnTurn(t)

	// Batched, so the one command carries the event listener and the countdown: a
	// view that armed only one of them either stops updating or freezes its clock.
	assert.NotNil(t, m.Init())
}

// Esc is overloaded: it cancels the picker while one is open and asks to leave the
// table otherwise. Collapsing the two would forfeit a seat on a mistyped cancel.
func TestHandleEscape_CancelsThePickerBeforeLeavingTheTable(t *testing.T) {
	t.Parallel()
	_, m := tableOnTurn(t)
	m.pickingColor = true

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Nil(t, cmd, "the first esc only closes the picker")
	assert.False(t, m.pickingColor)

	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Nil(t, cmd, "the second esc asks before forfeiting")
	assert.Contains(t, m.View().Content, "forfeit")

	_, cmd = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	assert.NotNil(t, cmd, "y leaves the table")
}

// A wild is the one card that needs a second decision, so enter opens the picker
// rather than playing it into whatever colour the engine would guess.
func TestHandleEnter_AWildOpensThePickerAndTheNextEnterCommits(t *testing.T) {
	t.Parallel()

	for _, rank := range []deck.Rank{logic.Wild, logic.WildDrawFour} {
		t.Run(strconv.Itoa(int(rank)), func(t *testing.T) {
			t.Parallel()
			// No engine: the submit is refused for want of a seat, which is the
			// rejection path without depending on where a real deal landed the wilds.
			m := &Model{}
			m.Base.MyTurn = true
			m.Base.Hand = []deck.Card{{Rank: rank}}

			_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			require.True(t, m.pickingColor, "a wild asks which colour it becomes")
			require.Equal(t, 0, m.colorCursor, "the picker opens on the first colour")

			m.colorCursor = 3
			_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			assert.False(t, m.pickingColor, "committing closes the picker whatever the engine says")
			// A rejected move has to surface a message rather than fail silently.
			assert.Error(t, m.lastActionErr)
		})
	}
}

func TestHandleEnter_AnOrdinaryCardIsPlayedStraightAway(t *testing.T) {
	t.Parallel()
	_, m := tableOnTurn(t)
	m.Base.Hand = []deck.Card{{Rank: logic.Zero, Suit: logic.ColorRed}}
	m.Selected = 0

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.False(t, m.pickingColor, "only a wild opens the picker")
}

func TestHandleDraw_OnlyActsOnYourOwnTurn(t *testing.T) {
	t.Parallel()
	engine, m := tableOnTurn(t)

	before := engine.Snapshot().DeckSize
	_, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	require.NoError(t, m.lastActionErr)
	require.Less(t, engine.Snapshot().DeckSize, before, "drawing takes a card off the stock")

	m.syncState()
	require.False(t, m.Base.MyTurn, "drawing passes the turn on")

	after := engine.Snapshot().DeckSize
	_, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	assert.Equal(t, after, engine.Snapshot().DeckSize, "d off-turn must not reach the engine")
}

// Number keys are a shortcut into the hand, so they must not move behind the picker -
// the digit would silently retarget the card being played.
func TestSelectDigit_IsIgnoredWhileThePickerIsOpen(t *testing.T) {
	t.Parallel()
	m := &Model{}
	m.Base.Hand = make([]deck.Card, 5)

	_, _ = m.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	require.Equal(t, 3, m.Selected)

	m.pickingColor = true
	_, _ = m.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	assert.Equal(t, 3, m.Selected, "the hand cursor is frozen behind the picker")
}

// GridStep clamps the picker's cursor, but the submit path checks again: a cursor out
// of range must be dropped rather than index past the colour table.
func TestSubmitColorPick_IgnoresACursorOutOfRange(t *testing.T) {
	t.Parallel()
	_, m := tableOnTurn(t)
	m.pickingColor = true
	m.colorCursor = len(colorChoices)

	_, _ = m.submitColorPick(deck.Card{Rank: logic.Wild})
	assert.True(t, m.pickingColor, "nothing was committed, so the picker stays open")
}

// Once the game is over enter is the way out, not another move.
func TestHandleEnter_LeavesAFinishedGame(t *testing.T) {
	t.Parallel()
	m := &Model{}
	m.Base.Phase = game.Finished

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.NotNil(t, cmd)
}
