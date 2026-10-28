package observability

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"slices"
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
	SSHAuthReject(ctx, "ratelimit")
	RateLimitReject(ctx, "http")
	GameStarted(ctx, "Poker", true)
	GameFinished(ctx, "Poker", true, "win", time.Minute)
	TurnTimedOut(ctx, "Hearts")
	PlayerIdleRemoved(ctx, "Uno")
	ActionRejected(ctx, "Gin Rummy")
	MatchFinalize(ctx, "dropped", false)
	MatchDamped(ctx, "hearts")
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
		"ttycards.ssh.sessions", "ttycards.ssh.session.duration",
		"ttycards.ssh.session.panics", "ttycards.ssh.auth.rejects", "ttycards.ratelimit.rejects",
		"ttycards.games.started", "ttycards.games.finished",
		"ttycards.game.duration", "ttycards.game.turn.timeouts",
		"ttycards.game.players.idle_removed", "ttycards.game.action.rejected",
		"ttycards.match.finalize", "ttycards.match.damped", "ttycards.broadcaster.events.dropped",
		"ttycards.broadcaster.subscribe.failures", "ttycards.lobby.joins",
		"ttycards.lobby.time_to_start",
	} {
		assert.Truef(t, seen[name], "%s was never exported", name)
	}
}

// The SDK's default boundaries (0 to 10000) are meant for milliseconds. On seconds
// they gave no resolution under 5s and put everything past 2.8h into +Inf.
//
//nolint:paralleltest // reads a process-global manual reader
func TestHistograms_BucketTheirOwnRange(t *testing.T) {
	recordEverything(t.Context())

	// finest is a positive bound the histogram must have at or below it, so short
	// values are told apart; longest is what the last finite bucket must still reach.
	want := map[string]struct{ finest, longest float64 }{
		"ttycards.ssh.session.duration": {finest: 1, longest: 8 * 3600},
		"ttycards.game.duration":        {finest: 10, longest: 3 * 3600},
		"ttycards.lobby.time_to_start":  {finest: 1, longest: 30 * 60},
	}
	seen := 0
	for _, scope := range collect(t).ScopeMetrics {
		for _, m := range scope.Metrics {
			w, ok := want[m.Name]
			if !ok {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.Truef(t, ok, "%s is not a float histogram", m.Name)
			require.NotEmpty(t, hist.DataPoints)
			seen++
			bounds := hist.DataPoints[0].Bounds
			require.NotEmpty(t, bounds)
			assert.Truef(t, slices.ContainsFunc(bounds, func(b float64) bool { return b > 0 && b <= w.finest }),
				"%s cannot tell short values apart", m.Name)
			assert.GreaterOrEqualf(t, bounds[len(bounds)-1], w.longest, "%s lumps long values into +Inf", m.Name)
		}
	}
	assert.Equal(t, len(want), seen)
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
// queries start queueing silently, so its callback has to actually observe - and the
// max beside the used count is what makes "near saturation" a ratio an alert can read.
//
//nolint:paralleltest // reads a process-global manual reader
func TestRegisterDBStats_ObservesThePool(t *testing.T) {
	// sql.OpenDB rather than sql.Open: the pool's counters are the subject, and a
	// connector that is never dialled gives them without dragging a driver in.
	pool := sql.OpenDB(unusedConnector{})
	pool.SetMaxOpenConns(25)
	t.Cleanup(func() { _ = pool.Close() })

	RegisterDBStats(pool)

	found := map[string]int64{}
	for _, scope := range collect(t).ScopeMetrics {
		for _, m := range scope.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Gauge[int64]:
				found[m.Name] = data.DataPoints[0].Value
			case metricdata.Sum[int64]:
				found[m.Name] = data.DataPoints[0].Value
			}
		}
	}
	assert.Contains(t, found, "ttycards.db.pool.connections.used")
	assert.Contains(t, found, "ttycards.db.pool.connections.idle")
	assert.Contains(t, found, "ttycards.db.pool.waits")
	assert.Equal(t, int64(25), found["ttycards.db.pool.connections.max"])
}

// The gauge reads the tracker's count at collection time; a second counter kept
// beside it is what used to be able to disagree with the number the tracker enforces.
//
//nolint:paralleltest // reads a process-global manual reader
func TestRegisterSessionGauge_ObservesTheCount(t *testing.T) {
	online := 3
	RegisterSessionGauge(func() int { return online })
	online = 7

	var got []int64
	for _, scope := range collect(t).ScopeMetrics {
		for _, m := range scope.Metrics {
			if gauge, ok := m.Data.(metricdata.Gauge[int64]); ok && m.Name == "ttycards.ssh.sessions.active" {
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
