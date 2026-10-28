package lobby

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/broadcaster"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/elo"
	"github.com/Pieczasz/tty-cards/internal/game"

	"uuid"

	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// gameRegistry is a registry of rules under one game, seating 1 to 9. Its slug, the
// key a table is opened with, is name in lower case.
func gameRegistry(name string, rules game.Rules) *game.Registry {
	return boundedRegistry(name, rules, 1, 9)
}

// boundedRegistry is gameRegistry for a game seating minP to maxP.
func boundedRegistry(name string, rules game.Rules, minP, maxP int) *game.Registry {
	return game.NewRegistry(game.Module{
		Name:       name,
		Slug:       strings.ToLower(name),
		MinPlayers: minP,
		MaxPlayers: maxP,
		Factory:    func() game.Rules { return rules },
	})
}

type MockRules struct {
	mock.Mock
}

func (m *MockRules) OnGameStart(state *game.State) error { return m.Called(state).Error(0) }
func (m *MockRules) ValidateAction(state *game.State, action game.Action) error {
	return m.Called(state, action).Error(0)
}
func (m *MockRules) ApplyAction(state *game.State, action game.Action) error {
	m.Called(state, action)
	return nil
}
func (m *MockRules) CheckWinCondition(state *game.State) bool { return m.Called(state).Bool(0) }
func (m *MockRules) TimeoutAction(state *game.State) game.Action {
	action, _ := m.Called(state).Get(0).(game.Action)
	return action
}

// Standings takes the order a test expects as players, one place each.
func (m *MockRules) Standings(state *game.State) []game.Standing {
	return ranked(m.Called(state).Get(0).([]*game.Player))
}

// ranked places players one each, in the order given: a ranking with no draws.
func ranked(players []*game.Player) []game.Standing {
	out := make([]game.Standing, len(players))
	for i, p := range players {
		out[i] = game.Standing{Player: p, Place: i + 1}
	}
	return out
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
) (map[uuid.UUID]uint32, error) {
	return finalized(m.Called(ctx, ref, orderedUserIDs, places))
}

func (m *MockMatchRepo) FinalizeInterruptedMatch(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID, places []int, leavers []uuid.UUID,
) (map[uuid.UUID]uint32, error) {
	return finalized(m.Called(ctx, ref, orderedUserIDs, places, leavers))
}

// finalized reads a finalize expectation: Return(err) for a test that does not care
// what was written, or Return(ratings, err) for one that does.
func finalized(args mock.Arguments) (map[uuid.UUID]uint32, error) {
	if len(args) == 1 {
		return nil, args.Error(0)
	}
	ratings, _ := args.Get(0).(map[uuid.UUID]uint32)
	return ratings, args.Error(1)
}

// gameRef mirrors what registerGame put in the registry: the persisted key is the
// slug, so an expectation written against the display name would pass while the row
// was being written under a different identity.
func gameRef(name string) db.GameRef {
	return db.GameRef{Slug: strings.ToLower(name), Name: name}
}

// testGames is what newTestManager opens tables for: every game these tests name,
// dealt by stubRules and seating 1 to 10, so a table's size is the test's to choose.
// Each slug is the name in lower case, as with gameRegistry.
var testGames = func() *game.Registry {
	names := []string{
		"TestGame", "TestGame2", "Game", "Mock", "Mock2", "MockGame",
		"CrazyEights", "Poker", "Uno", "Hidden",
	}
	mods := make([]game.Module, len(names))
	for i, name := range names {
		mods[i] = game.Module{
			Name: name, Slug: strings.ToLower(name), MinPlayers: 1, MaxPlayers: 10,
			Factory: func() game.Rules { return stubRules{} },
		}
	}
	return game.NewRegistry(mods...)
}()

// newTestManager is newManagerFor over testGames.
func newTestManager(tb testing.TB, repo db.MatchRepository) *Manager {
	tb.Helper()
	return newManagerFor(tb, repo, testGames)
}

