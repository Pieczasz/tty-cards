package observability

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// meter uses the otel global, which delegates: instruments created before
// Setup installs the real provider start recording once it does.
var meter = otel.Meter("terminal-card")

// mustCounter panics only on a malformed instrument name, which is a compile-time
// class of mistake; the otel global never fails for provider reasons.
func mustCounter(name, desc string) metric.Int64Counter {
	c, err := meter.Int64Counter(name, metric.WithDescription(desc))
	if err != nil {
		panic(fmt.Sprintf("observability: create counter %s: %v", name, err))
	}
	return c
}

// Every histogram here measures seconds, so the unit is not a parameter.
func mustHistogram(name, desc string) metric.Float64Histogram {
	h, err := meter.Float64Histogram(name,
		metric.WithDescription(desc), metric.WithUnit("s"))
	if err != nil {
		panic(fmt.Sprintf("observability: create histogram %s: %v", name, err))
	}
	return h
}

// Attribute values here are bounded sets only (game names, small enums, route
// patterns) - never a user ID, session ID, or address, which belong on spans
// and log lines where cardinality is free.
var (
	sshSessions = mustCounter("terminalcard.ssh.sessions",
		"SSH connection outcomes")
	sshSessionDuration = mustHistogram("terminalcard.ssh.session.duration",
		"SSH session duration")
	sshPanics = mustCounter("terminalcard.ssh.session.panics",
		"Panics recovered during SSH sessions")
	rateLimitRejects = mustCounter("terminalcard.ratelimit.rejects",
		"Requests rejected by a rate limiter")
	gamesStarted = mustCounter("terminalcard.games.started",
		"Games started")
	gamesFinished = mustCounter("terminalcard.games.finished",
		"Games finished")
	gameDuration = mustHistogram("terminalcard.game.duration",
		"Wall-clock duration of a game")
	turnTimeouts = mustCounter("terminalcard.game.turn.timeouts",
		"Turns played by the clock instead of the player")
	idleRemovals = mustCounter("terminalcard.game.players.idle_removed",
		"Seats taken for idling")
	actionsRejected = mustCounter("terminalcard.game.action.rejected",
		"Player actions the rules refused")
	matchFinalize = mustCounter("terminalcard.match.finalize",
		"Match persistence attempts by outcome")
	broadcastDrops = mustCounter("terminalcard.broadcaster.events.dropped",
		"Events dropped by slow subscribers")
	subscribeFailures = mustCounter("terminalcard.broadcaster.subscribe.failures",
		"Event feed subscriptions refused")
	lobbyJoins = mustCounter("terminalcard.lobby.joins",
		"Lobby join attempts by outcome")
	lobbyTimeToStart = mustHistogram("terminalcard.lobby.time_to_start",
		"Time from lobby creation to game start")
)

