package testutil

import (
	"fmt"

	"github.com/Pieczasz/terminal-card/internal/game"
)

// Players is n seats as lobby.NewPlayer would seat accounts UID(1) through UID(n),
// named p1 through pn.
func Players(n int) []*game.Player {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf("p%d", i+1)
	}
	return NamedPlayers(names...)
}

// NamedPlayers is Players with the given display names, in seat order.
func NamedPlayers(names ...string) []*game.Player {
	players := make([]*game.Player, len(names))
	for i, name := range names {
		players[i] = &game.Player{ID: SeatID(i + 1), UserID: UID(i + 1), Name: name}
	}
	return players
}
