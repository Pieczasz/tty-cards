package lobby

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"

	"uuid"

	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// registerGame registers rules under a display name. These tests only ever look
// games up by name, so the slug just has to be present and distinct.
func registerGame(r *game.Registry, name string, rules game.Rules) {
	r.RegisterModule(game.Module{
		Name:    name,
		Slug:    strings.ToLower(name),
		Factory: func() game.Rules { return rules },
	})
}

type MockRules struct {
	mock.Mock
}

func (m *MockRules) MinPlayers() int                     { return m.Called().Int(0) }
func (m *MockRules) MaxPlayers() int                     { return m.Called().Int(0) }
func (m *MockRules) InitialDeck() []deck.Card            { return m.Called().Get(0).([]deck.Card) }
func (m *MockRules) InitialDealCount() int               { return m.Called().Int(0) }
func (m *MockRules) OnGameStart(state *game.State) error { return m.Called(state).Error(0) }
func (m *MockRules) ValidateAction(state *game.State, action game.Action) error {
	return m.Called(state, action).Error(0)
}
func (m *MockRules) ApplyAction(state *game.State, action game.Action) error {
	m.Called(state, action)
	return nil
}
func (m *MockRules) AfterAction(state *game.State, action game.Action) error {
	return m.Called(state, action).Error(0)
}
func (m *MockRules) CheckWinCondition(state *game.State) bool { return m.Called(state).Bool(0) }
func (m *MockRules) Standings(state *game.State) []*game.Player {
	return m.Called(state).Get(0).([]*game.Player)
}

type MockMatchRepo struct {
	mock.Mock
}

func (m *MockMatchRepo) RecordCasualMatch(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID,
) error {
	return m.Called(ctx, ref, orderedUserIDs).Error(0)
}

func (m *MockMatchRepo) FinalizeRankedMatch(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID, places []int,
) error {
	return m.Called(ctx, ref, orderedUserIDs, places).Error(0)
}

// gameRef mirrors what registerGame put in the registry: the persisted key is the
// slug, so an expectation written against the display name would pass while the row
// was being written under a different identity.
func gameRef(name string) db.GameRef {
	return db.GameRef{Slug: strings.ToLower(name), Name: name}
}

// newTestManager is NewManager with teardown. A started game parks a watcher
// goroutine on the engine feed, and closing the engine - which only RemoveLobby does -
// is what ends it. Production always gets there; a test that drops the manager on the
// floor would strand the goroutine and trip goleak.
func newTestManager(t *testing.T, repo db.MatchRepository) *Manager {
	t.Helper()
	m := NewManager(context.Background(), repo)
	t.Cleanup(func() {
		m.mu.RLock()
		codes := slices.Collect(maps.Keys(m.lobbies))
		m.mu.RUnlock()
		for _, code := range codes {
			m.RemoveLobby(code)
		}
	})
	return m
}

func mockPlayer(id string, dbID uuid.UUID) *game.Player {
	return &game.Player{ID: id, UserID: dbID}
}

func TestLobby_ToggleReady(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	leader := mockPlayer("p1", testutil.UID(1))
	guest := mockPlayer("p2", testutil.UID(2))

	cardGame := "MockGame"
	l, err := m.New(leader, WithMaxPlayers(4), WithCardGame(cardGame))
	require.NoError(t, err)
	require.NoError(t, m.JoinLobbyByCode(l.Code(), guest))

	registry := game.NewRegistry()
	mockRules := new(MockRules)

	mockRules.On("MinPlayers").Return(2)
	mockRules.On("MaxPlayers").Return(4)
	mockRules.On("InitialDeck").Return(deck.StandardDeck())
	mockRules.On("InitialDealCount").Return(5)
	mockRules.On("OnGameStart", mock.Anything).Return(nil)

	registerGame(registry, "MockGame", mockRules)

	err = l.ToggleReady(leader, registry)
	require.NoError(t, err)
	assert.True(t, l.IsReady(leader))
	assert.Equal(t, Waiting, l.state)

	err = l.ToggleReady(guest, registry)
	require.NoError(t, err)

	assert.Equal(t, InGame, l.state)
	assert.NotNil(t, l.activeEngine)

	mockRules.AssertExpectations(t)
}

