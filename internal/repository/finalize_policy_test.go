//go:build integration

package repository_test

import (
	"context"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/repository"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"uuid"

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

// D-1: a match one leaver ended early for everyone. The seated players' ratings and
// track records do not move - a friend quitting must not bank a lead - but the leaver
// still takes the loss Elo gives them for finishing last, or quitting would be free.
func TestFinalizeInterruptedMatchChargesOnlyTheLeaver(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	ctx := context.Background()
	gameID, ids := antiFarmTable(t, gormDB,
		seat{elo: 1600, played: 10}, seat{elo: 1500, played: 10}, seat{elo: 1500, played: 10})
	first, second, leaver := ids[0], ids[1], ids[2]

	// A seated player with no ranking yet must not be put on the board by a match that
	// did not count for them.
	newcomer := db.User{Username: "newcomer"}
	require.NoError(t, gormDB.Create(&newcomer).Error)
	standings := []uuid.UUID{first, second, newcomer.ID, leaver}

	require.NoError(t, repository.NewMatchRepository(gormDB).FinalizeInterruptedMatch(
		ctx, gameRef(antiFarmGame), standings, nil, []uuid.UUID{leaver}))

	for id, was := range map[uuid.UUID]uint32{first: 1600, second: 1500} {
		r := rankingOf(t, gormDB, id, gameID)
		assert.Equal(t, was, r.Elo, "a seated rating moved")
		assert.Equal(t, uint64(10), r.MatchesPlayed, "a seated player was counted for an unfinished match")
	}
	var newcomerRows int64
	require.NoError(t, gormDB.Model(&db.Ranking{}).Where("user_id = ?", newcomer.ID.String()).Count(&newcomerRows).Error)
	assert.Zero(t, newcomerRows, "the seated newcomer was seeded onto the leaderboard")

	quit := rankingOf(t, gormDB, leaver, gameID)
	assert.Less(t, quit.Elo, uint32(1500), "the leaver quit for free")
	assert.Equal(t, uint64(11), quit.MatchesPlayed, "the leaver's rated loss is a match played")

	deltas := lastMatchDeltas(t, gormDB)
	assert.Zero(t, deltas[first])
	assert.Zero(t, deltas[second])
	assert.Equal(t, int(quit.Elo)-1500, deltas[leaver], "history disagrees with the rating")

	var match db.Match
	require.NoError(t, gormDB.Order("id DESC").First(&match).Error)
	assert.True(t, match.Ranked, "a match that moved a rating is ranked history")
}

// A leaver never gains, even where Elo would pay them: two leavers tied last, the
// lower-rated one comes out ahead of the higher on the draw.
func TestFinalizeInterruptedMatchNeverPaysALeaver(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	ctx := context.Background()
	gameID, ids := antiFarmTable(t, gormDB,
		seat{elo: 1500, played: 10}, seat{elo: 1900, played: 10}, seat{elo: 1100, played: 10})

	require.NoError(t, repository.NewMatchRepository(gormDB).FinalizeInterruptedMatch(
		ctx, gameRef(antiFarmGame), ids, []int{1, 2, 2}, []uuid.UUID{ids[1], ids[2]}))

	assert.LessOrEqual(t, rankingOf(t, gormDB, ids[2], gameID).Elo, uint32(1100), "a leaver was paid for quitting")
	assert.Less(t, rankingOf(t, gormDB, ids[1], gameID).Elo, uint32(1900))
	assert.Equal(t, uint32(1500), rankingOf(t, gormDB, ids[0], gameID).Elo)
}
