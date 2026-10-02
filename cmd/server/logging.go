package main

import (
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
)

// installLogging sends every record at level or above both to stderr as JSON and to
// the OTLP pipeline observability.Setup installs.
func installLogging(level slog.Level) {
	slog.SetDefault(slog.New(slog.NewMultiHandler(
		slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}),
		levelGate{Handler: otelslog.NewHandler("tty-cards"), level: level},
	)))
}

// levelGate gives the otelslog handler, which has no level option of its own, the
// same minimum level as the stderr one.
type levelGate struct {
	slog.Handler
	level slog.Level
}

func (g levelGate) Enabled(_ context.Context, l slog.Level) bool { return l >= g.level }

func (g levelGate) WithAttrs(attrs []slog.Attr) slog.Handler {
	return levelGate{Handler: g.Handler.WithAttrs(attrs), level: g.level}
}

func (g levelGate) WithGroup(name string) slog.Handler {
	return levelGate{Handler: g.Handler.WithGroup(name), level: g.level}
}