// newManagerFor is NewManager with teardown. A started game parks a watcher
// goroutine on the engine feed, and closing the engine - which only RemoveLobby does -
// is what ends it. Production always gets there; a test that drops the manager on the
// floor would strand the goroutine and trip goleak.
func newManagerFor(tb testing.TB, repo db.MatchRepository, games *game.Registry) *Manager {
	tb.Helper()
	m := NewManager(tb.Context(), repo, games)
	tb.Cleanup(func() {
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
	mockRules := new(MockRules)
	m := newManagerFor(t, testutil.NopMatches{}, boundedRegistry("MockGame", mockRules, 2, 4))
	leader := mockPlayer("p1", testutil.UID(1))
	guest := mockPlayer("p2", testutil.UID(2))

	cardGame := "mockgame"
	l, err := m.CreateLobby(leader, WithMaxPlayers(4), WithCardGame(cardGame))
	require.NoError(t, err)
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	mockRules.On("OnGameStart", mock.Anything).Return(nil)

	err = m.ToggleReady(leader)
	require.NoError(t, err)
	assert.True(t, l.Snapshot().Leader.Ready)
	assert.Equal(t, waiting, l.state)

	err = m.ToggleReady(guest)
	require.NoError(t, err)

	assert.Equal(t, inGame, l.state)
	assert.NotNil(t, l.activeEngine)

	mockRules.AssertExpectations(t)
}

func TestLobby_SettersAndGetters(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, testutil.NopMatches{})
	leader := mockPlayer("p1", testutil.UID(1))

	cardGame := "crazyeights"
	l, err := m.CreateLobby(leader, WithCardGame(cardGame), WithPrivate(true), WithRanked(true))
	require.NoError(t, err)

	assert.Equal(t, cardGame, l.options.cardGame)
	assert.True(t, l.Snapshot().Private)
	assert.True(t, l.Snapshot().Ranked)

	require.NoError(t, l.SetPrivate(leader, false))
	assert.False(t, l.Snapshot().Private)

	require.NoError(t, l.SetRanked(leader, false))
	assert.False(t, l.Snapshot().Ranked)

	require.NoError(t, l.SetMaxPlayers(leader, 5))
	assert.Equal(t, 5, l.Snapshot().Limit)
}

func TestLobby_DefaultCasual(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, testutil.NopMatches{})
	leader := mockPlayer("leader", testutil.UID(1))
	l, err := m.CreateLobby(leader, WithCardGame("testgame"))
	require.NoError(t, err)
	assert.False(t, l.Snapshot().Ranked, "new lobbies default to casual to limit Elo farming under open registration")
}

func TestLobby_BasicGetters(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, testutil.NopMatches{})
	leader := mockPlayer("leader", testutil.UID(1))

	cardGame := "crazyeights"
	l, err := m.CreateLobby(leader, WithCardGame(cardGame), WithPrivate(true), WithMaxPlayers(4))
	require.NoError(t, err)

	assert.Equal(t, cardGame, l.Snapshot().Slug)
	assert.Equal(t, "CrazyEights", l.Snapshot().GameName, "the name is the registry's, for display")
	assert.Len(t, l.Code(), 8)
	assert.NotNil(t, l.broadcaster)
	assert.Empty(t, l.Snapshot().Guests)
	assert.Equal(t, 4, l.Snapshot().Limit)
	assert.True(t, l.Snapshot().Private)

	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), mockPlayer("guest", testutil.UID(2)))))
	assert.Len(t, l.Snapshot().Guests, 1)
	assert.Nil(t, l.ActiveGame(), "a waiting table has no game to land in")
}

