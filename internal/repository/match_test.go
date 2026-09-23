//go:build integration

package repository_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/elo"
	"github.com/Pieczasz/terminal-card/internal/repository"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// gameRef is what the lobby hands the repository: the slug is the stable key rows are
// written under, the name is display only.
const (
	antiFarmGame = "Poker"
	antiFarmSlug = "poker"
)

func gameRef(name string) db.GameRef {
	return db.GameRef{Slug: strings.ToLower(strings.ReplaceAll(name, " ", "_")), Name: name}
}

func TestMatchRepositoryRecordCasualMatch(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)

	u1 := &db.User{Username: "player1"}
	u2 := &db.User{Username: "player2"}
	require.NoError(t, gormDB.Create(u1).Error)
	require.NoError(t, gormDB.Create(u2).Error)

	repo := repository.NewMatchRepository(gormDB)
	ctx := t.Context()

	require.NoError(t, repo.RecordCasualMatch(ctx, gameRef("Crazy Eights"), []uuid.UUID{u1.ID, u2.ID}))

	var match db.Match
	require.NoError(t, gormDB.Preload("Participants").First(&match).Error)
	assert.False(t, match.Ranked, "a casual match is recorded without changing Elo")
	assert.Len(t, match.Participants, 2)

	var game db.Game
	require.NoError(t, gormDB.Where("name = ?", "Crazy Eights").First(&game).Error)
	assert.Equal(t, game.ID, match.GameID, "the game row is created on first sight")

	// A casual result must not create rankings.
	var rankings int64
	require.NoError(t, gormDB.Model(&db.Ranking{}).Count(&rankings).Error)
	assert.Zero(t, rankings)
}

// getOrCreateGame is an internal transaction helper, so its idempotence is only
// observable through a second RecordCasualMatch reusing the same games row.
func TestMatchRepositoryReusesTheGameRow(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)

	u := &db.User{Username: "solo"}
	require.NoError(t, gormDB.Create(u).Error)

	repo := repository.NewMatchRepository(gormDB)
	ctx := t.Context()

	require.NoError(t, repo.RecordCasualMatch(ctx, gameRef("Poker"), []uuid.UUID{u.ID}))
	require.NoError(t, repo.RecordCasualMatch(ctx, gameRef("Poker"), []uuid.UUID{u.ID}))

	var games []db.Game
	require.NoError(t, gormDB.Where("name = ?", "Poker").Find(&games).Error)
	require.Len(t, games, 1, "the second match must reuse the first game row")

	var matches int64
	require.NoError(t, gormDB.Model(&db.Match{}).Where("game_id = ?", games[0].ID).Count(&matches).Error)
	assert.Equal(t, int64(2), matches)
}

func TestMatchRepositoryFinalizeRankedMatch(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)

	u1 := &db.User{Username: "final1"}
	u2 := &db.User{Username: "final2"}
	require.NoError(t, gormDB.Create(u1).Error)
	require.NoError(t, gormDB.Create(u2).Error)

	repo := repository.NewMatchRepository(gormDB)
	ctx := t.Context()

	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Crazy Eights"), []uuid.UUID{u1.ID, u2.ID}, nil))

	var game db.Game
	require.NoError(t, gormDB.Where("name = ?", "Crazy Eights").First(&game).Error)

	var matchCount int64
	require.NoError(t, gormDB.Model(&db.Match{}).Where("game_id = ?", game.ID).Count(&matchCount).Error)
	assert.Equal(t, int64(1), matchCount)

	var r1 db.Ranking
	require.NoError(t, gormDB.Where("user_id = ? AND game_id = ?", u1.ID.String(), game.ID).First(&r1).Error)
	assert.Greater(t, r1.Elo, uint32(elo.DefaultRating))
}

func TestMatchRepositoryRejectsUnknownUsers(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	repo := repository.NewMatchRepository(gormDB)
	ctx := t.Context()

	require.NoError(t, repo.RecordCasualMatch(ctx, gameRef("Poker"), nil), "no players is a no-op")
	require.Error(t, repo.RecordCasualMatch(ctx, gameRef("Poker"), []uuid.UUID{uuid.New(), uuid.New()}),
		"the participants foreign key must reject users that do not exist")
}

