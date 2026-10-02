package tuitest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A key a test presses has to be the key a view's switch matches, or the test drives
// a binding nobody can reach.
func TestKey_SpellsWhatItWasGiven(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"esc", "enter", "backspace", "tab", "up", "down", "left", "right", "pgup", "pgdown",
		"space", "ctrl+c", "a", "Y", "3", "?",
	} {
		assert.Equal(t, s, Key(s).String())
	}
}

func TestStripANSI(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "red", StripANSI("\x1b[31mred\x1b[0m"))
}

type ctxKey struct{}

//nolint:paralleltest // CaptureLogs swaps the process-wide slog.Default
func TestCaptureLogs_KeepsLevelMessageAndContext(t *testing.T) {
	var logs *Logs
	t.Run("capturing", func(t *testing.T) {
		logs = CaptureLogs(t)
		ctx := context.WithValue(t.Context(), ctxKey{}, "session")
		slog.With("k", "v").WithGroup("g").DebugContext(ctx, "quiet")
		slog.ErrorContext(ctx, "loud")
	})

	errs := logs.AtLeast(slog.LevelError)
	require.Len(t, errs, 1)
	assert.Equal(t, "loud", errs[0].Message)
	assert.Equal(t, "session", errs[0].Ctx.Value(ctxKey{}))
	assert.Len(t, logs.AtLeast(slog.LevelDebug), 2, "debug is captured too")

	slog.Error("after the test")
	assert.Len(t, logs.AtLeast(slog.LevelDebug), 2, "the default is restored on cleanup")
}

func TestRecord_SameTrace(t *testing.T) {
	t.Parallel()
	traced := TracedContext(t)
	assert.True(t, Record{Ctx: traced}.SameTrace(traced))
	assert.False(t, Record{Ctx: t.Context()}.SameTrace(traced), "a record logged without the trace")
	assert.False(t, Record{}.SameTrace(traced), "a record logged with no context at all")
	assert.False(t, Record{Ctx: traced}.SameTrace(t.Context()), "there is no trace to match")
}
