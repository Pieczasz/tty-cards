package lobby

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Stop cannot recall a timer whose callback is already waiting on Manager.mu. If the
// player drops again in that window the stale callback used to find the new hold and
// expire it early; it now names the generation it was armed with and is refused.
func TestGraceTimers_AStaleCallbackCannotExpireANewerHold(t *testing.T) {
	t.Parallel()
	d := newGraceTimers()
	t.Cleanup(func() { d.clear("p") })

	d.arm("p", time.Hour, func(uint64) {})
	stale := d.pending["p"].generation
	d.arm("p", time.Hour, func(uint64) {})
	current := d.pending["p"].generation
	require.NotEqual(t, stale, current)

	assert.False(t, d.beginExpire("p", stale), "the replaced hold's callback does nothing")
	assert.Contains(t, d.pending, "p", "and the newer hold is still pending")
	assert.True(t, d.beginExpire("p", current))
}

func TestGraceTimers_AnyGenerationClaimsThePendingHold(t *testing.T) {
	t.Parallel()
	d := newGraceTimers()
	d.arm("p", time.Hour, func(uint64) {})

	assert.True(t, d.beginExpire("p", anyGeneration))
	assert.False(t, d.beginExpire("p", anyGeneration), "nothing is left to claim")
}