// A repeated seat would move that player's rating twice before colliding on the
// match_participants primary key and rolling the whole match back.
func TestMatchRepositoryRejectsDuplicateUserIDs(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)

	u := &db.User{Username: "twice"}
	require.NoError(t, gormDB.Create(u).Error)

	repo := repository.NewMatchRepository(gormDB)
	ctx := t.Context()

	require.ErrorContains(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), []uuid.UUID{u.ID, u.ID}, nil),
		"duplicate user id")
	require.ErrorContains(t, repo.RecordCasualMatch(ctx, gameRef("Poker"), []uuid.UUID{u.ID, u.ID}),
		"duplicate user id")

	var matches int64
	require.NoError(t, gormDB.Model(&db.Match{}).Count(&matches).Error)
	assert.Zero(t, matches, "the duplicate is rejected before anything is written")
}

// Reproduction: a brand-new (user_id, game_id) has no row for the FOR UPDATE select
// to lock, so two concurrent finalizes both inserted the ranking, one lost to a
// unique violation and its entire match - Elo and history - was rolled back.
func TestMatchRepositoryConcurrentFinalizeFirstEverMatch(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)

	u1 := &db.User{Username: "fresh1"}
	u2 := &db.User{Username: "fresh2"}
	require.NoError(t, gormDB.Create(u1).Error)
	require.NoError(t, gormDB.Create(u2).Error)

	repo := repository.NewMatchRepository(gormDB)

	const workers = 4
	errs := make(chan error, workers)
	start := make(chan struct{})
	for range workers {
		go func() {
			<-start
			errs <- repo.FinalizeRankedMatch(t.Context(), gameRef("Poker"), []uuid.UUID{u1.ID, u2.ID}, nil)
		}()
	}
	close(start)
	for range workers {
		require.NoError(t, <-errs, "a first-ever ranking must not lose the match to a write conflict")
	}

	var matches int64
	require.NoError(t, gormDB.Model(&db.Match{}).Count(&matches).Error)
	assert.Equal(t, int64(workers), matches, "every finalize has to leave history behind")

	var rankings int64
	require.NoError(t, gormDB.Model(&db.Ranking{}).Count(&rankings).Error)
	assert.Equal(t, int64(2), rankings, "one ranking row per player, not one per match")
}

// Proves the FOR UPDATE lock actually serializes: concurrent finalizes on an
// existing ranking must land on the same Elo as running them one after another.
func TestMatchRepositoryConcurrentFinalizeMatchesSequential(t *testing.T) {
	t.Parallel()
	// Two rounds on top of the seed keeps the pair under maxSamePairingPerDay in both
	// orders: run sequentially the fourth would be damped, run concurrently the
	// uncommitted siblings are invisible to each other and none would be.
	const rounds = 2

	elos := func(t *testing.T, concurrent bool) (uint32, uint32) {
		t.Helper()
		gormDB := testutil.SetupTestDB(t)

		u1 := &db.User{Username: "seq1"}
		u2 := &db.User{Username: "seq2"}
		require.NoError(t, gormDB.Create(u1).Error)
		require.NoError(t, gormDB.Create(u2).Error)

		repo := repository.NewMatchRepository(gormDB)
		ctx := t.Context()

		// Seed the rankings so this exercises the locking path, not the seeding one.
		require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), []uuid.UUID{u1.ID, u2.ID}, nil))

		run := func() error { return repo.FinalizeRankedMatch(ctx, gameRef("Poker"), []uuid.UUID{u1.ID, u2.ID}, nil) }
		if concurrent {
			errs := make(chan error, rounds)
			start := make(chan struct{})
			for range rounds {
				go func() {
					<-start
					errs <- run()
				}()
			}
			close(start)
			for range rounds {
				require.NoError(t, <-errs)
			}
		} else {
			for range rounds {
				require.NoError(t, run())
			}
		}

		var got []db.Ranking
		require.NoError(t, gormDB.Where("user_id IN ?", []string{u1.ID.String(), u2.ID.String()}).
			Order("user_id").Find(&got).Error)
		require.Len(t, got, 2)
		return got[0].Elo, got[1].Elo
	}

	wantWinner, wantLoser := elos(t, false)
	gotWinner, gotLoser := elos(t, true)

	assert.Equal(t, wantWinner, gotWinner, "a lost update would leave the winner short")
	assert.Equal(t, wantLoser, gotLoser)
}

