// Package catalog is the single registration point for playable games. Rules and
// TUI view are declared in the same entry.
package catalog

import (
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/tui/router"

	crazyeightrules "github.com/Pieczasz/terminal-card/internal/game/crazyeight"
	ginrummyrules "github.com/Pieczasz/terminal-card/internal/game/ginrummy"
	heartsrules "github.com/Pieczasz/terminal-card/internal/game/hearts"
	pokerrules "github.com/Pieczasz/terminal-card/internal/game/poker"
	unorules "github.com/Pieczasz/terminal-card/internal/game/uno"
	crazyeightview "github.com/Pieczasz/terminal-card/internal/tui/views/game/crazyeight"
	ginrummyview "github.com/Pieczasz/terminal-card/internal/tui/views/game/ginrummy"
	heartsview "github.com/Pieczasz/terminal-card/internal/tui/views/game/hearts"
	pokerview "github.com/Pieczasz/terminal-card/internal/tui/views/game/poker"
	unoview "github.com/Pieczasz/terminal-card/internal/tui/views/game/uno"

	tea "charm.land/bubbletea/v2"
)

// Entry pairs a game's Module with the view that renders it, so neither can be
// declared without the other.
type Entry struct {
	game.Module
	View func(router.GlobalContext, *game.Engine) tea.Model
}

// NewRegistry is the registry of every game in All, in catalog order.
func NewRegistry() *game.Registry {
	mods := make([]game.Module, 0, len(All))
	for _, e := range All {
		mods = append(mods, e.Module)
	}
	return game.NewRegistry(mods...)
}

// All is the single point of game registration: every entry carries both the
// rules factory and the TUI view constructor, and catalog_test fails on a missing
// field or duplicate slug. A new game ships by adding one entry here.
var All = []Entry{
	{
		Module: game.Module{
			Name:    "Crazy Eights",
			Slug:    "crazy_eights",
			Factory: func() game.Rules { return &crazyeightrules.Rules{} },
		},
		View: crazyeightview.New,
	},
	{
		Module: game.Module{
			Name:    "Poker",
			Slug:    "poker",
			Factory: func() game.Rules { return &pokerrules.Rules{} },
		},
		View: pokerview.New,
	},
	{
		Module: game.Module{
			Name:    "Uno",
			Slug:    "uno",
			Factory: func() game.Rules { return &unorules.Rules{} },
		},
		View: unoview.New,
	},
	{
		Module: game.Module{
			Name:    "Hearts",
			Slug:    "hearts",
			Factory: func() game.Rules { return &heartsrules.Rules{} },
		},
		View: heartsview.New,
	},
	{
		Module: game.Module{
			Name:    "Gin Rummy",
			Slug:    "gin_rummy",
			Factory: func() game.Rules { return &ginrummyrules.Rules{} },
		},
		View: ginrummyview.New,
	},
}
