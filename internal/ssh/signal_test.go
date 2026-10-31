package ssh

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// A SIGTERM or SIGINT is the process drain's to act on: Shutdown stops accepting and
// the tables play on through the window. A session program with bubbletea's own
// signal handler quit on the signal itself, so every session ended the instant it
// arrived and the drain had nobody left to drain.
//
//nolint:paralleltest // raises real signals at the test process
func TestNewServer_ASignalLeavesTheSessionsToTheDrain(t *testing.T) {
	// Registered first, or the signal takes the test binary down with it.
	caught := make(chan os.Signal, 1)
	signal.Notify(caught, syscall.SIGINT, syscall.SIGTERM)
	t.Cleanup(func() { signal.Stop(caught) })

	server, addr, tracker := startServer(t,
		stubUserRepo{user: &db.User{ID: testutil.UID(95), Username: "drained"}},
		newSessionRegistry(cellBudget, extraCellBudget))
	s, err := dialInProcess(t, addr, "drained").NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	out := &countingDiscard{}
	s.Stdout, s.Stderr = out, io.Discard
	require.NoError(t, s.RequestPty("xterm", 24, 80, gossh.TerminalModes{}))
	require.NoError(t, s.Shell())
	ended := make(chan struct{})
	go func() {
		_ = s.Wait()
		close(ended)
	}()
	// bubbletea installs its handlers before it draws the first frame.
	require.Eventually(t, func() bool { return tracker.Count() == 1 && out.n.Load() > 0 },
		5*time.Second, 10*time.Millisecond, "the session never drew its first frame")

	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		require.NoError(t, syscall.Kill(os.Getpid(), sig))
		<-caught
	}
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, server.Shutdown(ctx), context.DeadlineExceeded,
		"the drain window found no connection left to wait for")

	select {
	case <-ended:
		t.Fatal("the signal ended the session before the drain did")
	default:
	}
	assert.Equal(t, 1, tracker.Count(), "the session gave up its seat before the drain")
}
