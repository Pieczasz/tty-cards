package catalog

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/game"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAll_EntriesComplete(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, All)

	names := make(map[string]bool, len(All))
	slugs := make(map[string]bool, len(All))
	for _, e := range All {
		require.NotEmpty(t, e.Name, "entry with slug %q has no name", e.Slug)
		require.NotEmpty(t, e.Slug, "entry %q has no slug", e.Name)
		require.NotNil(t, e.Factory, "entry %q has no rules factory", e.Name)
		require.NotNil(t, e.View, "entry %q has no TUI view", e.Name)

		assert.False(t, names[e.Name], "duplicate name %q", e.Name)
		assert.False(t, slugs[e.Slug], "duplicate slug %q", e.Slug)
		names[e.Name] = true
		slugs[e.Slug] = true

		assert.NotNil(t, e.Factory(), "rules factory for %q returned nil", e.Name)
		_, scores := e.Factory().(game.StandingScorer)
		assert.True(t, scores, "%q must implement StandingScorer so equal scores share a place", e.Name)
	}
}

// NewRegistry is the seam between the catalog and the engine: a game missing from it
// is a game nobody can start, and the catalog is the only place it is declared.
func TestNewRegistry_HasEveryCatalogGame(t *testing.T) {
	t.Parallel()
	registry := NewRegistry()

	names := make([]string, 0, len(All))
	for _, e := range All {
		names = append(names, e.Name)
		mod, ok := registry.Module(e.Name)
		require.Truef(t, ok, "%q is in the catalog but not in the registry", e.Name)
		assert.Equal(t, e.Slug, mod.Slug)
		rules, err := registry.Create(e.Name)
		require.NoError(t, err)
		assert.NotNil(t, rules)
	}
	assert.Equal(t, names, registry.GameNames(), "the lobby menu keeps catalog order")
}

// Module.Name is persisted as games.name and is the registry key the lobby looks a
// game up by, so a rename is a data migration, not a cosmetic edit: existing rows,
// existing rankings and every match already recorded point at the old string. Freeze
// them here so changing one has to be deliberate.
func TestAll_NamesArePersistedAndFrozen(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		"crazy_eights": "Crazy Eights",
		"poker":        "Poker",
		"uno":          "Uno",
		"hearts":       "Hearts",
		"gin_rummy":    "Gin Rummy",
	}

	got := make(map[string]string, len(All))
	for _, e := range All {
		got[e.Slug] = e.Name
	}

	assert.Equal(t, want, got,
		"update this map when adding a game; renaming or re-slugging one orphans its rows in games, rankings and match_participants")
}

// Migration 000005 backfills games.slug from the display names that existed when it
// shipped. If a catalog slug ever disagrees with that table, a running server writes
// ratings under one slug while the migration filed the old rows under another, and the
// leaderboard for that game silently splits in two.
func TestAll_SlugsMatchTheMigrationBackfill(t *testing.T) {
	t.Parallel()
	matches, err := filepath.Glob("../db/migrations/000005_*.up.sql")
	require.NoError(t, err)
	require.Len(t, matches, 1, "exactly one slug migration")
	sql, err := os.ReadFile(matches[0])
	require.NoError(t, err)

	backfill := regexp.MustCompile(`WHEN '([^']+)'\s+THEN '([^']+)'`).FindAllStringSubmatch(string(sql), -1)
	want := make(map[string]string, len(backfill))
	for _, m := range backfill {
		want[m[1]] = m[2]
	}

	gotByName := make(map[string]string, len(All))
	for _, e := range All {
		gotByName[e.Name] = e.Slug
	}
	for name, slug := range want {
		if got, ok := gotByName[name]; ok {
			assert.Equal(t, slug, got, "%q must still backfill to its catalog slug", name)
		}
	}
}
