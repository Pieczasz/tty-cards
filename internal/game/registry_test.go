package game

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeModule(name, slug string) Module {
	return Module{Name: name, Slug: slug, MinPlayers: 2, MaxPlayers: 4, Factory: func() Rules { return &MockRules{} }}
}

func TestRegistry_ModuleLooksUpBySlug(t *testing.T) {
	t.Parallel()
	r := NewRegistry(fakeModule("Crazy Eights", "crazy_eights"))

	mod, ok := r.Module("crazy_eights")
	require.True(t, ok)
	assert.Equal(t, "Crazy Eights", mod.Name)

	_, ok = r.Module("Crazy Eights")
	assert.False(t, ok, "the display name is not the registry key")
}

func TestRegistry_ModulesKeepDeclarationOrder(t *testing.T) {
	t.Parallel()
	r := NewRegistry(fakeModule("Poker", "poker"), fakeModule("Hearts", "hearts"), fakeModule("Uno", "uno"))

	mods := r.Modules()
	require.Len(t, mods, 3)
	assert.Equal(t, []string{"poker", "hearts", "uno"}, []string{mods[0].Slug, mods[1].Slug, mods[2].Slug})

	mods[0].Slug = "mutated"
	assert.Equal(t, "poker", r.Modules()[0].Slug, "callers get a copy")
}

// A half-declared module is a wiring bug, and a registry that accepted one would fail
// later as a missing route or a nil factory panic at the moment somebody starts a
// table. catalog_test.go leans on this being loud. A name declared twice is the
// same mistake: the menus list names, and two alike cannot be told apart.
// A slug declared twice files two games' ratings under one identity, and seat bounds
// no table could satisfy make a game nobody can start.
func TestNewRegistry_RejectsAMisdeclaredGame(t *testing.T) {
	t.Parallel()

	factory := func() Rules { return &MockRules{} }
	tests := []struct {
		name string
		mods []Module
	}{
		{name: "no display name", mods: []Module{{Slug: "s", MinPlayers: 2, MaxPlayers: 2, Factory: factory}}},
		{name: "no slug", mods: []Module{{Name: "N", MinPlayers: 2, MaxPlayers: 2, Factory: factory}}},
		{name: "no factory", mods: []Module{{Name: "N", Slug: "s", MinPlayers: 2, MaxPlayers: 2}}},
		{name: "no seat bounds", mods: []Module{{Name: "N", Slug: "s", Factory: factory}}},
		{name: "a max below the min", mods: []Module{{Name: "N", Slug: "s", MinPlayers: 4, MaxPlayers: 2, Factory: factory}}},
		{name: "a name declared twice", mods: []Module{fakeModule("Poker", "old"), fakeModule("Poker", "new")}},
		{name: "a slug declared twice", mods: []Module{fakeModule("Poker", "poker"), fakeModule("Texas", "poker")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Panics(t, func() { NewRegistry(tt.mods...) })
		})
	}
}
