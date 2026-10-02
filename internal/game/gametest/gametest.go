package gametest

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/Pieczasz/tty-cards/internal/game"

	"github.com/stretchr/testify/require"
)

// ForeignAction is a move from some other game: the validator is the only thing
// between it and an ApplyAction that has no case for it.
type ForeignAction struct{}

func (ForeignAction) Name() string { return "gametest.ForeignAction" }

// TB is the slice of *testing.T and *rapid.T the helpers need, so a property failure
// is reported to rapid, which then shrinks the counterexample.
type TB interface {
	require.TestingT
	Helper()
}

// Extra is the table's Extra as E, failing the test rather than panicking on
// anything else.
func Extra[E any](t TB, s *game.State) E {
	t.Helper()
	e, ok := s.Extra.(E)
	require.True(t, ok, "state.Extra is %T, not %T", s.Extra, e)
	return e
}

// LogSink is where a test's table logger writes, decoded back into records. The engine's
// timer goroutine may write while the test reads.
type LogSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *LogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p) //nolint:wrapcheck // bytes.Buffer never fails a write
}

// SinkLobby is the lobby code every line through a LogSink's Logger carries.
const SinkLobby = "TESTLOBBY"

// Logger is a table logger into the sink, carrying a lobby and game as the lobby's
// does, for game.WithLogger.
func (s *LogSink) Logger() *slog.Logger {
	h := slog.NewJSONHandler(s, &slog.HandlerOptions{Level: slog.LevelDebug})
	return slog.New(h).With("lobby", SinkLobby, "game", "test")
}

// Records is every line written so far, decoded.
func (s *LogSink) Records(t TB) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	dec := json.NewDecoder(bytes.NewReader(s.buf.Bytes()))
	for dec.More() {
		var rec map[string]any
		require.NoError(t, dec.Decode(&rec))
		out = append(out, rec)
	}
	return out
}
