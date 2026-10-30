package drain

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestMain fails the package if any waiter outlives its test.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestGate_WaitReturnsOnceEveryEntryIsReleased(t *testing.T) {
	t.Parallel()
	var g Gate
	release, ok := g.Enter()
	require.True(t, ok)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	require.False(t, g.Wait(ctx), "an entry still in flight must hold the drain")

	release()
	require.True(t, g.Wait(t.Context()))
}

func TestGate_EnterAfterCloseIsRefused(t *testing.T) {
	t.Parallel()
	var g Gate
	g.Close()
	release, ok := g.Enter()
	assert.False(t, ok, "a closed gate admitted an entry")
	release()
	require.True(t, g.Wait(t.Context()), "a refused entry was counted")
}

func TestGate_EnterAfterWaitIsRefused(t *testing.T) {
	t.Parallel()
	var g Gate
	require.True(t, g.Wait(t.Context()))
	_, ok := g.Enter()
	assert.False(t, ok, "Wait must close the gate")
}

// The nil-engine path and the watcher goroutine in lobby can both release the same
// entry; a second Done would drive the WaitGroup negative and panic.
func TestGate_ReleaseTwiceDoesNotPanic(t *testing.T) {
	t.Parallel()
	var g Gate
	release, ok := g.Enter()
	require.True(t, ok)
	release()
	release()
	require.True(t, g.Wait(t.Context()))
}

// A drain that times out is retried by design. Each retry used to strand its own
// waiter goroutine; the one waiter is started once, reused, and exits with the last
// release.
func TestGate_TimedOutWaitsShareOneWaiter(t *testing.T) {
	t.Parallel()
	var g Gate
	release, ok := g.Enter()
	require.True(t, ok)

	first := timedOutWait(t, &g)
	second := timedOutWait(t, &g)
	assert.Equal(t, first, second, "a second Wait started a second waiter")

	release()
	require.True(t, g.Wait(t.Context()))
}

// timedOutWait runs a Wait that must time out and returns the waiter's channel.
func timedOutWait(t *testing.T, g *Gate) chan struct{} {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	require.False(t, g.Wait(ctx))
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.done
}
