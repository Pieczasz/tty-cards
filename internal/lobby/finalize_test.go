package lobby

import (
	"bytes"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"

	"github.com/Pieczasz/tty-cards/internal/testutil"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// newFinishedGameLobby is a two-seat ranked lobby whose game ends the moment both
// players are ready, with the watcher goroutine running.
func newFinishedGameLobby(t *testing.T, repo db.MatchRepository) (*Manager, *Lobby, *game.Engine) {
	t.Helper()
	m := newTestManager(t, repo)
	leader := mockPlayer("leader", testutil.UID(1))
	guest := mockPlayer("guest", testutil.UID(2))

	l, err := m.CreateLobby(leader, WithMaxPlayers(2), WithCardGame("mockgame"), WithRanked(true))
	require.NoError(t, err)
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	// The standings are the seats in order: leader, then guest.
	require.NoError(t, m.ToggleReady(leader))
	require.NoError(t, m.ToggleReady(guest))

	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	require.NotNil(t, engine)
	return m, l, engine
}

// A match that ends while the lobby is being torn down must not be recorded twice,
// and the watcher goroutine must not outlive either path.
func TestConcurrent_FinalizeRacesRemoveLobby(t *testing.T) {
	t.Parallel()

	repo := new(MockMatchRepo)
	recorded := make(chan struct{}, 4)
	repo.On("FinalizeRankedMatch", mock.Anything, gameRef("MockGame"), []uuid.UUID{testutil.UID(1), testutil.UID(2)}, mock.Anything).
		Run(func(mock.Arguments) { recorded <- struct{}{} }).
		Return(nil)

	m, l, engine := newFinishedGameLobby(t, repo)
	// The hand really is over before RemoveLobby closes the feed, so the event is
	// already buffered: a closed channel still delivers it, and the watcher must write
	// the rated row once rather than again through the finished engine at feed end.
	endHand(engine, stubWin)
	go m.RemoveLobby(l.Code())

	select {
	case <-recorded:
	case <-time.After(2 * time.Second):
		t.Fatal("the finished match was never recorded")
	}
	// Whichever path lost the race must not write a second row for the same hand.
	require.True(t, m.WaitForFinalizers(2*time.Second), "a finalizer outlived the drain")
	assert.Empty(t, recorded, "the match was recorded more than once")
}

// A single hand produces a single row no matter how many times something tries to end
// it: the engine ends once, the watcher stops reading after the first.
func TestFinalize_IsNotAppliedTwice(t *testing.T) {
	t.Parallel()

	repo := new(MockMatchRepo)
	calls := make(chan struct{}, 4)
	repo.On("FinalizeRankedMatch", mock.Anything, gameRef("MockGame"), []uuid.UUID{testutil.UID(1), testutil.UID(2)}, mock.Anything).
		Run(func(mock.Arguments) { calls <- struct{}{} }).
		Return(nil)

	m, _, engine := newFinishedGameLobby(t, repo)

	endHand(engine, stubWin)
	endHand(engine, stubWin)

	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("the finished match was never recorded")
	}
	require.True(t, m.WaitForFinalizers(2*time.Second))
	assert.Empty(t, calls, "the same hand was persisted twice")
}

