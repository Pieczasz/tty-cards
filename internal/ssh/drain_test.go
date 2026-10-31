package ssh

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	tea "charm.land/bubbletea/v2"
	"charm.land/ssh"
	bm "charm.land/wish/v2/bubbletea"
	"charm.land/wish/v2/testsession"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// terminalRestore is what bubbletea's teardown writes to undo the session's start:
// leave the alt screen, show the cursor, stop bracketed paste, pop the kitty
// keyboard mode. Without it the player's shell is left on a hidden cursor in the
// alt screen, its input garbled by the keyboard protocol.
var terminalRestore = []string{"\x1b[?1049l", "\x1b[?25h", "\x1b[?2004l", "\x1b[<1u"}

// shellRecording opens a pty session on client and records everything it is sent,
// stdout and stderr alike, once it has switched to the alt screen its teardown has
// to leave.
func shellRecording(t *testing.T, client *gossh.Client) (*gossh.Session, *lockedBuffer) {
	t.Helper()
	s, err := client.NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	out := &lockedBuffer{}
	s.Stdout, s.Stderr = out, out
	require.NoError(t, s.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{}))
	require.NoError(t, s.Shell())
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "\x1b[?1049h") },
		5*time.Second, 10*time.Millisecond, "the session never entered the alt screen")
	return s, out
}

// assertRestored checks got for every restore sequence.
func assertRestored(t *testing.T, got string) {
	t.Helper()
	for _, seq := range terminalRestore {
		assert.Containsf(t, got, seq, "the client's terminal was not restored: no %q", seq)
	}
}

// When the drain window is over, Close hangs up on whoever is left. It used to be
// all that happened: the client got no word of why and kept the alt screen, the
// hidden cursor and the kitty keyboard mode the session had switched on.
func TestServer_EndSessionsRestoresTheTerminalAndSaysWhy(t *testing.T) {
	t.Parallel()
	server, addr, tracker := startServer(t,
		stubUserRepo{user: &db.User{ID: testutil.UID(96), Username: "restarted"}},
		newSessionRegistry(cellBudget, extraCellBudget))
	s, out := shellRecording(t, dialInProcess(t, addr, "restarted"))
	require.Eventually(t, func() bool { return tracker.Count() == 1 }, 5*time.Second, 10*time.Millisecond)

	// cmd/server's drain: a window that the session outlasts, then the end.
	window, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, server.Shutdown(window), context.DeadlineExceeded)
	ending, cancelEnding := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelEnding()
	require.NoError(t, server.EndSessions(ending), "a session that could quit did not")
	_ = server.Close()
	within(t, "the session's end", func() { _ = s.Wait() })

	assertRestored(t, out.String())
	assert.Contains(t, out.String(), restartNotice, "the client was never told why it was closed")
}

// A program can be stuck where a quit never reaches it: a peer that stops granting
// ssh window holds the event loop in a render, as blockedModel does. Ending the
// sessions must give up on it when its context ends, and leave it to Close.
func TestServer_EndSessionsDoesNotWaitOnAWedgedSession(t *testing.T) {
	t.Parallel()
	reg := newSessionRegistry(cellBudget, extraCellBudget)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	srv := &ssh.Server{Handler: sessionLifecycle(Deps{}, reg)(
		reg.runProgram(func(s ssh.Session) *tea.Program {
			return tea.NewProgram(blockedModel{entered: entered, release: release},
				append(bm.MakeOptions(s), tea.WithoutSignalHandler())...)
		})(func(ssh.Session) {}))}
	server := &Server{Server: srv, sessions: reg}
	session := testsession.New(t, srv, nil)
	t.Cleanup(func() { close(release) }) // runs before testsession's own cleanup
	require.NoError(t, session.RequestPty("xterm", 24, 80, gossh.TerminalModes{}))
	require.NoError(t, session.Shell())
	<-entered

	ending, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	within(t, "ending a wedged session", func() {
		assert.Error(t, server.EndSessions(ending), "a wedged session was reported as ended")
	})
	_ = server.Close()
	within(t, "closing the wedged session", func() { _ = session.Wait() })
}
