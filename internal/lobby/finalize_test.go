package lobby

import (
	"bytes"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"

	"github.com/Pieczasz/terminal-card/internal/testutil"

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

	l, err := m.New(leader, WithMaxPlayers(2), WithCardGame("MockGame"), WithRanked(true))
	require.NoError(t, err)
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	rules := new(MockRules)
	rules.On("MinPlayers").Return(2).Maybe()
	rules.On("MaxPlayers").Return(4).Maybe()
	rules.On("InitialDeck").Return(deck.StandardDeck()).Maybe()
	rules.On("InitialDealCount").Return(2).Maybe()
	rules.On("OnGameStart", mock.Anything).Return(nil).Maybe()
	rules.On("CheckWinCondition", mock.Anything).Return(false).Maybe()
	rules.On("Standings", mock.Anything).Return([]*game.Player{leader, guest}).Maybe()
	registry := gameRegistry("MockGame", rules)

	require.NoError(t, l.ToggleReady(leader, registry))
	require.NoError(t, l.ToggleReady(guest, registry))

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
	// The hand really is over, so both racers have a result to persist: the watcher
	// through the event, or - if RemoveLobby closes the feed first and the event is
	// never delivered - through the finished engine it finds when the feed ends.
	engine.WithState(func(state *game.State) { state.Phase = game.Finished })

	go engine.Broadcaster().Broadcast(game.Event{Type: game.EventGameEnded, Reason: game.EndReasonWin})
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

// A single hand produces a single row no matter how many times the terminal event is
// published: the engine ends once, the watcher stops reading after the first.
func TestFinalize_IsNotAppliedTwice(t *testing.T) {
	t.Parallel()

	repo := new(MockMatchRepo)
	calls := make(chan struct{}, 4)
	repo.On("FinalizeRankedMatch", mock.Anything, gameRef("MockGame"), []uuid.UUID{testutil.UID(1), testutil.UID(2)}, mock.Anything).
		Run(func(mock.Arguments) { calls <- struct{}{} }).
		Return(nil)

	m, _, engine := newFinishedGameLobby(t, repo)

	engine.Broadcaster().Broadcast(game.Event{Type: game.EventGameEnded, Reason: game.EndReasonWin})
	engine.Broadcaster().Broadcast(game.Event{Type: game.EventGameEnded, Reason: game.EndReasonWin})

	select {
	case <-calls:
	case <-time.After(2 * time.Second):
		t.Fatal("the finished match was never recorded")
	}
	require.True(t, m.WaitForFinalizers(2*time.Second))
	assert.Empty(t, calls, "the same hand was persisted twice")
}

// The window between observing the end of a game and registering the finalizer used
// to be a silent drop: shutdown could start inside it and the result vanished with
// nothing logged and nothing for WaitForFinalizers to wait on.
//
//nolint:paralleltest // slog.SetDefault is process-wide, so this cannot share the process
func TestShutdown_MatchEndingDuringDrainIsNotSilentlyDropped(t *testing.T) {
	logged := &syncBuffer{}
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(original) })

	for i := range 20 {
		var recorded atomic.Int32
		repo := new(MockMatchRepo)
		repo.On("FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
			Run(func(mock.Arguments) { recorded.Add(1) }).Return(nil)
		repo.On("RecordCasualMatch", mock.Anything, mock.Anything, mock.Anything).
			Run(func(mock.Arguments) { recorded.Add(1) }).Return(nil)

		m, l, engine := newFinishedGameLobby(t, repo)
		logged.Reset()

		done := make(chan struct{})
		go func() {
			defer close(done)
			engine.Broadcaster().Broadcast(game.Event{Type: game.EventGameEnded, Reason: game.EndReasonWin})
		}()
		drained := m.WaitForFinalizers(2 * time.Second)
		<-done
		require.True(t, drained, "the drain gave up on iteration %d", i)

		// Every iteration either persisted the result or shouted that it could not.
		require.Eventually(t, func() bool {
			return recorded.Load() > 0 || logged.contains("finished match dropped")
		}, 2*time.Second, 5*time.Millisecond,
			"iteration %d neither persisted the match nor logged the drop", i)

		m.RemoveLobby(l.Code())
	}
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

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
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

	m, l, registry := newTestLobby(t, 2)
	leader := l.Leader()
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	require.NoError(t, l.ToggleReady(leader, registry))
	require.NoError(t, l.ToggleReady(guest, registry))
	require.Equal(t, InGame, l.state)

	require.ErrorContains(t, m.Kick(leader, guest), "cannot kick during a game")
	assert.True(t, l.HasPlayer(guest), "the target is still at the table")
	assert.Equal(t, l, m.FindLobbyByPlayer(guest), "and still indexed to it")

	l.mu.RLock()
	engine := l.activeEngine
	l.mu.RUnlock()
	engine.WithState(func(state *game.State) { state.Phase = game.Finished })
	l.releaseFinishedGame()

	require.NoError(t, m.Kick(leader, guest), "and can be kicked once the hand is over")
}

