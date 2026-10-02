package db

import (
	"io/fs"
	"regexp"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/schema"
)

// GORM derives a table from the Go type's name, so renaming a model (PublicKey to
// PublicSSHKey) silently points it at a table no migration creates. This catches that
// without Docker; TestSchemaNullabilityMatchesStructs checks the columns.
func TestModelsMapOntoMigratedTables(t *testing.T) {
	t.Parallel()
	created := map[string]bool{}
	steps, err := fs.Glob(Migrations, "migrations/*.up.sql")
	require.NoError(t, err)
	createTable := regexp.MustCompile(`(?m)^CREATE TABLE (\w+)`)
	for _, step := range steps {
		sql, err := Migrations.ReadFile(step)
		require.NoError(t, err)
		for _, m := range createTable.FindAllStringSubmatch(string(sql), -1) {
			created[m[1]] = true
		}
	}
	require.NotEmpty(t, created)

	for _, model := range []any{&User{}, &PublicSSHKey{}, &Game{}, &Ranking{}, &Match{}, &MatchParticipant{}} {
		s, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
		require.NoError(t, err)
		assert.True(t, created[s.Table], "%s maps onto %q, which no migration creates", s.Name, s.Table)
	}
}
