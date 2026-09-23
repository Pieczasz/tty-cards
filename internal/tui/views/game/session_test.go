package game

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/game/crazyeight"
	"github.com/Pieczasz/terminal-card/internal/tui/router"

	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A view that never reads its event channel shows a frozen table while the hand plays
// on without it.
func TestSession_Listen_DeliversWhatTheEngineBroadcasts(t *testing.T) {
	t.Parallel()

	events := make(chan game.Event, 1)
	events <- game.Event{Type: game.EventTurnAdvanced}
	s := Session{Events: events}

	msg := s.Listen()()

	require.IsType(t, EventMsg{}, msg)
	assert.Equal(t, game.EventTurnAdvanced, game.Event(msg.(EventMsg)).Type)
}

func TestSession_Listen_EndsQuietlyWithoutAFeed(t *testing.T) {
	t.Parallel()

	var none Session
	assert.Nil(t, none.Listen()(), "no feed is not an event")

	closed := make(chan game.Event)
	close(closed)
	ended := Session{Events: closed}
	assert.Nil(t, ended.Listen()(), "a closed feed ends the listener")
}

// Listen captures the channel when the command is built, so unsubscribing between
// build and run cannot turn the read into a nil-channel block.
func TestSession_Listen_UnsubscribeBeforeRunDoesNotBlock(t *testing.T) {
	t.Parallel()

	events := make(chan game.Event, 1)
	events <- game.Event{Type: game.EventTurnAdvanced}
	s := Session{Events: events}

	cmd := s.Listen()
	s.Events = nil
	assert.NotNil(t, cmd())
}

func TestSession_IdleRemoved_OnlyForThisPlayersSeat(t *testing.T) {
	t.Parallel()

	var unbound Session
	assert.False(t, unbound.IdleRemoved(game.Event{Type: game.EventPlayerIdle, PlayerID: "1"}),
		"an unbound session has no seat to lose")
}

func TestSession_Cursor(t *testing.T) {
	t.Parallel()
	hand := []deck.Card{
		{Rank: deck.Two, Suit: deck.Clubs},
		{Rank: deck.Three, Suit: deck.Clubs},
		{Rank: deck.Four, Suit: deck.Clubs},
	}

	t.Run("moves and stops at both ends", func(t *testing.T) {
		t.Parallel()
		s := Session{Base: BaseState{Hand: hand}}

		s.MoveCursor(-1)
		assert.Equal(t, 0, s.Selected, "left of the first card stays put")

		s.MoveCursor(1)
		s.MoveCursor(1)
		s.MoveCursor(1)
		assert.Equal(t, 2, s.Selected, "right of the last card stays put")
	})

	t.Run("an empty hand keeps the cursor at zero", func(t *testing.T) {
		t.Parallel()
		var s Session
		s.MoveCursor(1)
		assert.Equal(t, 0, s.Selected)

		_, ok := s.SelectedCard()
		assert.False(t, ok)
	})

	t.Run("digits select within the hand and are ignored past it", func(t *testing.T) {
		t.Parallel()
		s := Session{Base: BaseState{Hand: hand}}

		s.SelectDigit("2")
		assert.Equal(t, 2, s.Selected)

		s.SelectDigit("7")
		assert.Equal(t, 2, s.Selected, "a digit past the hand changes nothing")

		s.SelectDigit("x")
		assert.Equal(t, 2, s.Selected, "a non-digit changes nothing")
	})

	t.Run("SelectedCard follows the cursor", func(t *testing.T) {
		t.Parallel()
		s := Session{Base: BaseState{Hand: hand}, Selected: 1}
		card, ok := s.SelectedCard()
		require.True(t, ok)
		assert.Equal(t, hand[1], card)
	})
}

func TestSession_SubmitWithoutASeatIsRefused(t *testing.T) {
	t.Parallel()
	var s Session
	require.ErrorIs(t, s.Submit(nil), errNotSeated)
}

// Only a live table holds the session past the router's idle limit: a finished one is
// a game-over screen with nothing left to forfeit.
func TestSession_IdleExemptOnlyWhilePlaying(t *testing.T) {
	t.Parallel()

	for phase, want := range map[game.Phase]bool{game.Waiting: false, game.Playing: true, game.Finished: false} {
		s := Session{Base: BaseState{Phase: phase}}
		assert.Equal(t, want, s.IdleExempt(), "phase %v", phase)
	}
}

func TestSession_UnsubscribeIsIdempotent(t *testing.T) {
	t.Parallel()
	var s Session
	assert.NotPanics(t, func() {
		s.Unsubscribe()
		s.Close()
	})
}

