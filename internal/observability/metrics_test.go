package observability

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// boundedAttributeKeys is the whole vocabulary a metric attribute may use. Anything
// that identifies a person or a session - username, user id, lobby code, address,
// game id - multiplies the time series by the number of players and is what turns a
// 512MB Prometheus into an OOM. Those belong on spans and log lines, where
// cardinality is free.
var boundedAttributeKeys = map[string]bool{
	"outcome": true, "limiter": true, "game_type": true,
	"ranked": true, "reason": true, "stream": true,
}

// recordEverything calls every instrument this package exposes once, so the
// collection below sees the complete attribute vocabulary rather than a sample.
func recordEverything(ctx context.Context) {
	SSHSession(ctx, "accepted")
	SSHSessionEnded(ctx, 42*time.Second, "clean")
	SSHPanicRecovered(ctx)
	RateLimitReject(ctx, "http")
	GameStarted(ctx, "Poker", true)
	GameFinished(ctx, "Poker", true, "win", time.Minute)
	TurnTimedOut(ctx, "Hearts")
	PlayerIdleRemoved(ctx, "Uno")
	ActionRejected(ctx, "Gin Rummy")
	MatchFinalize(ctx, "dropped", false)
	BroadcastDropped(ctx, "engine", 3)
	SubscribeFailure(ctx, "lobby")
	LobbyJoin(ctx, "full")
	LobbyStarted(ctx, "Crazy Eights", 12*time.Second)
}

func collect(t *testing.T) metricdata.ResourceMetrics {
	t.Helper()
	var got metricdata.ResourceMetrics
	require.NoError(t, testReader.Collect(t.Context(), &got))
	return got
}

// Shares the process-global meter, so it cannot run beside anything else that reads it.
//
//nolint:paralleltest // reads a process-global manual reader
func TestMetrics_AreRecordedWithBoundedAttributesOnly(t *testing.T) {
	ctx := t.Context()
	recordEverything(ctx)

	got := collect(t)
	require.NotEmpty(t, got.ScopeMetrics)

	seen := map[string]bool{}
	for _, scope := range got.ScopeMetrics {
		for _, m := range scope.Metrics {
			seen[m.Name] = true
			for _, set := range attributeSets(m) {
				for _, kv := range set {
					assert.Truef(t, boundedAttributeKeys[string(kv.Key)],
						"metric %q carries attribute %q=%q, which is not a bounded enum",
						m.Name, kv.Key, kv.Value.String())
				}
			}
		}
	}

	for _, name := range []string{
		"terminalcard.ssh.sessions", "terminalcard.ssh.session.duration",
		"terminalcard.ssh.session.panics", "terminalcard.ratelimit.rejects",
		"terminalcard.games.started", "terminalcard.games.finished",
		"terminalcard.game.duration", "terminalcard.game.turn.timeouts",
		"terminalcard.game.players.idle_removed", "terminalcard.game.action.rejected",
		"terminalcard.match.finalize", "terminalcard.broadcaster.events.dropped",
		"terminalcard.broadcaster.subscribe.failures", "terminalcard.lobby.joins",
		"terminalcard.lobby.time_to_start",
	} {
		assert.Truef(t, seen[name], "%s was never exported", name)
	}
}

// attributeSets pulls the attribute sets out of whichever aggregation the metric
// happens to use; the SDK has no common interface for that.
type attributeSet = []attribute.KeyValue

func attributeSets(m metricdata.Metrics) []attributeSet {
	var out []attributeSet
	switch data := m.Data.(type) {
	case metricdata.Sum[int64]:
		for _, dp := range data.DataPoints {
			out = append(out, dp.Attributes.ToSlice())
		}
	case metricdata.Gauge[int64]:
		for _, dp := range data.DataPoints {
			out = append(out, dp.Attributes.ToSlice())
		}
	case metricdata.Histogram[float64]:
		for _, dp := range data.DataPoints {
			out = append(out, dp.Attributes.ToSlice())
		}
	}
	return out
}

// RegisterDBStats is the only early warning before the connection pool saturates and
// queries start queueing silently, so its callback has to actually observe.
//
//nolint:paralleltest // reads a process-global manual reader
func TestRegisterDBStats_ObservesThePool(t *testing.T) {
	// sql.OpenDB rather than sql.Open: the pool's counters are the subject, and a
	// connector that is never dialled gives them without dragging a driver in.
	pool := sql.OpenDB(unusedConnector{})
	t.Cleanup(func() { _ = pool.Close() })

	require.NoError(t, RegisterDBStats(pool))

	var found []string
	for _, scope := range collect(t).ScopeMetrics {
		for _, m := range scope.Metrics {
			found = append(found, m.Name)
		}
	}
	assert.Contains(t, found, "db.client.connections.used")
	assert.Contains(t, found, "db.client.connections.idle")
	assert.Contains(t, found, "db.client.connections.wait_count")
}

// The gauge reads the tracker's count at collection time; a second counter kept
// beside it is what used to be able to disagree with the number the tracker enforces.
//
//nolint:paralleltest // reads a process-global manual reader
func TestRegisterSessionGauge_ObservesTheCount(t *testing.T) {
	online := 3
	require.NoError(t, RegisterSessionGauge(func() int { return online }))
	online = 7

	var got []int64
	for _, scope := range collect(t).ScopeMetrics {
		for _, m := range scope.Metrics {
			if gauge, ok := m.Data.(metricdata.Gauge[int64]); ok && m.Name == "terminalcard.ssh.sessions.active" {
				for _, dp := range gauge.DataPoints {
					got = append(got, dp.Value)
				}
			}
		}
	}
	assert.Equal(t, []int64{7}, got, "the gauge must report the count as of the collection")
}

type unusedConnector struct{}

func (unusedConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.ErrUnsupported
}
func (unusedConnector) Driver() driver.Driver { return nil }

// The must* helpers trade an error return for a panic because a bad instrument name
// is a typo in this file, not a runtime condition. That bargain only holds if the
// panic is real: a silently nil instrument would drop the metric instead.
func TestMustInstruments_PanicOnAMalformedName(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() { mustCounter("", "no name") })
	assert.Panics(t, func() { mustHistogram("", "no name") })
}