// A match ending at the same moment the drain begins used to race it: a finalize
// registered only once the end was observed, so the drain could see nothing in flight
// and refuse the write. The watcher is counted from game start now, so whichever
// lands first, the match is written before the drain returns.
func TestShutdown_MatchEndingDuringDrainIsRecorded(t *testing.T) {
	t.Parallel()
	for i := range 20 {
		var recorded atomic.Int32
		repo := new(MockMatchRepo)
		repo.On("FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(mock.Arguments) { recorded.Add(1) }).Return(nil)
		repo.On("RecordCasualMatch", mock.Anything, mock.Anything, mock.Anything).
			Run(func(mock.Arguments) { recorded.Add(1) }).Return(nil)

		m, l, engine := newFinishedGameLobby(t, repo)

		done := make(chan struct{})
		go func() {
			defer close(done)
			endHand(engine, stubWin)
		}()
		drained := m.WaitForFinalizers(2 * time.Second)
		<-done
		require.True(t, drained, "the drain gave up on iteration %d", i)

		require.Equal(t, int32(1), recorded.Load(), "iteration %d drained without the match", i)

		m.RemoveLobby(l.Code())
	}
}

// A late session teardown can end a table after the drain has begun - after
// BeginShutdown, or even after WaitForFinalizers has started waiting. Its watcher was
// counted when the game started, so the drain waits for it and the match is written,
// unrated, rather than dropped.
func TestShutdown_AGameEndingAfterBeginShutdownIsRecorded(t *testing.T) {
	t.Parallel()
	for _, endsWhileWaiting := range []bool{false, true} {
		t.Run(fmt.Sprintf("ends_while_waiting=%t", endsWhileWaiting), func(t *testing.T) {
			t.Parallel()
			repo := new(MockMatchRepo)
			repo.On("RecordCasualMatch", mock.Anything, gameRef("MockGame"), mock.Anything).Return(nil).Once()
			m, l, _ := newFinishedGameLobby(t, repo)
			guest := l.Snapshot().Guests[0].Player
			m.BeginShutdown()

			if endsWhileWaiting {
				assert.False(t, m.WaitForFinalizers(50*time.Millisecond),
					"the drain gave up on nothing while a game was still in play")
			}
			m.LeaveLobby(guest) // the teardown: the leader is left standing

			require.True(t, m.WaitForFinalizers(2*time.Second))
			repo.AssertExpectations(t)
			repo.AssertNotCalled(t, "FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// No game starts once the drain has begun: it would be cut short by the teardown that
// follows, and nothing would wait for it. Readying up still works; only the start is
// refused, and the table is left waiting.
func TestToggleReady_NoGameStartsWhileTheServerDrains(t *testing.T) {
	t.Parallel()
	m, l := newTestLobby(t, 2)
	leader, guest := l.Snapshot().Leader.Player, mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	require.NoError(t, m.ToggleReady(leader))

	m.BeginShutdown()

	require.ErrorIs(t, m.ToggleReady(guest), errServerRestarting)
	assert.True(t, isWaiting(l), "a game started during the drain")
	assert.Nil(t, l.ActiveGame())
	require.NoError(t, m.ToggleReady(leader), "an unready is not a start")
	assert.True(t, m.WaitForFinalizers(time.Second), "the refused start kept its watcher counted")
}

// syncBuffer is a log sink two goroutines can touch: the finalizer writes to it while
// the assertion polls it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) contains(want string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.buf.Bytes(), []byte(want))
}

// A leader who can kick mid-hand can farm Elo: drop whoever is winning and let the
// engine hand them the rating.
func TestKick_IsRejectedWhileInGame(t *testing.T) {
	t.Parallel()

	m, l := newTestLobby(t, 2)
	leader := l.Snapshot().Leader.Player
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	require.NoError(t, m.ToggleReady(leader))
	require.NoError(t, m.ToggleReady(guest))
	require.Equal(t, inGame, l.state)

	require.ErrorIs(t, m.Kick(leader, guest), errKickInGame)
	assert.True(t, l.HasPlayer(guest), "the target is still at the table")
	assert.Equal(t, l, m.FindLobbyByPlayer(guest), "and still indexed to it")

	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	engine.WithState(func(state *game.State) { state.Phase = game.Finished })
	m.releaseFinishedGame(l)

	require.NoError(t, m.Kick(leader, guest), "and can be kicked once the hand is over")
}

// The ready flip is committed before the start is attempted, so a failed start still
// has to publish it; otherwise every other client shows a stale roster.
func TestToggleReady_FailedStartStillBroadcasts(t *testing.T) {
	t.Parallel()

	m := newManagerFor(t, nil, boundedRegistry("Mock", stubRules{}, 3, 4))
	leader := mockPlayer("p1", testutil.UID(1))
	guest := mockPlayer("p2", testutil.UID(2))
	l, err := m.CreateLobby(leader, WithMaxPlayers(4), WithCardGame("mock"))
	require.NoError(t, err)
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	observer, err := l.Subscribe("observer")
	require.NoError(t, err)

	// The game needs a third player, so the start fails after both flips.
	require.NoError(t, m.ToggleReady(leader))
	require.Error(t, m.ToggleReady(guest))

	assert.Equal(t, []EventType{EventPlayersUpdated, EventPlayersUpdated}, drainEventTypes(observer),
		"the second flip was committed without telling anyone")
	assert.True(t, isReady(l, guest), "and it really was committed")
}

// A drain that times out used to strand its waiter goroutine, one per attempt, on a
// path that is retried by design.
func TestWaitForFinalizers_TimeoutDoesNotLeakItsWaiter(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, nil)
	require.True(t, m.admitGame())

	assert.False(t, m.WaitForFinalizers(10*time.Millisecond), "a game in play blocks the drain")

	m.gateMu.Lock()
	first := m.drained
	m.gateMu.Unlock()

	assert.False(t, m.WaitForFinalizers(10*time.Millisecond))

	m.gateMu.Lock()
	second := m.drained
	m.gateMu.Unlock()
	assert.Equal(t, first, second, "a second attempt started a second waiter")

	// goleak's TestMain is the other half of this: the one waiter has to exit.
	m.watchers.Done()
	assert.True(t, m.WaitForFinalizers(2*time.Second))
}

// A ranked hand that ends while the server shuts down has no honest winner - teardown
// order, not play, decided it - so it is history only.
func TestFinalize_RankedMatchEndingDuringShutdownIsRecordedWithoutElo(t *testing.T) {
	t.Parallel()

	repo := new(MockMatchRepo)
	recorded := make(chan struct{}, 1)
	repo.On("RecordCasualMatch", mock.Anything, gameRef("MockGame"), []uuid.UUID{testutil.UID(1), testutil.UID(2)}).
		Run(func(mock.Arguments) { recorded <- struct{}{} }).
		Return(nil)

	m, _, engine := newFinishedGameLobby(t, repo)
	m.BeginShutdown()

	engine.RemovePlayer("guest") // the last seat standing is a forfeit

	select {
	case <-recorded:
	case <-time.After(2 * time.Second):
		t.Fatal("the match ending during shutdown was never recorded")
	}
	require.True(t, m.WaitForFinalizers(2*time.Second))
	repo.AssertNotCalled(t, "FinalizeRankedMatch",
		mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// A rules bug that ends the hand must not move the ladder: half-applied state is not a result.
func TestFinalize_RulesErrorIsRecordedWithoutElo(t *testing.T) {
	t.Parallel()

	repo := new(MockMatchRepo)
	recorded := make(chan struct{}, 1)
	repo.On("RecordCasualMatch", mock.Anything, gameRef("MockGame"), []uuid.UUID{testutil.UID(1), testutil.UID(2)}).
		Run(func(mock.Arguments) { recorded <- struct{}{} }).
		Return(nil)

	_, _, engine := newFinishedGameLobby(t, repo)
	endHand(engine, stubBoom)

	select {
	case <-recorded:
	case <-time.After(2 * time.Second):
		t.Fatal("the rules-error match was never recorded")
	}
	repo.AssertNotCalled(t, "FinalizeRankedMatch",
		mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// A dropped session must not forfeit a live match: the seat survives for the grace
// window and a reconnect cancels the pending leave.
func TestDisconnectPlayer_MidGameSeatSurvivesTheGraceWindow(t *testing.T) {
	t.Parallel()

	m, l, _, guest := startedGame(t)

	m.DisconnectPlayer(guest)

	assert.True(t, l.HasPlayer(guest), "the seat is held for the grace window")
	assert.Equal(t, l, m.FindLobbyByPlayer(guest), "and stays indexed")

	m.ResumePlayer(guest)
	require.Equal(t, l, m.FindLobbyByPlayer(guest), "a reconnect lands back at the same lobby")
	m.mu.Lock()
	_, pending := m.grace.pending[guest.ID]
	m.mu.Unlock()
	assert.False(t, pending, "the pending leave is cancelled by the resume")
}

func TestDisconnectPlayer_GraceExpiryForfeitsTheSeat(t *testing.T) {
	t.Parallel()

	m, l, _, guest := startedGame(t)

	m.DisconnectPlayer(guest)
	m.expireLeaveGen(guest, 0)

	assert.False(t, l.HasPlayer(guest), "the expired seat is given up")
	m.ResumePlayer(guest)
	assert.Nil(t, m.FindLobbyByPlayer(guest), "nothing to resume after expiry")
}

// releaseHeldSeats collects the holds under m.mu and expires them after releasing it.
// A hold armed in that gap for a newer game is not the one it saw, and must survive
// the stale release (invariant 4).
func TestReleaseHeldSeats_StaleReleaseKeepsANewerHold(t *testing.T) {
	t.Parallel()

	m, l, leader, guest := startedGame(t)
	m.DisconnectPlayer(guest)
	m.mu.Lock()
	held := m.heldSeatsLocked(l)
	m.mu.Unlock()
	require.Len(t, held, 1)

	m.ResumePlayer(guest)
	endHand(l.ActiveGame(), stubWin)
	require.Eventually(t, func() bool { return l.ActiveGame() == nil }, 2*time.Second, time.Millisecond)
	require.NoError(t, m.ToggleReady(leader))
	require.NoError(t, m.ToggleReady(guest))
	require.NotNil(t, l.ActiveGame(), "the second game did not start")
	m.DisconnectPlayer(guest)

	for id, gen := range held {
		m.expireLeaveGen(&game.Player{ID: id}, gen)
	}
	assert.True(t, pendingGrace(m, guest.ID), "a stale release expired the newer hold")
	assert.True(t, l.HasPlayer(guest), "a stale release forfeited the newer game's seat")
}

// expireLeaveGen claims the grace before LeaveLobby runs. A resume in that gap must
// not return — and so must not sit the player back down — until the leave finishes.
func TestResumePlayer_WaitsUntilAClaimedExpiryFinishes(t *testing.T) {
	t.Parallel()

	m, l, _, guest := startedGame(t)

	m.DisconnectPlayer(guest)
	m.mu.Lock()
	h := m.grace.pending[guest.ID]
	delete(m.grace.pending, guest.ID)
	m.grace.expiring[guest.ID] = h
	m.mu.Unlock()
	require.True(t, l.HasPlayer(guest))

	returned := make(chan struct{})
	go func() {
		m.ResumePlayer(guest)
		close(returned)
	}()
	select {
	case <-returned:
		t.Fatal("resumed while the expiry still owns the seat")
	case <-time.After(50 * time.Millisecond):
	}

	m.LeaveLobby(guest)
	h.finish()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("resume did not return once the expiry finished")
	}
	assert.Nil(t, m.FindLobbyByPlayer(guest), "the expiry took the seat before the resume returned")
}

// A second SSH session can take over a mid-game seat while the first is still
// half-open (no pending leave yet).
func TestResumePlayer_TakeoverWithoutPendingLeave(t *testing.T) {
	t.Parallel()

	m, l, _, guest := startedGame(t)

	m.ResumePlayer(guest)
	assert.Equal(t, l, m.FindLobbyByPlayer(guest), "zombie session still holds the seat")
}

// A dropped session that was not at any table has no seat to hold.
func TestDisconnectPlayer_UnseatedPlayerHoldsNothing(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	p := mockPlayer("p1", testutil.UID(1))

	m.DisconnectPlayer(p)

	assert.False(t, pendingGrace(m, p.ID), "a grace was armed for a seat that does not exist")
	m.ResumePlayer(p)
	assert.Nil(t, m.FindLobbyByPlayer(p))
}

// The timer can fire while a reconnect is on its way in: Stop then fails, and the
// expiry already owns the seat. The reconnect is refused rather than resumed into a
// seat about to be given up.
func TestResumePlayer_RefusedOnceTheTimerHasFired(t *testing.T) {
	t.Parallel()
	m, l, _, guest := startedGame(t)
	fired := make(chan struct{})
	m.mu.Lock()
	m.grace.arm(guest.ID, time.Nanosecond, func(uint64) { close(fired) })
	m.mu.Unlock()
	<-fired

	m.ResumePlayer(guest)
	assert.True(t, pendingGrace(m, guest.ID), "the refused resume took the hold from the expiry")
	assert.True(t, l.HasPlayer(guest))
}

// A second drop before any reconnect restarts the grace from that drop, and the
// timer is what finally gives the seat up.
func TestDisconnectPlayer_ASecondDropRestartsTheGrace(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		m, l, _, guest := startedGame(t)
		m.DisconnectPlayer(guest)
		time.Sleep(disconnectGrace / 2)
		m.DisconnectPlayer(guest)

		time.Sleep(disconnectGrace/2 + time.Second)
		synctest.Wait()
		require.True(t, l.HasPlayer(guest), "the first drop's timer still fired")

		time.Sleep(disconnectGrace / 2)
		synctest.Wait()
		assert.False(t, l.HasPlayer(guest), "the grace ran out and the seat was kept")
		assert.Nil(t, m.FindLobbyByPlayer(guest))
	})
}

func TestDisconnectPlayer_WaitingLobbyLeavesImmediately(t *testing.T) {
	t.Parallel()

	m, l := newTestLobby(t, 2)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	require.Equal(t, waiting, l.state)

	m.DisconnectPlayer(guest)

	assert.False(t, l.HasPlayer(guest), "nothing is lost by leaving a waiting lobby")
	assert.Nil(t, m.FindLobbyByPlayer(guest))
}

// startedGame is a two-seat lobby with a running game - the only state in which a
// dropped session keeps its seat instead of leaving at once.
func startedGame(t *testing.T) (*Manager, *Lobby, *game.Player, *game.Player) {
	t.Helper()
	m, l := newTestLobby(t, 2)
	leader := l.Snapshot().Leader.Player
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	require.NoError(t, m.ToggleReady(leader))
	require.NoError(t, m.ToggleReady(guest))
	require.NotNil(t, l.ActiveGame(), "the game did not start")
	return m, l, leader, guest
}

func pendingGrace(m *Manager, id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.grace.pending[id]
	return ok
}

// A seat the engine takes for idling has to leave the lobby roster too, or the table
// can never reach all-ready again. That used to ride EventPlayerIdle through the
// watcher, which was skipped with no match repository; the engine's seat hook now
// carries it.
//
// The turn clock is real, so this runs in a synctest bubble: the idle removal takes
// MaxMissedTurns of one seat's turns, minutes of fake time that pass at once.
func TestLobby_IdleRemovalLeavesTheRoster(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		m := newTestManager(t, nil)
		leader, guest := mockPlayer("p1", testutil.UID(1)), mockPlayer("p2", testutil.UID(2))
		l, err := m.CreateLobby(leader, WithMaxPlayers(2), WithCardGame("mock"))
		require.NoError(t, err)
		require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
		require.NoError(t, m.ToggleReady(leader))
		require.NoError(t, m.ToggleReady(guest))
		idler, other := leader, guest
		if l.ActiveGame().CurrentPlayerID() != leader.ID {
			idler, other = guest, leader
		}

		// Each seat's turn times out in turn; the first seat's third miss takes it.
		time.Sleep(2 * game.MaxMissedTurns * game.DefaultTurnTimeout)
		synctest.Wait()

		assert.False(t, l.HasPlayer(idler), "the engine took the seat but the lobby roster kept it")
		assert.Nil(t, m.FindLobbyByPlayer(idler))
		assert.True(t, l.HasPlayer(other), "only the idle seat leaves")
	})
}

// EventPlayerIdle rides the latest-wins feed, so it can be lost; the roster must not
// depend on it. This engine has no subscriber at all - every event it sends is
// dropped - and the seat it takes still leaves the roster through the hook.
func TestLobby_IdleRemovalReachesTheRosterWithNothingReadingTheFeed(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		m, l := newTestLobby(t, 2)
		leader, guest := l.Snapshot().Leader.Player, mockPlayer("p2", testutil.UID(2))
		require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

		engine := game.NewEngine(stubRules{}, []*game.Player{leader, guest}, m.seatHook(l))
		require.NoError(t, engine.Start())
		idler, other := leader, guest
		if engine.CurrentPlayerID() != leader.ID {
			idler, other = guest, leader
		}

		time.Sleep(2 * game.MaxMissedTurns * game.DefaultTurnTimeout)
		synctest.Wait()
		engine.Close()

		assert.False(t, l.HasPlayer(idler), "the idle seat stayed on the roster")
		assert.Nil(t, m.FindLobbyByPlayer(idler))
		assert.True(t, l.HasPlayer(other), "only the idle seat leaves")
	})
}

// The hook runs after the engine lock is released, so a player can leave in between
// and sit down elsewhere. The late hook must not take that new seat.
func TestDropSeat_LeavesAPlayerWhoMovedOnAlone(t *testing.T) {
	t.Parallel()
	m, l, _, guest := startedGame(t)
	m.LeaveLobby(guest)
	next, err := m.CreateLobby(mockPlayer("p3", testutil.UID(3)), WithMaxPlayers(2), WithCardGame("mock"))
	require.NoError(t, err)
	require.NoError(t, joinErr(m.JoinLobbyByCode(next.Code(), guest)))

	m.dropSeat(l, guest.ID)

	assert.True(t, next.HasPlayer(guest), "the late hook took the seat at the next table")
	assert.Same(t, next, m.FindLobbyByPlayer(guest))
}

// A seat is held for a reconnect because the hand is still running. Once the game is
// over the lobby is Waiting, where DisconnectPlayer gives a seat up immediately - so a
// hold that outlives the hand locks the player out of every table for up to 90s.
func TestDisconnectPlayer_HeldSeatIsGivenUpWhenTheGameEnds(t *testing.T) {
	t.Parallel()
	m, l, leader, guest := startedGame(t)

	m.DisconnectPlayer(guest)
	require.True(t, l.HasPlayer(guest), "a mid-game seat is held, not dropped")
	require.True(t, pendingGrace(m, guest.ID))

	// The last player leaving finishes the engine, which is what the watcher turns
	// into a finalize and a reopened table.
	m.LeaveLobby(leader)

	require.Eventually(t, func() bool { return m.FindLobbyByPlayer(guest) == nil },
		2*time.Second, 10*time.Millisecond,
		"the finished table kept holding a seat for a session that is gone")
	assert.False(t, pendingGrace(m, guest.ID), "the grace timer outlived the game")
}

// Giving a hold up when its hand ends has to stop its timer too. One left running
// fires at the old deadline, finds the hold the player's next drop armed at another
// table, and forfeits that seat with most of its grace still to run.
func TestDisconnectPlayer_ReleasedHoldDoesNotFireIntoTheNextTable(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		m, _, leader, guest := startedGame(t)
		m.DisconnectPlayer(guest)
		m.LeaveLobby(leader)
		synctest.Wait()
		require.Nil(t, m.FindLobbyByPlayer(guest), "the finished table gave the held seat up")

		time.Sleep(disconnectGrace / 2)
		host := mockPlayer("p3", testutil.UID(3))
		next, err := m.CreateLobby(host, WithMaxPlayers(2), WithCardGame("mock"))
		require.NoError(t, err)
		require.NoError(t, joinErr(m.JoinLobbyByCode(next.Code(), guest)))
		require.NoError(t, m.ToggleReady(host))
		require.NoError(t, m.ToggleReady(guest))
		m.DisconnectPlayer(guest)

		time.Sleep(disconnectGrace/2 + time.Second)
		synctest.Wait()
		assert.True(t, next.HasPlayer(guest), "the first table's timer took the seat at the next one")
		assert.True(t, pendingGrace(m, guest.ID), "the next table's hold is still running")
	})
}