// The ready flip is committed before the start is attempted, so a failed start still
// has to publish it; otherwise every other client shows a stale roster.
func TestToggleReady_FailedStartStillBroadcasts(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, nil)
	leader := mockPlayer("p1", testutil.UID(1))
	guest := mockPlayer("p2", testutil.UID(2))
	l, err := m.New(leader, WithMaxPlayers(4), WithCardGame("Unregistered"))
	require.NoError(t, err)
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	observer, err := l.Subscribe("observer")
	require.NoError(t, err)

	// The registry has no rules under this name, so the start fails after both flips.
	require.NoError(t, l.ToggleReady(leader, game.NewRegistry()))
	require.Error(t, l.ToggleReady(guest, game.NewRegistry()))

	assert.Equal(t, []string{EventPlayersUpdated, EventPlayersUpdated}, drainEventTypes(observer),
		"the second flip was committed without telling anyone")
	assert.True(t, l.IsReady(guest), "and it really was committed")
}

// A drain that times out used to strand its waiter goroutine, one per attempt, on a
// path that is retried by design.
func TestWaitForFinalizers_TimeoutDoesNotLeakItsWaiter(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, nil)
	require.True(t, m.registerFinalizer())

	assert.False(t, m.WaitForFinalizers(10*time.Millisecond), "an in-flight write blocks the drain")

	m.finalizerMu.Lock()
	first := m.drained
	m.finalizerMu.Unlock()

	assert.False(t, m.WaitForFinalizers(10*time.Millisecond))

	m.finalizerMu.Lock()
	second := m.drained
	m.finalizerMu.Unlock()
	assert.Equal(t, first, second, "a second attempt started a second waiter")

	// goleak's TestMain is the other half of this: the one waiter has to exit.
	m.finalizing.Done()
	assert.True(t, m.WaitForFinalizers(2*time.Second))
}