func TestLobby_SettersAndGetters(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	leader := mockPlayer("p1", testutil.UID(1))

	cardGame := "CrazyEights"
	l, err := m.New(leader, WithCardGame(cardGame), WithPrivate(true), WithRanked(true))
	require.NoError(t, err)

	assert.Equal(t, cardGame, l.options.cardGame)
	assert.True(t, l.IsPrivate())
	assert.True(t, l.IsRanked())

	require.NoError(t, l.SetPrivate(leader, false))
	assert.False(t, l.IsPrivate())

	require.NoError(t, l.SetRanked(leader, false))
	assert.False(t, l.IsRanked())

	require.NoError(t, l.SetMaxPlayers(leader, 5, 2, 6))
	assert.Equal(t, 5, l.MaxPlayers())

	newGame := "Poker"
	require.NoError(t, l.SetCardGame(leader, newGame))
	assert.Equal(t, newGame, l.options.cardGame)
}

func TestLobby_DefaultCasual(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	leader := mockPlayer("leader", testutil.UID(1))
	l, err := m.New(leader, WithCardGame("TestGame"))
	require.NoError(t, err)
	assert.False(t, l.IsRanked(), "new lobbies default to casual to limit Elo farming under open registration")
}

func TestLobby_BasicGetters(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	leader := mockPlayer("leader", testutil.UID(1))

	cardGame := "CrazyEights"
	l, err := m.New(leader, WithCardGame(cardGame), WithPrivate(true), WithMaxPlayers(4))
	require.NoError(t, err)

	assert.Equal(t, cardGame, l.GameName())
	assert.Len(t, l.Code(), 8)
	assert.NotNil(t, l.Broadcaster())
	assert.Empty(t, l.Guests())
	assert.Equal(t, 4, l.MaxPlayers())
	assert.True(t, l.IsPrivate())

	require.NoError(t, m.JoinLobbyByCode(l.Code(), mockPlayer("guest", testutil.UID(2))))
	assert.Len(t, l.Guests(), 1)
}