// Reproduction: a ranked match must still read back as ranked through the history
// the profile screen renders from. The write and the read are in different
// repositories, so each being correct on its own does not prove the pair is.
func TestRankedMatchReadsBackAsRankedInHistory(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)

	u1 := &db.User{Username: "winner"}
	u2 := &db.User{Username: "loser"}
	require.NoError(t, gormDB.Create(u1).Error)
	require.NoError(t, gormDB.Create(u2).Error)

	matches := repository.NewMatchRepository(gormDB)
	users := repository.NewUserRepository(gormDB)
	ctx := t.Context()

	require.NoError(t, matches.FinalizeRankedMatch(ctx, gameRef("Poker"), []uuid.UUID{u1.ID, u2.ID}, nil))

	var stored db.Match
	require.NoError(t, gormDB.First(&stored).Error)
	assert.True(t, stored.Ranked, "the row itself has to say ranked")

	history, err := users.UserMatchHistory(ctx, u1.ID, 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.True(t, history[0].Match.Ranked,
		"the profile reads Match.Ranked from here, so it has to survive the preload")
	assert.Equal(t, "Poker", history[0].Match.Game.Name)
	assert.NotZero(t, history[0].EloDelta, "a ranked match moved the rating")
}

// seat is a starting ranking row: matches_played below the provisional threshold is
// what makes the seat provisional, and distinct Elos keep every seat's natural delta
// nonzero (equal ratings cancel out to zero for the middle seats of a table, which
// would make "the delta was zeroed" unfalsifiable).
type seat struct {
	elo    uint32
	played uint64
}

// antiFarmTable creates the game and one user per seat, pre-seeding the ranking rows
// so a track record does not cost five real matches to build. Every anti-farm case
// uses one game: the rules are cross-game, so which one it is proves nothing.
func antiFarmTable(t *testing.T, gormDB *gorm.DB, seats ...seat) (uint, []uuid.UUID) {
	t.Helper()

	game := db.Game{Slug: antiFarmSlug, Name: antiFarmGame}
	require.NoError(t, gormDB.Create(&game).Error)

	userIDs := make([]uuid.UUID, 0, len(seats))
	for i, s := range seats {
		u := db.User{Username: fmt.Sprintf("seat%d", i)}
		require.NoError(t, gormDB.Create(&u).Error)
		require.NoError(t, gormDB.Create(&db.Ranking{
			UserID: u.ID, GameID: game.ID, Elo: s.elo, MatchesPlayed: s.played,
		}).Error)
		userIDs = append(userIDs, u.ID)
	}
	return game.ID, userIDs
}

func rankingOf(t *testing.T, gormDB *gorm.DB, userID uuid.UUID, gameID uint) db.Ranking {
	t.Helper()
	var r db.Ranking
	require.NoError(t, gormDB.Where("user_id = ? AND game_id = ?", userID.String(), gameID).First(&r).Error)
	return r
}

// lastMatchDeltas is the elo_delta history of the most recent match, which is where a
// damped or unpaid result has to show up as a zero rather than as nothing at all.
func lastMatchDeltas(t *testing.T, gormDB *gorm.DB) map[uuid.UUID]int {
	t.Helper()
	var match db.Match
	require.NoError(t, gormDB.Preload("Participants").Order("id DESC").First(&match).Error)

	deltas := make(map[uuid.UUID]int, len(match.Participants))
	for _, p := range match.Participants {
		deltas[p.UserID] = p.EloDelta
	}
	return deltas
}

// Minting an account is free, so beating a fresh one must pay nothing - while the
// fresh account's own rating still converges. The rule is per pair, not per table:
// an established seat that lost to the fresh account still pays, or seating an alt
// would freeze a rating in place, and seats that never faced it are rated as usual.
func TestFinalizeRankedMatchProvisionalOpponentPaysNothing(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		seats []seat
		moves []bool // whether each seat's rating changes at all
	}{
		{
			"heads up, established beats fresh: only the fresh account moves",
			[]seat{{1500, 10}, {1500, 0}},
			[]bool{false, true},
		},
		{
			"heads up, fresh beats established: the loss still costs",
			[]seat{{1500, 0}, {1500, 10}},
			[]bool{true, true},
		},
		{
			"one fresh account at a table of four: only its beaten neighbour is unpaid",
			[]seat{{1500, 10}, {1450, 0}, {1300, 12}, {1100, 7}},
			[]bool{false, true, true, true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gormDB := testutil.SetupTestDB(t)
			gameID, userIDs := antiFarmTable(t, gormDB, tt.seats...)

			repo := repository.NewMatchRepository(gormDB)
			require.NoError(t, repo.FinalizeRankedMatch(t.Context(), gameRef("Poker"), userIDs, nil))

			deltas := lastMatchDeltas(t, gormDB)
			require.Len(t, deltas, len(tt.seats), "history is written either way")

			for i, s := range tt.seats {
				r := rankingOf(t, gormDB, userIDs[i], gameID)
				assert.Equal(t, s.played+1, r.MatchesPlayed, "seat %d has played another match", i)

				if tt.moves[i] {
					assert.NotEqual(t, s.elo, r.Elo, "seat %d is rated", i)
					assert.NotZero(t, deltas[userIDs[i]], "and its history records the move")
					continue
				}
				assert.Equal(t, s.elo, r.Elo, "seat %d must not be paid by a provisional", i)
				assert.Zero(t, deltas[userIDs[i]], "and its history delta is zeroed too")
			}
		})
	}
}