func TestLobby_StartGameAndBroadcasterEvents(t *testing.T) {
	t.Parallel()
	mockRepo := new(MockMatchRepo)
	mockRules := new(MockRules)
	m := newManagerFor(t, mockRepo, boundedRegistry("MockGame", mockRules, 2, 4))
	leader := mockPlayer("leader", testutil.UID(1))
	guest := mockPlayer("guest", testutil.UID(2))

	cardGame := "mockgame"
	l, err := m.CreateLobby(leader, WithMaxPlayers(2), WithCardGame(cardGame), WithRanked(true))
	require.NoError(t, err)
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	mockRules.On("OnGameStart", mock.Anything).Return(nil)
	mockRules.On("CheckWinCondition", mock.Anything).Return(true) // Immediate win to end game
	acceptAnyAction(mockRules)
	mockRules.On("Standings", mock.Anything).Return([]*game.Player{leader, guest})

	done := make(chan struct{})
	mockRepo.On("FinalizeRankedMatch", mock.Anything, gameRef("MockGame"), []uuid.UUID{testutil.UID(1), testutil.UID(2)}, mock.Anything).
		Run(func(mock.Arguments) {
			close(done)
		}).
		Return(nil)

	ch, subErr := l.broadcaster.Subscribe()
	require.NoError(t, subErr)

	err = m.ToggleReady(leader)
	require.NoError(t, err)
	err = m.ToggleReady(guest)
	require.NoError(t, err)

	// Game is started, let's trigger GameEnded event directly to test
	// handleBroadcasterEvents. Read under the lock: the watcher releases a finished
	// game from its own goroutine, so activeEngine has a concurrent writer.
	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	require.NotNil(t, engine)

	require.NoError(t, endHand(engine, stubAction("move")))

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ranked finalize")
	}

	l.broadcaster.Unsubscribe(ch)
	mockRepo.AssertExpectations(t)
}

// A casual game is still a game the players want to find in their history, so it is
// recorded; only the Elo write is reserved for ranked lobbies.
func TestLobby_CasualGameIsRecordedWithoutElo(t *testing.T) {
	t.Parallel()
	mockRepo := new(MockMatchRepo)
	mockRules := new(MockRules)
	m := newManagerFor(t, mockRepo, boundedRegistry("MockGame", mockRules, 2, 4))
	leader := mockPlayer("leader", testutil.UID(1))
	guest := mockPlayer("guest", testutil.UID(2))

	cardGame := "mockgame"
	l, err := m.CreateLobby(leader, WithMaxPlayers(2), WithCardGame(cardGame), WithRanked(false))
	require.NoError(t, err)
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	mockRules.On("OnGameStart", mock.Anything).Return(nil)
	mockRules.On("CheckWinCondition", mock.Anything).Return(true)
	mockRules.On("Standings", mock.Anything).Return([]*game.Player{leader, guest})
	acceptAnyAction(mockRules)

	done := make(chan struct{})
	mockRepo.On("RecordCasualMatch", mock.Anything, gameRef("MockGame"), []uuid.UUID{testutil.UID(1), testutil.UID(2)}).
		Run(func(mock.Arguments) { close(done) }).
		Return(nil)

	require.NoError(t, m.ToggleReady(leader))
	require.NoError(t, m.ToggleReady(guest))

	engine := l.activeEngine
	require.NotNil(t, engine)
	require.NoError(t, endHand(engine, stubAction("move")))

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
	m := newTestManager(t, testutil.NopMatches{})
	leader := mockPlayer("p5", testutil.UID(5))
	guest := mockPlayer("p6", testutil.UID(6))
	nobody := mockPlayer("p3", testutil.UID(3))

	l, err := m.CreateLobby(leader, WithCardGame("mock2"))
	require.NoError(t, err)
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	require.NoError(t, m.ToggleReady(leader))
	require.NoError(t, m.ToggleReady(guest)) // Starts game!

	require.ErrorIs(t, m.ToggleReady(leader), errGameInProgress)

	// A player seated nowhere has no table to be ready at.
	require.ErrorIs(t, m.ToggleReady(nobody), errNotInLobby)

	// Removing a player who is in no lobby is a no-op and closes nothing.
	m.LeaveLobby(nobody)
	_, err = m.FindLobbyByCode(l.Code())
	require.NoError(t, err)
}

// newTestLobby is a table of stubRules under "Mock", seating 2 to maxPlayers, for
// tests that care about lobby bookkeeping rather than the game.
func newTestLobby(t *testing.T, maxPlayers int) (*Manager, *Lobby) {
	t.Helper()
	m := newManagerFor(t, testutil.NopMatches{}, boundedRegistry("Mock", stubRules{}, 2, maxPlayers))
	leader := mockPlayer("p1", testutil.UID(1))

	l, err := m.CreateLobby(leader, WithMaxPlayers(maxPlayers), WithCardGame("mock"))
	require.NoError(t, err)
	return m, l
}

