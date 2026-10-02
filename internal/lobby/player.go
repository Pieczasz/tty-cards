package lobby

import (
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
)

// NewPlayer seats a database user. It is the only place a db.User becomes a
// game.Player, which is what lets internal/game stay free of internal/db: the
// ratings are flattened here, once, from the Rankings the session preloaded.
func NewPlayer(u *db.User) *game.Player {
	if u == nil {
		return nil
	}
	ratings := make(map[string]uint32, len(u.Rankings))
	for _, r := range u.Rankings {
		if r.Game.Name != "" {
			ratings[r.Game.Name] = r.Elo
		}
	}
	return &game.Player{
		ID:      u.ID.String(),
		UserID:  u.ID,
		Name:    u.Username,
		Ratings: ratings,
	}
}
