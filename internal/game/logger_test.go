package game

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lockedBuffer is a log sink the timer goroutine and the test can share.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// records decodes every JSON line written so far.
func (b *lockedBuffer) records(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(b.buf.Bytes()))
	for dec.More() {
		var rec map[string]any
		require.NoError(t, dec.Decode(&rec))
		out = append(out, rec)
	}
	return out
}

// "hand 7" matches every table on the server; the engine's own lines have to name
// the one they are about, which is whatever the logger it was given carries.
func TestEngine_LogsThroughItsTableLogger(t *testing.T) {
	t.Parallel()
	sink := &lockedBuffer{}
	table := slog.New(slog.NewJSONHandler(sink, nil)).With("lobby", "ABCD1234", "game", "stub")

	// No safe move, so the expiry takes the seat: a Warn and an Info from the clock.
	engine := NewEngine(&timeoutRules{}, []*Player{{ID: "a"}, {ID: "b"}},
		WithTurnTimeout(time.Hour), WithLogger(table))

	t.Cleanup(engine.Close)
	require.NoError(t, engine.Start())

	fireTurnTimeout(t, engine)

	recs := sink.records(t)
	require.Len(t, recs, 2, "the clock's no-safe-move warning and the idle removal")
	for _, rec := range recs {
		assert.Equal(t, "ABCD1234", rec["lobby"], "%v", rec["msg"])
		assert.Equal(t, "stub", rec["game"], "%v", rec["msg"])
	}

	var seen *slog.Logger
	engine.WithState(func(s *State) { seen = s.Logger() })
	assert.Same(t, table, seen, "the rules log through the same table logger")
}

// A State the rules build by hand, as their own tests do, must still log somewhere.
func TestState_LoggerDefaultsToTheProcessLogger(t *testing.T) {
	t.Parallel()
	assert.Same(t, slog.Default(), (&State{}).Logger())

	engine := NewEngine(&timeoutRules{}, []*Player{{ID: "a"}}, WithLogger(nil))
	t.Cleanup(engine.Close)
	engine.WithState(func(s *State) {
		assert.Same(t, slog.Default(), s.Logger(), "a nil logger is no logger, not a panic")
	})
}
