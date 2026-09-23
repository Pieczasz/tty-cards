//go:build integration

package repository_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/repository"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUserRepository_RegisterUserWithKey(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)

	t.Run("successful registration", func(t *testing.T) {
		t.Parallel()
		user, key, err := repo.RegisterUserWithKey(context.Background(), "reg_ok", "fp_reg_ok")
		require.NoError(t, err)
		require.NotNil(t, user)
		assert.Equal(t, "reg_ok", user.Username)
		assert.Equal(t, "fp_reg_ok", key.Fingerprint)
		assert.NotEqual(t, uuid.Nil(), user.ID, "the account id is assigned on insert")
		assert.EqualValues(t, 7, user.ID[6]>>4, "users.id is UUIDv7, not a sequence")
	})

	t.Run("username already taken", func(t *testing.T) {
		t.Parallel()
		_, _, err := repo.RegisterUserWithKey(context.Background(), "dup_name", "fp_name_1")
		require.NoError(t, err, "seed the name first")

		_, _, err = repo.RegisterUserWithKey(context.Background(), "dup_name", "fp_name_2")
		require.ErrorContains(t, err, "username already taken")
	})

	// D-4: "Alice" and "alice" read as one player on a leaderboard.
	t.Run("a case variant of a taken name is taken", func(t *testing.T) {
		t.Parallel()
		first, _, err := repo.RegisterUserWithKey(context.Background(), "Case_Name", "fp_case_1")
		require.NoError(t, err, "seed the name first")
		assert.Equal(t, "Case_Name", first.Username, "the display case is kept")

		_, _, err = repo.RegisterUserWithKey(context.Background(), "case_NAME", "fp_case_2")
		require.ErrorIs(t, err, db.ErrUsernameTaken)
	})

	t.Run("invalid username length", func(t *testing.T) {
		t.Parallel()
		_, _, err := repo.RegisterUserWithKey(context.Background(), "this_username_is_way_too_long", "fp_too_long")
		require.ErrorContains(t, err, "username cannot exceed 16 characters")
	})

	t.Run("erasure prefix is not registerable", func(t *testing.T) {
		t.Parallel()
		_, _, err := repo.RegisterUserWithKey(context.Background(), "deleted_1", "fp_deleted")
		require.ErrorIs(t, err, db.ErrInvalidUsername)
	})

	t.Run("duplicate fingerprint", func(t *testing.T) {
		t.Parallel()
		_, _, err := repo.RegisterUserWithKey(context.Background(), "fp_owner", "fp_shared")
		require.NoError(t, err, "seed the fingerprint first")

		_, _, err = repo.RegisterUserWithKey(context.Background(), "fp_thief", "fp_shared")
		require.ErrorContains(t, err, "public key already registered")
	})
}

func TestUserRepository_RegisterUserWithKey_ConcurrentSameFingerprint(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)

	const workers = 8
	errs := make(chan error, workers)
	for i := range workers {
		go func(i int) {
			_, _, err := repo.RegisterUserWithKey(context.Background(), fmt.Sprintf("user_%d", i), "shared_fingerprint")
			errs <- err
		}(i)
	}

	var success, fail int
	for range workers {
		err := <-errs
		if err == nil {
			success++
		} else {
			fail++
		}
	}
	assert.Equal(t, 1, success)
	assert.Equal(t, workers-1, fail)

	var userCount int64
	database.Model(&db.User{}).Count(&userCount)
	assert.Equal(t, int64(1), userCount)
}

func TestUserRepository_LoadUserByFingerprint(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)

	_, _, err := repo.RegisterUserWithKey(context.Background(), "player_two", "fingerprint_abc")
	require.NoError(t, err)

	t.Run("existing user", func(t *testing.T) {
		t.Parallel()
		user, key, err := repo.LoadUserByFingerprint(context.Background(), "fingerprint_abc")
		require.NoError(t, err)
		require.NotNil(t, user)
		require.NotNil(t, key)
		assert.Equal(t, "player_two", user.Username)
	})

	t.Run("non-existent user", func(t *testing.T) {
		t.Parallel()
		user, key, err := repo.LoadUserByFingerprint(context.Background(), "fingerprint_unknown")
		require.NoError(t, err)
		assert.Nil(t, user)
		assert.Nil(t, key)
	})
}