// drainEventTypes collects whatever is already queued. Broadcast happens under the
// lobby lock and returns before the call that caused it, so anything that is going to
// arrive has arrived by the time the caller gets control back.
func drainEventTypes(ch <-chan Event) []EventType {
	var types []EventType
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
		m, l := newTestLobby(t, 4)
		ch, err := l.Subscribe("p1")
		require.NoError(t, err)

		require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), mockPlayer("p2", testutil.UID(2)))))

		assert.Equal(t, []EventType{EventPlayersUpdated}, drainEventTypes(ch))
	})

	t.Run("a guest leaving", func(t *testing.T) {
		t.Parallel()
		m, l := newTestLobby(t, 4)
		guest := mockPlayer("p2", testutil.UID(2))
		require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

		ch, err := l.Subscribe("p1")
		require.NoError(t, err)
		m.LeaveLobby(guest)

		assert.Equal(t, []EventType{EventPlayersUpdated}, drainEventTypes(ch))
	})

	t.Run("the leader leaving an empty lobby closes it", func(t *testing.T) {
		t.Parallel()
		m, l := newTestLobby(t, 4)
		// Not l.Subscribe: a leaving player's own subscription is closed before the
		// event goes out, so only an observer that is not the leaver can see it.
		observer, err := l.broadcaster.Subscribe()
		require.NoError(t, err)
		own, err := l.Subscribe("p1")
		require.NoError(t, err)

		m.LeaveLobby(l.Snapshot().Leader.Player)

		_, err = m.FindLobbyByCode(l.Code())
		require.Error(t, err, "the last player out closes the lobby")
		assert.Equal(t, []EventType{EventLobbyClosed}, drainEventTypes(observer))
		assert.Empty(t, drainEventTypes(own), "the leaver's own stream is already closed")
	})

	t.Run("a settings change", func(t *testing.T) {
		t.Parallel()
		_, l := newTestLobby(t, 4)
		ch, err := l.Subscribe("p1")
		require.NoError(t, err)

		require.NoError(t, l.SetPrivate(l.Snapshot().Leader.Player, false))

		assert.Equal(t, []EventType{EventSettingsUpdated, EventPlayersUpdated}, drainEventTypes(ch),
			"the change un-readies the table, so the rosters hear it too")
	})

	t.Run("a rejected settings change is not announced", func(t *testing.T) {
		t.Parallel()
		_, l := newTestLobby(t, 4)
		ch, err := l.Subscribe("p1")
		require.NoError(t, err)

		require.Error(t, l.SetMaxPlayers(l.Snapshot().Leader.Player, 0))

		assert.Empty(t, drainEventTypes(ch), "nothing changed, so there is nothing to publish")
	})
}

// SetMaxPlayers is the one setting that can contradict reality: it must not be allowed to
// drop below the players already seated, or outside what the game itself deals. The
// game's bounds are the table's own, never the caller's.
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			start := cmp.Or(tt.rulesMax, 9)
			m := newManagerFor(t, testutil.NopMatches{}, boundedRegistry("Mock", stubRules{}, cmp.Or(tt.rulesMin, 1), start))
			roster := testutil.Players(tt.guests + 1)
			l, err := m.CreateLobby(roster[0], WithCardGame("mock"), WithMaxPlayers(start))
			require.NoError(t, err)
			for _, g := range roster[1:] {
				require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), g)))
			}

			err = l.SetMaxPlayers(l.Snapshot().Leader.Player, tt.limit)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Equal(t, start, l.Snapshot().Limit, "a refused change must not be applied")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.limit, l.Snapshot().Limit)
		})
	}
}

// A player who leaves must stop receiving lobby events.
func TestLobby_LeavingUnsubscribesThePlayer(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 4)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

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
	assert.Equal(t, []EventType{EventPlayersUpdated}, drainEventTypes(leaderCh), "everybody else still hears about it")
}