func TestLobby_StartGameAndBroadcasterEvents(t *testing.T) {
	t.Parallel()
	mockRepo := new(MockMatchRepo)
	m := newTestManager(t, mockRepo)
	leader := mockPlayer("leader", testutil.UID(1))
	guest := mockPlayer("guest", testutil.UID(2))

	cardGame := "MockGame"
	l, err := m.New(leader, WithMaxPlayers(2), WithCardGame(cardGame), WithRanked(true))
	require.NoError(t, err)
	require.NoError(t, m.JoinLobbyByCode(l.Code(), guest))

	registry := game.NewRegistry()
	mockRules := new(MockRules)

	mockRules.On("MinPlayers").Return(2)
	mockRules.On("MaxPlayers").Return(4)
	mockRules.On("InitialDeck").Return(deck.StandardDeck())
	mockRules.On("InitialDealCount").Return(5)
	mockRules.On("OnGameStart", mock.Anything).Return(nil)
	mockRules.On("CheckWinCondition", mock.Anything).Return(true) // Immediate win to end game
	mockRules.On("Standings", mock.Anything).Return([]*game.Player{leader, guest})

	registerGame(registry, "MockGame", mockRules)

	done := make(chan struct{})
	mockRepo.On("FinalizeRankedMatch", mock.Anything, gameRef("MockGame"), []uuid.UUID{testutil.UID(1), testutil.UID(2)}, mock.Anything).
		Run(func(mock.Arguments) {
			close(done)
		}).
		Return(nil)

	ch, subErr := l.Broadcaster().Subscribe()
	require.NoError(t, subErr)

	err = l.ToggleReady(leader, registry)
	require.NoError(t, err)
	err = l.ToggleReady(guest, registry)
	require.NoError(t, err)

	// Game is started, let's trigger GameEnded event directly to test
	// handleBroadcasterEvents. Read under the lock: the watcher releases a finished
	// game from its own goroutine, so activeEngine has a concurrent writer.
	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	require.NotNil(t, engine)

	engine.Broadcaster().Broadcast(game.Event{
		Type: game.EventGameEnded,
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ranked finalize")
	}

	l.Broadcaster().Unsubscribe(ch)
	mockRepo.AssertExpectations(t)
}

// A casual game is still a game the players want to find in their history, so it is
// recorded; only the Elo write is reserved for ranked lobbies.
func TestLobby_CasualGameIsRecordedWithoutElo(t *testing.T) {
	t.Parallel()
	mockRepo := new(MockMatchRepo)
	m := newTestManager(t, mockRepo)
	leader := mockPlayer("leader", testutil.UID(1))
	guest := mockPlayer("guest", testutil.UID(2))

	cardGame := "MockGame"
	l, err := m.New(leader, WithMaxPlayers(2), WithCardGame(cardGame), WithRanked(false))
	require.NoError(t, err)
	require.NoError(t, m.JoinLobbyByCode(l.Code(), guest))

	registry := game.NewRegistry()
	mockRules := new(MockRules)
	mockRules.On("MinPlayers").Return(2)
	mockRules.On("MaxPlayers").Return(4)
	mockRules.On("InitialDeck").Return(deck.StandardDeck())
	mockRules.On("InitialDealCount").Return(5)
	mockRules.On("OnGameStart", mock.Anything).Return(nil)
	mockRules.On("CheckWinCondition", mock.Anything).Return(true)
	mockRules.On("Standings", mock.Anything).Return([]*game.Player{leader, guest})
	registerGame(registry, "MockGame", mockRules)

	done := make(chan struct{})
	mockRepo.On("RecordCasualMatch", mock.Anything, gameRef("MockGame"), []uuid.UUID{testutil.UID(1), testutil.UID(2)}).
		Run(func(mock.Arguments) { close(done) }).
		Return(nil)

	require.NoError(t, l.ToggleReady(leader, registry))
	require.NoError(t, l.ToggleReady(guest, registry))

	engine := l.activeEngine
	require.NotNil(t, engine)
	engine.Broadcaster().Broadcast(game.Event{Type: game.EventGameEnded})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the casual match to be recorded")
	}

	mockRepo.AssertExpectations(t)
	mockRepo.AssertNotCalled(t, "FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestLobby_ToggleReady_EdgeCases(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	leader := mockPlayer("p1", testutil.UID(1))
	guest := mockPlayer("p2", testutil.UID(2))
	guest3 := mockPlayer("p3", testutil.UID(3))

	l, err := m.New(leader, WithMaxPlayers(3), WithCardGame("Mock"))
	require.NoError(t, err)
	require.NoError(t, m.JoinLobbyByCode(l.Code(), guest))
	require.NoError(t, m.JoinLobbyByCode(l.Code(), guest3))

	registry := game.NewRegistry()
	mockRules := new(MockRules)

	// mockRules limits max players to 2, but lobby has 3!
	mockRules.On("MinPlayers").Return(2)
	mockRules.On("MaxPlayers").Return(2)
	registerGame(registry, "Mock", mockRules)

	require.NoError(t, l.ToggleReady(leader, registry))
	require.NoError(t, l.ToggleReady(guest, registry))
	err = l.ToggleReady(guest3, registry) // This triggers start game and should fail due to max players!
	require.ErrorContains(t, err, "too many players")

	// Missing game in registry
	leader2 := mockPlayer("p4", testutil.UID(4))
	l2, err := m.New(leader2, WithCardGame("Missing"))
	require.NoError(t, err)
	err = l2.ToggleReady(leader2, registry) // Should fail on create game rules
	require.ErrorContains(t, err, "failed to create game rules")

	// Game already in progress
	mockRules2 := new(MockRules)
	mockRules2.On("MinPlayers").Return(2)
	mockRules2.On("MaxPlayers").Return(4)
	mockRules2.On("InitialDeck").Return(deck.StandardDeck())
	mockRules2.On("InitialDealCount").Return(5)
	mockRules2.On("OnGameStart", mock.Anything).Return(nil)
	registerGame(registry, "Mock2", mockRules2)

	leader3 := mockPlayer("p5", testutil.UID(5))
	l3, err := m.New(leader3, WithCardGame("Mock2"))
	require.NoError(t, err)
	_ = m.JoinLobbyByCode(l3.Code(), guest) // guest is already in lobby l, so this join is expected to fail.
	guest4 := mockPlayer("p6", testutil.UID(6))
	require.NoError(t, m.JoinLobbyByCode(l3.Code(), guest4))
	require.NoError(t, l3.ToggleReady(leader3, registry))
	require.NoError(t, l3.ToggleReady(guest4, registry)) // Starts game!

	err = l3.ToggleReady(leader3, registry) // Game is already in progress
	require.ErrorContains(t, err, "game is already in progress")

	// Unknown player toggling ready
	err = l2.ToggleReady(guest3, registry)
	require.ErrorContains(t, err, "not in lobby")

	// Removing a player who is in no lobby is a no-op and closes nothing.
	m.LeaveLobby(guest3)
	_, err = m.FindLobbyByCode(l3.Code())
	require.NoError(t, err)
}

// newTestLobby is a lobby with rules registered under "Mock" that accepts up to
// maxPlayers, for tests that care about lobby bookkeeping rather than the game.
func newTestLobby(t *testing.T, maxPlayers int) (*Manager, *Lobby, *game.Registry) {
	t.Helper()
	m := newTestManager(t, nil)
	leader := mockPlayer("p1", testutil.UID(1))

	l, err := m.New(leader, WithMaxPlayers(maxPlayers), WithCardGame("Mock"))
	require.NoError(t, err)

	registry := game.NewRegistry()
	rules := new(MockRules)
	rules.On("MinPlayers").Return(2).Maybe()
	rules.On("MaxPlayers").Return(maxPlayers).Maybe()
	rules.On("InitialDeck").Return(deck.StandardDeck()).Maybe()
	rules.On("InitialDealCount").Return(2).Maybe()
	rules.On("OnGameStart", mock.Anything).Return(nil).Maybe()
	rules.On("CheckWinCondition", mock.Anything).Return(false).Maybe()
	rules.On("Standings", mock.Anything).Return([]*game.Player{}).Maybe()
	registerGame(registry, "Mock", rules)

	return m, l, registry
}

// drainEventTypes collects whatever is already queued. Broadcast happens under the
// lobby lock and returns before the call that caused it, so anything that is going to
// arrive has arrived by the time the caller gets control back.
func drainEventTypes(ch <-chan Event) []string {
	var types []string
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				// Leaving closes the leaver's own subscription, so the stream ending is
				// an ordinary outcome here rather than the absence of events.
				return types
			}
			types = append(types, ev.Type)
			continue
		default:
		}
		return types
	}
}

