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

const PostgresImage = "postgres:18-alpine"

func RequireContainer(t *testing.T, err error) {
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
	t.Fatalf("failed to start postgres container: %v", err)
}

func SetupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	gormDB := SetupEmptyTestDB(t)

	// Up, down, up. A down file is never exercised in CI otherwise, so a broken one
	// is only found during the rollback that needed it. The container is already
	// paid for; a second pass over a handful of DDL statements is not.
	runMigrations(t, gormDB, "*.up.sql")
	runMigrations(t, gormDB, "*.down.sql")
	runMigrations(t, gormDB, "*.up.sql")

	return gormDB
}

// SetupEmptyTestDB is a fresh Postgres with no schema, for a test that drives the
// migrations itself - one that has to plant data a migration must refuse.
func SetupEmptyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping DB integration test in short mode")
	}

	_, err := testcontainers.ProviderDocker.GetProvider()
	if err != nil {
		t.Skipf("skipping test because Docker provider is not available: %v", err)
	}

	ctx := context.Background()

	postgresContainer, err := tcpostgres.Run(ctx,
		PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("user"),
		tcpostgres.WithPassword("password"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(time.Second*60),
		),
	)
	RequireContainer(t, err)

	t.Cleanup(func() {
		if err := postgresContainer.Terminate(ctx); err != nil {
			t.Errorf("failed to terminate container: %v", err)
		}
	})

	connStr, err := postgresContainer.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get postgres connection string: %v", err)
	}

	gormDB, err := gorm.Open(gormpostgres.Open(connStr), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("failed to connect to database: %v", err)
	}

	t.Cleanup(func() {
		sqlDB, err := gormDB.DB()
		if err != nil {
			t.Errorf("failed to reach the sql.DB for teardown: %v", err)
			return
		}
		if err := sqlDB.Close(); err != nil {
			t.Errorf("failed to close the database pool: %v", err)
		}
	})

	return gormDB
}

// runMigrations applies every migration matching pattern, up files in ascending
// version order and down files in descending order.
func runMigrations(t *testing.T, gormDB *gorm.DB, pattern string) {
	t.Helper()

	steps, err := fs.Glob(db.Migrations, "migrations/"+pattern)
	if err != nil {
		t.Fatalf("failed to list migrations: %v", err)
	}
	slices.Sort(steps)
	if strings.HasSuffix(pattern, ".down.sql") {
		slices.Reverse(steps)
	}

	for _, step := range steps {
		sql, err := db.Migrations.ReadFile(step)
		if err != nil {
			t.Fatalf("failed to read migration %s: %v", step, err)
		}
		if err := gormDB.Exec(string(sql)).Error; err != nil {
			t.Fatalf("failed to apply migration %s: %v", step, err)
		}
	}
}