// SSHSession counts an ssh connection outcome: accepted, or why it was refused.
func SSHSession(ctx context.Context, outcome string) {
	sshSessions.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// SSHSessionEnded records how long a session lasted and how it ended.
func SSHSessionEnded(ctx context.Context, d time.Duration, outcome string) {
	sshSessionDuration.Record(ctx, d.Seconds(),
		metric.WithAttributes(attribute.String("outcome", outcome)))
}

// SSHPanicRecovered counts a panic recovered inside an ssh session.
func SSHPanicRecovered(ctx context.Context) {
	sshPanics.Add(ctx, 1)
}

// RateLimitReject counts a request refused by the named limiter.
func RateLimitReject(ctx context.Context, limiter string) {
	rateLimitRejects.Add(ctx, 1, metric.WithAttributes(attribute.String("limiter", limiter)))
}

// GameStarted counts a game that began.
func GameStarted(ctx context.Context, gameType string, ranked bool) {
	gamesStarted.Add(ctx, 1, metric.WithAttributes(
		attribute.String("game_type", gameType), attribute.Bool("ranked", ranked)))
}

// GameFinished counts a finished game and records its wall-clock duration.
func GameFinished(ctx context.Context, gameType string, ranked bool, reason string, d time.Duration) {
	attrs := metric.WithAttributes(
		attribute.String("game_type", gameType),
		attribute.Bool("ranked", ranked),
		attribute.String("reason", reason))
	gamesFinished.Add(ctx, 1, attrs)
	gameDuration.Record(ctx, d.Seconds(), attrs)
}

// TurnTimedOut counts a turn the clock played instead of the player.
func TurnTimedOut(ctx context.Context, gameType string) {
	turnTimeouts.Add(ctx, 1, metric.WithAttributes(attribute.String("game_type", gameType)))
}

// PlayerIdleRemoved counts a seat taken for idling.
func PlayerIdleRemoved(ctx context.Context, gameType string) {
	idleRemovals.Add(ctx, 1, metric.WithAttributes(attribute.String("game_type", gameType)))
}

// ActionRejected counts a player action the rules refused.
func ActionRejected(ctx context.Context, gameType string) {
	actionsRejected.Add(ctx, 1, metric.WithAttributes(attribute.String("game_type", gameType)))
}

// MatchFinalize records a persistence attempt. outcome "dropped" and "error" are
// the alertable ones: either means a finished match will not be in the database.
func MatchFinalize(ctx context.Context, outcome string, ranked bool) {
	matchFinalize.Add(ctx, 1, metric.WithAttributes(
		attribute.String("outcome", outcome), attribute.Bool("ranked", ranked)))
}

// BroadcastDropped counts n events a slow subscriber on stream never saw.
func BroadcastDropped(ctx context.Context, stream string, n int64) {
	broadcastDrops.Add(ctx, n, metric.WithAttributes(attribute.String("stream", stream)))
}

// SubscribeFailure counts a refused subscription to stream.
func SubscribeFailure(ctx context.Context, stream string) {
	subscribeFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("stream", stream)))
}

// LobbyJoin counts a lobby join attempt by outcome.
func LobbyJoin(ctx context.Context, outcome string) {
	lobbyJoins.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// LobbyStarted records how long a lobby waited before its game started.
func LobbyStarted(ctx context.Context, gameType string, waited time.Duration) {
	lobbyTimeToStart.Record(ctx, waited.Seconds(),
		metric.WithAttributes(attribute.String("game_type", gameType)))
}

// RegisterSessionGauge exposes count as the connected-sessions gauge. It reads the
// session tracker's own count rather than a second counter kept beside it, which
// could only drift from the number the tracker enforces.
func RegisterSessionGauge(count func() int) error {
	active, err := meter.Int64ObservableGauge("terminalcard.ssh.sessions.active",
		metric.WithDescription("Currently connected SSH sessions"))
	if err != nil {
		return fmt.Errorf("create sessions gauge: %w", err)
	}
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(active, int64(count()))
		return nil
	}, active)
	if err != nil {
		return fmt.Errorf("register sessions callback: %w", err)
	}
	return nil
}

// RegisterDBStats exposes the connection pool as gauges. The pool is a hard cap
// that queues silently, so this is the only early warning before saturation.
func RegisterDBStats(db *sql.DB) error {
	inUse, errUsed := meter.Int64ObservableGauge("db.client.connections.used",
		metric.WithDescription("Connections currently in use"))
	idle, errIdle := meter.Int64ObservableGauge("db.client.connections.idle",
		metric.WithDescription("Idle connections in the pool"))
	waits, errWaits := meter.Int64ObservableCounter("db.client.connections.wait_count",
		metric.WithDescription("Times a query waited for a free connection"))
	if err := errors.Join(errUsed, errIdle, errWaits); err != nil {
		return fmt.Errorf("create pool instruments: %w", err)
	}

	_, err := meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := db.Stats()
		o.ObserveInt64(inUse, int64(s.InUse))
		o.ObserveInt64(idle, int64(s.Idle))
		o.ObserveInt64(waits, s.WaitCount)
		return nil
	}, inUse, idle, waits)
	if err != nil {
		return fmt.Errorf("register pool callback: %w", err)
	}
	return nil
}
