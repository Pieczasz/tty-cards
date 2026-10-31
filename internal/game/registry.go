package game

import "fmt"

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

// Registry is the set of games a server offers, keyed by slug: the identity a game's
// ratings and history are persisted under. A name is only what a player reads.
type Registry struct {
	modules map[string]Module
	order   []string
}

// NewRegistry indexes mods in declaration order. A module missing a name, slug or
// factory, with impossible seat bounds, or declaring a name or slug twice panics: it
// is a wiring bug the server must not boot with.
func NewRegistry(mods ...Module) *Registry {
	r := &Registry{
		modules: make(map[string]Module, len(mods)),
		order:   make([]string, 0, len(mods)),
	}
	names := make(map[string]bool, len(mods))
	for _, m := range mods {
		if m.Name == "" || m.Slug == "" || m.Factory == nil {
			panic(fmt.Sprintf("game.NewRegistry: %q (slug %q): Name, Slug, and Factory are required", m.Name, m.Slug))
		}
		if m.MinPlayers < 1 || m.MaxPlayers < m.MinPlayers {
			panic(fmt.Sprintf("game.NewRegistry: %q: seat bounds %d..%d", m.Name, m.MinPlayers, m.MaxPlayers))
		}
		if names[m.Name] {
			panic(fmt.Sprintf("game.NewRegistry: %q declared twice", m.Name))
		}
		if _, dup := r.modules[m.Slug]; dup {
			panic(fmt.Sprintf("game.NewRegistry: slug %q declared twice", m.Slug))
		}
		names[m.Name] = true
		r.modules[m.Slug] = m
		r.order = append(r.order, m.Slug)
	}
	return r
}

// Modules lists the games in declaration order. The slice is the caller's.
func (r *Registry) Modules() []Module {
	mods := make([]Module, len(r.order))
	for i, slug := range r.order {
		mods[i] = r.modules[slug]
	}
	return mods
}

// Module looks a game up by slug.
func (r *Registry) Module(slug string) (Module, bool) {
	m, ok := r.modules[slug]
	return m, ok
}