func TestUserRepository_UpdateUserActivity(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)

	user, key, err := repo.RegisterUserWithKey(context.Background(), "player_three", "fingerprint_xyz")
	require.NoError(t, err)

	// Backdate both timestamps. Asserting only "After or Equal" would pass even if
	// UpdateUserActivity did nothing at all, since Equal covers the no-op.
	stale := time.Now().Add(-24 * time.Hour).UTC()
	require.NoError(t, database.Model(&db.User{}).Where("id = ?", user.ID.String()).
		Update("last_seen_at", stale).Error)
	require.NoError(t, database.Model(&db.PublicKey{}).Where("id = ?", key.ID).
		Update("last_used_at", stale).Error)

	require.NoError(t, repo.UpdateUserActivity(context.Background(), user, key))

	updatedUser, updatedKey, err := repo.LoadUserByFingerprint(context.Background(), "fingerprint_xyz")
	require.NoError(t, err)

	assert.True(t, updatedUser.LastSeenAt.After(stale),
		"last_seen_at must move forward: got %v, seeded %v", updatedUser.LastSeenAt, stale)
	assert.True(t, updatedKey.LastUsedAt.After(stale),
		"last_used_at must move forward: got %v, seeded %v", updatedKey.LastUsedAt, stale)
}

// Reproduction: soft-deleting a user leaves its public_keys row matching, and the
// Preload"s deleted_at IS NULL only empties the association. The old code returned
// &dbKey.User unconditionally, so the ssh auth path saw a non-nil user with ID 0 and
// logged the deleted account in as user zero.
func TestUserRepository_SoftDeletedUserDoesNotAuthenticate(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()

	created, createdKey, err := repo.RegisterUserWithKey(ctx, "activity_user", "activity_fp")
	require.NoError(t, err)
	require.NoError(t, database.Delete(&db.User{}, "id = ?", created.ID.String()).Error)

	// The key row is deliberately left behind: that is the state the fix is about.
	var key db.PublicKey
	require.NoError(t, database.First(&key, createdKey.ID).Error)

	user, loadedKey, err := repo.LoadUserByFingerprint(ctx, "activity_fp")
	require.NoError(t, err, "a deleted account is a miss, not an error")
	assert.Nil(t, user, "a soft-deleted user must not authenticate")
	assert.Nil(t, loadedKey, "and its key must not come back either")

	var users []db.User
	require.NoError(t, database.Unscoped().Find(&users).Error)
	require.Len(t, users, 1)
	assert.True(t, users[0].DeletedAt.Valid, "a deleted user must not come back on login")
}

// The public key carries a preloaded User, which GORM would happily upsert
// alongside the timestamp - so every login became a write to the users table.
func TestUserRepository_UpdateUserActivityDoesNotWriteUsers(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()

	_, createdKey, err := repo.RegisterUserWithKey(ctx, "activity_two", "activity_fp2")
	require.NoError(t, err)

	stale := time.Now().Add(-24 * time.Hour).UTC()
	require.NoError(t, database.Model(&db.PublicKey{}).Where("id = ?", createdKey.ID).
		Update("last_used_at", stale).Error)

	user, key, err := repo.LoadUserByFingerprint(ctx, "activity_fp2")
	require.NoError(t, err)
	require.NotNil(t, user)
	require.NoError(t, repo.UpdateUserActivity(ctx, user, key))

	var reloaded db.PublicKey
	require.NoError(t, database.First(&reloaded, createdKey.ID).Error)
	assert.True(t, reloaded.LastUsedAt.After(stale),
		"the association write must not take the key's own update down with it")

	var users int64
	require.NoError(t, database.Model(&db.User{}).Count(&users).Error)
	assert.Equal(t, int64(1), users, "updating a key must not insert a user")
}

func TestUserRepository_UserMatchHistoryRejectsNegativeLimit(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()

	u, _, err := repo.RegisterUserWithKey(ctx, "neg_limit", "neg_limit_fp")
	require.NoError(t, err)

	game := &db.Game{Slug: "neggame", Name: "NegGame"}
	require.NoError(t, database.Create(game).Error)
	match := &db.Match{GameID: game.ID}
	require.NoError(t, database.Create(match).Error)
	require.NoError(t, database.Create(&db.MatchParticipant{MatchID: match.ID, UserID: u.ID, Placement: 1}).Error)

	// GORM treats a negative Limit as "no limit", which would stream the whole table.
	history, err := repo.UserMatchHistory(ctx, u.ID, -1)
	require.NoError(t, err)
	assert.Empty(t, history)
}

