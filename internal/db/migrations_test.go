//go:build integration

package db_test

import (
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// migrateUpTo applies every up migration whose file name sorts before stop.
func migrateUpTo(t *testing.T, gormDB *gorm.DB, stop string) {
	t.Helper()
	steps, err := fs.Glob(db.Migrations, "migrations/*.up.sql")
	require.NoError(t, err)
	slices.Sort(steps)
	for _, step := range steps {
		if strings.TrimPrefix(step, "migrations/") >= stop {
			return
		}
		applyMigration(t, gormDB, step)
	}
}

func applyMigration(t *testing.T, gormDB *gorm.DB, step string) {
	t.Helper()
	require.NoError(t, execMigration(gormDB, step), "migration %s", step)
}

func execMigration(gormDB *gorm.DB, step string) error {
	sql, err := db.Migrations.ReadFile(step)
	if err != nil {
		return err
	}
	return gormDB.Exec(string(sql)).Error
}

// D-4: 000006 must not guess which of two case-colliding accounts keeps the name. It
// refuses, and says which names collide, so the operator can decide.
func TestMigration000006_RefusesExistingCaseCollisions(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupEmptyTestDB(t)
	migrateUpTo(t, gormDB, "000006")

	for _, name := range []string{"Alice", "alice", "bob"} {
		require.NoError(t, gormDB.Exec(`INSERT INTO users (username) VALUES (?)`, name).Error)
	}

	err := execMigration(gormDB, "migrations/000006_username_ci.up.sql")
	require.Error(t, err, "the migration went ahead over a collision")
	assert.Contains(t, err.Error(), "Alice, alice", "the error does not name the collision")
	assert.NotContains(t, err.Error(), "bob")

	var indexes int64
	require.NoError(t, gormDB.Raw(
		`SELECT count(*) FROM pg_indexes WHERE indexname = 'idx_users_username_lower'`).Scan(&indexes).Error)
	assert.Zero(t, indexes, "a refused migration left its index behind")

	// Resolved by the operator, it goes through, and from then on the database itself
	// refuses a case variant - which is what closes a concurrent-registration race.
	require.NoError(t, gormDB.Exec(`UPDATE users SET username = 'alice_2' WHERE username = 'alice'`).Error)
	applyMigration(t, gormDB, "migrations/000006_username_ci.up.sql")
	assert.Error(t, gormDB.Exec(`INSERT INTO users (username) VALUES ('BOB')`).Error,
		"the index let a case variant in")
}
