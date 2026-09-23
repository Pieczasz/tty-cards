//go:build integration

package repository_test

import (
	"context"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/repository"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A match can finish after one of its seats erased their account - the table plays
// on, the finalize comes later. Seeding that seat's ranking brought the erased
// account straight back onto the leaderboard under its anonymised name.
func TestFinalizeRankedMatchSkipsAnErasedSeat(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	ctx := context.Background()
	gameID, ids := antiFarmTable(t, gormDB,
		seat{elo: 1500, played: 10}, seat{elo: 1500, played: 10}, seat{elo: 1500, played: 10})
	erased, second, third := ids[0], ids[1], ids[2]

	users := repository.NewUserRepository(gormDB)
	require.NoError(t, users.DeleteAccount(ctx, erased))

	require.NoError(t, repository.NewMatchRepository(gormDB).
		FinalizeRankedMatch(ctx, gameRef(antiFarmGame), ids, nil))

	var rows int64
	require.NoError(t, gormDB.Unscoped().Model(&db.Ranking{}).
		Where("user_id = ?", erased.String()).Count(&rows).Error)
	assert.Zero(t, rows, "the finalize re-created the erased account's ranking")

	best, err := users.BestPlayers(ctx, 10, "")
	require.NoError(t, err)
	for _, r := range best {
		assert.NotEqual(t, erased, r.UserID, "the erased account is back on the leaderboard")
	}

	// The rest of the table is still rated among itself, and the erased seat keeps its
	// history row - other players' history has to keep resolving.
	assert.Greater(t, rankingOf(t, gormDB, second, gameID).Elo, rankingOf(t, gormDB, third, gameID).Elo)
	deltas := lastMatchDeltas(t, gormDB)
	assert.Len(t, deltas, 3)
	assert.Zero(t, deltas[erased])
}

// An erasure and a finalize for the same user serialize on the seat's advisory lock,
// so neither can interleave with the other: after both, the erased user has no
// ranking whichever ran first.
func TestEraseAndFinalizeSerialize(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	ctx := context.Background()
	_, ids := antiFarmTable(t, gormDB, seat{elo: 1500, played: 10}, seat{elo: 1500, played: 10})

	users := repository.NewUserRepository(gormDB)
	matches := repository.NewMatchRepository(gormDB)
	errs := make(chan error, 2)
	go func() { errs <- users.DeleteAccount(ctx, ids[0]) }()
	go func() { errs <- matches.FinalizeRankedMatch(ctx, gameRef(antiFarmGame), ids, nil) }()
	for range 2 {
		require.NoError(t, <-errs)
	}

	var rows int64
	require.NoError(t, gormDB.Unscoped().Model(&db.Ranking{}).
		Where("user_id = ?", ids[0].String()).Count(&rows).Error)
	assert.Zero(t, rows, "a finalize racing the erasure left the erased account rated")
}