// A lobby whose broadcaster has been torn down by RemoveLobby is still reachable from any
// session that was holding it, so every path that publishes has to tolerate that.
func TestLobby_SurvivesATornDownBroadcaster(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 4)

	m.RemoveLobby(l.Code())

	require.NotPanics(t, func() {
		l.mu.Lock()
		l.broadcastLocked(Event{Type: EventPlayersUpdated})
		l.unsubscribePlayerLocked("p1")
		l.mu.Unlock()

		assert.NoError(t, l.SetPrivate(l.Snapshot().Leader.Player, true))
		l.Unsubscribe("p1", make(chan Event))
	})

	_, err := l.Subscribe("p1")
	require.ErrorIs(t, err, errLobbyClosed)
}

// A table can be re-used for a second hand once the first is over, but never while one is
// still running.
func TestLobby_ToggleReadyAfterAFinishedGame(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 2)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	require.NoError(t, m.ToggleReady(l.Snapshot().Leader.Player))
	require.NoError(t, m.ToggleReady(guest))
	require.Equal(t, inGame, l.state)

	require.ErrorIs(t, m.ToggleReady(l.Snapshot().Leader.Player), errGameInProgress,
		"an unfinished game holds the table")

	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	engine.WithState(func(state *game.State) { state.Phase = game.Finished })

	require.NoError(t, m.ToggleReady(l.Snapshot().Leader.Player))
	l.mu.RLock()
	defer l.mu.RUnlock()
	assert.Nil(t, l.activeEngine, "the finished engine is released")
	assert.True(t, l.ready["p1"], "and the toggle that noticed applies to the next hand")
}

// startGameLocked has to accept a table that is exactly full: refusing it would make the
// last seat unusable in every game.
func TestLobby_StartsWithExactlyMaxPlayers(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 3)
	guests := []*game.Player{mockPlayer("p2", testutil.UID(2)), mockPlayer("p3", testutil.UID(3))}
	for _, g := range guests {
		require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), g)))
	}

	require.NoError(t, m.ToggleReady(l.Snapshot().Leader.Player))
	for i, g := range guests {
		err := m.ToggleReady(g)
		require.NoErrorf(t, err, "guest %d", i)
	}

	assert.Equal(t, inGame, l.state, "a full table is a startable table")
}

