// Package observability owns the app's own metric instruments, each behind a small
// function that fixes its attribute keys. The values are plain strings, so a bounded
// series count rests on the callers: every value must be a literal from a small fixed
// set (an outcome, a reason, a game slug, a stream name), never an error text, a lobby
// code, a user or an address. The OTLP providers they record into are installed by
// otelsetup.
package observability

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// meter uses the otel global, which delegates: instruments created before
// otelsetup.Setup installs the real provider start recording once it does.
var meter = otel.Meter("tty-cards")

// must panics only on a malformed instrument name or a callback over another
// meter's instruments, both mistakes in this file rather than runtime conditions;
// the otel global never fails for provider reasons.
func must[T any](v T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("observability: %v", err))
	}
	return v
}

func mustCounter(name, desc string) metric.Int64Counter {
	return must(meter.Int64Counter(name, metric.WithDescription(desc)))
}

// Every histogram here measures seconds, so the unit is not a parameter. The
// boundaries are: the SDK default is meant for milliseconds.
func mustHistogram(name, desc string, bounds ...float64) metric.Float64Histogram {
	return must(meter.Float64Histogram(name, metric.WithDescription(desc), metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(bounds...)))
}

// Bucket boundaries in seconds, each sized to its quantity: a session runs from a
// refused handshake to a night at the table, a game from an early forfeit to a long
// match, and a lobby wait from an instant quick-match to a private room left open.
var (
	sessionBuckets   = []float64{1, 5, 15, 30, 60, 300, 600, 1800, 3600, 7200, 14400, 28800, 86400}
	gameBuckets      = []float64{10, 30, 60, 120, 300, 600, 900, 1200, 1800, 2700, 3600, 5400, 7200, 10800}
	lobbyWaitBuckets = []float64{1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 1200, 1800, 3600}
)

// Attribute values here are bounded sets only (game slugs, small enums, route
// patterns) - never a user ID, session ID, or address, which belong on spans
// and log lines where cardinality is free.
var (
	sshSessions = mustCounter("ttycards.ssh.sessions",
		"SSH connection outcomes")
	sshSessionDuration = mustHistogram("ttycards.ssh.session.duration",
		"SSH session duration", sessionBuckets...)
	sshPanics = mustCounter("ttycards.ssh.session.panics",
		"Panics recovered during SSH sessions")
	sshAuthRejects = mustCounter("ttycards.ssh.auth.rejects",
		"SSH connections refused at authentication, once per connection")
	rateLimitRejects = mustCounter("ttycards.ratelimit.rejects",
		"Requests rejected by a rate limiter")
	gamesStarted = mustCounter("ttycards.games.started",
		"Games started")
	gamesFinished = mustCounter("ttycards.games.finished",
		"Games finished")
	gameDuration = mustHistogram("ttycards.game.duration",
		"Wall-clock duration of a game", gameBuckets...)
	turnTimeouts = mustCounter("ttycards.game.turn.timeouts",
		"Turns played by the clock instead of the player")
	idleRemovals = mustCounter("ttycards.game.players.idle_removed",
		"Seats taken for idling")
	actionsRejected = mustCounter("ttycards.game.action.rejected",
		"Player actions the rules refused")
	matchFinalize = mustCounter("ttycards.match.finalize",
		"Match persistence attempts by outcome")
	matchDamped = mustCounter("ttycards.match.damped",
		"Ranked matches the anti-farm cap stopped paying")
	broadcastDrops = mustCounter("ttycards.broadcaster.events.dropped",
		"Events dropped by slow subscribers")
	subscribeFailures = mustCounter("ttycards.broadcaster.subscribe.failures",
		"Event feed subscriptions refused")
	lobbyJoins = mustCounter("ttycards.lobby.joins",
		"Lobby join attempts by outcome")
	lobbyTimeToStart = mustHistogram("ttycards.lobby.time_to_start",
		"Time from lobby creation to game start", lobbyWaitBuckets...)
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

// SSHAuthReject counts a connection refused before it authenticated: "ratelimit" or
// "unkeyable" (a remote address the limiter cannot key on). These are not sessions,
// so they stay out of ttycards.ssh.sessions and its duration histogram.
func SSHAuthReject(ctx context.Context, reason string) {
	sshAuthRejects.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
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

// MatchDamped counts a ranked match that was stored but paid nobody, because its
// players had already met too often inside 24h.
func MatchDamped(ctx context.Context, gameType string) {
	matchDamped.Add(ctx, 1, metric.WithAttributes(attribute.String("game_type", gameType)))
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
func RegisterSessionGauge(count func() int) {
	active := must(meter.Int64ObservableGauge("ttycards.ssh.sessions.active",
		metric.WithDescription("Currently connected SSH sessions")))
	must(meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(active, int64(count()))
		return nil
	}, active))
}

// RegisterDBStats exposes the connection pool as gauges. The pool is a hard cap
// that queues silently, so this is the only early warning before saturation. The
// names are the app's own rather than semconv's db.client.connection.*, whose
// required pool-name and state attributes buy nothing with one pool.
func RegisterDBStats(db *sql.DB) {
	inUse := must(meter.Int64ObservableGauge("ttycards.db.pool.connections.used",
		metric.WithDescription("Connections currently in use")))
	idle := must(meter.Int64ObservableGauge("ttycards.db.pool.connections.idle",
		metric.WithDescription("Idle connections in the pool")))
	maxOpen := must(meter.Int64ObservableGauge("ttycards.db.pool.connections.max",
		metric.WithDescription("The pool's cap on open connections")))
	waits := must(meter.Int64ObservableCounter("ttycards.db.pool.waits",
		metric.WithDescription("Times a query waited for a free connection")))
	must(meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := db.Stats()
		o.ObserveInt64(inUse, int64(s.InUse))
		o.ObserveInt64(idle, int64(s.Idle))
		o.ObserveInt64(maxOpen, int64(s.MaxOpenConnections))
		o.ObserveInt64(waits, s.WaitCount)
		return nil
	}, inUse, idle, maxOpen, waits))
}

// SeedAlertedSeries exports a zero for every series an alert watches with
// increase(), for the attribute values its callers use (internal/lobby, internal/ssh).
// A counter has no series until its first Add, and increase() needs two samples, so
// without the zero the first lost match or panic after a boot is the series' first
// sample and never fires.
func SeedAlertedSeries(ctx context.Context) {
	for _, outcome := range []string{"dropped", "error"} {
		for _, ranked := range []bool{false, true} {
			matchFinalize.Add(ctx, 0, metric.WithAttributes(
				attribute.String("outcome", outcome), attribute.Bool("ranked", ranked)))
		}
	}
	for _, stream := range []string{"game", "lobby"} {
		broadcastDrops.Add(ctx, 0, metric.WithAttributes(attribute.String("stream", stream)))
	}
	sshPanics.Add(ctx, 0)
}
