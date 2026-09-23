//go:build integration

package repository_test

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/repository"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// holdRankingsQueries parks every leaderboard read after Postgres has answered it and
// before the repository sees the rows - the window a slow query leaves open. It counts
// the reads that reached the database.
func holdRankingsQueries(t *testing.T, database *gorm.DB) (entered <-chan struct{}, release func(), reads *atomic.Int32) {
	t.Helper()
	in := make(chan struct{}, 64)
	gate := make(chan struct{})
	var count atomic.Int32
	require.NoError(t, database.Callback().Query().After("gorm:query").Register("test:hold_rankings", func(tx *gorm.DB) {
		if tx.Statement.Table != "rankings" {
			return
		}
		count.Add(1)
		in <- struct{}{}
		<-gate
	}))
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return in, release, &count
}

func seedLeaderboard(t *testing.T, database *gorm.DB, names ...string) []*db.User {
	t.Helper()
	game := &db.Game{Slug: "board", Name: "Board"}
	require.NoError(t, database.Create(game).Error)
	users := make([]*db.User, 0, len(names))
	for _, name := range names {
		u := &db.User{Username: name}
		require.NoError(t, database.Create(u).Error)
		require.NoError(t, database.Create(&db.Ranking{UserID: u.ID, GameID: game.ID, Elo: 1600, MatchesPlayed: 9}).Error)
		users = append(users, u)
	}
	return users
}

// DeleteAccount clears the leaderboard cache, but a read that had already fetched its
// rows stored them afterwards, and the erased account sat on the board for the whole
// five-minute TTL.
func TestUserRepository_BestPlayersDoesNotRecacheAnErasedRow(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := t.Context()
	users := seedLeaderboard(t, database, "erased", "kept")
	entered, release, _ := holdRankingsQueries(t, database)

	slow := make(chan error, 1)
	go func() {
		_, err := repo.BestPlayers(ctx, "", 10)
		slow <- err
	}()
	<-entered // the slow read has its pre-erasure rows in hand

	require.NoError(t, repo.DeleteAccount(ctx, users[0].ID))
	release()
	require.NoError(t, <-slow)

	best, err := repo.BestPlayers(ctx, "", 10)
	require.NoError(t, err)
	for _, r := range best {
		assert.NotEqual(t, users[0].ID, r.UserID, "the erased account was re-cached by a read that started before the erasure")
	}
}

// A cold leaderboard - the TTL just lapsed, or an erasure just cleared it - is read by
// every open lobby screen and the website at once. They should share one query.
func TestUserRepository_BestPlayersSharesOneQueryPerMiss(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := t.Context()
	seedLeaderboard(t, database, "one", "two")
	entered, release, reads := holdRankingsQueries(t, database)

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Go(func() {
			best, err := repo.BestPlayers(ctx, "", 10)
			if err == nil && len(best) != 2 {
				err = assert.AnError
			}
			errs <- err
		})
	}
	<-entered
	// The other callers have to be queued behind the first before it finishes; there is
	// nothing to wait on for that, so give them a moment.
	time.Sleep(200 * time.Millisecond)
	release()
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), reads.Load(), "concurrent misses each ran their own query")
}