func TestUserRepository_BestPlayersCachesShortTables(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()

	game := &db.Game{Slug: "shorttable", Name: "ShortTable"}
	require.NoError(t, database.Create(game).Error)
	u := &db.User{Username: "only_player"}
	require.NoError(t, database.Create(u).Error)
	require.NoError(t, database.Create(&db.Ranking{UserID: u.ID, GameID: game.ID, Elo: 1700, MatchesPlayed: 5}).Error)

	best, err := repo.BestPlayers(ctx, 25, "")
	require.NoError(t, err)
	require.Len(t, best, 1)

	// Fewer rankings than the limit still counts as a warm cache, so pulling the
	// rows out from under it must not change the answer.
	require.NoError(t, database.Where("1 = 1").Delete(&db.Ranking{}).Error)

	cached, err := repo.BestPlayers(ctx, 25, "")
	require.NoError(t, err)
	assert.Len(t, cached, 1, "a table shorter than the limit must still be cached")
}

// A limit above bestPlayersCacheSize (200) cannot be answered from an entry that
// only holds 200 rows, so it must bypass the cache in both directions rather than
// serve - or store - a silently truncated board.
func TestUserRepository_BestPlayersBypassesCacheAboveItsSize(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()

	game := &db.Game{Slug: "bigask", Name: "BigAsk"}
	require.NoError(t, database.Create(game).Error)
	u := &db.User{Username: "big_ask"}
	require.NoError(t, database.Create(u).Error)
	require.NoError(t, database.Create(&db.Ranking{UserID: u.ID, GameID: game.ID, Elo: 1700, MatchesPlayed: 5}).Error)

	best, err := repo.BestPlayers(ctx, 201, "")
	require.NoError(t, err)
	require.Len(t, best, 1)

	require.NoError(t, database.Where("1 = 1").Delete(&db.Ranking{}).Error)

	// Had the oversized ask been cached, this would still answer 1.
	again, err := repo.BestPlayers(ctx, 201, "")
	require.NoError(t, err)
	assert.Empty(t, again, "an oversized limit must not be served from the cache")
}

func TestUserRepository_BestPlayers(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()

	game := &db.Game{Slug: "testgame", Name: "TestGame"}
	database.Create(game)

	for i := 1; i <= 5; i++ {
		u := &db.User{Username: "player" + string(rune(i+48))}
		database.Create(u)
		database.Create(&db.Ranking{UserID: u.ID, GameID: game.ID, Elo: uint32(1000 + i*100), MatchesPlayed: 5})
	}

	best, err := repo.BestPlayers(ctx, 3, "")
	require.NoError(t, err)
	require.Len(t, best, 3)
	assert.Equal(t, uint32(1500), best[0].Elo)
	assert.Equal(t, uint32(1400), best[1].Elo)
	assert.Equal(t, uint32(1300), best[2].Elo)

	bestCached, err := repo.BestPlayers(ctx, 2, "")
	require.NoError(t, err)
	assert.Len(t, bestCached, 2)
}

func TestUserRepository_BestPlayers_FiltersByGame(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()

	poker := &db.Game{Slug: "poker", Name: "Poker"}
	uno := &db.Game{Slug: "uno", Name: "Uno"}
	require.NoError(t, database.Create(poker).Error)
	require.NoError(t, database.Create(uno).Error)

	alice := &db.User{Username: "alice"}
	bob := &db.User{Username: "bob"}
	require.NoError(t, database.Create(alice).Error)
	require.NoError(t, database.Create(bob).Error)
	require.NoError(t, database.Create(&db.Ranking{UserID: alice.ID, GameID: poker.ID, Elo: 1800, MatchesPlayed: 5}).Error)
	require.NoError(t, database.Create(&db.Ranking{UserID: bob.ID, GameID: uno.ID, Elo: 1900, MatchesPlayed: 5}).Error)

	unoOnly, err := repo.BestPlayers(ctx, 10, "uno")
	require.NoError(t, err)
	require.Len(t, unoOnly, 1)
	assert.Equal(t, "bob", unoOnly[0].User.Username)

	all, err := repo.BestPlayers(ctx, 10, "")
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, "bob", all[0].User.Username, "highest Elo across games wins the mixed board")

	missing, err := repo.BestPlayers(ctx, 10, "hearts")
	require.NoError(t, err)
	assert.Empty(t, missing)
}