// Every roster and settings change has to reach the other clients: a lobby view that is
// never told somebody joined shows a stale table until the player presses a key.
func TestLobby_ChangesAreBroadcast(t *testing.T) {
	t.Parallel()

	t.Run("a guest joining", func(t *testing.T) {
		t.Parallel()
		m, l, _ := newTestLobby(t, 4)
		ch, err := l.Subscribe("p1")
		require.NoError(t, err)

		require.NoError(t, m.JoinLobbyByCode(l.Code(), mockPlayer("p2", testutil.UID(2))))

		assert.Equal(t, []string{EventPlayersUpdated}, drainEventTypes(ch))
	})

	t.Run("a guest leaving", func(t *testing.T) {
		t.Parallel()
		m, l, _ := newTestLobby(t, 4)
		guest := mockPlayer("p2", testutil.UID(2))
		require.NoError(t, m.JoinLobbyByCode(l.Code(), guest))

		ch, err := l.Subscribe("p1")
		require.NoError(t, err)
		m.LeaveLobby(guest)

		assert.Equal(t, []string{EventPlayersUpdated}, drainEventTypes(ch))
	})

	t.Run("the leader leaving an empty lobby closes it", func(t *testing.T) {
		t.Parallel()
		m, l, _ := newTestLobby(t, 4)
		// Not l.Subscribe: a leaving player's own subscription is closed before the
		// event goes out, so only an observer that is not the leaver can see it.
		observer, err := l.Broadcaster().Subscribe()
		require.NoError(t, err)
		own, err := l.Subscribe("p1")
		require.NoError(t, err)

		m.LeaveLobby(l.Leader())

		_, err = m.FindLobbyByCode(l.Code())
		require.Error(t, err, "the last player out closes the lobby")
		assert.Equal(t, []string{EventLobbyClosed}, drainEventTypes(observer))
		assert.Empty(t, drainEventTypes(own), "the leaver's own stream is already closed")
	})

	t.Run("a settings change", func(t *testing.T) {
		t.Parallel()
		_, l, _ := newTestLobby(t, 4)
		ch, err := l.Subscribe("p1")
		require.NoError(t, err)

		require.NoError(t, l.SetPrivate(l.Leader(), false))

		assert.Equal(t, []string{EventSettingsUpdated, EventPlayersUpdated}, drainEventTypes(ch),
			"the change un-readies the table, so the rosters hear it too")
	})

	t.Run("a rejected settings change is not announced", func(t *testing.T) {
		t.Parallel()
		_, l, _ := newTestLobby(t, 4)
		ch, err := l.Subscribe("p1")
		require.NoError(t, err)

		require.Error(t, l.SetMaxPlayers(l.Leader(), 0, 0, 0))

		assert.Empty(t, drainEventTypes(ch), "nothing changed, so there is nothing to publish")
	})
}