// A ranked hand the deploy interrupted has no honest winner, so it is history only.
func TestFinalize_InterruptedRankedMatchIsRecordedWithoutElo(t *testing.T) {
	t.Parallel()

	repo := new(MockMatchRepo)
	recorded := make(chan struct{}, 1)
	repo.On("RecordCasualMatch", mock.Anything, gameRef("MockGame"), []uuid.UUID{testutil.UID(1), testutil.UID(2)}).
		Run(func(mock.Arguments) { recorded <- struct{}{} }).
		Return(nil)

	m, _, engine := newFinishedGameLobby(t, repo)
	m.BeginShutdown()

	engine.Broadcaster().Broadcast(game.Event{Type: game.EventGameEnded, Reason: game.EndReasonForfeit})

	select {
	case <-recorded:
	case <-time.After(2 * time.Second):
		t.Fatal("the interrupted match was never recorded")
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
	engine.Broadcaster().Broadcast(game.Event{Type: game.EventGameEnded, Reason: game.EndReasonRulesError})

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

	m, l, registry := newTestLobby(t, 2)
	leader := l.Leader()
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	require.NoError(t, l.ToggleReady(leader, registry))
	require.NoError(t, l.ToggleReady(guest, registry))
	require.Equal(t, InGame, l.state)

	m.DisconnectPlayer(guest)

	assert.True(t, l.HasPlayer(guest), "the seat is held for the grace window")
	assert.Equal(t, l, m.FindLobbyByPlayer(guest), "and stays indexed")

	resumed := m.ResumePlayer(guest)
	require.Equal(t, l, resumed, "a reconnect lands back at the same lobby")
	m.mu.Lock()
	_, pending := m.grace.pending[guest.ID]
	m.mu.Unlock()
	assert.False(t, pending, "the pending leave is cancelled by the resume")
}

func TestDisconnectPlayer_GraceExpiryForfeitsTheSeat(t *testing.T) {
	t.Parallel()

	m, l, registry := newTestLobby(t, 2)
	leader := l.Leader()
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	require.NoError(t, l.ToggleReady(leader, registry))
	require.NoError(t, l.ToggleReady(guest, registry))

	m.DisconnectPlayer(guest)
	m.expireLeave(guest)

	assert.False(t, l.HasPlayer(guest), "the expired seat is given up")
	assert.Nil(t, m.FindLobbyByPlayer(guest))
	assert.Nil(t, m.ResumePlayer(guest), "nothing to resume after expiry")
}

// expireLeave deletes the pending entry before LeaveLobby; ResumePlayer must not
// treat a missing entry as "still seated" or it routes into a seat about to vanish.
func TestResumePlayer_AfterExpireClaimReturnsNil(t *testing.T) {
	t.Parallel()

	m, l, registry := newTestLobby(t, 2)
	leader := l.Leader()
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	require.NoError(t, l.ToggleReady(leader, registry))
	require.NoError(t, l.ToggleReady(guest, registry))

	m.DisconnectPlayer(guest)
	m.mu.Lock()
	delete(m.grace.pending, guest.ID) // what expireLeave does before LeaveLobby
	m.grace.expiring[guest.ID] = struct{}{}
	stillSeated := m.playerLobby[guest.ID] != nil
	m.mu.Unlock()
	require.True(t, stillSeated)

	assert.Nil(t, m.ResumePlayer(guest), "grace already claimed: do not resume")
	assert.True(t, l.HasPlayer(guest), "LeaveLobby has not run yet")
}

// A second SSH session can take over a mid-game seat while the first is still
// half-open (no pending leave yet).
func TestResumePlayer_TakeoverWithoutPendingLeave(t *testing.T) {
	t.Parallel()

	m, l, registry := newTestLobby(t, 2)
	leader := l.Leader()
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	require.NoError(t, l.ToggleReady(leader, registry))
	require.NoError(t, l.ToggleReady(guest, registry))

	assert.Equal(t, l, m.ResumePlayer(guest), "zombie session still holds the seat")
}

func TestDisconnectPlayer_WaitingLobbyLeavesImmediately(t *testing.T) {
	t.Parallel()

	m, l, _ := newTestLobby(t, 2)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	require.Equal(t, Waiting, l.state)

	m.DisconnectPlayer(guest)

	assert.False(t, l.HasPlayer(guest), "nothing is lost by leaving a waiting lobby")
	assert.Nil(t, m.FindLobbyByPlayer(guest))
}

// startedGame is a two-seat lobby with a running game - the only state in which a
// dropped session keeps its seat instead of leaving at once.
func startedGame(t *testing.T) (*Manager, *Lobby, *game.Player, *game.Player) {
	t.Helper()
	m, l, registry := newTestLobby(t, 2)
	leader := l.Leader()
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	require.NoError(t, l.ToggleReady(leader, registry))
	require.NoError(t, l.ToggleReady(guest, registry))
	require.NotNil(t, l.ActiveGame(), "the game did not start")
	return m, l, leader, guest
}

func pendingGrace(m *Manager, id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.grace.pending[id]
	return ok
}

// The watcher goroutine is the only consumer of EventPlayerIdle, and it used to be
// skipped entirely when no match repository was configured. The engine then dropped
// the seat while the lobby roster kept it, and the table could never reach all-ready
// again.
func TestLobby_IdleRemovalLeavesTheRosterWithoutAMatchRepository(t *testing.T) {
	t.Parallel()
	m, l, _, guest := startedGame(t)

	l.ActiveGame().Broadcaster().Broadcast(game.Event{Type: game.EventPlayerIdle, PlayerID: guest.ID})

	require.Eventually(t, func() bool { return !l.HasPlayer(guest) }, 2*time.Second, 10*time.Millisecond,
		"the engine took the seat but the lobby roster kept it")
	assert.Nil(t, m.FindLobbyByPlayer(guest))
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

	_ = l.ToggleReady(leader, game.NewRegistry())

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

	// Both in one condition: expireLeave drops the pending grace under m.mu and only
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

// ResumePlayer's two siblings re-validate the index against the roster; it did not,
// so a stale entry routed the reconnect into a lobby that no longer held them.
func TestResumePlayer_DropsAStaleIndexEntry(t *testing.T) {
	t.Parallel()
	m, l, _ := newTestLobby(t, 3)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))

	l.mu.Lock()
	l.guests = nil
	l.mu.Unlock()

	assert.Nil(t, m.ResumePlayer(guest), "resumed into a lobby whose roster has no such player")
	m.mu.RLock()
	_, stale := m.playerLobby[guest.ID]
	m.mu.RUnlock()
	assert.False(t, stale, "the stale index entry survived the lookup")
}

