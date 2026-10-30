package ssh

import (
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	tea "charm.land/bubbletea/v2"
	"charm.land/ssh"
	bm "charm.land/wish/v2/bubbletea"
	"charm.land/wish/v2/testsession"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// blockedModel's event loop stops at its first message until release closes, as a
// session's does when its peer stops taking output: the renderer holds its lock
// across a write the ssh window will not take, and the next render waits on it.
type blockedModel struct {
	// entered is told once the loop is stopped, which is after the session's every
	// read of its pty: charm ssh writes the size a resize carries without a lock.
	entered chan<- struct{}
	release <-chan struct{}
}

func (blockedModel) Init() tea.Cmd { return nil }

func (m blockedModel) Update(tea.Msg) (tea.Model, tea.Cmd) {
	select {
	case m.entered <- struct{}{}:
	default:
	}
	<-m.release
	return m, nil
}

func (blockedModel) View() tea.View { return tea.NewView("") }

// wish hands window changes to the program from the one channel charm ssh fills
// with a blocking send, so a program that stopped taking messages stopped charm ssh
// reading the session's requests. The connection behind it then wedged as in
// TestNewServer_WindowChangeBeforeShellDoesNotWedge, and closing it no longer ended
// the session, which kept its tracker slot until a restart.
func TestSessionLifecycle_AResizeNeverWaitsOnTheProgram(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}, 1), make(chan struct{})
	srv := &ssh.Server{Handler: sessionLifecycle(Deps{}, &sessionRegistry{})(
		bm.MiddlewareWithProgramHandler(func(s ssh.Session) *tea.Program {
			return tea.NewProgram(blockedModel{entered: entered, release: release}, bm.MakeOptions(s)...)
		})(func(ssh.Session) {}))}
	session := testsession.New(t, srv, nil)
	t.Cleanup(func() { close(release) }) // runs before testsession's own cleanup
	require.NoError(t, session.RequestPty("xterm", 40, 80, gossh.TerminalModes{}))
	require.NoError(t, session.Shell())
	<-entered

	for i := range 40 {
		within(t, "a resize", func() {
			_, _ = session.SendRequest("window-change", true, windowChangePayload(uint32(81+i), 40))
		})
	}
}

func TestFilterSessionMsg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  tea.Msg
		want tea.Msg
	}{
		{
			name: "a resize is left to the size budget",
			msg:  tea.WindowSizeMsg{Width: 4_000_000, Height: 3_000_000},
			want: tea.WindowSizeMsg{Width: 4_000_000, Height: 3_000_000},
		},
		{
			name: "suspend is still answered with resume",
			msg:  tea.SuspendMsg{},
			want: tea.ResumeMsg{},
		},
		{
			name: "anything else passes through",
			msg:  tea.QuitMsg{},
			want: tea.QuitMsg{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, filterSessionMsg(nil, tt.msg))
		})
	}
}

type panickyModel struct{}

func (panickyModel) Close() { panic("closing the view exploded") }

// The assertion on the message matters as much as the one on the tracker: charm ssh
// recovers panics itself, so a test that only checks the slot was freed passes even
// with `defer recoverSession(s)` deleted.
func TestSessionLifecycle_PanicClosingTheViewStillReleasesTheSession(t *testing.T) {
	t.Parallel()
	tracker := NewSessionTracker(0)
	user := &db.User{ID: testutil.UID(7)}
	gen, err := tracker.Connect(user.ID, nil)
	require.NoError(t, err)

	deps := Deps{LobbyManager: lobby.NewManager(t.Context(), nil, catalog.NewRegistry()), Tracker: tracker}
	reg := &sessionRegistry{}
	srv := &ssh.Server{
		Handler: sessionLifecycle(deps, reg)(func(s ssh.Session) {
			st, ok := reg.load(s)
			require.True(t, ok)
			st.user = user
			st.gen = gen
			st.model = panickyModel{}
		}),
	}

	out, _ := testsession.New(t, srv, nil).CombinedOutput("")

	assert.Contains(t, string(out), "An unexpected internal error occurred",
		"recoverSession did not handle the panic; the library swallowed it instead")
	require.Eventually(t, func() bool { return tracker.Count() == 0 }, 2*time.Second, 10*time.Millisecond,
		"the session slot was stranded, so this player can never reconnect")
}