// SetMaxPlayers is the one setting that can contradict reality: it must not be allowed to
// drop below the players already seated, or below what the game itself needs.
func TestLobby_SetMaxPlayers_Bounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		guests   int
		limit    int
		rulesMin int
		rulesMax int
		wantErr  string
	}{
		{name: "equal to the roster is allowed", guests: 2, limit: 3},
		{name: "above the roster is allowed", guests: 2, limit: 4},
		{name: "below the roster is refused", guests: 2, limit: 2, wantErr: "below current roster (3)"},
		{name: "equal to the game minimum is allowed", guests: 0, limit: 2, rulesMin: 2},
		{name: "below the game minimum is refused", guests: 0, limit: 2, rulesMin: 3, wantErr: "at least 3"},
		{name: "equal to the game maximum is allowed", guests: 0, limit: 4, rulesMax: 4},
		{name: "above the game maximum is refused", guests: 0, limit: 5, rulesMax: 4, wantErr: "cannot exceed 4"},
		{name: "unbounded rules impose nothing", guests: 0, limit: 9, rulesMin: 0, rulesMax: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, l, _ := newTestLobby(t, 9)
			for i := range tt.guests {
				require.NoError(t, m.JoinLobbyByCode(l.Code(), mockPlayer(fmt.Sprintf("g%d", i), testutil.UID(uint64(10+i)))))
			}

			err := l.SetMaxPlayers(l.Leader(), tt.limit, tt.rulesMin, tt.rulesMax)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, 9, l.MaxPlayers(), "a refused change must not be applied")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.limit, l.MaxPlayers())
		})
	}
}

// A player who leaves must stop receiving lobby events.
func TestLobby_LeavingUnsubscribesThePlayer(t *testing.T) {
	t.Parallel()
	m, l, _ := newTestLobby(t, 4)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, m.JoinLobbyByCode(l.Code(), guest))

	guestCh, err := l.Subscribe(guest.ID)
	require.NoError(t, err)
	leaderCh, err := l.Subscribe("p1")
	require.NoError(t, err)

	m.LeaveLobby(guest)

	select {
	case _, ok := <-guestCh:
		assert.False(t, ok, "the departed player's channel must be closed")
	case <-time.After(time.Second):
		t.Fatal("the departed player is still subscribed")
	}

	l.mu.RLock()
	_, stillTracked := l.playerSubs[guest.ID]
	l.mu.RUnlock()
	assert.False(t, stillTracked, "and their subscription must not be tracked any more")
	assert.Equal(t, []string{EventPlayersUpdated}, drainEventTypes(leaderCh), "everybody else still hears about it")
}

