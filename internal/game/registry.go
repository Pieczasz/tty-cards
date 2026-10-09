package game

import (
	"fmt"
	"slices"
)

// Module declares one playable game: its display name, the slug routes and persisted
// ratings hang off, the seat range it deals for, and the factory that builds a fresh
// Rules per table. The bounds are the game's, not a table's, so reading them builds
// nothing.
type Module struct {
	Name       string
	Slug       string // e.g. "crazy_eights"
	MinPlayers int
	MaxPlayers int
	Factory    func() Rules
}

// Registry is the set of games a server offers, keyed by display name.
type Registry struct {
	modules map[string]Module
	order   []string
}

func NewRegistry(mods ...Module) *Registry {
	r := &Registry{
		modules: make(map[string]Module, len(mods)),
		order:   make([]string, 0, len(mods)),
	}
	slugs := make(map[string]bool, len(mods))
	for _, m := range mods {
		if m.Name == "" || m.Slug == "" || m.Factory == nil {
			panic(fmt.Sprintf("game.NewRegistry: %q (slug %q): Name, Slug, and Factory are required", m.Name, m.Slug))
		}
		if m.MinPlayers < 1 || m.MaxPlayers < m.MinPlayers {
			panic(fmt.Sprintf("game.NewRegistry: %q: seat bounds %d..%d", m.Name, m.MinPlayers, m.MaxPlayers))
		}
		if _, dup := r.modules[m.Name]; dup {
			panic(fmt.Sprintf("game.NewRegistry: %q declared twice", m.Name))
		}
		if slugs[m.Slug] {
			panic(fmt.Sprintf("game.NewRegistry: slug %q declared twice", m.Slug))
		}
		slugs[m.Slug] = true
		r.modules[m.Name] = m
		r.order = append(r.order, m.Name)
	}
	return r
}

// GameNames lists the display names in declaration order.
func (r *Registry) GameNames() []string {
	return slices.Clone(r.order)
}

// Module looks a game up by display name.
func (r *Registry) Module(name string) (Module, bool) {
	m, ok := r.modules[name]
	return m, ok
}

// Create builds a fresh Rules for a registered game.
func (r *Registry) Create(name string) (Rules, error) {
	m, ok := r.Module(name)
	if !ok {
		return nil, fmt.Errorf("game %q not found in registry", name)
	}
	return m.Factory(), nil
}
