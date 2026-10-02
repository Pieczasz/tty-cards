//go:build integration

package repository_test

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/repository"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnect_Success(t *testing.T) {
	t.Parallel()

	database, err := repository.Connect(testutil.PostgresDSN(t), repository.Pool{MaxOpenConns: 5})
	require.NoError(t, err)
	require.NotNil(t, database)

	sqlDB, err := database.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	require.NoError(t, sqlDB.PingContext(t.Context()))
	assert.Equal(t, 5, sqlDB.Stats().MaxOpenConnections,
		"MaxOpenConns has to reach the pool, not just the settings struct")
}
