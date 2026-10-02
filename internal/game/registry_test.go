package game

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeModule(name, slug string) Module {
	return Module{Name: name, Slug: slug, Factory: func() Rules { return &MockRules{} }}
}

func TestRegistry_CreateBuildsADeclaredGame(t *testing.T) {
	t.Parallel()
	r := NewRegistry(fakeModule("FakeGame", "fakegame"))

	rules, err := r.Create("FakeGame")
	require.NoError(t, err)
	assert.NotNil(t, rules)
}

// The error names the game, so a log line says which lobby option points nowhere.
func TestRegistry_CreateNamesAnUnknownGame(t *testing.T) {
	t.Parallel()
	_, err := NewRegistry(fakeModule("FakeGame", "fakegame")).Create("NotExists")
	require.ErrorContains(t, err, `"NotExists"`)
}

func TestRegistry_ModuleLooksUpByDisplayName(t *testing.T) {
	t.Parallel()
	r := NewRegistry(fakeModule("Crazy Eights", "crazy_eights"))

	mod, ok := r.Module("Crazy Eights")
	require.True(t, ok)
	assert.Equal(t, "crazy_eights", mod.Slug)

	_, ok = r.Module("crazy_eights")
	assert.False(t, ok, "the slug is not the registry key")
}

func TestRegistry_GameNamesKeepDeclarationOrder(t *testing.T) {
	t.Parallel()
	r := NewRegistry(fakeModule("Poker", "poker"), fakeModule("Hearts", "hearts"), fakeModule("Uno", "uno"))

	names := r.GameNames()
	require.Equal(t, []string{"Poker", "Hearts", "Uno"}, names)

	names[0] = "mutated"
	assert.Equal(t, "Poker", r.GameNames()[0], "callers get a copy")
}

// A half-declared module is a wiring bug, and a registry that accepted one would fail
// later as a missing route or a nil factory panic at the moment somebody starts a
// table. catalog_test.go leans on this being loud. A name declared twice is the
// same mistake: GameNames drives the menu, and one of the two would be unreachable.
func TestNewRegistry_RejectsAMisdeclaredGame(t *testing.T) {
	t.Parallel()

	factory := func() Rules { return &MockRules{} }
	tests := []struct {
		name string
		mods []Module
	}{
		{name: "no display name", mods: []Module{{Slug: "s", Factory: factory}}},
		{name: "no slug", mods: []Module{{Name: "N", Factory: factory}}},
		{name: "no factory", mods: []Module{{Name: "N", Slug: "s"}}},
		{name: "a name declared twice", mods: []Module{fakeModule("Poker", "old"), fakeModule("Poker", "new")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Panics(t, func() { NewRegistry(tt.mods...) })
		})
	}
}
