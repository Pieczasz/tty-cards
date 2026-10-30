package ssh

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
	"uuid"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	tea "charm.land/bubbletea/v2"
	"charm.land/ssh"
	"charm.land/wish/v2/testsession"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// release is ReleaseWith with nothing to do under the lock.
func release(tracker *SessionTracker, userID uuid.UUID, gen uint64) bool {
	return tracker.ReleaseWith(userID, gen, func() {})
}

type recordingCloser struct{ closed *bool }

func (c recordingCloser) Close() { *c.closed = true }

// charm ssh hands one Context per TCP connection to every channel opened on it, so
// session state kept there is shared by sessions that must not see each other: a
// rejected second channel's teardown used to close the first channel's model and
// hand back its tracker slot.
func TestSessionState_IsPerChannelNotPerConnection(t *testing.T) {
	t.Parallel()

	tracker := NewSessionTracker(0)
	user := &db.User{ID: testutil.UID(11), Username: "shared"}
	gen, err := tracker.Connect(user.ID, nil, nil)
	require.NoError(t, err)
	deps := Deps{LobbyManager: lobby.NewManager(t.Context(), nil, catalog.NewRegistry()), Tracker: tracker}
	reg := &sessionRegistry{}

	// Both channels of one connection, so they would share an ssh.Context.
	accepted := &stubSession{}
	rejected := &stubSession{}

	modelClosed := false
	reg.store(accepted, &sessionState{
		user:  user,
		gen:   gen,
		model: recordingCloser{closed: &modelClosed},
	})
	reg.store(rejected, &sessionState{})

	reg.closeSessionModel(rejected)
	reg.releaseSession(rejected, deps)

	assert.False(t, modelClosed, "the rejected channel closed the accepted channel's view")
	assert.Equal(t, 1, tracker.Count(), "and freed the accepted channel's session slot")
}

// releaseSession must give up the lobby seat before the tracker slot when the
// session still owns its generation. A displaced reconnect must not tear down the
// seat the replacement is about to resume.
func TestReleaseSession_GivesUpTheSeatBeforeTheSlot(t *testing.T) {
	t.Parallel()

	manager := lobby.NewManager(t.Context(), nil, catalog.NewRegistry())
	host := &db.User{ID: testutil.UID(1), Username: "host"}
	guest := &db.User{ID: testutil.UID(2), Username: "guest"}
	guestPlayer := lobby.NewPlayer(guest)

	table, err := manager.CreateLobby(lobby.NewPlayer(host), lobby.WithCardGame(catalog.Games[0].Slug))
	require.NoError(t, err)
	_, err = manager.JoinLobbyByCode(t.Context(), table.Code(), guestPlayer)
	require.NoError(t, err)

	tracker := NewSessionTracker(0)
	oldGen, err := tracker.Connect(guest.ID, nil, nil)
	require.NoError(t, err)
	deps := Deps{LobbyManager: manager, Tracker: tracker}
	reg := &sessionRegistry{}

	// The reconnect displaces the zombie session before teardown runs, so
	// releaseSession sees a stale generation and leaves the seat alone.
	reconnected := make(chan struct{})
	displaced := make(chan struct{})
	reconnect := func() {
		defer close(reconnected)
		_, err := tracker.Connect(guest.ID, nil, nil)
		if err != nil {
			t.Errorf("displace reconnect failed: %v", err)
			return
		}
		close(displaced)
		manager.ResumePlayer(t.Context(), guestPlayer)
		if resumed := manager.FindLobbyByPlayer(guestPlayer); resumed != table {
			t.Errorf("takeover did not resume the waiting seat: got %v", resumed)
		}
	}

	srv := &ssh.Server{
		Handler: sessionLifecycle(deps, reg)(func(s ssh.Session) {
			st, ok := reg.load(s)
			require.True(t, ok)
			st.user = guest
			st.gen = oldGen
			go reconnect()
			<-displaced
		}),
	}
	_, _ = testsession.New(t, srv, nil).Output("")

	<-reconnected
	assert.True(t, table.HasPlayer(guestPlayer), "the reconnected session lost the seat it just took")
	assert.Equal(t, table, manager.FindLobbyByPlayer(guestPlayer), "and the index disagrees with the roster")
}