// recordFinishedMatch is the last step before a result exists at all, so every branch has
// to report failure rather than swallow it: a silent error loses the match. The error is
// the repository's own, which already names the write: wrapping it again logged
// "finalize ranked match: finalize ranked match: ...".
func TestLobby_RecordFinishedMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ranked  bool
		reason  game.EndReason
		setup   func(*MockMatchRepo)
		wantErr bool
	}{
		{
			name:   "interrupted failure is reported",
			ranked: true,
			reason: game.EndReasonInterrupted,
			setup: func(r *MockMatchRepo) {
				r.On("FinalizeInterruptedMatch", mock.Anything, gameRef("Mock"), []uuid.UUID{testutil.UID(1)},
					mock.Anything, mock.Anything).Return(assert.AnError)
			},
			wantErr: true,
		},
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
			wantErr: true,
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
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := new(MockMatchRepo)
			tt.setup(repo)
			m := newTestManager(t, repo)

			_, err := m.recordFinishedMatch(t.Context(), matchResult{
				ref:         gameRef("Mock"),
				userIDs:     []uuid.UUID{testutil.UID(1)},
				interrupted: tt.reason == game.EndReasonInterrupted,
			}, tt.ranked)

			if tt.wantErr {
				require.ErrorIs(t, err, assert.AnError)
				assert.Equal(t, assert.AnError.Error(), err.Error(), "the repository error is not wrapped again")
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
	_, l := newTestLobby(t, 4)

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

// A reconnect briefly holds two feeds. The old view closing its own must leave the new
// one tracked, or a later leave would not close it.
func TestLobby_UnsubscribeKeepsThePlayersOtherFeed(t *testing.T) {
	t.Parallel()
	_, l := newTestLobby(t, 4)
	old, err := l.Subscribe("p1")
	require.NoError(t, err)
	current, err := l.Subscribe("p1")
	require.NoError(t, err)

	l.Unsubscribe("p1", old)

	l.mu.RLock()
	defer l.mu.RUnlock()
	assert.Equal(t, []<-chan Event{current}, l.playerSubs["p1"])
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
	l, err := m.CreateLobby(mockPlayer("p1", testutil.UID(1)), WithCardGame("mock"), WithRanked(true))
	require.NoError(t, err)

	engine := game.NewEngine(&stubRules{}, []*game.Player{mockPlayer("p1", testutil.UID(1))})
	t.Cleanup(engine.Close)

	m.requestFinalize(l, engine, game.EndReasonWin, finalizeRequest{
		lobbyCode: l.code,
		game:      gameRef("Mock"),
		isRanked:  true,
		startedAt: time.Now(),
	})

	assert.Contains(t, logged.String(), "record finished match",
		"a lost match result has to be shouted about")
	repo.AssertExpectations(t)
}

// stubRules is the smallest Rules that lets an engine start and hand back standings,
// the seats in order.
type stubRules struct{}

func (stubRules) OnGameStart(*game.State) error                 { return nil }
func (stubRules) ValidateAction(*game.State, game.Action) error { return nil }
func (stubRules) CheckWinCondition(s *game.State) bool          { return s.Extra == stubWin }
func (stubRules) Standings(s *game.State) []game.Standing {
	left := slices.Clone(s.LeftPlayers)
	slices.Reverse(left)
	return ranked(slices.Concat(s.Players, left))
}

// TimeoutAction passes, so an expired turn moves on.
func (stubRules) TimeoutAction(*game.State) game.Action { return stubAction("pass") }

func (stubRules) ApplyAction(s *game.State, a game.Action) error {
	switch a {
	case stubBoom:
		return errors.New("boom")
	case stubWin:
		s.Extra = stubWin
	}
	return nil
}

// stubAction is a move for stubRules: stubWin ends the hand as a win, stubBoom fails
// it as a rules error, anything else passes the turn.
type stubAction string

func (a stubAction) Name() string { return string(a) }

const (
	stubWin  stubAction = "win"
	stubBoom stubAction = "boom"
)

// endHand plays a on the seat to act, which is how these tests finish a real hand.
// The error is dropped: stubBoom returns one by design, and a finished or closed
// engine refusing the move is what the racing tests are about.
// endHand submits a as the seat on turn. The error is the caller's to assert: a hand
// that was meant to end and was refused would otherwise leave the test waiting on a
// result that is never written.
func endHand(engine *game.Engine, a game.Action) error {
	return engine.SubmitAction(engine.CurrentPlayerID(), a)
}

// acceptAnyAction lets a MockRules table take any move, so a test can end the hand
// through CheckWinCondition.
func acceptAnyAction(m *MockRules) {
	m.On("ValidateAction", mock.Anything, mock.Anything).Return(nil)
	m.On("ApplyAction", mock.Anything, mock.Anything)
}

// A match that ends has to reopen the table by itself.
func TestLobby_FinishedGameReopensTheTableForSettings(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 2)
	leader := l.Snapshot().Leader.Player
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	require.NoError(t, m.ToggleReady(leader))
	require.NoError(t, m.ToggleReady(guest))
	require.Equal(t, inGame, l.state)
	require.ErrorIs(t, l.SetRanked(leader, true), errSettingsLocked,
		"settings are correctly refused while a game is actually running")

	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	engine.WithState(func(state *game.State) { state.Phase = game.Finished })
	m.releaseFinishedGame(l)

	assert.True(t, isWaiting(l), "the table is open again")
	require.NoError(t, l.SetRanked(leader, true), "and the leader can change settings")
	assert.True(t, l.Snapshot().Ranked)

	l.mu.RLock()
	defer l.mu.RUnlock()
	assert.Nil(t, l.activeEngine, "the finished engine is released")
	assert.Empty(t, l.ready, "and nobody carries a stale ready flag into the next game")
}

// The player left holding a lobby after everyone else walked out is its leader, and a
// leader who cannot change anything is indistinguishable from a broken screen.
func TestLobby_InheritedLeaderCanChangeSettingsAfterTheGameEnds(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 4)
	original := l.Snapshot().Leader.Player
	inheritor := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), inheritor)))

	require.NoError(t, m.ToggleReady(original))
	require.NoError(t, m.ToggleReady(inheritor))
	require.Equal(t, inGame, l.state)

	// The original leader walks out mid-match, which both promotes the guest and,
	// leaving one player, finishes the game.
	m.LeaveLobby(original)
	require.Equal(t, inheritor, l.Snapshot().Leader.Player, "the remaining player inherits the lobby")

	m.releaseFinishedGame(l)

	require.NoError(t, l.SetPrivate(inheritor, false), "the new leader owns the settings")
	require.NoError(t, l.SetMaxPlayers(inheritor, 3))
	assert.Equal(t, 3, l.Snapshot().Limit)
}