// Two accounts trading wins all day is farming or a private rivalry; either way the
// fourth match of the day between the same set stops paying.
func TestFinalizeRankedMatchDampsRepeatedPairing(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	gameID, userIDs := antiFarmTable(t, gormDB, seat{1500, 10}, seat{1500, 10})

	repo := repository.NewMatchRepository(gormDB)
	ctx := t.Context()

	for range 3 {
		require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), userIDs, nil))
	}
	before := []db.Ranking{
		rankingOf(t, gormDB, userIDs[0], gameID),
		rankingOf(t, gormDB, userIDs[1], gameID),
	}
	require.NotEqual(t, uint32(1500), before[0].Elo, "the first three still move rating")

	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), userIDs, nil))

	deltas := lastMatchDeltas(t, gormDB)
	require.Len(t, deltas, 2, "the damped match is still recorded")
	for i, userID := range userIDs {
		after := rankingOf(t, gormDB, userID, gameID)
		assert.Equal(t, before[i].Elo, after.Elo, "the fourth pairing of the day moves nothing")
		assert.Zero(t, deltas[userID], "and records a zero delta")
		assert.Equal(t, before[i].MatchesPlayed+1, after.MatchesPlayed,
			"a damped match is still a match played, or a farmer never graduates")
	}

	var matches int64
	require.NoError(t, gormDB.Model(&db.Match{}).Count(&matches).Error)
	assert.Equal(t, int64(4), matches)
}

// Switching game is not a new pairing: A and B meeting at Poker then Hearts is still
// the same two accounts trading rating. The fourth match of the day pays nobody.
func TestFinalizeRankedMatchDampsAPairAcrossGames(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	ctx := t.Context()
	repo := repository.NewMatchRepository(gormDB)

	u1 := &db.User{Username: "cross1"}
	u2 := &db.User{Username: "cross2"}
	require.NoError(t, gormDB.Create(u1).Error)
	require.NoError(t, gormDB.Create(u2).Error)
	ids := []uuid.UUID{u1.ID, u2.ID}

	for _, g := range []db.Game{
		{Slug: "poker", Name: "Poker"},
		{Slug: "hearts", Name: "Hearts"},
	} {
		require.NoError(t, gormDB.Create(&g).Error)
		for _, id := range ids {
			require.NoError(t, gormDB.Create(&db.Ranking{
				UserID: id, GameID: g.ID, Elo: 1500, MatchesPlayed: 10,
			}).Error)
		}
	}

	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), ids, nil))
	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), ids, nil))
	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Hearts"), ids, nil))

	before := rankingOfSlug(t, gormDB, u1.ID, "hearts")
	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Hearts"), ids, nil))

	deltas := lastMatchDeltas(t, gormDB)
	after := rankingOfSlug(t, gormDB, u1.ID, "hearts")
	assert.Equal(t, before.Elo, after.Elo, "the fourth pairing of the day across games still pays")
	assert.Zero(t, deltas[u1.ID])
	assert.Zero(t, deltas[u2.ID])
	assert.Equal(t, before.MatchesPlayed+1, after.MatchesPlayed)
}