func TestUserRepository_BestPlayers_FiltersBySlugAfterRename(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()

	poker := &db.Game{Slug: "poker", Name: "Poker"}
	require.NoError(t, database.Create(poker).Error)
	alice := &db.User{Username: "slug_alice"}
	require.NoError(t, database.Create(alice).Error)
	require.NoError(t, database.Create(&db.Ranking{
		UserID: alice.ID, GameID: poker.ID, Elo: 1800, MatchesPlayed: 5,
	}).Error)

	require.NoError(t, database.Model(poker).Update("name", "Texas Holdem").Error)

	bySlug, err := repo.BestPlayers(ctx, 10, "poker")
	require.NoError(t, err)
	require.Len(t, bySlug, 1)
	assert.Equal(t, "slug_alice", bySlug[0].User.Username)

	byName, err := repo.BestPlayers(ctx, 10, "Texas Holdem")
	require.NoError(t, err)
	assert.Empty(t, byName, "the display name is not the filter identity")
}

func TestUserRepository_UserProfile(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()

	u, _, _ := repo.RegisterUserWithKey(ctx, "profile_user", "profile_fp")

	game := &db.Game{Slug: "profilegame", Name: "ProfileGame"}
	database.Create(game)
	database.Create(&db.Ranking{UserID: u.ID, GameID: game.ID, Elo: 1600})

	profile, err := repo.UserProfile(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, "profile_user", profile.Username)
	assert.Len(t, profile.PublicKeys, 1)
	assert.Len(t, profile.Rankings, 1)
	assert.Equal(t, "ProfileGame", profile.Rankings[0].Game.Name)

	_, err = repo.UserProfile(ctx, uuid.New())
	assert.Error(t, err)
}

func TestUserRepository_UserMatchHistory(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()

	u, _, _ := repo.RegisterUserWithKey(ctx, "history_user", "history_fp")

	game := &db.Game{Slug: "historygame", Name: "HistoryGame"}
	database.Create(game)

	match := &db.Match{GameID: game.ID}
	database.Create(match)

	database.Create(&db.MatchParticipant{MatchID: match.ID, UserID: u.ID, Placement: 1, EloDelta: 15})

	history, err := repo.UserMatchHistory(ctx, u.ID, 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, 1, history[0].Placement)
	assert.Equal(t, 15, history[0].EloDelta)
	assert.Equal(t, "HistoryGame", history[0].Match.Game.Name)
}

// Equal Elo is the common case at the starting rating. Without a tiebreaker Postgres
// may return those rows in any order, so the board visibly reshuffles every time the
// cache expires and nobody can tell which rank is theirs.
func TestUserRepository_BestPlayersOrderIsStableAcrossEqualRatings(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	ctx := context.Background()

	game := &db.Game{Slug: "tiebreak", Name: "Tiebreak"}
	require.NoError(t, database.Create(game).Error)
	for i := range 20 {
		u := &db.User{Username: fmt.Sprintf("tied%02d", i)}
		require.NoError(t, database.Create(u).Error)
		require.NoError(t, database.Create(&db.Ranking{
			UserID: u.ID, GameID: game.ID, Elo: 1500, MatchesPlayed: 9,
		}).Error)
	}

	// A fresh repository per read, so each one is a real query rather than the cache.
	first, err := repository.NewUserRepository(database).BestPlayers(ctx, 20, "tiebreak")
	require.NoError(t, err)
	require.Len(t, first, 20)

	for range 5 {
		again, err := repository.NewUserRepository(database).BestPlayers(ctx, 20, "tiebreak")
		require.NoError(t, err)
		assert.Equal(t, usernamesOf(first), usernamesOf(again),
			"the board reshuffled between two identical queries")
	}
}

func usernamesOf(rankings []db.Ranking) []string {
	names := make([]string, len(rankings))
	for i, r := range rankings {
		names[i] = r.User.Username
	}
	return names
}