// charm ssh runs each session handler on a goroutine its Shutdown and Close never
// wait for, so a teardown still giving up its seat would race the finalizer drain
// that is meant to come after it.
func TestWaitSessions_HoldsWhileAHandlerIsTearingDown(t *testing.T) {
	t.Parallel()
	reg := &sessionRegistry{}
	server := &Server{sessions: reg}
	entered, finish := make(chan struct{}), make(chan struct{})
	srv := &ssh.Server{Handler: sessionLifecycle(Deps{}, reg)(func(ssh.Session) {
		close(entered)
		<-finish
	})}
	session := testsession.New(t, srv, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = session.Output("")
	}()
	<-entered

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, server.WaitSessions(ctx), context.DeadlineExceeded,
		"the drain moved on while a session handler was still running")

	close(finish)
	require.NoError(t, server.WaitSessions(t.Context()))
	<-done
}

// A handler charm ssh started just before Close can reach the lifecycle after the
// drain stopped counting. Run untracked, its teardown would race the drain again.
func TestWaitSessions_ASessionArrivingDuringTheDrainIsRefused(t *testing.T) {
	t.Parallel()
	reg := &sessionRegistry{}
	require.NoError(t, (&Server{sessions: reg}).WaitSessions(t.Context()))

	var ran atomic.Bool
	srv := &ssh.Server{Handler: sessionLifecycle(Deps{}, reg)(func(ssh.Session) { ran.Store(true) })}
	out, _ := testsession.New(t, srv, nil).CombinedOutput("")

	assert.False(t, ran.Load(), "a session started after the drain began")
	assert.Contains(t, string(out), "restarting")
}

func TestSessionTracker_RefusesBeyondCapacityWithDistinctError(t *testing.T) {
	t.Parallel()
	tracker := NewSessionTracker(2)
	_, err := tracker.Connect(testutil.UID(1), nil, nil)
	require.NoError(t, err)
	gen, err := tracker.Connect(testutil.UID(2), nil, nil)
	require.NoError(t, err)
	_, err = tracker.Connect(testutil.UID(3), nil, nil)
	require.ErrorIs(t, err, errServerFull)

	// A second session for an already-connected account displaces rather than
	// failing: half-open TCP otherwise blocks the mid-game reconnect grace.
	gen1, err := tracker.Connect(testutil.UID(1), nil, nil)
	require.NoError(t, err)
	gen2, err := tracker.Connect(testutil.UID(1), nil, nil)
	require.NoError(t, err)
	assert.NotEqual(t, gen1, gen2)
	assert.False(t, release(tracker, testutil.UID(1), gen1), "stale generation must not free the slot")
	assert.Equal(t, 2, tracker.Count())
	assert.True(t, release(tracker, testutil.UID(1), gen2))

	assert.True(t, release(tracker, testutil.UID(2), gen))
	_, err = tracker.Connect(testutil.UID(3), nil, nil)
	require.NoError(t, err, "capacity frees with the seat")
}

func TestSessionTracker_ReleaseWithFreesTheSlotWhenTheTeardownPanics(t *testing.T) {
	t.Parallel()
	tracker := NewSessionTracker(0)
	user := testutil.UID(51)
	gen, err := tracker.Connect(user, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, tracker.Count())

	assert.Panics(t, func() {
		tracker.ReleaseWith(user, gen, func() { panic("disconnect") })
	})
	assert.Equal(t, 0, tracker.Count(), "a panicking teardown still frees the slot")
	_, err = tracker.Connect(user, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 1, tracker.Count())
}

