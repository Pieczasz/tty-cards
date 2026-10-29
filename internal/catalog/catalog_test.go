package catalog

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGames_EntriesComplete(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, Games)

	names := make(map[string]bool, len(Games))
	slugs := make(map[string]bool, len(Games))
	for _, e := range Games {
		require.NotEmpty(t, e.Name, "entry with slug %q has no name", e.Slug)
		require.NotEmpty(t, e.Slug, "entry %q has no slug", e.Name)
		require.NotNil(t, e.Factory, "entry %q has no rules factory", e.Name)
		require.NotNil(t, e.View, "entry %q has no TUI view", e.Name)
		require.Positive(t, e.MinPlayers, "entry %q has no seat bounds", e.Name)
		require.GreaterOrEqual(t, e.MaxPlayers, e.MinPlayers, "entry %q seats fewer than it needs", e.Name)

		assert.False(t, names[e.Name], "duplicate name %q", e.Name)
		assert.False(t, slugs[e.Slug], "duplicate slug %q", e.Slug)
		names[e.Name] = true
		slugs[e.Slug] = true

		assert.NotNil(t, e.Factory(), "rules factory for %q returned nil", e.Name)
	}
}

// NewRegistry is the seam between the catalog and the engine: a game missing from it
// is a game nobody can start, and the catalog is the only place it is declared.
func TestNewRegistry_HasEveryCatalogGame(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()

	slugs := make([]string, 0, len(Games))
	for _, e := range Games {
		slugs = append(slugs, e.Slug)
		mod, ok := registry.Module(e.Slug)
		require.Truef(t, ok, "%q is in the catalog but not in the registry", e.Slug)
		assert.Equal(t, e.Name, mod.Name)
		assert.NotNil(t, mod.Factory())
	}
	got := make([]string, 0, len(Games))
	for _, mod := range registry.Modules() {
		got = append(got, mod.Slug)
	}
	assert.Equal(t, slugs, got, "the lobby menu keeps catalog order")
}
