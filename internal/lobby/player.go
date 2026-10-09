package lobby

import (
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
)

// NewPlayer seats a database user. It is the only place a db.User becomes a
// game.Player, which is what lets internal/game stay free of internal/db: the
// ratings are flattened here, once, from the Rankings the session preloaded, keyed
// by slug - a game's name can change without moving anyone's rating.
func NewPlayer(u *db.User) *game.Player {
	if u == nil {
		return nil
	}
	ratings := make(map[string]uint32, len(u.Rankings))
	for _, r := range u.Rankings {
		if r.Game.Slug != "" {
			ratings[r.Game.Slug] = r.Elo
		}
	}
	return &game.Player{
		ID:      u.ID.String(),
		UserID:  u.ID,
		Name:    u.Username,
		Ratings: ratings,
	}
}