// A lobby whose broadcaster has been torn down by RemoveLobby is still reachable from any
// session that was holding it, so every path that publishes has to tolerate that.
func TestLobby_SurvivesATornDownBroadcaster(t *testing.T) {
	t.Parallel()
	m, l, _ := newTestLobby(t, 4)

	m.RemoveLobby(l.Code())

	require.NotPanics(t, func() {
		l.mu.Lock()
		l.broadcastLocked(Event{Type: EventPlayersUpdated})
		l.unsubscribePlayerLocked("p1")
		l.mu.Unlock()

		assert.NoError(t, l.SetPrivate(l.Leader(), true))
		l.Unsubscribe("p1", make(chan Event))
	})

	_, err := l.Subscribe("p1")
	require.ErrorContains(t, err, "lobby is closed")
}

// A table can be re-used for a second hand once the first is over, but never while one is
// still running.
func TestLobby_ToggleReadyAfterAFinishedGame(t *testing.T) {
	t.Parallel()
	m, l, registry := newTestLobby(t, 2)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, m.JoinLobbyByCode(l.Code(), guest))

	require.NoError(t, l.ToggleReady(l.Leader(), registry))
	require.NoError(t, l.ToggleReady(guest, registry))
	require.Equal(t, InGame, l.state)

	require.ErrorContains(t, l.ToggleReady(l.Leader(), registry), "already in progress",
		"an unfinished game holds the table")

	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	engine.WithState(func(state *game.State) { state.Phase = game.Finished })

	require.NoError(t, l.ToggleReady(l.Leader(), registry))
	l.mu.RLock()
	defer l.mu.RUnlock()
	assert.Nil(t, l.activeEngine, "the finished engine is released")
	assert.True(t, l.ready["p1"], "and the toggle that noticed applies to the next hand")
}

// startGameLocked has to accept a table that is exactly full: refusing it would make the
// last seat unusable in every game.
func TestLobby_StartsWithExactlyMaxPlayers(t *testing.T) {
	t.Parallel()
	m, l, registry := newTestLobby(t, 3)
	guests := []*game.Player{mockPlayer("p2", testutil.UID(2)), mockPlayer("p3", testutil.UID(3))}
	for _, g := range guests {
		require.NoError(t, m.JoinLobbyByCode(l.Code(), g))
	}

	require.NoError(t, l.ToggleReady(l.Leader(), registry))
	for i, g := range guests {
		err := l.ToggleReady(g, registry)
		require.NoErrorf(t, err, "guest %d", i)
	}

	assert.Equal(t, InGame, l.state, "a full table is a startable table")
}

// recordFinishedMatch is the last step before a result exists at all, so both branches have
// to report failure rather than swallow it: a silent error loses the match.
func TestLobby_RecordFinishedMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ranked  bool
		setup   func(*MockMatchRepo)
		wantErr string
	}{
		{
			name:   "ranked success",
			ranked: true,
			setup: func(r *MockMatchRepo) {
				r.On("FinalizeRankedMatch", mock.Anything, gameRef("Mock"), []uuid.UUID{testutil.UID(1)}, mock.Anything).Return(nil)
			},
		},
		{
			name:   "ranked failure is reported",
			ranked: true,
			setup: func(r *MockMatchRepo) {
				r.On("FinalizeRankedMatch", mock.Anything, gameRef("Mock"), []uuid.UUID{testutil.UID(1)}, mock.Anything).Return(assert.AnError)
			},
			wantErr: "finalize ranked match",
		},
		{
			name: "casual success",
			setup: func(r *MockMatchRepo) {
				r.On("RecordCasualMatch", mock.Anything, gameRef("Mock"), []uuid.UUID{testutil.UID(1)}).Return(nil)
			},
		},
		{
			name: "casual failure is reported",
			setup: func(r *MockMatchRepo) {
				r.On("RecordCasualMatch", mock.Anything, gameRef("Mock"), []uuid.UUID{testutil.UID(1)}).Return(assert.AnError)
			},
			wantErr: "record casual match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := new(MockMatchRepo)
			tt.setup(repo)
			m := newTestManager(t, repo)

			err := m.recordFinishedMatch(context.Background(), gameRef("Mock"), []uuid.UUID{testutil.UID(1)}, nil, tt.ranked)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			repo.AssertExpectations(t)
		})
	}
}