// A login must touch exactly two rows. The preloads hang Rankings (and their Games)
// off the user and a User off the key, and GORM's save hooks upsert every association
// they can see - so a timestamp write used to rewrite the player's whole rating set.
func TestUserRepository_UpdateUserActivityWritesNoAssociations(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()

	user, key, err := repo.RegisterUserWithKey(ctx, "assoc_owner", "assoc_fp")
	require.NoError(t, err)
	game := &db.Game{Slug: "assoc_game", Name: "AssocGame"}
	require.NoError(t, database.Create(game).Error)
	require.NoError(t, database.Create(&db.Ranking{
		UserID: user.ID, GameID: game.ID, Elo: 1234, MatchesPlayed: 7,
	}).Error)

	loaded, loadedKey, err := repo.LoadUserByFingerprint(ctx, "assoc_fp")
	require.NoError(t, err)
	require.Len(t, loaded.Rankings, 1, "the preload is what makes this dangerous")

	var before db.Ranking
	require.NoError(t, database.Where("user_id = ?", user.ID.String()).First(&before).Error)

	require.NoError(t, repo.UpdateUserActivity(ctx, loaded, loadedKey))

	var after db.Ranking
	require.NoError(t, database.Where("user_id = ?", user.ID.String()).First(&after).Error)
	assert.Equal(t, before.UpdatedAt.UnixNano(), after.UpdatedAt.UnixNano(),
		"logging in rewrote the player's ranking row")
	assert.Equal(t, uint32(1234), after.Elo)
	assert.Equal(t, uint64(7), after.MatchesPlayed)

	var games, users int64
	require.NoError(t, database.Model(&db.Game{}).Count(&games).Error)
	require.NoError(t, database.Model(&db.User{}).Count(&users).Error)
	assert.Equal(t, int64(1), games, "logging in inserted a game row")
	assert.Equal(t, int64(1), users, "logging in inserted a user row")
	_ = key
}

// deletionFixture seats two players at one recorded ranked match, so what erasing one
// of them removes can be checked against what the other keeps.
type deletionFixture struct {
	leaver *db.User
	other  *db.User
	game   *db.Game
	match  *db.Match
}

func seedDeletionFixture(t *testing.T, database *gorm.DB, repo db.UserRepository) deletionFixture {
	t.Helper()
	ctx := context.Background()

	leaver, _, err := repo.RegisterUserWithKey(ctx, "leaver", "fp_leaver")
	require.NoError(t, err)
	other, _, err := repo.RegisterUserWithKey(ctx, "stayer", "fp_stayer")
	require.NoError(t, err)

	game := &db.Game{Slug: "erasure", Name: "Erasure"}
	require.NoError(t, database.Create(game).Error)
	for _, u := range []*db.User{leaver, other} {
		require.NoError(t, database.Create(&db.Ranking{
			UserID: u.ID, GameID: game.ID, Elo: 1700, MatchesPlayed: 9,
		}).Error)
	}

	match := &db.Match{GameID: game.ID, Ranked: true}
	require.NoError(t, database.Create(match).Error)
	require.NoError(t, database.Create(&db.MatchParticipant{
		MatchID: match.ID, UserID: leaver.ID, Placement: 1, EloDelta: 12,
	}).Error)
	require.NoError(t, database.Create(&db.MatchParticipant{
		MatchID: match.ID, UserID: other.ID, Placement: 2, EloDelta: -12,
	}).Error)

	return deletionFixture{leaver: leaver, other: other, game: game, match: match}
}

func assertIdentityErased(t *testing.T, database *gorm.DB, f deletionFixture) {
	t.Helper()

	// Unscoped on both counts: a soft delete would leave the row - and with it the
	// unique fingerprint and the (user_id, game_id) ranking key - in place.
	var keys, rankings int64
	require.NoError(t, database.Unscoped().Model(&db.PublicKey{}).
		Where("user_id = ?", f.leaver.ID.String()).Count(&keys).Error)
	assert.Zero(t, keys, "the erased account still has a key to log in with")
	require.NoError(t, database.Unscoped().Model(&db.Ranking{}).
		Where("user_id = ?", f.leaver.ID.String()).Count(&rankings).Error)
	assert.Zero(t, rankings, "the erased account still has a rating")

	var row struct {
		Username   string
		LastSeenAt *time.Time
	}
	require.NoError(t, database.Raw(
		`SELECT username, last_seen_at FROM users WHERE id = ?`, f.leaver.ID.String()).Scan(&row).Error)
	assert.Equal(t, db.AnonymisedUsername(f.leaver.ID), row.Username)
	assert.Nil(t, row.LastSeenAt, "last_seen_at still says when the erased player was last here")

	var stayer db.User
	require.NoError(t, database.Where("id = ?", f.other.ID.String()).First(&stayer).Error)
	assert.Equal(t, "stayer", stayer.Username, "the other player's row is not this player's to touch")
}

