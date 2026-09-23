//go:build integration

package db_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/config"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/gorm"
)

func TestConnect_Success(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()
	postgresContainer, err := tcpostgres.Run(ctx,
		testutil.PostgresImage,
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("testuser"),
		tcpostgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(time.Second*60),
		),
	)
	testutil.RequireContainer(t, err)
	t.Cleanup(func() {
		_ = postgresContainer.Terminate(ctx)
	})

	host, err := postgresContainer.Host(ctx)
	require.NoError(t, err)
	mappedPort, err := postgresContainer.MappedPort(ctx, "5432/tcp")
	require.NoError(t, err)

	cfg := &config.Config{
		DBHost:     host,
		DBPort:     int(mappedPort.Num()),
		DBUser:     "testuser",
		DBPassword: "testpass",
		DBName:     "testdb",
		DBSSLMode:  "disable",
		// The pool cap is DBMaxOpenConnections. MaxConnections is the ssh session cap
		// and setting it here asserted nothing about the pool at all.
		DBMaxOpenConnections: 5,
		MaxConnections:       200,
		Env:                  "production",
	}

	database, err := db.Connect(cfg)
	require.NoError(t, err)
	require.NotNil(t, database)

	sqlDB, err := database.DB()
	require.NoError(t, err)
	require.NotNil(t, sqlDB)

	err = sqlDB.Ping()
	require.NoError(t, err)

	assert.Equal(t, 5, sqlDB.Stats().MaxOpenConnections,
		"DBMaxOpenConnections has to reach the pool, not just the config struct")
}

// A NULL scans into the Go zero value, so a nullable column whose struct field is a
// plain string/int/bool cannot tell "unset" from "empty" or "zero" or false. The list
// is derived from the GORM structs rather than hand-written: a hand-written one had
// already fallen behind by two columns, and a new scalar field must fail CI until the
// migration pins it.
func TestSchemaNullabilityMatchesStructs(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)

	models := []any{&db.User{}, &db.PublicKey{}, &db.Game{}, &db.Ranking{}, &db.Match{}, &db.MatchParticipant{}}
	checked := 0
	for _, model := range models {
		table, columns := scalarColumns(t, database, model)
		for _, column := range columns {
			checked++
			var isNullable string
			err := database.Raw(`SELECT is_nullable FROM information_schema.columns
				WHERE table_name = ? AND column_name = ?`, table, column).
				Scan(&isNullable).Error
			require.NoError(t, err)
			require.NotEmpty(t, isNullable, "%s.%s does not exist", table, column)
			assert.Equal(t, "NO", isNullable, "%s.%s must be NOT NULL", table, column)
		}
	}
	assert.Greater(t, checked, 10, "the derivation found almost nothing, so it is asserting almost nothing")
}

// scalarColumns is every column of model whose Go field is a plain string, integer or
// bool. Time fields are excluded on purpose: the schema deliberately leaves the
// timestamp columns nullable, and gorm.DeletedAt models NULL explicitly. Associations
// carry no column of their own and parse with an empty DataType.
func scalarColumns(t *testing.T, database *gorm.DB, model any) (string, []string) {
	t.Helper()
	stmt := &gorm.Statement{DB: database}
	require.NoError(t, stmt.Parse(model))

	columns := make([]string, 0, len(stmt.Schema.Fields))
	for _, field := range stmt.Schema.Fields {
		if field.DataType == "" || field.DBName == "" {
			continue
		}
		switch field.FieldType.Kind() {
		case reflect.String, reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			columns = append(columns, field.DBName)
		default:
		}
	}
	return stmt.Schema.Table, columns
}

// The leaderboard and history queries are index-shaped; losing one is a silent
// regression to a sequential scan.
func TestSchemaHasHotPathIndexes(t *testing.T) {
	t.Parallel()
	database := testutil.SetupTestDB(t)

	for _, name := range []string{
		"idx_rankings_game_elo",
		"idx_match_participants_user_match",
		"idx_matches_game_id",
		"idx_rankings_game_id",
		"idx_games_slug",
		"idx_users_username_lower",
		"idx_matches_ranked_created",
	} {
		var count int64
		require.NoError(t, database.Raw(
			`SELECT count(*) FROM pg_indexes WHERE indexname = ?`, name).Scan(&count).Error)
		assert.Equal(t, int64(1), count, "index %s is missing", name)
	}
}