// Unsubscribe is what a view calls when it navigates away.
func TestLobby_UnsubscribeClosesTheChannel(t *testing.T) {
	t.Parallel()
	_, l, _ := newTestLobby(t, 4)

	ch, err := l.Subscribe("p1")
	require.NoError(t, err)

	l.Unsubscribe("p1", ch)

	select {
	case _, ok := <-ch:
		assert.False(t, ok, "the channel must be closed")
	case <-time.After(time.Second):
		t.Fatal("Unsubscribe left the channel open")
	}

	l.mu.RLock()
	defer l.mu.RUnlock()
	assert.NotContains(t, l.playerSubs, "p1", "and the player must no longer be tracked")
}

// A failed write is the one outcome nobody else will notice: the players see a
// finished game either way, so the log line is the only signal the result was lost.
//
//nolint:paralleltest // slog.SetDefault is process-wide, so this cannot share the process
func TestLobby_FailedMatchWriteIsLoggedLoudly(t *testing.T) {
	var logged bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(original) })

	repo := new(MockMatchRepo)
	repo.On("FinalizeRankedMatch", mock.Anything, gameRef("Mock"), []uuid.UUID{testutil.UID(1)}, mock.Anything).Return(assert.AnError)
	m := newTestManager(t, repo)
	l, err := m.New(mockPlayer("p1", testutil.UID(1)), WithCardGame("Mock"), WithRanked(true))
	require.NoError(t, err)

	engine := game.NewEngine(&stubRules{}, []*game.Player{mockPlayer("p1", testutil.UID(1))}, deck.StandardDeck())
	t.Cleanup(engine.Close)

	l.requestFinalize(engine, game.EndReasonWin, finalizeRequest{
		lobbyCode: l.code,
		game:      gameRef("Mock"),
		isRanked:  true,
		startedAt: time.Now(),
	})

	assert.Contains(t, logged.String(), "failed to record finished match",
		"a lost match result has to be shouted about")
	repo.AssertExpectations(t)
}

// stubRules is the smallest Rules that lets an engine hand back standings; the
// finalize path only needs Standings to resolve.
type stubRules struct{}

func (stubRules) MinPlayers() int                               { return 1 }
func (stubRules) MaxPlayers() int                               { return 9 }
func (stubRules) InitialDeck() []deck.Card                      { return deck.StandardDeck() }
func (stubRules) InitialDealCount() int                         { return 1 }
func (stubRules) OnGameStart(*game.State) error                 { return nil }
func (stubRules) ValidateAction(*game.State, game.Action) error { return nil }
func (stubRules) ApplyAction(*game.State, game.Action) error    { return nil }
func (stubRules) AfterAction(*game.State, game.Action) error    { return nil }
func (stubRules) CheckWinCondition(*game.State) bool            { return false }
func (stubRules) Standings(s *game.State) []*game.Player        { return s.Players }

// A match that ends has to reopen the table by itself.
func TestLobby_FinishedGameReopensTheTableForSettings(t *testing.T) {
	t.Parallel()
	m, l, registry := newTestLobby(t, 2)
	leader := l.Leader()
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, m.JoinLobbyByCode(l.Code(), guest))

	require.NoError(t, l.ToggleReady(leader, registry))
	require.NoError(t, l.ToggleReady(guest, registry))
	require.Equal(t, InGame, l.state)
	require.ErrorContains(t, l.SetRanked(leader, true), "in progress",
		"settings are correctly refused while a game is actually running")

	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	engine.WithState(func(state *game.State) { state.Phase = game.Finished })
	l.releaseFinishedGame()

	assert.True(t, l.IsWaiting(), "the table is open again")
	require.NoError(t, l.SetRanked(leader, true), "and the leader can change settings")
	assert.True(t, l.IsRanked())

	l.mu.RLock()
	defer l.mu.RUnlock()
	assert.Nil(t, l.activeEngine, "the finished engine is released")
	assert.Empty(t, l.ready, "and nobody carries a stale ready flag into the next game")
}

