//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A login stamps activity synchronously, and the stamp is allowed to fail, so it must
// not queue behind a ranked finalize holding this player's seat lock.
func TestUpdateUserActivityDoesNotWaitForAFinalize(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)
	repo := NewUserRepository(gormDB)
	user, key, err := repo.RegisterUserWithSSHKey(t.Context(), "seat_held", "seat_held_fp")
	require.NoError(t, err)
	stale := time.Now().Add(-24 * time.Hour).UTC()
	require.NoError(t, gormDB.Model(&db.User{}).Where("id = ?", user.ID.String()).
		Update("last_seen_at", stale).Error)

	finalize := gormDB.Begin()
	t.Cleanup(func() { finalize.Rollback() })
	require.NoError(t, lockSeat(finalize, user.ID))

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	require.NoError(t, repo.UpdateUserActivity(ctx, user.ID, key.ID))

	var row db.User
	require.NoError(t, gormDB.First(&row, "id = ?", user.ID.String()).Error)
	assert.True(t, row.LastSeenAt.After(stale), "last_seen_at did not move")
}