// Releasing is only for a match that is actually over, and running twice must not disturb a
// table that has already reopened.
func TestLobby_ReleaseFinishedGameIsANoOpOtherwise(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 2)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	require.NotPanics(t, func() { m.releaseFinishedGame(l) })
	assert.True(t, isWaiting(l), "a lobby that never started is left alone")

	require.NoError(t, m.ToggleReady(l.Snapshot().Leader.Player))
	require.NoError(t, m.ToggleReady(guest))
	m.releaseFinishedGame(l)
	assert.Equal(t, inGame, l.state, "a game still being played is not released")

	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	engine.WithState(func(state *game.State) { state.Phase = game.Finished })

	m.releaseFinishedGame(l)
	require.NotPanics(t, func() { m.releaseFinishedGame(l) })
	assert.True(t, isWaiting(l))
}

// The watcher runs after the game ends, by which point the lobby may have reopened
// and been reconfigured. The finished match must still be recorded as the game that
// was actually played: snapshotting at finalize time wrote a ranked result to the
// casual path.
func TestLobby_FinalizeUsesTheSettingsTheGameStartedWith(t *testing.T) {
	t.Parallel()
	repo := new(MockMatchRepo)
	done := make(chan struct{})
	repo.On("FinalizeRankedMatch", mock.Anything, gameRef("Mock"), []uuid.UUID{testutil.UID(1)}, mock.Anything).
		Return(nil).Run(func(mock.Arguments) { close(done) })

	m := newTestManager(t, repo)
	leader := mockPlayer("p1", testutil.UID(1))
	l, err := m.CreateLobby(leader, WithCardGame("mock"), WithRanked(true))
	require.NoError(t, err)

	require.NoError(t, m.ToggleReady(leader))

	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	require.NotNil(t, engine)
	t.Cleanup(engine.Close)

	// The table reopens and is reconfigured while the finished game is still on its
	// way to the watcher.
	l.mu.Lock()
	l.options.isRanked = false
	l.mu.Unlock()

	require.NoError(t, endHand(engine, stubWin))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the match was never finalized as the ranked Mock game it was")
	}
	repo.AssertExpectations(t)
	repo.AssertNotCalled(t, "RecordCasualMatch", mock.Anything, mock.Anything, mock.Anything)
}

// A seat reads the same on the roster as the browse matches it: missing and zero are
// both the starting rating, and only the table's own game counts.
func TestRatingFor_ZeroAndMissingReadAsTheStartingRating(t *testing.T) {
	t.Parallel()
	start := elo.ToUint32(elo.DefaultRating)
	assert.Equal(t, start, ratingFor(nil, "poker"))
	assert.Equal(t, start, ratingFor(map[string]uint32{"uno": 1720}, "poker"))
	assert.Equal(t, start, ratingFor(map[string]uint32{"poker": 0}, "poker"))
	assert.Equal(t, uint32(1720), ratingFor(map[string]uint32{"poker": 1720}, "poker"))
}

// refusedStartRules is stubRules whose deal fails, so the engine never starts: a
// defect in the rules, not something the players did.
type refusedStartRules struct{ stubRules }

func (refusedStartRules) OnGameStart(*game.State) error { return errors.New("no deal") }