// ToggleReady is the other reopen path: the watcher persists before it reopens, so
// the TUI can already be back in the lobby and ready-ing while InGame still holds a
// grace. Reopening has to give that seat up the same way the watcher does.
func TestToggleReady_ReleasesHeldSeatsWhenReopening(t *testing.T) {
	t.Parallel()
	m, l, leader, guest := startedGame(t)

	m.DisconnectPlayer(guest)
	require.True(t, l.HasPlayer(guest), "a mid-game seat is held, not dropped")
	require.True(t, pendingGrace(m, guest.ID))

	engine := l.ActiveGame()
	require.NotNil(t, engine)
	engine.WithState(func(state *game.State) { state.Phase = game.Finished })

	_ = m.ToggleReady(leader)

	assert.False(t, pendingGrace(m, guest.ID), "reopening via ready left the grace armed")
	assert.False(t, l.HasPlayer(guest), "the ghost seat stayed on the roster")
}

// The feed ending is how a dropped EventGameEnded still finalizes. It also has to
// reopen, or a disconnected seat stays held until DisconnectGrace.
func TestFeedEnd_ReleasesHeldSeatsWhenFinished(t *testing.T) {
	t.Parallel()
	m, l, _, guest := startedGame(t)

	m.DisconnectPlayer(guest)
	require.True(t, pendingGrace(m, guest.ID))

	engine := l.ActiveGame()
	require.NotNil(t, engine)
	engine.WithState(func(state *game.State) { state.Phase = game.Finished })
	engine.Close()

	// Both in one condition: expireLeaveGen drops the pending grace under m.mu and only
	// then takes the seat, so "grace gone" alone can be observed a moment early.
	require.Eventually(t, func() bool { return !pendingGrace(m, guest.ID) && !l.HasPlayer(guest) },
		2*time.Second, 10*time.Millisecond,
		"closing the finished feed left the grace armed or the ghost seat on the roster")
}

