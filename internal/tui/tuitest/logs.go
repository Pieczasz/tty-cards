package tuitest

import (
	"context"
	"log"
	"log/slog"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

// Record is one captured log line and the context it was logged under, which is what
// ties it to the session's trace.
type Record struct {
	Level   slog.Level
	Message string
	Ctx     context.Context //nolint:containedctx // the context is what a test inspects
}

// Logs is every record the process logged since CaptureLogs, at every level.
type Logs struct {
	mu      sync.Mutex
	records []Record
}

// CaptureLogs routes slog.Default into the returned Logs until t ends. slog.Default is
// process-wide, so a test calling it cannot run in parallel.
func CaptureLogs(t *testing.T) *Logs {
	t.Helper()
	logs := &Logs{}
	original, out, flags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(logs))
	// SetDefault also points the log package at the new handler, and restoring the
	// stock slog handler does not point it back.
	t.Cleanup(func() {
		slog.SetDefault(original)
		log.SetOutput(out)
		log.SetFlags(flags)
	})
	return logs
}

// AtLeast is the records logged at level or above.
func (l *Logs) AtLeast(level slog.Level) []Record {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Record
	for _, r := range l.records {
		if r.Level >= level {
			out = append(out, r)
		}
	}
	return out
}

// Enabled captures every level: a test asserting a user error is not an ERROR wants
// to see what it was logged at instead.
func (l *Logs) Enabled(context.Context, slog.Level) bool { return true }

// Handle keeps the record's level, message and context.
func (l *Logs) Handle(ctx context.Context, r slog.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, Record{Level: r.Level, Message: r.Message, Ctx: ctx})
	return nil
}

// WithAttrs drops the attributes, which no test compares.
func (l *Logs) WithAttrs([]slog.Attr) slog.Handler { return l }

// WithGroup drops the group, which no test compares.
func (l *Logs) WithGroup(string) slog.Handler { return l }

// TracedContext is a context carrying a sampled span, standing in for the session
// span the ssh layer starts.
func TracedContext(t *testing.T) context.Context {
	t.Helper()
	return trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled,
	}))
}

// SameTrace reports whether the record was logged under ctx's trace.
func (r Record) SameTrace(ctx context.Context) bool {
	want := trace.SpanContextFromContext(ctx)
	return want.IsValid() && r.Ctx != nil && trace.SpanContextFromContext(r.Ctx).TraceID() == want.TraceID()
}
