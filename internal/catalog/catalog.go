// Package catalog is the single registration point for all playable games. Rules and
// TUI view are declared in the same entry.
package catalog

import (
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/tui/router"

	crazyeightrules "github.com/Pieczasz/tty-cards/internal/game/crazyeight"
	ginrummyrules "github.com/Pieczasz/tty-cards/internal/game/ginrummy"
	heartsrules "github.com/Pieczasz/tty-cards/internal/game/hearts"
	pokerrules "github.com/Pieczasz/tty-cards/internal/game/poker"
	unorules "github.com/Pieczasz/tty-cards/internal/game/uno"
	crazyeightview "github.com/Pieczasz/tty-cards/internal/tui/views/gameview/crazyeight"
	ginrummyview "github.com/Pieczasz/tty-cards/internal/tui/views/gameview/ginrummy"
	heartsview "github.com/Pieczasz/tty-cards/internal/tui/views/gameview/hearts"
	pokerview "github.com/Pieczasz/tty-cards/internal/tui/views/gameview/poker"
	unoview "github.com/Pieczasz/tty-cards/internal/tui/views/gameview/uno"

	tea "charm.land/bubbletea/v2"
)

// Entry pairs a game's Module with the view that renders it, so neither can be
// declared without the other. View is handed the session's own seat, never the engine:
// a view can act only as the player it renders for.
type Entry struct {
	game.Module
	View func(global router.GlobalContext, table *game.BoundEngine, slug string) tea.Model
}

// NewRegistry is the registry of every game in Games, in catalog order.
func NewRegistry() *game.Registry {
	return game.NewRegistry(Modules(Games)...)
}

// Modules is each entry's Module, in the entries' order.
func Modules(entries []Entry) []game.Module {
	mods := make([]game.Module, len(entries))
	for i, entry := range entries {
		mods[i] = entry.Module
	}
	return mods
}

// Games is the single point of game registration: every entry carries both the
// rules factory and the TUI view constructor.
var Games = []Entry{
	{
		Name:       "Crazy Eights",
		Slug:       "crazy_eights",
		MinPlayers: crazyeightrules.MinPlayers,
		MaxPlayers: crazyeightrules.MaxPlayers,
		Factory:    func() game.Rules { return &crazyeightrules.Rules{} },
		View:       crazyeightview.New,
	},
	{
		Name:       "Poker",
		Slug:       "poker",
		MinPlayers: pokerrules.MinPlayers,
		MaxPlayers: pokerrules.MaxPlayers,
		Factory:    func() game.Rules { return &pokerrules.Rules{} },
		View:       pokerview.New,
	},
	{
		Name:       "Uno",
		Slug:       "uno",
		MinPlayers: unorules.MinPlayers,
		MaxPlayers: unorules.MaxPlayers,
		Factory:    func() game.Rules { return &unorules.Rules{} },
		View:       unoview.New,
	},
	{
		Name:       "Hearts",
		Slug:       "hearts",
		MinPlayers: heartsrules.MinPlayers,
		MaxPlayers: heartsrules.MaxPlayers,
		Factory:    func() game.Rules { return &heartsrules.Rules{} },
		View:       heartsview.New,
	},
	{
		Name:       "Gin Rummy",
		Slug:       "gin_rummy",
		MinPlayers: ginrummyrules.MinPlayers,
		MaxPlayers: ginrummyrules.MaxPlayers,
		Factory:    func() game.Rules { return &ginrummyrules.Rules{} },
		View:       ginrummyview.New,
	},
}
