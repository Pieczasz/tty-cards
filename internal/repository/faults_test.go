//go:build integration

package repository_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/repository"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var errInjected = errors.New("injected fault")

type hookKey struct{}

type statementHook struct {
	fragment string
	run      func(tx *gorm.DB)
}

// withHook arms run for the statements issued under the returned context whose SQL
// contains fragment. It runs after the statement and before the repository sees the
// result, so it can fail one step deep inside a transaction, or change the world in
// the gap between a check and the write that trusts it.
func withHook(ctx context.Context, fragment string, run func(tx *gorm.DB)) context.Context {
	return context.WithValue(ctx, hookKey{}, statementHook{fragment: fragment, run: run})
}

func failAt(ctx context.Context, fragment string) context.Context {
	return withHook(ctx, fragment, func(tx *gorm.DB) { _ = tx.AddError(errInjected) })
}

// installStatementHooks lets withHook reach database. The hook rides the context, so
// parallel subtests sharing one database each see only their own.
func installStatementHooks(t *testing.T, database *gorm.DB) {
	t.Helper()
	hook := func(tx *gorm.DB) {
		h, ok := tx.Statement.Context.Value(hookKey{}).(statementHook)
		if ok && strings.Contains(tx.Statement.SQL.String(), h.fragment) {
			h.run(tx)
		}
	}
	cb := database.Callback()
	require.NoError(t, cb.Create().After("gorm:create").Register("test:hook", hook))
	require.NoError(t, cb.Query().After("gorm:query").Register("test:hook", hook))
	require.NoError(t, cb.Update().After("gorm:update").Register("test:hook", hook))
	require.NoError(t, cb.Delete().After("gorm:delete").Register("test:hook", hook))
	require.NoError(t, cb.Raw().After("gorm:raw").Register("test:hook", hook))
}

// finalizeSnapshot is everything a finalize may write for one table.
type finalizeSnapshot struct {
	gameName     string
	rankings     []db.Ranking
	participants int64
}

func snapshotFinalize(t *testing.T, database *gorm.DB, slug string, ids []uuid.UUID) finalizeSnapshot {
	t.Helper()
	var s finalizeSnapshot
	require.NoError(t, database.Model(&db.Game{}).Where("slug = ?", slug).Pluck("name", &s.gameName).Error)
	require.NoError(t, database.Select("user_id", "game_id", "elo", "matches_played").
		Where("user_id IN ?", uuidStrings(ids)).Order("user_id").Find(&s.rankings).Error)
	require.NoError(t, database.Model(&db.MatchParticipant{}).
		Where("user_id IN ?", uuidStrings(ids)).Count(&s.participants).Error)
	return s
}

func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// A finalize is one transaction: a failure at any step, however deep, must surface
// with the step it happened at and leave nothing behind - no rating moved, no history
// row, not even the rename the game upsert carried.
func TestFinalizeFailureAtAnyStepWritesNothing(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	installStatementHooks(t, database)
	matches := repository.NewMatchRepository(database)

	tests := []struct {
		fragment string
		want     string
	}{
		{`FROM "games" WHERE slug`, "find game"},
		{`INSERT INTO "games"`, "create game"},
		{`pg_advisory_xact_lock`, "lock seat key"},
		{`username LIKE`, "query erased seats"},
		{`UPDATE "rankings" SET "deleted_at"`, "revive rankings"},
		{`INSERT INTO "rankings"`, "seed rankings"},
		{`FOR UPDATE`, "fetch rankings"},
		{`matches.ranked`, "query recent pairings"},
		{`FROM "match_participants" WHERE match_id IN`, "query pairing participants"},
		{`UPDATE "rankings" SET "elo"`, "update ranking"},
		{`INSERT INTO "matches"`, "create match"},
	}
	for i, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			slug := fmt.Sprintf("fault_%d", i)
			ids := make([]uuid.UUID, 0, 2)
			for seat := range 2 {
				u := &db.User{Username: fmt.Sprintf("f%d_%d", i, seat)}
				require.NoError(t, database.Create(u).Error)
				ids = append(ids, u.ID)
			}
			// A match already played gives the pairing count participants to read.
			require.NoError(t, matches.FinalizeRankedMatch(ctx, db.GameRef{Slug: slug, Name: "Before"}, ids, nil))
			before := snapshotFinalize(t, database, slug, ids)

			err := matches.FinalizeRankedMatch(failAt(ctx, tt.fragment), db.GameRef{Slug: slug, Name: "After"}, ids, nil)
			require.ErrorIs(t, err, errInjected)
			require.ErrorContains(t, err, "finalize ranked match: "+tt.want)
			assert.Equal(t, before, snapshotFinalize(t, database, slug, ids), "a failed finalize left a write behind")
		})
	}
}

