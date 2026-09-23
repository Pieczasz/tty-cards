package hearts

import (
	"slices"

	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/hearts"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"
	"github.com/Pieczasz/terminal-card/internal/tui/views/gameview"

	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testUser() *db.User {
	return &db.User{ID: testutil.UID(1), Username: "alice"}
}

func startedTable(t *testing.T) (*game.Engine, *model) {
	t.Helper()
	players := testutil.NamedPlayers("alice", "bob", "carol", "dave")
	engine := game.NewEngine(&logic.Rules{}, players, deck.Standard())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	// A real manager, because leaving the table goes through it: the view is
	// constructed exactly as app.go builds it.
	global := router.GlobalContext{
		User:         testUser(),
		LobbyManager: lobby.NewManager(t.Context(), nil),
		Width:        80,
		Height:       40,
	}
	m, ok := New(global, engine).(*model)
	require.True(t, ok)
	return engine, m
}

func TestSyncState_LoadsHeartsExtra(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)

	assert.Equal(t, 1, m.handNumber)
	assert.Contains(t, []logic.Phase{logic.PhasePassing, logic.PhaseTrickPlay}, m.phase)
	assert.Len(t, m.Base.SeatOrder(), 4)
	assert.Equal(t, "alice", m.Base.SeatNames()[testutil.SeatID(1)])
	assert.False(t, m.heartsBroken)
}

func TestClose_ReleasesEngineSubscription(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	require.Equal(t, 1, engine.SubscriberCount())
	m.Close()
	assert.Zero(t, engine.SubscriberCount())
}

// The pass staging used to be keyed by hand position, which survives a re-sort or a
// re-deal underneath it and then passes cards the player never picked. Keyed by card,
// a hand that moves around keeps the same three cards staged.
func TestPassSelection_FollowsTheCardsNotThePositions(t *testing.T) {
	t.Parallel()

	hand := []deck.Card{
		{Rank: deck.Two, Suit: deck.Clubs},
		{Rank: deck.Five, Suit: deck.Hearts},
		{Rank: deck.King, Suit: deck.Spades},
		{Rank: deck.Nine, Suit: deck.Diamonds},
	}
	m := &model{
		Base:         gameview.BaseState{Hand: slices.Clone(hand), MyTurn: true},
		passSelected: map[deck.Card]struct{}{},
		phase:        logic.PhasePassing,
	}

	m.Selected = 1
	m.handleSpace()
	m.Selected = 2
	m.handleSpace()
	require.Len(t, m.passSelected, 2)

	// The same cards, dealt back in a different order.
	m.Base.Hand = []deck.Card{hand[2], hand[3], hand[0], hand[1]}
	assert.Equal(t, map[int]struct{}{0: {}, 3: {}}, m.passIndices(),
		"the markers move with the cards")

	// A card that left the hand stops being staged.
	m.Base.Hand = []deck.Card{hand[0], hand[2]}
	m.prunePassSelection()
	assert.Equal(t, map[deck.Card]struct{}{hand[2]: {}}, m.passSelected)

	// And the staging goes entirely once the pass is over.
	m.phase = logic.PhaseTrickPlay
	m.prunePassSelection()
	assert.Empty(t, m.passSelected)
}

// A view that skips Close parks a listener goroutine and holds a broadcaster slot for
// every disconnected player until the engine itself is closed.
func TestClose_IsIdempotentAndSurvivesAClosedEngine(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)
	require.Equal(t, 1, engine.SubscriberCount())

	m.Close()
	assert.Zero(t, engine.SubscriberCount())

	assert.NotPanics(t, m.Close, "session teardown may follow a view that already exited")

	engine.Close()
	assert.NotPanics(t, m.Close, "and may follow the engine going away")
}

// The three maps the Sync callback lifts out of the engine state are shared with the
// engine until they are cloned. An aliased one is a data race under -race and, worse,
// lets the next trick rewrite the numbers a player is reading.
func TestSyncState_CopiesTheMapsItKeepsFromTheEngine(t *testing.T) {
	t.Parallel()
	engine, m := startedTable(t)

	engine.WithState(func(state *game.State) {
		s, ok := state.Extra.(*logic.State)
		require.True(t, ok)
		s.TrickCards = map[string]deck.Card{"2": {Rank: deck.Ace, Suit: deck.Spades}}
		s.HandPoints = map[string]int{"1": 4}
		s.CumulativeScores = map[string]int{"1": 40}
	})
	m.syncState()
	require.Equal(t, 4, m.handPoints["1"])

	engine.WithState(func(state *game.State) {
		s, ok := state.Extra.(*logic.State)
		require.True(t, ok)
		s.TrickCards["2"] = deck.Card{Rank: deck.Two, Suit: deck.Clubs}
		s.HandPoints["1"] = 999
		s.CumulativeScores["1"] = 999
	})

	assert.Equal(t, deck.Ace, m.trickCards["2"].Rank, "the trick is cloned, not aliased")
	assert.Equal(t, 4, m.handPoints["1"])
	assert.Equal(t, 40, m.cumulativeScores["1"])
}