// ReleaseWith is what orders an old session's teardown before a reconnect: while
// the teardown's DisconnectPlayer runs, the reconnect's Connect has to wait, or it
// resumes a seat the teardown then puts on a grace timer.
func TestSessionTracker_ReleaseWithHoldsOffTheReconnect(t *testing.T) {
	t.Parallel()
	tracker := NewSessionTracker(0)
	user := testutil.UID(21)
	gen, err := tracker.Connect(user, nil, nil)
	require.NoError(t, err)

	inTeardown, finish := make(chan struct{}), make(chan struct{})
	released := make(chan bool, 1)
	go func() {
		released <- tracker.ReleaseWith(user, gen, func() { close(inTeardown); <-finish })
	}()
	<-inTeardown

	reconnected := make(chan struct{})
	go func() {
		_, _ = tracker.Connect(user, nil, nil)
		close(reconnected)
	}()
	select {
	case <-reconnected:
		t.Fatal("the reconnect ran while the old session was still tearing down")
	case <-time.After(50 * time.Millisecond):
	}
	close(finish)
	<-reconnected
	assert.True(t, <-released)
	assert.Equal(t, 1, tracker.Count(), "the reconnect's slot survived the teardown")

	ran := false
	assert.False(t, tracker.ReleaseWith(user, gen, func() { ran = true }), "a stale generation freed the slot")
	assert.False(t, ran, "and ran its teardown against the live session's seat")
}

// The teardown ReleaseWith holds the account's lock across goes all the way down to a
// table's engine. Only that account's reconnect has to wait for it: under one lock
// for every account, any slow disconnect stalled every login on the server.
func TestSessionTracker_ATeardownDoesNotHoldOffOtherAccounts(t *testing.T) {
	t.Parallel()
	tracker := NewSessionTracker(0)
	gen, err := tracker.Connect(testutil.UID(41), nil, nil)
	require.NoError(t, err)

	inTeardown, finish := make(chan struct{}), make(chan struct{})
	released := make(chan struct{})
	go func() {
		tracker.ReleaseWith(testutil.UID(41), gen, func() { close(inTeardown); <-finish })
		close(released)
	}()
	<-inTeardown

	connected := make(chan error, 1)
	go func() {
		_, err := tracker.Connect(testutil.UID(42), nil, nil)
		connected <- err
	}()
	select {
	case err := <-connected:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("another account's login waited for this account's teardown")
	}
	close(finish)
	<-released
	assert.Equal(t, 1, tracker.Count())
}

// An entry outlives its session only while someone is waiting on it, or a server
// that has seen every account once would keep one per account forever.
func TestSessionTracker_ForgetsAnAccountOnceItsSessionEnds(t *testing.T) {
	t.Parallel()
	tracker := NewSessionTracker(1)
	gen, err := tracker.Connect(testutil.UID(51), nil, nil)
	require.NoError(t, err)
	_, err = tracker.Connect(testutil.UID(52), nil, nil)
	require.ErrorIs(t, err, errServerFull)
	require.True(t, release(tracker, testutil.UID(51), gen))

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	assert.Empty(t, tracker.accounts, "a released or refused account kept its entry")
}

// Count is what the public stats API reads on every poll. ReleaseWith holds the
// account's lock across a teardown that goes all the way down to a table's engine,
// so a Count that waited on it left /v1/stats behind the slowest disconnect.
func TestSessionTracker_CountDoesNotWaitForATeardown(t *testing.T) {
	t.Parallel()
	tracker := NewSessionTracker(0)
	gen, err := tracker.Connect(testutil.UID(31), nil, nil)
	require.NoError(t, err)
	_, err = tracker.Connect(testutil.UID(32), nil, nil)
	require.NoError(t, err)

	inTeardown, finish := make(chan struct{}), make(chan struct{})
	released := make(chan struct{})
	go func() {
		tracker.ReleaseWith(testutil.UID(31), gen, func() { close(inTeardown); <-finish })
		close(released)
	}()
	<-inTeardown

	counted := make(chan int, 1)
	go func() { counted <- tracker.Count() }()
	select {
	case n := <-counted:
		assert.Equal(t, 2, n, "the slot being torn down is still held until the teardown ends")
	case <-time.After(time.Second):
		t.Fatal("Count waited for the teardown holding the tracker lock")
	}
	close(finish)
	<-released
	assert.Equal(t, 1, tracker.Count())
}