func TestRecordCasualMatchReportsAFailedGameLookup(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	installStatementHooks(t, database)
	u := &db.User{Username: "casual"}
	require.NoError(t, database.Create(u).Error)

	err := repository.NewMatchRepository(database).
		RecordCasualMatch(failAt(t.Context(), `FROM "games"`), gameRef("Poker"), []uuid.UUID{u.ID})
	require.ErrorIs(t, err, errInjected)
	assert.ErrorContains(t, err, "record casual match: find game")
}

// Erasure is one transaction too: a failure part-way must not leave an account that
// has lost its key but still carries its name, or the reverse.
func TestDeleteAccountFailureAtAnyStepErasesNothing(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	installStatementHooks(t, database)
	users := repository.NewUserRepository(database)

	tests := []struct {
		fragment string
		want     string
	}{
		{`pg_advisory_xact_lock`, "lock seat key"},
		{`DELETE FROM "public_keys"`, "delete public keys"},
		{`DELETE FROM "rankings"`, "delete rankings"},
		{`UPDATE "users" SET`, "anonymise user"},
	}
	for i, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			name := fmt.Sprintf("erase_%d", i)
			user, _, err := users.RegisterUserWithSSHKey(ctx, name, "fp_"+name)
			require.NoError(t, err)

			err = users.DeleteAccount(failAt(ctx, tt.fragment), user.ID)
			require.ErrorIs(t, err, errInjected)
			require.ErrorContains(t, err, "delete account transaction: "+tt.want)

			still, key, err := users.LoadUserByFingerprint(ctx, "fp_"+name)
			require.NoError(t, err)
			require.NotNil(t, key, "the failed erasure removed the key")
			assert.Equal(t, name, still.Username, "the failed erasure anonymised the account")
		})
	}
}

func TestRegisterFailureAtAnyStepCreatesNothing(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	installStatementHooks(t, database)
	users := repository.NewUserRepository(database)

	tests := []struct {
		fragment string
		want     string
	}{
		{`lower(username)`, "check username"},
		{`FROM "public_keys" WHERE fingerprint`, "check fingerprint"},
		{`INSERT INTO "users"`, "create user"},
		{`INSERT INTO "public_keys"`, "create public key"},
	}
	for i, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			name := fmt.Sprintf("reg_%d", i)
			_, _, err := users.RegisterUserWithSSHKey(failAt(t.Context(), tt.fragment), name, "fp_"+name)
			require.ErrorIs(t, err, errInjected)
			require.ErrorContains(t, err, "register user transaction: "+tt.want)
			assertNoUser(t, database, name)
		})
	}
}

// The existence checks cannot see a registration that commits after them; the unique
// indexes can. Losing that race has to read as the same refusal the check gives, not
// as a failed registration.
func TestRegisterLosingTheRaceAfterItsCheck(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	installStatementHooks(t, database)
	users := repository.NewUserRepository(database)
	ctx := t.Context()

	t.Run("to the same name", func(t *testing.T) {
		t.Parallel()
		racing := withHook(ctx, `lower(username)`, func(*gorm.DB) {
			_, _, err := users.RegisterUserWithSSHKey(ctx, "Race_Name", "fp_race_winner")
			require.NoError(t, err)
		})
		_, _, err := users.RegisterUserWithSSHKey(racing, "race_name", "fp_race_loser")
		require.ErrorIs(t, err, db.ErrUsernameTaken)
	})

	t.Run("to the same key", func(t *testing.T) {
		t.Parallel()
		racing := withHook(ctx, `FROM "public_keys" WHERE fingerprint`, func(*gorm.DB) {
			_, _, err := users.RegisterUserWithSSHKey(ctx, "key_winner", "fp_race_key")
			require.NoError(t, err)
		})
		_, _, err := users.RegisterUserWithSSHKey(racing, "key_loser", "fp_race_key")
		require.ErrorIs(t, err, db.ErrKeyAlreadyRegistered)
		assertNoUser(t, database, "key_loser")
	})
}

func assertNoUser(t *testing.T, database *gorm.DB, name string) {
	t.Helper()
	var n int64
	require.NoError(t, database.Unscoped().Model(&db.User{}).Where("username = ?", name).Count(&n).Error)
	assert.Zero(t, n, "a failed registration left the account %q behind", name)
}

func TestUpdateUserActivityReportsAFailedKeyStamp(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	installStatementHooks(t, database)
	users := repository.NewUserRepository(database)
	user, key, err := users.RegisterUserWithSSHKey(t.Context(), "stamp", "fp_stamp")
	require.NoError(t, err)

	err = users.UpdateUserActivity(failAt(t.Context(), `UPDATE "public_keys"`), user, key)
	require.ErrorIs(t, err, errInjected)
	assert.ErrorContains(t, err, "update key last used")
}
