package ginrummy

import (
	"context"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/ginrummy"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/tui/router"

	tea "charm.land/bubbletea/v2"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testUser() *db.User {
	return &db.User{ID: testutil.UID(1), Username: "alice"}
}

func startedTable(t *testing.T) (*game.Engine, *Model) {
	t.Helper()
	players := []*game.Player{
		{ID: testutil.SeatID(1), UserID: testutil.UID(1), Name: "alice"},
		{ID: testutil.SeatID(2), UserID: testutil.UID(2), Name: "bob"},
	}
	engine := game.NewEngine(&logic.Rules{}, players, deck.StandardDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	// A real manager, because leaving the table goes through it: the view is
	// constructed exactly as app.go builds it.
	global := router.GlobalContext{
		User:         testUser(),
		LobbyManager: lobby.NewManager(context.Background(), nil),
		Width:        80,
		Height:       40,
	}
	m, ok := New(global, engine).(*Model)
	require.True(t, ok)
	return engine, m
}

func TestSyncState_LoadsGinExtra(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)

	assert.Equal(t, 1, m.handNumber)
	assert.Equal(t, logic.AwaitingDraw, m.handPhase)
	assert.Len(t, m.Base.Hand, 10)
	assert.Len(t, m.seatOrder, 2)
	assert.Equal(t, "alice", m.seatNames[testutil.SeatID(1)])
	assert.Equal(t, 31, m.stockSize)
}

func TestClose_ReleasesEngineSubscription(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	require.Equal(t, 1, engine.Broadcaster().Len())
	m.Close()
	assert.Zero(t, engine.Broadcaster().Len())
}

// Mirrors the other game views' teardown test: without Close the listener goroutine
// stays parked on the event channel and the broadcaster slot is never returned.
func TestClose_IsIdempotentAndSurvivesAClosedEngine(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	require.Equal(t, 1, engine.Broadcaster().Len())

	m.Close()
	assert.Zero(t, engine.Broadcaster().Len())

	assert.NotPanics(t, m.Close, "session teardown may follow a view that already exited")

	engine.Close()
	assert.NotPanics(t, m.Close, "and may follow the engine going away")
}

// Everything the Sync callback keeps past the engine lock has to be a copy. The map
// and the hand result are the two that carry references, and an aliased one lets the
// next hand rewrite a summary the player is still reading - under -race it is a data
// race outright, since the engine mutates them from its own goroutines.
func TestSyncState_CopiesEverythingItKeepsFromTheEngine(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)

	engine.WithState(func(state *game.State) {
		s, ok := state.Extra.(*logic.State)
		require.True(t, ok)
		s.CumulativeScores = map[string]int{"1": 11, "2": 22}
		s.LastHandResult = &logic.HandResult{
			ScoreDelta:       7,
			Winner:           "1",
			OpponentDeadwood: []deck.Card{{Rank: deck.King, Suit: deck.Spades}},
			KnockerMelds:     [][]deck.Card{{{Rank: deck.Two, Suit: deck.Spades}}},
		}
	})
	m.syncState()
	require.Equal(t, 11, m.cumulativeScores["1"])
	require.Equal(t, 7, m.lastHandResult.ScoreDelta)

	engine.WithState(func(state *game.State) {
		s, ok := state.Extra.(*logic.State)
		require.True(t, ok)
		s.CumulativeScores["1"] = 999
		s.LastHandResult.ScoreDelta = 999
		s.LastHandResult.OpponentDeadwood[0] = deck.Card{Rank: deck.Ace, Suit: deck.Clubs}
		s.LastHandResult.KnockerMelds[0][0] = deck.Card{Rank: deck.Ace, Suit: deck.Clubs}
	})

	assert.Equal(t, 11, m.cumulativeScores["1"], "the score map is cloned, not aliased")
	assert.Equal(t, 7, m.lastHandResult.ScoreDelta)
	assert.Equal(t, deck.King, m.lastHandResult.OpponentDeadwood[0].Rank, "deadwood is cloned too")
	assert.Equal(t, deck.Two, m.lastHandResult.KnockerMelds[0][0].Rank, "and so is every meld")
}

// A knock is the one move a misfire cannot be taken back from, so it only reaches the
// engine on the hero's turn and only once a card has been drawn.
func TestHandleKnock_OnlyFiresWhenAKnockIsLegal(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		myTurn    bool
		phase     logic.Phase
		wantReach bool
	}{
		{name: "off turn", myTurn: false, phase: logic.AwaitingDiscard},
		{name: "before drawing", myTurn: true, phase: logic.AwaitingDraw},
		{name: "on turn holding eleven", myTurn: true, phase: logic.AwaitingDiscard, wantReach: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, m := startedTable(t)
			m.Base.MyTurn = tc.myTurn
			m.handPhase = tc.phase
			m.Selected = 0

			_, _ = m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
			if tc.wantReach {
				// The seat holds ten cards, so the engine rejects the knock - which is
				// what proves the key reached it rather than being swallowed.
				assert.Error(t, m.lastActionErr)
				return
			}
			assert.NoError(t, m.lastActionErr, "the key must not reach the engine")
		})
	}
}

// s and t are the two draws, and both are meaningless off turn.
func TestDrawKeys_OnlyActOnYourOwnTurn(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"s", "t"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			_, m := startedTable(t)
			m.Base.MyTurn = false

			_, _ = m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
			require.NoError(t, m.lastActionErr, "an off-turn draw never reaches the engine")

			m.Base.MyTurn = true
			m.handPhase = logic.AwaitingDraw
			_, _ = m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
			m.syncState()
			assert.True(t, m.handPhase == logic.AwaitingDiscard || m.lastActionErr != nil,
				"on turn the draw either lands or is rejected, but it is not swallowed")
		})
	}
}

// Enter is three different moves depending on the screen, and the wrong one would
// either forfeit the match or discard a card the player meant to keep.
func TestHandleEnter_MeansWhateverTheScreenSays(t *testing.T) {
	t.Parallel()

	t.Run("a finished match leaves the table", func(t *testing.T) {
		t.Parallel()
		_, m := startedTable(t)
		m.Base.Phase = game.Finished

		_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		assert.NotNil(t, cmd)
	})

	t.Run("between hands only the dealer deals", func(t *testing.T) {
		t.Parallel()
		_, m := startedTable(t)
		m.handComplete = true
		m.Base.MyTurn = false

		_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		assert.Nil(t, cmd)
		assert.NoError(t, m.lastActionErr, "waiting for the other seat is not an error")
	})

	t.Run("before drawing there is nothing to discard", func(t *testing.T) {
		t.Parallel()
		_, m := startedTable(t)
		m.Base.MyTurn = true
		m.handPhase = logic.AwaitingDraw

		_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		assert.NoError(t, m.lastActionErr, "the discard never reaches the engine")
	})
}

func TestHandleKey_EscapeAsksThenLeavesTheTable(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.Nil(t, cmd, "esc asks before forfeiting")
	assert.Contains(t, m.View().Content, "forfeit")
	_, cmd = m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	assert.NotNil(t, cmd)
}

func TestInit_ArmsBothTheFeedAndTheClock(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)
	assert.NotNil(t, m.Init())
}