// matches_played is what a provisional account graduates on, so a finalize that
// forgot to increment it would leave every account provisional forever.
func TestFinalizeRankedMatchCountsMatchesPlayed(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)

	u1 := &db.User{Username: "counted1"}
	u2 := &db.User{Username: "counted2"}
	require.NoError(t, gormDB.Create(u1).Error)
	require.NoError(t, gormDB.Create(u2).Error)

	repo := repository.NewMatchRepository(gormDB)
	ctx := t.Context()

	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), []uuid.UUID{u1.ID, u2.ID}, nil))
	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), []uuid.UUID{u1.ID, u2.ID}, nil))

	var game db.Game
	require.NoError(t, gormDB.Where("name = ?", "Poker").First(&game).Error)

	for _, u := range []*db.User{u1, u2} {
		r := rankingOf(t, gormDB, u.ID, game.ID)
		assert.Equal(t, uint64(2), r.MatchesPlayed, "a seeded row starts at zero and counts up")
		// Both seats are provisional, so both ratings still move.
		assert.NotEqual(t, uint32(elo.DefaultRating), r.Elo)
	}
}

// Soft-deleted rankings used to make seed's ON CONFLICT DO NOTHING leave fetch
// empty, aborting the whole table's Elo write.
func TestFinalizeRankedMatchRevivesSoftDeletedRanking(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	gameID, userIDs := antiFarmTable(t, gormDB, seat{1600, 3}, seat{1400, 3})

	require.NoError(t, gormDB.Where("user_id = ? AND game_id = ?", userIDs[0].String(), gameID).
		Delete(&db.Ranking{}).Error)

	repo := repository.NewMatchRepository(gormDB)
	require.NoError(t, repo.FinalizeRankedMatch(t.Context(), gameRef("Poker"), userIDs, nil))

	r := rankingOf(t, gormDB, userIDs[0], gameID)
	assert.False(t, r.DeletedAt.Valid, "the soft-deleted row is revived")
	assert.Equal(t, uint64(4), r.MatchesPlayed)
}

// A soft-deleted games row still occupies the unique slug, so ON CONFLICT DO NOTHING
// left the insert with no id and the default-scoped reload could not see the row it
// had just conflicted with: every finalize for that game failed, forever.
func TestMatchRepositoryRevivesASoftDeletedGame(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	ctx := t.Context()
	repo := repository.NewMatchRepository(gormDB)

	u1 := &db.User{Username: "revive1"}
	u2 := &db.User{Username: "revive2"}
	require.NoError(t, gormDB.Create(u1).Error)
	require.NoError(t, gormDB.Create(u2).Error)

	buried := db.Game{Slug: "poker", Name: "Poker"}
	require.NoError(t, gormDB.Create(&buried).Error)
	require.NoError(t, gormDB.Delete(&buried).Error)

	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), []uuid.UUID{u1.ID, u2.ID}, nil))

	var game db.Game
	require.NoError(t, gormDB.Where("slug = ?", "poker").First(&game).Error)
	assert.Equal(t, buried.ID, game.ID, "the finalize created a second row instead of reviving the first")

	var matches int64
	require.NoError(t, gormDB.Model(&db.Match{}).Where("game_id = ?", game.ID).Count(&matches).Error)
	assert.Equal(t, int64(1), matches)
}

// The slug is the identity a rating hangs off; the display name is refreshed in place.
// Keying on the name meant a rename created a second games row and orphaned every
// ranking on the first.
func TestMatchRepositoryRenamingAGameKeepsItsRatings(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	ctx := t.Context()
	repo := repository.NewMatchRepository(gormDB)

	u1 := &db.User{Username: "rename1"}
	u2 := &db.User{Username: "rename2"}
	require.NoError(t, gormDB.Create(u1).Error)
	require.NoError(t, gormDB.Create(u2).Error)

	require.NoError(t, repo.FinalizeRankedMatch(ctx, db.GameRef{Slug: "poker", Name: "Poker"},
		[]uuid.UUID{u1.ID, u2.ID}, nil))
	before := rankingOfSlug(t, gormDB, u1.ID, "poker")

	// Same slug, new display name - what renaming the catalog entry looks like here.
	require.NoError(t, repo.FinalizeRankedMatch(ctx, db.GameRef{Slug: "poker", Name: "Texas Holdem"},
		[]uuid.UUID{u1.ID, u2.ID}, nil))

	var games []db.Game
	require.NoError(t, gormDB.Find(&games).Error)
	require.Len(t, games, 1, "the rename created a second game row")
	assert.Equal(t, "Texas Holdem", games[0].Name, "the display name did not follow the rename")

	after := rankingOfSlug(t, gormDB, u1.ID, "poker")
	assert.Equal(t, before.GameID, after.GameID, "the rating moved to a different game")
	assert.Equal(t, uint64(2), after.MatchesPlayed, "the track record was orphaned by the rename")
}