// A timer armed for a reconnect fires long after the drain is over: nothing would
// then remove the lobby or close its engine, and the player is not coming back to a
// process that is exiting.
func TestBeginShutdown_GivesUpSeatsHeldForAReconnect(t *testing.T) {
	t.Parallel()
	m, l, _, guest := startedGame(t)

	m.DisconnectPlayer(guest)
	require.True(t, pendingGrace(m, guest.ID))

	m.BeginShutdown()

	assert.False(t, pendingGrace(m, guest.ID), "a grace timer survived shutdown")
	assert.False(t, l.HasPlayer(guest), "the seat was still held when the process went away")
	assert.Nil(t, m.FindLobbyByPlayer(guest))
}

// A finished match that produces no row still has to show up as one: these paths
// were the only bail-outs that dropped a result with no log and no counter.
//
//nolint:paralleltest // slog.SetDefault is process-wide, so this cannot share the process
func TestFinalize_SilentDropsAreCountedAndLogged(t *testing.T) {
	tests := []struct {
		name    string
		req     finalizeRequest
		reason  game.EndReason
		rules   game.Rules
		seats   []*game.Player
		wantLog string
	}{
		{
			name:    "no game on the snapshot",
			req:     finalizeRequest{lobbyCode: "AAAAAAAA", isRanked: true, startedAt: time.Now()},
			reason:  game.EndReasonWin,
			rules:   noStandingsRules{},
			wantLog: "the lobby recorded no game",
		},
		{
			name:    "abandoned with nobody left standing",
			req:     finalizeRequest{lobbyCode: "BBBBBBBB", game: gameRef("Mock"), startedAt: time.Now()},
			reason:  game.EndReasonAbandoned,
			rules:   noStandingsRules{},
			wantLog: "finished match had no standings",
		},
		{
			name:    "a win the rules ranked nobody in",
			req:     finalizeRequest{lobbyCode: "CCCCCCCC", game: gameRef("Mock"), isRanked: true, startedAt: time.Now()},
			reason:  game.EndReasonWin,
			rules:   noStandingsRules{},
			wantLog: "reason=win",
		},
		{
			name:    "a seat with no database user",
			req:     finalizeRequest{lobbyCode: "DDDDDDDD", game: gameRef("Mock"), isRanked: true},
			reason:  game.EndReasonWin,
			rules:   stubRules{},
			seats:   []*game.Player{mockPlayer("a", testutil.UID(1)), {ID: "guest-without-account"}},
			wantLog: "standing player has no database user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logged syncBuffer
			original := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(original) })

			repo := new(MockMatchRepo)
			m := newTestManager(t, repo)
			engine := game.NewEngine(tt.rules, tt.seats)
			t.Cleanup(engine.Close)

			m.finalizeFinishedGame(tt.req, engine, tt.reason)

			assert.True(t, logged.contains(tt.wantLog), "the drop was silent: %s", logged.String())
			repo.AssertNotCalled(t, "FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			repo.AssertNotCalled(t, "RecordCasualMatch", mock.Anything, mock.Anything, mock.Anything)
			repo.AssertNotCalled(t, "FinalizeInterruptedMatch",
				mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// A table every seat walked away from has standings in reverse leave order, so
// rating it would pay whoever quit last. It is history only, ranked or not.
func TestFinalize_AbandonedRankedMatchIsRecordedWithoutElo(t *testing.T) {
	t.Parallel()
	seats := []*game.Player{mockPlayer("a", testutil.UID(1)), mockPlayer("b", testutil.UID(2))}
	repo := new(MockMatchRepo)
	repo.On("RecordCasualMatch", mock.Anything, gameRef("Mock"), []uuid.UUID{testutil.UID(1), testutil.UID(2)}).
		Return(nil).Once()
	m := newTestManager(t, repo)
	engine := game.NewEngine(&stubRules{}, seats)
	t.Cleanup(engine.Close)

	m.finalizeFinishedGame(finalizeRequest{
		lobbyCode: "EEEEEEEE", game: gameRef("Mock"), isRanked: true, startedAt: time.Now(),
	}, engine, game.EndReasonAbandoned)

	repo.AssertExpectations(t)
	repo.AssertNotCalled(t, "FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

// noStandingsRules is stubRules with an empty standings list, which is the shape an
// abandoned table, or a Standings that panicked, leaves behind.
type noStandingsRules struct{ stubRules }

func (noStandingsRules) Standings(*game.State) []game.Standing { return nil }

// The finish metric is labelled with EndReason.String(). These are the labels the
// dashboards already group by, so a rename there splits every series in two, and a
// reason falling through to "unknown" hides a whole class of endings.
func TestFinalize_EndReasonMetricLabelsAreStable(t *testing.T) {
	t.Parallel()
	for reason, want := range map[game.EndReason]string{
		game.EndReasonWin:         "win",
		game.EndReasonRulesError:  "rules_error",
		game.EndReasonForfeit:     "forfeit",
		game.EndReasonAbandoned:   "abandoned",
		game.EndReasonInterrupted: "interrupted",
		game.EndReasonUnknown:     "unknown",
	} {
		assert.Equal(t, want, reason.String())
	}
}

// The watcher reopens the table before it persists, and reopening waits on m.mu in
// releaseHeldSeats. A drain that begins inside that wait must still wait for the
// write: it once saw nothing in flight, and the process was told it was safe to exit
// with a finished match unwritten.
func TestFinalize_DrainWaitsForAWatcherStillReopeningTheTable(t *testing.T) {
	t.Parallel()

	repo := new(MockMatchRepo)
	recorded := make(chan struct{}, 1)
	repo.On("FinalizeRankedMatch", mock.Anything, gameRef("MockGame"), mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { recorded <- struct{}{} }).Return(nil)
	repo.On("RecordCasualMatch", mock.Anything, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { recorded <- struct{}{} }).Return(nil)

	m, l, engine := newFinishedGameLobby(t, repo)
	m.mu.Lock()
	endHand(engine, stubWin)
	// The table is Waiting again, so the watcher is parked in releaseHeldSeats on m.mu.
	require.Eventually(t, func() bool {
		l.mu.RLock()
		defer l.mu.RUnlock()
		return l.state == waiting
	}, 2*time.Second, time.Millisecond)

	assert.False(t, m.WaitForFinalizers(50*time.Millisecond),
		"the drain finished while a finished match had not been written")
	m.mu.Unlock()

	require.True(t, m.WaitForFinalizers(2*time.Second))
	select {
	case <-recorded:
	default:
		t.Fatal("the match that ended during the drain was dropped")
	}
}

// A match one leaver ended early for everyone (decision D-1): the seats still
// playing are not rated on a result nobody finished, but the leaver still loses, or
// quitting a losing match would be free. The repository applies that; the lobby's
// job is to route the match there with the leavers named.
func TestFinalize_InterruptedMatchNamesItsLeavers(t *testing.T) {
	t.Parallel()

	seated := []*game.Player{mockPlayer("a", testutil.UID(1)), mockPlayer("b", testutil.UID(2))}
	leaver := mockPlayer("c", testutil.UID(3))
	standings := []uuid.UUID{testutil.UID(1), testutil.UID(2), testutil.UID(3)}

	tests := []struct {
		name     string
		ranked   bool
		shutdown bool
		expect   func(r *MockMatchRepo)
	}{
		{
			name:   "ranked: only the leaver is rated",
			ranked: true,
			expect: func(r *MockMatchRepo) {
				r.On("FinalizeInterruptedMatch", mock.Anything, gameRef("Mock"), standings, mock.Anything,
					[]uuid.UUID{testutil.UID(3)}).Return(nil).Once()
			},
		},
		{
			name: "casual: history only",
			expect: func(r *MockMatchRepo) {
				r.On("RecordCasualMatch", mock.Anything, gameRef("Mock"), standings).Return(nil).Once()
			},
		},
		{
			name:     "shutdown: not even the leaver is rated",
			ranked:   true,
			shutdown: true,
			expect: func(r *MockMatchRepo) {
				r.On("RecordCasualMatch", mock.Anything, gameRef("Mock"), standings).Return(nil).Once()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := new(MockMatchRepo)
			tt.expect(repo)
			m := newTestManager(t, repo)
			if tt.shutdown {
				m.shuttingDown.Store(true)
			}

			engine := game.NewEngine(&stubRules{}, seated)
			t.Cleanup(engine.Close)
			engine.WithState(func(state *game.State) { state.LeftPlayers = []*game.Player{leaver} })

			m.finalizeFinishedGame(finalizeRequest{
				lobbyCode: "CCCCCCCC", game: gameRef("Mock"), isRanked: tt.ranked, startedAt: time.Now(),
			}, engine, game.EndReasonInterrupted)

			repo.AssertExpectations(t)
			repo.AssertNotCalled(t, "FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// seated is a player as NewPlayer builds one: the seat key is the account id, which
// is what refreshRatings finds a player's current table by.
func seated(n uint64) *game.Player {
	return &game.Player{ID: testutil.UID(n).String(), UserID: testutil.UID(n), Ratings: map[string]uint32{"other": 1700}}
}

// A ranked write lands on the seats still at the table, so the waiting room,
// matchmaking and the table's average show the new rating rather than the one the
// session logged in with - without writing through a pointer a reader still holds.
func TestFinalize_RefreshesTheSeatedPlayersRatings(t *testing.T) {
	t.Parallel()
	leader, guest := seated(1), seated(2)
	written := map[uuid.UUID]uint32{leader.UserID: 1516, guest.UserID: 1484}
	repo := new(MockMatchRepo)
	repo.On("FinalizeRankedMatch", mock.Anything, gameRef("MockGame"), mock.Anything, mock.Anything).
		Return(written, nil)
	m := newTestManager(t, repo)
	l, err := m.CreateLobby(leader, WithMaxPlayers(2), WithCardGame("mockgame"), WithRanked(true))
	require.NoError(t, err)
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	events, err := l.Subscribe(leader.ID)
	require.NoError(t, err)
	require.NoError(t, m.ToggleReady(leader))
	require.NoError(t, m.ToggleReady(guest))
	drainEventTypes(events)

	endHand(l.ActiveGame(), stubWin)
	// Not WaitForFinalizers: a drain records a ranked match unrated.
	for _, why := range []string{"the reopened table", "its new ratings"} {
		select {
		case ev := <-events:
			require.Equal(t, EventPlayersUpdated, ev.Type, why)
		case <-time.After(2 * time.Second):
			t.Fatalf("%s was never announced", why)
		}
	}

	snap := l.Snapshot()
	assert.Equal(t, uint32(1516), snap.Leader.Elo)
	assert.Equal(t, uint32(1484), snap.Guests[0].Elo)
	assert.Equal(t, uint32(1700), snap.Leader.Player.Ratings["other"], "another game's rating was lost")
	assert.True(t, snap.Leader.Player.Equal(leader))
	assert.NotSame(t, leader, snap.Leader.Player, "the seat was written in place, not replaced")
	assert.Equal(t, map[string]uint32{"other": 1700}, leader.Ratings, "a reader's map changed under it")
}

// The write commits after the table reopened, so a player may be somewhere else by
// then: the rating follows them there, and a player seated nowhere is skipped.
func TestRefreshRatings_FollowsEachPlayerToTheirCurrentTable(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	stayed, moved, gone := seated(1), seated(2), seated(3)
	here, err := m.CreateLobby(stayed, WithCardGame("mockgame"))
	require.NoError(t, err)
	there, err := m.CreateLobby(moved, WithCardGame("mockgame"))
	require.NoError(t, err)
	quiet, err := there.Subscribe(moved.ID)
	require.NoError(t, err)

	m.refreshRatings("mockgame", map[uuid.UUID]uint32{stayed.UserID: 1520, moved.UserID: 1480, gone.UserID: 1400})
	m.refreshRatings("mockgame", map[uuid.UUID]uint32{moved.UserID: 1480})

	assert.Equal(t, uint32(1520), here.Snapshot().Leader.Elo)
	assert.Equal(t, uint32(1480), there.Snapshot().Leader.Elo)
	assert.Equal(t, []EventType{EventPlayersUpdated}, drainEventTypes(quiet), "an unchanged rating was announced again")
	assert.Nil(t, m.FindLobbyByPlayer(gone))
}

// Snapshots hand out the seated *game.Player, and its readers hold no lock: a
// refresh must replace it, never write its Ratings map, or this races.
func TestRefreshRatings_DoesNotRaceReadersOfTheOldSeat(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	p := seated(1)
	l, err := m.CreateLobby(p, WithCardGame("mockgame"))
	require.NoError(t, err)

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 200 {
				seat := l.Snapshot().Leader.Player
				_ = seat.Ratings["mockgame"]
				_ = len(seat.Ratings)
			}
		})
	}
	for i := range 200 {
		m.refreshRatings("mockgame", map[uuid.UUID]uint32{p.UserID: uint32(1500 + i)})
	}
	wg.Wait()
	assert.Equal(t, uint32(1699), l.Snapshot().Leader.Elo)
}
