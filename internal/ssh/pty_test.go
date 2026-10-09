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
	"charm.land/wish/v2/testsession"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