// The player left holding a lobby after everyone else walked out is its leader, and a
// leader who cannot change anything is indistinguishable from a broken screen.
func TestLobby_InheritedLeaderCanChangeSettingsAfterTheGameEnds(t *testing.T) {
	t.Parallel()
	m, l, registry := newTestLobby(t, 2)
	original := l.Leader()
	inheritor := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, m.JoinLobbyByCode(l.Code(), inheritor))

	require.NoError(t, l.ToggleReady(original, registry))
	require.NoError(t, l.ToggleReady(inheritor, registry))
	require.Equal(t, InGame, l.state)

	// The original leader walks out mid-match, which both promotes the guest and,
	// leaving one player, finishes the game.
	m.LeaveLobby(original)
	require.Equal(t, inheritor, l.Leader(), "the remaining player inherits the lobby")

	l.releaseFinishedGame()

	require.NoError(t, l.SetPrivate(inheritor, false), "the new leader owns the settings")
	require.NoError(t, l.SetMaxPlayers(inheritor, 4, 2, 9))
	assert.Equal(t, 4, l.MaxPlayers())
}

// Releasing is only for a match that is actually over, and running twice must not disturb a
// table that has already reopened.
func TestLobby_ReleaseFinishedGameIsANoOpOtherwise(t *testing.T) {
	t.Parallel()
	m, l, registry := newTestLobby(t, 2)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, m.JoinLobbyByCode(l.Code(), guest))

	require.NotPanics(t, l.releaseFinishedGame)
	assert.True(t, l.IsWaiting(), "a lobby that never started is left alone")

	require.NoError(t, l.ToggleReady(l.Leader(), registry))
	require.NoError(t, l.ToggleReady(guest, registry))
	l.releaseFinishedGame()
	assert.Equal(t, InGame, l.state, "a game still being played is not released")

	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	engine.WithState(func(state *game.State) { state.Phase = game.Finished })

	l.releaseFinishedGame()
	require.NotPanics(t, l.releaseFinishedGame)
	assert.True(t, l.IsWaiting())
}

// The watcher runs after the game ends, by which point the lobby may have reopened
// and been reconfigured. The finished match must still be recorded as the game that
// was actually played: snapshotting at finalize time wrote a ranked result to the
// casual path, or onto another game's ladder entirely.
func TestLobby_FinalizeUsesTheSettingsTheGameStartedWith(t *testing.T) {
	t.Parallel()
	repo := new(MockMatchRepo)
	done := make(chan struct{})
	repo.On("FinalizeRankedMatch", mock.Anything, gameRef("Mock"), []uuid.UUID{testutil.UID(1)}, mock.Anything).
		Return(nil).Run(func(mock.Arguments) { close(done) })

	m := newTestManager(t, repo)
	leader := mockPlayer("p1", testutil.UID(1))
	l, err := m.New(leader, WithCardGame("Mock"), WithRanked(true))
	require.NoError(t, err)

	registry := game.NewRegistry()
	registerGame(registry, "Mock", stubRules{})
	require.NoError(t, l.ToggleReady(leader, registry))

	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	require.NotNil(t, engine)
	t.Cleanup(engine.Close)

	// The table reopens and is reconfigured while the finished game is still on its
	// way to the watcher.
	l.mu.Lock()
	l.options.isRanked = false
	l.options.cardGame = "Something Else"
	l.mu.Unlock()

	engine.WithState(func(state *game.State) { state.Phase = game.Finished })
	engine.Broadcaster().Broadcast(game.Event{Type: game.EventGameEnded, Reason: game.EndReasonWin})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the match was never finalized as the ranked Mock game it was")
	}
	repo.AssertExpectations(t)
	repo.AssertNotCalled(t, "RecordCasualMatch", mock.Anything, mock.Anything, mock.Anything)
}
