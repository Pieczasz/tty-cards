//go:build integration

package repository_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/repository"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"uuid"
)

// Installed before any test runs: the repository's tracer and the observability
// instruments are otel globals, which forward only to the first provider given.
var (
	testSpans   = tracetest.NewSpanRecorder()
	testMetrics = sdkmetric.NewManualReader()
)

func TestMain(m *testing.M) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(testSpans)))
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(testMetrics)))
	os.Exit(m.Run())
}

// spansIn is the recorder's spans named name under traceID, which is what keeps a
// parallel test's spans out of the count.
func spansIn(traceID trace.TraceID, name string) []sdktrace.ReadOnlySpan {
	var out []sdktrace.ReadOnlySpan
	for _, s := range testSpans.Ended() {
		if s.Name() == name && s.SpanContext().TraceID() == traceID {
			out = append(out, s)
		}
	}
	return out
}

// Every website visitor polls the leaderboard. A span per call put a trace into Tempo
// per poll; only the query a cache miss runs, at most once per TTL, is worth one.
func TestUserRepository_BestPlayersSpansOnlyTheQuery(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	seedLeaderboard(t, database, "one", "two")

	ctx, caller := otel.Tracer("test").Start(t.Context(), "caller")
	defer caller.End()

	_, err := repo.BestPlayers(ctx, "", 5)
	require.NoError(t, err)
	assert.Len(t, spansIn(caller.SpanContext().TraceID(), "db.BestPlayers"), 1, "a miss runs one spanned query")

	for range 3 {
		_, err = repo.BestPlayers(ctx, "", 5)
		require.NoError(t, err)
	}
	assert.Len(t, spansIn(caller.SpanContext().TraceID(), "db.BestPlayers"), 1, "a cache hit opened a span")
}

// dampedCount is ttycards.match.damped for gameType, as the manual reader sees it.
func dampedCount(t *testing.T, gameType string) int64 {
	t.Helper()
	var got metricdata.ResourceMetrics
	require.NoError(t, testMetrics.Collect(t.Context(), &got))
	for _, scope := range got.ScopeMetrics {
		for _, m := range scope.Metrics {
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok || m.Name != "ttycards.match.damped" {
				continue
			}
			for _, dp := range sum.DataPoints {
				if v, _ := dp.Attributes.Value("game_type"); v.AsString() == gameType {
					return dp.Value
				}
			}
		}
	}
	return 0
}

// The anti-farm cap is logged at Info, which says a match was damped but not how
// often: the counter is what shows whether the cap bites a real rivalry or a farm.
func TestFinalizeRankedMatchCountsADampedMatch(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	// A game no other test finalizes, so parallel tests cannot move this count.
	ref := db.GameRef{Slug: "metered_rummy", Name: "Metered Rummy"}
	game := db.Game{Slug: ref.Slug, Name: ref.Name}
	require.NoError(t, gormDB.Create(&game).Error)
	userIDs := make([]uuid.UUID, 0, 2)
	for i := range 2 {
		u := db.User{Username: fmt.Sprintf("metered%d", i)}
		require.NoError(t, gormDB.Create(&u).Error)
		require.NoError(t, gormDB.Create(&db.Ranking{UserID: u.ID, GameID: game.ID, Elo: 1500, MatchesPlayed: 10}).Error)
		userIDs = append(userIDs, u.ID)
	}
	repo := repository.NewMatchRepository(gormDB)

	for range 3 {
		require.NoError(t, repo.FinalizeRankedMatch(t.Context(), ref, userIDs, nil))
	}
	require.Zero(t, dampedCount(t, ref.Slug), "the first three pairings of the day pay")

	require.NoError(t, repo.FinalizeRankedMatch(t.Context(), ref, userIDs, nil))
	assert.Equal(t, int64(1), dampedCount(t, ref.Slug))
}
