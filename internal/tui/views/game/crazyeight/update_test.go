package crazyeight

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/crazyeight"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/tui/router"

	"github.com/Pieczasz/terminal-card/internal/deck"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	"uuid"

	tea "charm.land/bubbletea/v2"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
)

func TestUpdate_Navigation(t *testing.T) {
	t.Parallel()
	m := Model{
		Base: gameview.BaseState{
			Hand: []deck.Card{
				{Rank: deck.Two, Suit: deck.Spades},
				{Rank: deck.Three, Suit: deck.Hearts},
				{Rank: deck.Four, Suit: deck.Clubs},
			},
		}}

	// Right
	msg := tea.KeyPressMsg{Code: rune("l"[0]), Text: "l"}
	newM, _ := m.Update(msg)
	assert.Equal(t, 1, newM.(*Model).Selected)

	// Left
	msg = tea.KeyPressMsg{Code: rune("h"[0]), Text: "h"}
	newM, _ = newM.Update(msg)
	assert.Equal(t, 0, newM.(*Model).Selected)
}

func TestUpdate_SuitPicking(t *testing.T) {
	t.Parallel()
	m := Model{
		pickingSuit: true,
		suitCursor:  0,
	}

	// Right (adds 1 to cursor)
	msg := tea.KeyPressMsg{Code: rune("l"[0]), Text: "l"}
	newM, _ := m.Update(msg)
	assert.Equal(t, 1, newM.(*Model).suitCursor)

	// Down (adds 2 to cursor)
	msg = tea.KeyPressMsg{Code: rune("j"[0]), Text: "j"}
	newM, _ = newM.Update(msg)
	assert.Equal(t, 3, newM.(*Model).suitCursor)
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
	engine := game.NewEngine(&logic.Rules{}, players, deck.StandardDeck())
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

// The suit picker is a modal over the hero's own turn. It used to survive the turn
// being taken by the clock, leaving a picker on screen with nothing left to confirm
// and the table hidden behind it.
func TestSyncState_ClosesTheSuitPickerWhenTheTurnIsLost(t *testing.T) {
	t.Parallel()
	engine, m := tableOnTurn(t)

	m.pickingSuit = true
	m.suitCursor = 2
	require.NoError(t, engine.SubmitAction(m.Bound.PlayerID(), logic.ActionDrawCard{}))
	m.syncState()

	require.False(t, m.Base.MyTurn, "drawing passes the turn on")
	assert.False(t, m.pickingSuit, "the picker cannot outlive the turn it belongs to")
}

func TestSyncState_KeepsTheSuitPickerWhileTheTurnIsStillYours(t *testing.T) {
	t.Parallel()
	_, m := tableOnTurn(t)

	m.pickingSuit = true
	m.syncState()

	assert.True(t, m.pickingSuit, "a refresh mid-turn must not close the picker")
}

// Mirrors the poker view's teardown test. Without it, a Close regression here parks
// a listener goroutine and holds a broadcaster slot for every disconnected player,
// and nothing in this package would notice.
func TestClose_ReleasesEngineSubscription(t *testing.T) {
	t.Parallel()

	players := []*game.Player{
		{ID: testutil.SeatID(1), UserID: testutil.UID(1), Name: "alice"},
		{ID: testutil.SeatID(2), UserID: testutil.UID(2), Name: "bob"},
	}
	engine := game.NewEngine(&logic.Rules{}, players, deck.StandardDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	global := router.GlobalContext{User: &db.User{ID: testutil.UID(1), Username: "alice"}}
	m, ok := New(global, engine).(*Model)
	require.True(t, ok)
	require.Equal(t, 1, engine.Broadcaster().Len(), "the view subscribed on construction")

	// Park a listener exactly as the Bubble Tea runtime would. Init returns a batch
	// that also drives the animation tick, so take the event listener directly.
	// Built here because Close writes m.Events.
	listen := m.Listen()
	done := make(chan tea.Msg, 1)
	go func() { done <- listen() }()

	m.Close()
	assert.Zero(t, engine.Broadcaster().Len(), "Close returns the subscriber slot")

	select {
	case msg := <-done:
		assert.Nil(t, msg, "unsubscribing closes the channel so the listener returns")
	case <-time.After(2 * time.Second):
		t.Fatal("listener goroutine did not return after Close")
	}

	m.Close() // idempotent: session teardown may follow a view that already exited
	assert.Zero(t, engine.Broadcaster().Len())
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
	m.pickingSuit = true

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Nil(t, cmd, "the first esc only closes the picker")
	assert.False(t, m.pickingSuit)

	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Nil(t, cmd, "the second esc asks before forfeiting")
	assert.Contains(t, m.View().Content, "forfeit")

	_, cmd = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	assert.NotNil(t, cmd, "y leaves the table")
}

// An eight is the one card that needs a second decision, so enter opens the picker
// rather than playing it blind.
func TestHandleEnter_AnEightOpensThePickerAndTheNextEnterCommits(t *testing.T) {
	t.Parallel()
	// No engine: the submit is refused for want of a seat, which is the rejection path
	// without depending on where a real deal happened to land the eights.
	m := &Model{}
	m.Base.MyTurn = true
	m.Base.Hand = []deck.Card{{Rank: deck.Eight, Suit: deck.Spades}}

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.True(t, m.pickingSuit, "an eight asks which suit it becomes")
	require.Equal(t, 0, m.suitCursor, "the picker opens on the first suit")

	m.suitCursor = 2
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.False(t, m.pickingSuit, "committing closes the picker whatever the engine says")
	// A rejected move has to surface a message rather than fail silently.
	assert.Error(t, m.ActionErr)
}

func TestHandleEnter_AnOrdinaryCardIsPlayedStraightAway(t *testing.T) {
	t.Parallel()
	_, m := tableOnTurn(t)
	m.Base.Hand = []deck.Card{{Rank: deck.Three, Suit: deck.Spades}}
	m.Selected = 0

	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.False(t, m.pickingSuit, "only an eight opens the picker")
}

func TestHandleDraw_OnlyActsOnYourOwnTurn(t *testing.T) {
	t.Parallel()
	engine, m := tableOnTurn(t)

	before := engine.Snapshot().DeckSize
	_, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	require.NoError(t, m.ActionErr)
	require.Less(t, engine.Snapshot().DeckSize, before, "drawing takes a card off the stock")

	m.syncState()
	require.False(t, m.Base.MyTurn, "drawing passes the turn on")

	after := engine.Snapshot().DeckSize
	_, _ = m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	assert.Equal(t, after, engine.Snapshot().DeckSize, "d off-turn must not reach the engine")
}

// Number keys are a shortcut into the hand, so they must not move the picker's cursor
// while it is open - the digit would silently retarget the card being played.
func TestSelectDigit_IsIgnoredWhileThePickerIsOpen(t *testing.T) {
	t.Parallel()
	m := &Model{}
	m.Base.Hand = make([]deck.Card, 5)

	_, _ = m.Update(tea.KeyPressMsg{Code: '3', Text: "3"})
	require.Equal(t, 3, m.Selected)

	m.pickingSuit = true
	_, _ = m.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	assert.Equal(t, 3, m.Selected, "the hand cursor is frozen behind the picker")
}

// The picker's cursor is clamped by GridStep, but the submit path checks again: a
// cursor out of range must be dropped rather than index past the suit table.
func TestSubmitSuitPick_IgnoresACursorOutOfRange(t *testing.T) {
	t.Parallel()
	_, m := tableOnTurn(t)
	m.pickingSuit = true
	m.suitCursor = len(suitChoices)

	_, _ = m.submitSuitPick(deck.Card{Rank: deck.Eight, Suit: deck.Spades})
	assert.True(t, m.pickingSuit, "nothing was committed, so the picker stays open")
}

// Once the game is over enter is the way out, not another move.
func TestHandleEnter_LeavesAFinishedGame(t *testing.T) {
	t.Parallel()
	m := &Model{}
	m.Base.Phase = game.Finished

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.NotNil(t, cmd)
}