// A start that fails leaves the table as it was - waiting, with no engine - so the
// players can fix what is wrong and ready again.
func TestLobby_FailedStartKeepsTheTableWaiting(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		rules     game.Rules
		min       int
		withGuest bool
		wantErr   string
		// refused is whether the player caused it: a deal the rules cannot make is a
		// failure the TUI logs, not a refusal it shows as it is.
		refused bool
	}{
		{name: "too few players", rules: stubRules{}, min: 2, wantErr: "need at least 2 players", refused: true},
		{name: "the rules refuse the deal", rules: refusedStartRules{}, withGuest: true, wantErr: "start game engine"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newManagerFor(t, testutil.NopMatches{}, boundedRegistry("Mock", tt.rules, cmp.Or(tt.min, 1), 9))
			seats := []*game.Player{mockPlayer("p1", testutil.UID(1))}
			l, err := m.CreateLobby(seats[0], WithCardGame("mock"))
			require.NoError(t, err)
			if tt.withGuest {
				seats = append(seats, mockPlayer("p2", testutil.UID(2)))
				require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), seats[1])))
			}
			for _, p := range seats[:len(seats)-1] {
				require.NoError(t, m.ToggleReady(p))
			}
			err = m.ToggleReady(seats[len(seats)-1])
			require.ErrorContains(t, err, tt.wantErr)
			assert.Equal(t, tt.refused, errors.Is(err, ErrRefused))

			assert.True(t, isWaiting(l), "a failed start left the table out of Waiting")
			assert.Nil(t, l.ActiveGame())
		})
	}
}

// A leader who leaves alone closes the table, but their screen can still send a
// ready. It must not start a game on a table the manager no longer holds.
func TestLobby_ClosedTableCannotStart(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 2)
	leader := l.Snapshot().Leader.Player
	m.LeaveLobby(leader)

	require.ErrorIs(t, m.ToggleReady(leader), errNotInLobby)
	assert.Nil(t, l.ActiveGame())
}

// A refused settings change must change nothing and announce nothing: every client
// would otherwise redraw a table that is still the same.
func TestLobby_OnlyTheLeaderChangesSettings(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 3)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	events, err := l.Subscribe(l.Snapshot().Leader.Player.ID)
	require.NoError(t, err)

	require.ErrorIs(t, l.SetPrivate(guest, false), errNotLeader)
	require.ErrorIs(t, l.SetRanked(guest, true), errNotLeader)
	require.ErrorIs(t, l.SetMaxPlayers(guest, 2), errNotLeader)
	require.ErrorIs(t, l.SetMaxPlayers(guest, 2), ErrRefused, "wrapped with what was tried, still a refusal")

	assert.True(t, l.Snapshot().Private)
	assert.False(t, l.Snapshot().Ranked)
	assert.Equal(t, 3, l.Snapshot().Limit)
	assert.Empty(t, drainEventTypes(events), "a refused change was broadcast")
}

// The feed has a fixed number of slots. A subscriber past them is told so rather than
// handed a channel that never delivers, and the refusal is not tracked as theirs.
func TestLobby_SubscribePastCapacityIsRefused(t *testing.T) {
	t.Parallel()
	_, l := newTestLobby(t, 2)
	id := l.Snapshot().Leader.Player.ID
	for range maxLobbySubscribers {
		_, err := l.Subscribe(id)
		require.NoError(t, err)
	}

	ch, err := l.Subscribe(id)
	require.ErrorIs(t, err, broadcaster.ErrAtCapacity)
	assert.Nil(t, ch)
	l.mu.RLock()
	defer l.mu.RUnlock()
	assert.Len(t, l.playerSubs[id], maxLobbySubscribers)
}

// JoinLobbyByCode turns a seated player away before reaching the table; this is the
// table's own guard against seating one player twice if that ever slips.
func TestLobby_AddGuestRefusesASeatedPlayer(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 3)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	require.ErrorIs(t, l.addGuest(l.Snapshot().Leader.Player), errAlreadyInLobby)
	require.ErrorIs(t, l.addGuest(guest), errAlreadyInLobby)
	assert.Len(t, l.Snapshot().Guests, 1)
}
