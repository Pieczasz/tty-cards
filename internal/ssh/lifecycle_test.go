package ssh

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/lobby"

	"uuid"

	tea "charm.land/bubbletea/v2"
	"charm.land/ssh"
	"charm.land/wish/v2/testsession"
	"github.com/Pieczasz/terminal-card/internal/testutil"
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
	gen, err := tracker.Connect(user.ID, nil)
	require.NoError(t, err)
	deps := Deps{LobbyManager: lobby.NewManager(t.Context(), nil), Tracker: tracker}
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

	manager := lobby.NewManager(t.Context(), nil)
	host := &db.User{ID: testutil.UID(1), Username: "host"}
	guest := &db.User{ID: testutil.UID(2), Username: "guest"}
	guestPlayer := lobby.NewPlayer(guest)

	table, err := manager.New(lobby.NewPlayer(host), lobby.WithCardGame("Mock"))
	require.NoError(t, err)
	_, err = manager.JoinLobbyByCode(table.Code(), guestPlayer)
	require.NoError(t, err)

	tracker := NewSessionTracker(0)
	oldGen, err := tracker.Connect(guest.ID, nil)
	require.NoError(t, err)
	deps := Deps{LobbyManager: manager, Tracker: tracker}
	reg := &sessionRegistry{}

	// The reconnect displaces the zombie session before teardown runs, so
	// releaseSession sees a stale generation and leaves the seat alone.
	reconnected := make(chan struct{})
	displaced := make(chan struct{})
	reconnect := func() {
		defer close(reconnected)
		_, err := tracker.Connect(guest.ID, nil)
		if err != nil {
			t.Errorf("displace reconnect failed: %v", err)
			return
		}
		close(displaced)
		if resumed := manager.ResumePlayer(guestPlayer); resumed != table {
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

func TestSessionTracker_RefusesBeyondCapacityWithDistinctError(t *testing.T) {
	t.Parallel()
	tracker := NewSessionTracker(2)
	_, err := tracker.Connect(testutil.UID(1), nil)
	require.NoError(t, err)
	gen, err := tracker.Connect(testutil.UID(2), nil)
	require.NoError(t, err)
	_, err = tracker.Connect(testutil.UID(3), nil)
	require.ErrorIs(t, err, ErrServerFull)

	// A second session for an already-connected account displaces rather than
	// failing: half-open TCP otherwise blocks the mid-game reconnect grace.
	gen1, err := tracker.Connect(testutil.UID(1), nil)
	require.NoError(t, err)
	gen2, err := tracker.Connect(testutil.UID(1), nil)
	require.NoError(t, err)
	assert.NotEqual(t, gen1, gen2)
	assert.False(t, release(tracker, testutil.UID(1), gen1), "stale generation must not free the slot")
	assert.Equal(t, 2, tracker.Count())
	assert.True(t, release(tracker, testutil.UID(1), gen2))

	assert.True(t, release(tracker, testutil.UID(2), gen))
	_, err = tracker.Connect(testutil.UID(3), nil)
	require.NoError(t, err, "capacity frees with the seat")
}

// ReleaseWith is what orders an old session's teardown before a reconnect: while
// the teardown's DisconnectPlayer runs, the reconnect's Connect has to wait, or it
// resumes a seat the teardown then puts on a grace timer.
func TestSessionTracker_ReleaseWithHoldsOffTheReconnect(t *testing.T) {
	t.Parallel()
	tracker := NewSessionTracker(0)
	user := testutil.UID(21)
	gen, err := tracker.Connect(user, nil)
	require.NoError(t, err)

	inTeardown, finish := make(chan struct{}), make(chan struct{})
	released := make(chan bool, 1)
	go func() {
		released <- tracker.ReleaseWith(user, gen, func() { close(inTeardown); <-finish })
	}()
	<-inTeardown

	reconnected := make(chan struct{})
	go func() {
		_, _ = tracker.Connect(user, nil)
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

	gen1, err := tracker.Connect(testutil.UID(7), first)
	require.NoError(t, err)
	assert.Zero(t, first.closed.Load(), "nothing is displaced yet")

	second := &countingCloser{err: errors.New("already gone")}
	gen2, err := tracker.Connect(testutil.UID(7), second)
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
