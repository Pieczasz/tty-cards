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

// Every query here has an error return that must be reported rather than swallowed: a
// finalize that quietly does nothing loses a match, and a login that quietly fails
// authenticates nobody. Closing the pool is the cheapest way to make all of them fail
// at once, against the real driver rather than a stub that cannot get the wrapping
// wrong.
func TestRepositoriesReportDatabaseFailures(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)
	ctx := context.Background()

	users := repository.NewUserRepository(database)
	matches := repository.NewMatchRepository(database)

	// One real user first, so the update paths have a row to aim at before the pool
	// goes away and are failing on the query, not on a missing record.
	user, key, err := users.RegisterUserWithKey(ctx, "closed_pool", "closed_fp")
	require.NoError(t, err)

	sqlDB, err := database.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())

	ref := db.GameRef{Slug: "closed", Name: "Closed"}
	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "loading a user",
			call: func() error { _, _, err := users.LoadUserByFingerprint(ctx, "closed_fp"); return err },
			want: "load user by fingerprint",
		},
		{
			name: "registering a user",
			call: func() error { _, _, err := users.RegisterUserWithKey(ctx, "another", "another_fp"); return err },
			want: "register user transaction",
		},
		{
			name: "the leaderboard",
			call: func() error { _, err := users.BestPlayers(ctx, "", 10); return err },
			want: "get best players",
		},
		{
			name: "a profile",
			call: func() error { _, err := users.UserProfile(ctx, user.ID); return err },
			want: "get user profile",
		},
		{
			name: "match history",
			call: func() error { _, err := users.UserMatchHistory(ctx, user.ID, 10); return err },
			want: "get user match history",
		},
		{
			name: "stamping activity",
			call: func() error { return users.UpdateUserActivity(ctx, user, key) },
			want: "update last seen",
		},
		{
			name: "erasing an account",
			call: func() error { return users.DeleteAccount(ctx, user.ID) },
			want: "delete account transaction",
		},
		{
			name: "recording a casual match",
			call: func() error { return matches.RecordCasualMatch(ctx, ref, []uuid.UUID{user.ID}) },
			want: "record casual match",
		},
		{
			name: "finalizing a ranked match",
			call: func() error { return matches.FinalizeRankedMatch(ctx, ref, []uuid.UUID{user.ID}, nil) },
			want: "finalize ranked match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.call()
			require.Error(t, err, "the failure was swallowed")
			assert.ErrorContains(t, err, tt.want, "the error lost the context of what failed")
		})
	}
}
