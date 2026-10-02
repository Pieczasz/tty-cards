package repository_test

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/repository"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// No container needed: the host cannot resolve, so this is a pure error path and
// belongs outside the integration build tag.
func TestConnect_Failure(t *testing.T) {
	t.Parallel()

	database, err := repository.Connect(
		"postgres://user:pass@invalid-host:5432/db?sslmode=disable", repository.Pool{MaxOpenConns: 5})
	require.Error(t, err)
	assert.Nil(t, database)
}
