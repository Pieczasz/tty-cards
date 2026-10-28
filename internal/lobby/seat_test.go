package lobby

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Seat is the only way the TUI reaches a running table, so the handle it returns has
// to act as the seat it was asked for and no other: a view holding it cannot play
// someone else's turn.
func TestLobby_SeatSubmitsOnlyAsItsOwnPlayer(t *testing.T) {
	t.Parallel()
	_, l, leader, guest := startedGame(t)
	engine := l.ActiveGame()

	onTurn, offTurn := leader, guest
	if engine.CurrentPlayerID() != leader.ID {
		onTurn, offTurn = guest, leader
	}

	off := l.Seat(offTurn.ID)
	require.NotNil(t, off)
	assert.Equal(t, offTurn.ID, off.PlayerID())
	require.Error(t, off.Submit(stubAction("pass")), "the off-turn seat cannot act for the seat on turn")
	assert.Equal(t, onTurn.ID, engine.CurrentPlayerID(), "a refused move changes nothing")

	require.NoError(t, l.Seat(onTurn.ID).Submit(stubAction("pass")))
	assert.Equal(t, offTurn.ID, engine.CurrentPlayerID())
}

func TestLobby_SeatIsNilWithoutALiveSeat(t *testing.T) {
	t.Parallel()

	t.Run("a waiting table", func(t *testing.T) {
		t.Parallel()
		_, l := newTestLobby(t, 2)
		assert.Nil(t, l.Seat(l.Snapshot().Leader.Player.ID))
	})

	t.Run("a player not at the table", func(t *testing.T) {
		t.Parallel()
		_, l, _, _ := startedGame(t)
		assert.Nil(t, l.Seat("stranger"))
	})

	// The engine takes an idle seat before the lobby's roster catches up, so a
	// reconnecting player the engine removed must keep the roster, not be routed to a
	// table with no seat for them.
	t.Run("a seat the engine removed", func(t *testing.T) {
		t.Parallel()
		m, l := newTestLobby(t, 3)
		guests := []*game.Player{mockPlayer("p2", testutil.UID(2)), mockPlayer("p3", testutil.UID(3))}
		for _, g := range guests {
			require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), g)))
		}
		require.NoError(t, m.ToggleReady(l.Snapshot().Leader.Player))
		for _, g := range guests {
			require.NoError(t, m.ToggleReady(g))
		}
		l.ActiveGame().RemovePlayer(guests[0].ID)

		assert.Nil(t, l.Seat(guests[0].ID))
		assert.NotNil(t, l.Seat(guests[1].ID), "the seats still at the table are unaffected")
	})

	// The lobby reopens a finished table on its own goroutine, so for a moment the
	// engine is still the active one. Routing there bounced the player onto a game-over
	// screen whose esc brought them straight back to the lobby.
	t.Run("a finished table", func(t *testing.T) {
		t.Parallel()
		_, l, leader, _ := startedGame(t)
		engine := l.ActiveGame()
		require.NoError(t, endHand(engine, stubWin))
		require.True(t, engine.IsFinished())

		assert.Nil(t, l.Seat(leader.ID))
	})
}

// The start event is delivered to every subscriber at once, so it cannot carry a
// handle bound to one of them; it binds on the reader's behalf instead.
func TestEvent_SeatBindsTheGameThatStarted(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 2)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	ch, err := l.Subscribe(guest.ID)
	require.NoError(t, err)

	require.NoError(t, m.ToggleReady(l.Snapshot().Leader.Player))
	require.NoError(t, m.ToggleReady(guest))

	var started Event
	for ev := range ch {
		if ev.Type == EventGameStarted {
			started = ev
			break
		}
		assert.Nil(t, ev.Seat(guest.ID), "only the start event carries a table")
	}
	seat := started.Seat(guest.ID)
	require.NotNil(t, seat)
	assert.Equal(t, guest.ID, seat.PlayerID())
	snap, _, _ := seat.Frame(nil)
	assert.Equal(t, l.ActiveGame().Snapshot().Players, snap.Players, "the handle is on the table that started")
}