// Space stages a card for the pass. Three is the whole rule: a fourth must be refused
// rather than silently replacing one, and toggling the same card takes it back.
func TestHandleSpace_StagesAtMostThreeCards(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)
	m.phase = logic.PhasePassing
	m.Base.MyTurn = true
	require.GreaterOrEqual(t, len(m.Base.Hand), 5)

	for i := range 4 {
		m.Selected = i
		_, _ = m.Update(tuitest.Key("space"))
	}
	assert.Len(t, m.passSelected, 3, "the fourth card is refused")

	m.Selected = 0
	_, _ = m.Update(tuitest.Key("space"))
	assert.Len(t, m.passSelected, 2, "space on a staged card takes it back")
}

func TestHandleSpace_IsInertOutsideThePassPhase(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		phase  logic.Phase
		myTurn bool
	}{
		{name: "during trick play", phase: logic.PhaseTrickPlay, myTurn: true},
		{name: "off turn", phase: logic.PhasePassing, myTurn: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, m := startedTable(t)
			m.phase, m.Base.MyTurn, m.Selected = tc.phase, tc.myTurn, 0

			_, _ = m.Update(tuitest.Key("space"))
			assert.Empty(t, m.passSelected)
		})
	}
}

// Passing fewer than three is the commonest mistake at the table, so it has to come
// back as a message rather than an engine rejection the player cannot act on.
func TestSubmitPass_RefusesAnythingButThreeCards(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)
	m.phase = logic.PhasePassing
	m.Base.MyTurn = true
	m.Selected = 0
	_, _ = m.Update(tuitest.Key("space"))

	_, _ = m.Update(tuitest.Key("enter"))
	require.ErrorIs(t, m.ActionErr, errNeedThreeCards)
	assert.Len(t, m.passSelected, 1, "the staging survives a refused pass")
}

func TestHandleEnter_MeansWhateverTheScreenSays(t *testing.T) {
	t.Parallel()

	t.Run("a finished match leaves the table", func(t *testing.T) {
		t.Parallel()
		_, m := startedTable(t)
		m.Base.Phase = game.Finished

		_, cmd := m.Update(tuitest.Key("enter"))
		assert.NotNil(t, cmd)
	})

	t.Run("between hands only the seat on turn deals", func(t *testing.T) {
		t.Parallel()
		_, m := startedTable(t)
		m.phase = logic.PhaseHandOver
		m.Base.MyTurn = false

		_, cmd := m.Update(tuitest.Key("enter"))
		assert.Nil(t, cmd)
		assert.NoError(t, m.ActionErr, "waiting for another seat is not an error")
	})

	t.Run("off turn nothing is played", func(t *testing.T) {
		t.Parallel()
		_, m := startedTable(t)
		m.phase = logic.PhaseTrickPlay
		m.Base.MyTurn = false

		_, _ = m.Update(tuitest.Key("enter"))
		assert.NoError(t, m.ActionErr, "the play never reaches the engine")
	})
}

func TestHandleKey_CursorAndEscape(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)

	_, _ = m.Update(tuitest.Key("l"))
	require.Equal(t, 1, m.Selected)
	_, _ = m.Update(tuitest.Key("h"))
	require.Equal(t, 0, m.Selected)
	_, _ = m.Update(tuitest.Key("4"))
	require.Equal(t, 4, m.Selected)

	_, cmd := m.Update(tuitest.Key("esc"))
	require.Nil(t, cmd, "esc asks before forfeiting")
	assert.Contains(t, m.View().Content, "forfeit")
	_, cmd = m.Update(tuitest.Key("y"))
	assert.NotNil(t, cmd, "y leaves the table")
}

func TestInit_ArmsBothTheFeedAndTheClock(t *testing.T) {
	t.Parallel()
	_, m := startedTable(t)
	assert.NotNil(t, m.Init())
}
