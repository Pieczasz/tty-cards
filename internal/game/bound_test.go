package game

import (
	"slices"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/broadcaster"
	"github.com/Pieczasz/terminal-card/internal/deck"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bindRules struct{}

func (bindRules) MinPlayers() int                     { return 2 }
func (bindRules) MaxPlayers() int                     { return 4 }
func (bindRules) InitialDeck() []deck.Card            { return deck.Standard() }
func (bindRules) InitialDealCount() int               { return 2 }
func (bindRules) OnGameStart(*State) error            { return nil }
func (bindRules) ValidateAction(*State, Action) error { return nil }
func (bindRules) AfterAction(*State, Action) error    { return nil }
func (bindRules) ApplyAction(*State, Action) error    { return nil }
func (bindRules) CheckWinCondition(*State) bool       { return false }
func (bindRules) Standings(s *State) []*Player        { return s.Players }

// boundHand is the hand slice of a Frame read, which is the one read path views use.
func boundHand(b *BoundEngine) []deck.Card {
	_, hand, _ := b.Frame(nil)
	return hand
}

func TestBoundEngine_HandIsClonedAndScoped(t *testing.T) {
	t.Parallel()

	p1 := &Player{ID: "1"}
	p2 := &Player{ID: "2"}
	engine := NewEngine(bindRules{}, []*Player{p1, p2}, deck.Standard())
	t.Cleanup(engine.Close)
	require.NoError(t, engine.Start())

	bound := Bind(engine, "1")
	hand := boundHand(bound)
	require.NotEmpty(t, hand)

	orig := hand[0]
	hand[0] = deck.Card{}
	again := boundHand(bound)
	assert.Equal(t, orig, again[0], "mutating the hand copy must not alter engine state")

	other := boundHand(Bind(engine, "2"))
	assert.Len(t, other, len(hand))
}

// The Frame hand is the redaction boundary: it is the only place a player's own
// cards are readable, so it has to match the bound seat and no other.
func TestBoundEngine_HandBelongsToTheBoundPlayerOnly(t *testing.T) {
	t.Parallel()

	p1 := &Player{ID: "1"}
	p2 := &Player{ID: "2"}
	engine := NewEngine(bindRules{}, []*Player{p1, p2}, deck.Standard())
	t.Cleanup(engine.Close)
	require.NoError(t, engine.Start())

	var dealt1, dealt2 []deck.Card
	engine.WithState(func(state *State) {
		dealt1 = slices.Clone(state.Players[0].Cards)
		dealt2 = slices.Clone(state.Players[1].Cards)
	})
	require.NotEqual(t, dealt1, dealt2, "a shuffled deck must not deal two identical hands")

	assert.Equal(t, dealt1, boundHand(Bind(engine, "1")), "each seat sees exactly its own cards")
	assert.Equal(t, dealt2, boundHand(Bind(engine, "2")))
	assert.Nil(t, boundHand(Bind(engine, "not-seated")), "an ID with no seat holds no cards")
}

type noopAction struct{}

func (noopAction) Name() string { return "noop" }

func TestBoundEngine_SubmitRequiresBoundPlayer(t *testing.T) {
	t.Parallel()

	p1 := &Player{ID: "1"}
	p2 := &Player{ID: "2"}
	engine := NewEngine(bindRules{}, []*Player{p1, p2}, deck.Standard())
	t.Cleanup(engine.Close)
	require.NoError(t, engine.Start())

	current := engine.CurrentPlayerID()
	other := "1"
	if current == "1" {
		other = "2"
	}
	boundOther := Bind(engine, other)
	err := boundOther.Submit(noopAction{})
	assert.ErrorContains(t, err, "wait for your turn")
}

// A view gets a channel, not the broadcaster: it must be able to join and leave the
// feed without being able to end it for the rest of the table.
func TestBoundEngine_SubscribeAndUnsubscribe(t *testing.T) {
	t.Parallel()

	engine := NewEngine(bindRules{}, []*Player{{ID: "1"}, {ID: "2"}}, deck.Standard())
	t.Cleanup(engine.Close)
	bound := Bind(engine, "1")

	events, err := bound.Subscribe()
	require.NoError(t, err)
	require.Equal(t, 1, engine.SubscriberCount())

	require.NoError(t, engine.Start())
	assert.Equal(t, EventGameStarted, (<-events).Type)

	bound.Unsubscribe(events)
	assert.Zero(t, engine.SubscriberCount(), "unsubscribing returns the slot")

	var unbound *BoundEngine
	_, err = unbound.Subscribe()
	require.Error(t, err, "a view with no engine is told so rather than handed a nil channel")
	assert.NotPanics(t, func() { unbound.Unsubscribe(nil) })
}

// A view can hold a nil BoundEngine: Bind refuses a nil engine, and the session
// keeps whatever it got. Every method has to answer rather than panic, because the
// view calls them from its render loop before it knows it has no game.
func TestBoundEngine_NilIsInert(t *testing.T) {
	t.Parallel()

	assert.Nil(t, Bind(nil, "1"), "there is no seat without an engine")

	var unbound *BoundEngine
	assert.Empty(t, unbound.PlayerID())
	require.ErrorContains(t, unbound.Submit(noopAction{}), "no active game")

	snap, hand, remaining := unbound.Frame(func(*State) { t.Fatal("there is no state to read") })
	assert.Equal(t, StateSnapshot{}, snap)
	assert.Nil(t, hand)
	assert.Zero(t, remaining)
}

// The bound ID is what scopes everything else a view does.
func TestBoundEngine_ExposesItsSeat(t *testing.T) {
	t.Parallel()

	engine := NewEngine(bindRules{}, []*Player{{ID: "1"}, {ID: "2"}}, deck.Standard())
	t.Cleanup(engine.Close)

	assert.Equal(t, "1", Bind(engine, "1").PlayerID())
}

// Frame is one lock hold: the callback sees the same state the snapshot and hand were
// taken from, or a view can render a hand against a table that has already moved on.
func TestBoundEngine_FrameCallbackSeesTheSnapshottedState(t *testing.T) {
	t.Parallel()

	engine := NewEngine(bindRules{}, []*Player{{ID: "1"}, {ID: "2"}}, deck.Standard())
	t.Cleanup(engine.Close)
	require.NoError(t, engine.Start())

	bound := Bind(engine, "1")
	var seenPhase Phase
	var seenHand int
	snap, hand, _ := bound.Frame(func(state *State) {
		seenPhase = state.Phase
		seenHand = len(state.Players[0].Cards)
	})

	assert.Equal(t, snap.Phase, seenPhase)
	assert.Len(t, hand, seenHand)
}

// A subscriber slot is finite, so a view that fails to get one has to be told why
// rather than handed a channel that never delivers.
func TestBoundEngine_SubscribeReportsCapacity(t *testing.T) {
	t.Parallel()

	engine := NewEngine(bindRules{}, []*Player{{ID: "1"}, {ID: "2"}}, deck.Standard())
	t.Cleanup(engine.Close)
	bound := Bind(engine, "1")

	for range 2 + 8 {
		_, err := bound.Subscribe()
		require.NoError(t, err)
	}

	_, err := bound.Subscribe()
	require.ErrorIs(t, err, broadcaster.ErrAtCapacity)
	assert.ErrorContains(t, err, "subscribe to game events", "the view shows this line verbatim")
}
