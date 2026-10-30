package ssh

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"charm.land/log/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedBuffer is a log sink the server's goroutines write while the test reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A closing connection ends its programs through runProgram's Quit, a clean exit. A
// run context tied to the connection's as well killed the program first, and every
// ordinary disconnect was logged as an ERROR, by wish and then by runProgram.
//
//nolint:paralleltest // swaps the process-wide slog and charm loggers
func TestNewServer_ADisconnectIsNotLoggedAsAnError(t *testing.T) {
	charm := &lockedBuffer{}
	prev := log.Default()
	log.SetDefault(log.New(charm))
	t.Cleanup(func() { log.SetDefault(prev) })
	logs := captureLogs(t)
	addr, tracker := startInProcessServer(t, &db.User{ID: testutil.UID(97), Username: "leaver"})
	client := dialInProcess(t, addr, "leaver")
	openShell(t, client)
	require.Eventually(t, func() bool { return tracker.Count() == 1 }, 3*time.Second, 10*time.Millisecond)
	// Long enough for the program to be running before the connection goes.
	time.Sleep(200 * time.Millisecond)

	require.NoError(t, client.Close())
	// The program's exit is reported before the teardown frees the slot.
	require.Eventually(t, func() bool { return tracker.Count() == 0 }, 5*time.Second, 10*time.Millisecond)

	assert.NotContains(t, charm.String(), "app exit with error")
	for _, r := range logs.snapshot() {
		assert.Less(t, r.Level, slog.LevelError, "a disconnect logged %q at %s", r.Message, r.Level)
	}
}