// panicModel panics from whichever method the test asks for.
type panicModel struct{ on string }

func (m panicModel) Init() tea.Cmd {
	if m.on == "init" {
		panic("init boom")
	}
	return nil
}

func (m panicModel) Update(tea.Msg) (tea.Model, tea.Cmd) {
	if m.on == "update" {
		panic("update boom")
	}
	return m, nil
}

func (m panicModel) View() tea.View {
	if m.on == "view" {
		panic("view boom")
	}
	return tea.NewView("")
}

// A TUI panic has to be reported against the session's own span before it leaves
// our code: bubbletea's recover is what keeps it off the process, but it knows
// nothing about the span, the metric or the trace.
func TestReportingModel_RecordsPanicsAndLetsThemUnwind(t *testing.T) {
	t.Parallel()

	for _, method := range []string{"init", "update", "view"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			s := &stubSession{}
			st := &sessionState{traceCtx: t.Context()}
			reg := &sessionRegistry{}
			reg.store(s, st)

			m := reportingModel{Model: panicModel{on: method}, session: s, reg: reg}
			require.Panics(t, func() {
				switch method {
				case "init":
					m.Init()
				case "update":
					m.Update(nil)
				case "view":
					m.View()
				}
			}, "the panic still unwinds, so bubbletea ends the program")

			assert.True(t, st.panicked, "the session is marked as having panicked")
		})
	}
}

// A model that does not panic must pass its command and view through untouched.
func TestReportingModel_PassesThroughWhenNothingPanics(t *testing.T) {
	t.Parallel()

	s := &stubSession{}
	reg := &sessionRegistry{}
	reg.store(s, &sessionState{traceCtx: t.Context()})

	m := reportingModel{Model: panicModel{on: "none"}, session: s, reg: reg}

	assert.Nil(t, m.Init())
	got, cmd := m.Update(nil)
	assert.Nil(t, cmd)
	assert.IsType(t, reportingModel{}, got, "the wrapper survives an update")
}

// countingCloser stands in for the displaced session's connection.
type countingCloser struct {
	closed atomic.Int32
	err    error
}

func (c *countingCloser) Close() error {
	c.closed.Add(1)
	return c.err
}

// "Displaces" has to mean the old session is hung up on, not merely forgotten. A
// tracker that only reassigns the generation leaves the zombie running its TUI and
// holding a lobby subscription until its TCP dies, so one keypair can hold as many
// live sessions as it opens while Count reports one.
func TestSessionTracker_ConnectClosesTheDisplacedSession(t *testing.T) {
	t.Parallel()
	tracker := NewSessionTracker(1)
	first := &countingCloser{}

	gen1, err := tracker.Connect(testutil.UID(7), first, nil)
	require.NoError(t, err)
	assert.Zero(t, first.closed.Load(), "nothing is displaced yet")

	second := &countingCloser{err: errors.New("already gone")}
	gen2, err := tracker.Connect(testutil.UID(7), second, nil)
	require.NoError(t, err)

	assert.NotEqual(t, gen1, gen2)
	assert.Equal(t, int32(1), first.closed.Load(), "the displaced session is closed exactly once")
	assert.Zero(t, second.closed.Load(), "the live session is left alone")
	assert.Equal(t, 1, tracker.Count(), "displacement does not grow the count")

	// A Close error is the peer already being gone, which is the common case here and
	// must not stop the new session from being tracked.
	assert.False(t, release(tracker, testutil.UID(7), gen1), "the displaced generation frees nothing")
	assert.True(t, release(tracker, testutil.UID(7), gen2))
}
