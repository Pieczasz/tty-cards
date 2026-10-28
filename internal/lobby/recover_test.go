package lobby

import (
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestNewManager_ANilMatchRepoIsAWiringBug(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "lobby.NewManager: nil match repository", func() {
		NewManager(t.Context(), nil, testGames)
	})
}

// The watcher is the only goroutine that writes a match, and nothing above it
// recovers: a panic in the repository used to end the process for every table.
func TestWatchGame_APanicInFinalizeIsRecovered(t *testing.T) {
	t.Parallel()
	repo := new(MockMatchRepo)
	repo.On("FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { panic("repository bug") })

	m, _, engine := newFinishedGameLobby(t, repo)
	endHand(engine, stubWin)

	require.True(t, m.WaitForFinalizers(2*time.Second),
		"the recovered watcher must still give its count back to the drain")
	repo.AssertNumberOfCalls(t, "FinalizeRankedMatch", 1)
}

// The helper every lobby goroutine defers; the grace timer and the idle hook use it
// on frames no test can reach without waiting out a real clock.
func TestRecoverGoroutine_StopsAPanicAtTheGoroutineFrame(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, testutil.NopMatches{})
	assert.NotPanics(t, func() {
		func() {
			defer m.recoverGoroutine("test")
			panic("boom")
		}()
	})
}

// A rules set whose factory panics is refused like a failed deal: the table stays
// Waiting with no seat left ready, and the drain's count comes back, or shutdown waits
// out its whole timeout for a game that never existed.
func TestToggleReady_APanickingFactoryIsRefusedAndTheTableRecovers(t *testing.T) {
	t.Parallel()
	reg := game.NewRegistry(game.Module{
		Name: "Boom", Slug: "boom", MinPlayers: 1, MaxPlayers: 2,
		Factory: func() game.Rules { panic("factory bug") },
	})
	m := newManagerFor(t, testutil.NopMatches{}, reg)
	leader := mockPlayer("p1", testutil.UID(1))
	_, err := m.CreateLobby(leader, WithMaxPlayers(1), WithCardGame("boom"))
	require.NoError(t, err)

	require.NotPanics(t, func() { err = m.ToggleReady(leader) })
	require.ErrorContains(t, err, "factory")

	l := m.FindLobbyByPlayer(leader)
	assert.True(t, isWaiting(l))
	assert.Nil(t, l.ActiveGame())
	l.mu.RLock()
	assert.Empty(t, l.ready, "seats were left ready for a game that never started")
	l.mu.RUnlock()
	assert.True(t, m.WaitForFinalizers(time.Second), "a watcher that never started is still counted")
}

// A hand that ends during the drain is a real result. Reopening the table can wait on
// m.mu, so BeginTeardown may land between the end and the write; the decision is the
// one taken at the end.
func TestFinalize_RankedMatchEndedBeforeTeardownStaysRatedWhenTeardownLandsMidFinalize(t *testing.T) {
	t.Parallel()
	repo := new(MockMatchRepo)
	repo.On("FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

	m, l, engine := newFinishedGameLobby(t, repo)
	m.BeginShutdown()

	m.mu.Lock()
	endHand(engine, stubWin)
	// Waiting again means the watcher passed its snapshot and is parked on m.mu.
	require.Eventually(t, func() bool { return isWaiting(l) }, 2*time.Second, time.Millisecond)
	m.BeginTeardown()
	m.mu.Unlock()

	require.True(t, m.WaitForFinalizers(2*time.Second))
	repo.AssertExpectations(t)
	repo.AssertNotCalled(t, "RecordCasualMatch", mock.Anything, mock.Anything, mock.Anything)
}

// Players may still be playing while the server waits for sessions to leave on their
// own. A hand that ends then is a real result; only the forced hang-up after
// BeginTeardown makes the result untrustworthy.
func TestFinalize_RankedMatchEndingBeforeTeardownIsStillRated(t *testing.T) {
	t.Parallel()
	repo := new(MockMatchRepo)
	repo.On("FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()

	m, _, engine := newFinishedGameLobby(t, repo)
	m.BeginShutdown()
	endHand(engine, stubWin)

	require.True(t, m.WaitForFinalizers(2*time.Second))
	repo.AssertExpectations(t)
	repo.AssertNotCalled(t, "RecordCasualMatch", mock.Anything, mock.Anything, mock.Anything)
}