func assertHistorySurvives(t *testing.T, database *gorm.DB, repo db.UserRepository, f deletionFixture) {
	t.Helper()
	ctx := context.Background()

	history, err := repo.UserMatchHistory(ctx, f.other.ID, 10)
	require.NoError(t, err)
	require.Len(t, history, 1, "the other player's history is their data, not the leaver's")
	assert.Equal(t, 2, history[0].Placement)

	// The erased seat is still in the match and still resolves - to the anonymised name.
	var seat db.MatchParticipant
	require.NoError(t, database.Preload("User").
		Where("match_id = ? AND user_id = ?", f.match.ID, f.leaver.ID.String()).First(&seat).Error)
	assert.Equal(t, db.AnonymisedUsername(f.leaver.ID), seat.User.Username)
	assert.Equal(t, 12, seat.EloDelta, "the Elo already paid out stays paid out")

	// Reading the row back is also what proves a NULL last_seen_at scans.
	profile, err := repo.UserProfile(ctx, f.leaver.ID)
	require.NoError(t, err)
	assert.Equal(t, db.AnonymisedUsername(f.leaver.ID), profile.Username)
	assert.True(t, profile.LastSeenAt.IsZero())
	assert.Empty(t, profile.Rankings)
	assert.Empty(t, profile.PublicKeys)
}

// Self-service erasure (GDPR art. 17). The account has to stop existing as an identity
// while the matches other players played against it keep resolving to a name.
func TestUserRepository_DeleteAccount(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()
	f := seedDeletionFixture(t, database, repo)

	// Warming the cache first is the point: the erased name has to go within the
	// request rather than whenever the five-minute TTL happens to lapse.
	before, err := repo.BestPlayers(ctx, 10, "")
	require.NoError(t, err)
	require.Len(t, before, 2, "both players start on the board")

	require.NoError(t, repo.DeleteAccount(ctx, f.leaver.ID))

	assertIdentityErased(t, database, f)
	assertHistorySurvives(t, database, repo, f)

	best, err := repo.BestPlayers(ctx, 10, "")
	require.NoError(t, err)
	require.Len(t, best, 1, "the erased account is off the leaderboard within the request")
	assert.Equal(t, "stayer", best[0].User.Username)

	require.NoError(t, repo.DeleteAccount(ctx, f.leaver.ID), "erasure repeats without failing")

	// The fingerprint is free again, and what it opens is a different account.
	returning, _, err := repo.RegisterUserWithKey(ctx, "returning", "fp_leaver")
	require.NoError(t, err)
	assert.NotEqual(t, f.leaver.ID, returning.ID, "a returning key must not reopen the erased account")
}

// An operator soft-delete hides the row from the default scope, and the anonymising
// UPDATE is default-scoped: it matched nothing, so erasure reported ErrUserNotFound for
// an account that still holds its name.
func TestUserRepository_DeleteAccountErasesASoftDeletedUser(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)
	ctx := context.Background()

	user, _, err := repo.RegisterUserWithKey(ctx, "hidden", "fp_hidden")
	require.NoError(t, err)
	require.NoError(t, database.Delete(&db.User{}, "id = ?", user.ID.String()).Error)

	require.NoError(t, repo.DeleteAccount(ctx, user.ID))

	var username string
	require.NoError(t, database.Raw(`SELECT username FROM users WHERE id = ?`, user.ID.String()).Scan(&username).Error)
	assert.Equal(t, db.AnonymisedUsername(user.ID), username, "the soft-deleted account kept its name")
}

// An id that was never a user is not a silent success: the caller asked to erase
// something specific and nothing was erased.
func TestUserRepository_DeleteAccountUnknownUser(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	repo := repository.NewUserRepository(database)

	assert.ErrorIs(t, repo.DeleteAccount(context.Background(), uuid.New()), db.ErrUserNotFound)
}
