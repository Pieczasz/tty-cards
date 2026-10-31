//go:build integration

package testutil

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// postgresImage is the server every integration test runs against: the Postgres 18
// floor uuidv7() sets, pinned to the multi-arch digest compose deploys so the suite
// tests the database that ships. CI fails when the two differ.
const postgresImage = "postgres:18-alpine@sha256:77f585114c32fbca283dc835b0596f4e52b51b4c6662d7810b2f4084f60a1873"

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

// SetupTestDB is a fresh Postgres with the schema applied up, down and up again.
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

// SetupEmptyTestDB is a fresh Postgres with no schema.
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
		// The log line can precede the host port mapping under load, and then
		// ConnectionString fails with "port not found".
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(time.Second*60),
			wait.ForListeningPort("5432/tcp").WithStartupTimeout(time.Second*60),
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