// A finished match that produces no row still has to show up as one: these two paths
// were the only bail-outs that dropped a result with no log and no counter.
//
//nolint:paralleltest // slog.SetDefault is process-wide, so this cannot share the process
func TestFinalize_SilentDropsAreCountedAndLogged(t *testing.T) {
	tests := []struct {
		name    string
		req     finalizeRequest
		reason  game.EndReason
		wantLog string
	}{
		{
			name:    "no game on the snapshot",
			req:     finalizeRequest{lobbyCode: "AAAAAAAA", isRanked: true, startedAt: time.Now()},
			reason:  game.EndReasonWin,
			wantLog: "the lobby recorded no game",
		},
		{
			name:    "abandoned with nobody left standing",
			req:     finalizeRequest{lobbyCode: "BBBBBBBB", game: gameRef("Mock"), startedAt: time.Now()},
			reason:  game.EndReasonAbandoned,
			wantLog: "abandoned match had no standings",
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
			engine := game.NewEngine(&noStandingsRules{}, nil, deck.StandardDeck())
			t.Cleanup(engine.Close)

			m.finalizeFinishedGame(tt.req, engine, tt.reason, m.registerFinalizer())

			assert.True(t, logged.contains(tt.wantLog), "the drop was silent: %s", logged.String())
			repo.AssertNotCalled(t, "FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			repo.AssertNotCalled(t, "RecordCasualMatch", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

// noStandingsRules is stubRules with an empty standings list, which is the shape an
// abandoned table leaves behind.
type noStandingsRules struct{ stubRules }

func (noStandingsRules) Standings(*game.State) []*game.Player { return nil }

// The label is a metric attribute: a reason falling through to "unknown" hides a whole
// class of endings from the dashboard.
func TestEndReasonLabel_NamesEveryReason(t *testing.T) {
	t.Parallel()
	for reason, want := range map[game.EndReason]string{
		game.EndReasonWin:         "win",
		game.EndReasonRulesError:  "rules_error",
		game.EndReasonForfeit:     "forfeit",
		game.EndReasonAbandoned:   "abandoned",
		game.EndReasonInterrupted: "interrupted",
		game.EndReasonUnknown:     "unknown",
	} {
		assert.Equal(t, want, endReasonLabel(reason))
	}
}

// The watcher reopens the table before it persists, and reopening waits on m.mu in
// releaseHeldSeats. Registering the finalizer only after that left the drain a window
// to see zero writes in flight and stop accepting them: the match was dropped at
// shutdown with the process already told it was safe to exit.
func TestFinalize_RegistersBeforeReopeningTheTable(t *testing.T) {
	t.Parallel()

	repo := new(MockMatchRepo)
	recorded := make(chan struct{}, 1)
	repo.On("FinalizeRankedMatch", mock.Anything, gameRef("MockGame"), mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { recorded <- struct{}{} }).Return(nil)
	repo.On("RecordCasualMatch", mock.Anything, mock.Anything, mock.Anything).
		Run(func(mock.Arguments) { recorded <- struct{}{} }).Return(nil)

	m, l, engine := newFinishedGameLobby(t, repo)
	engine.WithState(func(state *game.State) { state.Phase = game.Finished })

	m.mu.Lock()
	engine.Broadcaster().Broadcast(game.Event{Type: game.EventGameEnded, Reason: game.EndReasonWin})
	// The table is Waiting again, so the watcher is parked in releaseHeldSeats on m.mu.
	require.Eventually(t, func() bool {
		l.mu.RLock()
		defer l.mu.RUnlock()
		return l.state == Waiting
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

			engine := game.NewEngine(&stubRules{}, seated, deck.StandardDeck())
			t.Cleanup(engine.Close)
			engine.WithState(func(state *game.State) { state.LeftPlayers = []*game.Player{leaver} })

			m.finalizeFinishedGame(finalizeRequest{
				lobbyCode: "CCCCCCCC", game: gameRef("Mock"), isRanked: tt.ranked, startedAt: time.Now(),
			}, engine, game.EndReasonInterrupted, m.registerFinalizer())

			repo.AssertExpectations(t)
			repo.AssertNotCalled(t, "FinalizeRankedMatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}
