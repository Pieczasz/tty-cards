package ssh

import (
	"context"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/lobby"

	tea "charm.land/bubbletea/v2"
	"charm.land/ssh"
	"charm.land/wish/v2/testsession"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBoundedPty_RefusesGeometryNoTerminalHas(t *testing.T) {
	t.Parallel()

	srv := &ssh.Server{}
	require.NoError(t, srv.SetOption(boundedPty()))

	tests := []struct {
		name   string
		window ssh.Window
		want   bool
	}{
		{name: "an ordinary terminal", window: ssh.Window{Width: 80, Height: 24}, want: true},
		{name: "a very wide one", window: ssh.Window{Width: maxTerminalWidth, Height: maxTerminalHeight}, want: true},
		{name: "zero is left to the pty layer", window: ssh.Window{}, want: true},
		{name: "one column too many", window: ssh.Window{Width: maxTerminalWidth + 1, Height: 24}, want: false},
		{name: "one row too many", window: ssh.Window{Width: 80, Height: maxTerminalHeight + 1}, want: false},
		{name: "a whole uint32 of columns", window: ssh.Window{Width: 4_294_967_295, Height: 24}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, srv.PtyCallback(nil, ssh.Pty{Window: tt.window}))
		})
	}
}

func TestClampWindowSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		msg  tea.Msg
		want tea.Msg
	}{
		{
			name: "a real resize is untouched",
			msg:  tea.WindowSizeMsg{Width: 120, Height: 40},
			want: tea.WindowSizeMsg{Width: 120, Height: 40},
		},
		{
			name: "an absurd resize is bounded",
			msg:  tea.WindowSizeMsg{Width: 4_000_000, Height: 3_000_000},
			want: tea.WindowSizeMsg{Width: maxTerminalWidth, Height: maxTerminalHeight},
		},
		{
			name: "only the dimension out of range moves",
			msg:  tea.WindowSizeMsg{Width: 4_000_000, Height: 40},
			want: tea.WindowSizeMsg{Width: maxTerminalWidth, Height: 40},
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
			assert.Equal(t, tt.want, clampWindowSize(nil, tt.msg))
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

	deps := ServerDependencies{LobbyManager: lobby.NewManager(context.Background(), nil), Tracker: tracker}
	reg := &sessionRegistry{}
	srv := &ssh.Server{
		Handler: sessionLifecycle(deps, reg)(func(s ssh.Session) {
			st, ok := reg.load(s)
			require.True(t, ok)
			st.owns = true
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
