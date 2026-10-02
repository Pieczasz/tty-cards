//go:build integration

package testutil

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// postgresImage is the server every integration test runs against: the Postgres 18
// floor uuidv7() sets.
const postgresImage = "postgres:18-alpine"

// requireContainer fails the test on a container start error, or skips it when the
// error says Docker itself is missing.
func requireContainer(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	errStr := strings.ToLower(err.Error())

	if !strings.Contains(errStr, "response from daemon") {
		for _, marker := range []string{
			"cannot connect to the docker daemon",
			"is the docker daemon running",
			"docker daemon is not running",
			"rootless docker not found",
			"failed to find a viable docker provider",
		} {
			if strings.Contains(errStr, marker) {
				t.Skipf("skipping test because Docker is not available or not running: %v", err)
			}
		}
	}
	t.Fatalf("start postgres container: %v", err)
}

// SetupTestDB is a fresh Postgres with every migration applied up, down and up again.
func SetupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gormDB := SetupEmptyTestDB(t)

	// Up, down, up. A down file is never exercised in CI otherwise, so a broken one
	// is only found during the rollback that needed it. The container is already
	// paid for; a second pass over a handful of DDL statements is not.
	runMigrations(t, gormDB, "*.up.sql")
	seedRoundTripData(t, gormDB)
	runMigrations(t, gormDB, "*.down.sql")
	runMigrations(t, gormDB, "*.up.sql")

	return gormDB
}

// seedRoundTripData gives the down pass rows to work on. On an empty schema every
// UPDATE in a down file matches nothing, so a statement that breaks on real data -
// 000005's rename of duplicate display names, say - passed CI and failed the one
// rollback that needed it. The seed is the awkward case: a renamed game whose new
// display name another game already has.
func seedRoundTripData(t *testing.T, gormDB *gorm.DB) {
	t.Helper()
	for _, stmt := range []string{
		`INSERT INTO users (username) VALUES ('round_trip')`,
		`INSERT INTO games (slug, name) VALUES ('poker', 'Poker'), ('holdem', 'Poker')`,
		`INSERT INTO rankings (user_id, game_id, elo)
			SELECT u.id, g.id, 1500 FROM users u, games g WHERE u.username = 'round_trip'`,
		`INSERT INTO matches (game_id, ranked) SELECT id, TRUE FROM games`,
		`INSERT INTO match_participants (match_id, user_id, placement, elo_delta)
			SELECT m.id, u.id, 1, 0 FROM matches m, users u WHERE u.username = 'round_trip'`,
	} {
		if err := gormDB.Exec(stmt).Error; err != nil {
			t.Fatalf("seed the migration round trip: %v", err)
		}
	}
}

// SetupEmptyTestDB is a fresh Postgres with no schema, for a test that drives the
// migrations itself - one that has to plant data a migration must refuse.
func SetupEmptyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gormDB, err := gorm.Open(gormpostgres.Open(PostgresDSN(t)), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("connect to database: %v", err)
	}

	t.Cleanup(func() {
		sqlDB, err := gormDB.DB()
		if err != nil {
			t.Errorf("reach the sql.DB for teardown: %v", err)
			return
		}
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close the database pool: %v", err)
		}
	})

	return gormDB
}

// PostgresDSN starts a throwaway Postgres for the test and returns its connection
// string. It skips in -short mode and when Docker is unavailable.
func PostgresDSN(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}

	_, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		t.Skipf("skipping test because Docker provider is not available: %v", err)
	}

	postgresContainer, err := tcpostgres.Run(t.Context(),
		postgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("user"),
		tcpostgres.WithPassword("password"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(time.Second*60),
		),
	)
	requireContainer(t, err)

	t.Cleanup(func() {
		// t.Context() is already cancelled by the time cleanups run.
		if err := postgresContainer.Terminate(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("terminate container: %v", err)
		}
	})

	connStr, err := postgresContainer.ConnectionString(t.Context(), "sslmode=disable")
	if err != nil {
		t.Fatalf("get postgres connection string: %v", err)
	}
	return connStr
}

// runMigrations applies every migration matching pattern, up files in ascending
// version order and down files in descending order.
func runMigrations(t *testing.T, gormDB *gorm.DB, pattern string) {
	t.Helper()

	steps, err := fs.Glob(db.Migrations, "migrations/"+pattern)
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	slices.Sort(steps)
	if strings.HasSuffix(pattern, ".down.sql") {
		slices.Reverse(steps)
	}

	for _, step := range steps {
		sql, err := db.Migrations.ReadFile(step)
		if err != nil {
			t.Fatalf("read migration %s: %v", step, err)
		}
		if err := gormDB.Exec(string(sql)).Error; err != nil {
			t.Fatalf("apply migration %s: %v", step, err)
		}
	}
}