func rankingOfSlug(t *testing.T, gormDB *gorm.DB, userID uuid.UUID, slug string) db.Ranking {
	t.Helper()
	var game db.Game
	require.NoError(t, gormDB.Where("slug = ?", slug).First(&game).Error)
	return rankingOf(t, gormDB, userID, game.ID)
}

// Two accounts do not become legitimate by padding the table with a third: {A,B},
// {A,B,C} and {A,B,D} are three different participant sets, so a cap on exact-set
// repeats handed each of them its own budget and A and B could trade rating all day.
// The cap counts pairs, so A and B meeting for the fourth time pays nobody.
func TestFinalizeRankedMatchDampsAPairPaddedWithAlts(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	ctx := t.Context()
	repo := repository.NewMatchRepository(gormDB)

	// Six established seats: A and B farm, C/D/E/F are the padding.
	seats := make([]seat, 6)
	for i := range seats {
		seats[i] = seat{elo: 1500, played: 10}
	}
	gameID, ids := antiFarmTable(t, gormDB, seats...)
	a, b, padding := ids[0], ids[1], ids[2:]

	// Three different participant sets, each containing the same pair.
	for _, extra := range padding[:3] {
		require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), []uuid.UUID{a, b, extra}, nil))
	}

	before := rankingOf(t, gormDB, a, gameID)
	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), []uuid.UUID{a, b, padding[3]}, nil))
	after := rankingOf(t, gormDB, a, gameID)

	assert.Equal(t, before.Elo, after.Elo, "a padded repeat of the same pair still paid out")
	assert.Zero(t, lastMatchDeltas(t, gormDB)[a], "the damped result recorded a nonzero delta")
	assert.Equal(t, before.MatchesPlayed+1, after.MatchesPlayed, "a damped match is still a match played")
}

// Two tables that share a seat must not both read the pair count before either writes,
// or the cap can be undercut by finalizing in parallel. One advisory lock per seat,
// taken in user-id order, is what serializes them.
func TestFinalizeRankedMatchConcurrentOverlappingTablesRespectTheCap(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	ctx := t.Context()
	repo := repository.NewMatchRepository(gormDB)

	seats := make([]seat, 4)
	for i := range seats {
		seats[i] = seat{elo: 1500, played: 10}
	}
	gameID, ids := antiFarmTable(t, gormDB, seats...)
	a, b, c, d := ids[0], ids[1], ids[2], ids[3]

	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), []uuid.UUID{a, b, c}, nil))
	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), []uuid.UUID{a, b, d}, nil))
	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), []uuid.UUID{a, b, c}, nil))

	before := rankingOf(t, gormDB, a, gameID)

	// Both tables hold the same pair and are already at the cap.
	errs := make(chan error, 2)
	for _, third := range []uuid.UUID{c, d} {
		go func(third uuid.UUID) {
			errs <- repo.FinalizeRankedMatch(t.Context(), gameRef("Poker"), []uuid.UUID{a, b, third}, nil)
		}(third)
	}
	for range 2 {
		require.NoError(t, <-errs)
	}

	after := rankingOf(t, gormDB, a, gameID)
	assert.Equal(t, before.Elo, after.Elo, "parallel finalizes slipped past the pair cap")
	assert.Equal(t, before.MatchesPlayed+2, after.MatchesPlayed)
}

// An empty table is not an error: a game that ended with nobody in it has nothing to
// record, and refusing it would turn a finished match into a logged failure.
func TestMatchRepositoryEmptyStandingsAreANoOp(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	ctx := t.Context()
	repo := repository.NewMatchRepository(gormDB)

	require.NoError(t, repo.FinalizeRankedMatch(ctx, gameRef("Poker"), nil, nil))
	require.NoError(t, repo.RecordCasualMatch(ctx, gameRef("Poker"), nil))

	var matches, games int64
	require.NoError(t, gormDB.Model(&db.Match{}).Count(&matches).Error)
	require.NoError(t, gormDB.Model(&db.Game{}).Count(&games).Error)
	assert.Zero(t, matches, "an empty table wrote a match row")
	assert.Zero(t, games, "an empty table created a game row")
}