// startedSession builds a real two-player table and binds a session to seat 0.
func startedSession(t *testing.T) (*game.Engine, Session) {
	t.Helper()
	players := []*game.Player{
		{ID: testutil.SeatID(1), UserID: testutil.UID(1), Name: "alice"},
		{ID: testutil.SeatID(2), UserID: testutil.UID(2), Name: "bob"},
	}
	engine := game.NewEngine(&crazyeight.Rules{}, players, deck.StandardDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	global := router.GlobalContext{User: &db.User{ID: testutil.UID(1), Username: "alice"}}
	s, err := NewSession(global, engine, "crazy eights")
	require.NoError(t, err)
	return engine, s
}

func TestNewSession_BindsAndSubscribes(t *testing.T) {
	t.Parallel()
	engine, s := startedSession(t)

	require.NotNil(t, s.Bound)
	assert.Equal(t, testutil.SeatID(1), s.Bound.PlayerID())
	assert.NotNil(t, s.Events)
	assert.Equal(t, 1, engine.Broadcaster().Len())
}

func TestNewSession_WithoutAUserStillBuildsAView(t *testing.T) {
	t.Parallel()
	s, err := NewSession(router.GlobalContext{}, nil, "crazy eights")
	require.NoError(t, err)
	assert.Nil(t, s.Bound)
	assert.Nil(t, s.Events)
	assert.NotPanics(t, func() { s.Sync(nil) }, "an unbound view still renders")
}

func TestSession_SyncBase(t *testing.T) {
	t.Parallel()
	_, s := startedSession(t)
	s.Sync(nil)

	assert.Equal(t, game.Playing, s.Base.Phase)
	assert.NotEmpty(t, s.Base.Hand, "the bound seat sees its own hand")
	assert.Len(t, s.Base.Opponents, 1, "and only the other player as an opponent")

	// Seats carry every player in seat order, hero included; Opponents does not.
	require.Len(t, s.Base.Seats, 2)
	assert.Equal(t, []string{testutil.SeatID(1), testutil.SeatID(2)}, s.Base.SeatOrder())
	assert.Equal(t, map[string]string{testutil.SeatID(1): "alice", testutil.SeatID(2): "bob"}, s.Base.SeatNames())
	assert.Positive(t, s.Base.DeckSize)
}

func TestSession_SyncBase_PullsTheCursorBackIntoAShrinkingHand(t *testing.T) {
	t.Parallel()
	_, s := startedSession(t)
	s.Sync(nil)

	s.Selected = len(s.Base.Hand) + 5
	s.Sync(nil)
	assert.Equal(t, len(s.Base.Hand)-1, s.Selected)
}

func TestSession_SeatNamesFallBackToThePlayerID(t *testing.T) {
	t.Parallel()
	base := BaseState{Seats: []game.PlayerSnapshot{{ID: "7", Username: "7"}}}
	assert.Equal(t, map[string]string{"7": "7"}, base.SeatNames())
}

func TestSession_UnsubscribeReleasesTheEngineSlot(t *testing.T) {
	t.Parallel()
	engine, s := startedSession(t)
	require.Equal(t, 1, engine.Broadcaster().Len())

	s.Unsubscribe()

	assert.Zero(t, engine.Broadcaster().Len())
	assert.Nil(t, s.Events)
}

// Leaving mid-game forfeits the seat, so there is no table to go back to; once the
// game has finished the player returns to the lobby instead.
func TestSession_Leave(t *testing.T) {
	t.Parallel()

	t.Run("a player with no session goes home and unsubscribes", func(t *testing.T) {
		t.Parallel()
		engine, s := startedSession(t)
		s.Global.User = nil
		s.Sync(nil)

		msg := s.Leave()()

		require.IsType(t, router.ChangeViewMsg{}, msg)
		assert.Equal(t, router.RouteHome, msg.(router.ChangeViewMsg).ViewName)
		assert.Zero(t, engine.Broadcaster().Len(), "leaving always releases the feed")
	})

	t.Run("a finished game with no session still goes home", func(t *testing.T) {
		t.Parallel()
		_, s := startedSession(t)
		s.Global.User = nil
		s.Base.Phase = game.Finished

		msg := s.Leave()()
		assert.Equal(t, router.RouteHome, msg.(router.ChangeViewMsg).ViewName)
	})
}

// One Frame, one hold of the engine lock: the per-game state a view reads has to
// describe the same moment as the hand and the turn it is rendered beside. Reads that
// straddled a turn change put the highlight on one seat and the controls on another.
func TestSyncBaseState_ReadsTheGameStateInTheSameHold(t *testing.T) {
	t.Parallel()
	_, s := startedSession(t)

	var extraSuit deck.Suit
	var extraSeen bool
	// Nothing in here may reach back into the engine: the callback runs under the
	// engine lock, which is the whole point of it being one hold.
	s.Sync(func(state *game.State) {
		var extra *crazyeight.State
		extra, extraSeen = state.Extra.(*crazyeight.State)
		if extraSeen {
			extraSuit = extra.CurrentSuit
		}
	})

	require.True(t, extraSeen, "the per-game state is handed to the caller")
	assert.Equal(t, s.Base.TopDiscard.Suit, extraSuit,
		"the snapshot and the per-game read describe the same deal")
	assert.Equal(t, s.Base.CurrentPlayerID == s.Bound.PlayerID(), s.Base.MyTurn,
		"MyTurn comes off the same snapshot as CurrentPlayerID")
}

// Opponents is the seats with the hero removed. Consumers branch on its length, so an
// empty table and a solo table both have to be safe to render from.
func TestSyncBaseState_OpponentsExcludeTheHero(t *testing.T) {
	t.Parallel()

	var unbound BaseState
	assert.Empty(t, unbound.Opponents)

	_, s := startedSession(t)
	s.Sync(nil)
	for _, opp := range s.Base.Opponents {
		assert.NotEqual(t, s.Bound.PlayerID(), opp.ID)
	}
	assert.Len(t, s.Base.Seats, len(s.Base.Opponents)+1)
}
