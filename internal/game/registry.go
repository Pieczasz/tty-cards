package game

import (
	"fmt"
	"slices"
)

// Module declares one playable game: its display name, the slug routes and persisted
// ratings hang off, and the factory that builds a fresh Rules per table.
type Module struct {
	Name    string
	Slug    string // e.g. "crazy_eights"
	Factory func() Rules
}

// Registry is the set of games a server offers, keyed by display name. It is fixed at
// construction - nothing registers a game after startup - so it needs no lock.
type Registry struct {
	modules map[string]Module
	order   []string // declaration order, for GameNames
}

// NewRegistry indexes mods by display name in the order given. A half-declared module
// or a name declared twice is a wiring bug that would otherwise surface as a missing
// route or a nil factory the moment somebody starts a table, so it panics, naming the
// game.
func NewRegistry(mods ...Module) *Registry {
	r := &Registry{
		modules: make(map[string]Module, len(mods)),
		order:   make([]string, 0, len(mods)),
	}
	for _, m := range mods {
		if m.Name == "" || m.Slug == "" || m.Factory == nil {
			panic(fmt.Sprintf("game.NewRegistry: %q (slug %q): Name, Slug, and Factory are required", m.Name, m.Slug))
		}
		if _, dup := r.modules[m.Name]; dup {
			panic(fmt.Sprintf("game.NewRegistry: %q declared twice", m.Name))
		}
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
