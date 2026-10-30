package lobby

import (
	"maps"

	"github.com/Pieczasz/tty-cards/internal/broadcaster"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"

	"uuid"
)

// NewPlayer seats a database user. It is the only place a db.User becomes a
// game.Player, which is what lets internal/game stay free of internal/db: the
// ratings are flattened here, once, from the Rankings the session preloaded, keyed
// by slug - a game's name can change without moving anyone's rating.
//
// The seat key, Player.ID, is the account id as a string. Manager.refreshRatings
// finds a player's table by that key from the ids a ranked write returns, so a
// Player built any other way is not refreshed.
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

// refreshRatings replaces each seat written names with a copy carrying its new rating
// in slug, and returns the feed to announce it on, or nil when no seat changed.
//
// A copy, never a write through the pointer: snapshots, sessions and browse rows
// hold the seated *game.Player and its Ratings map without l.mu.
func (l *Lobby) refreshRatings(slug string, written map[uuid.UUID]uint32) *broadcaster.Broadcaster[Event] {
	l.mu.Lock()
	defer l.mu.Unlock()
	changed := false
	refresh := func(p *game.Player) *game.Player {
		elo, ok := written[p.UserID]
		if !ok || p.UserID == uuid.Nil() || p.Ratings[slug] == elo {
			return p
		}
		changed = true
		next := *p
		next.Ratings = maps.Clone(p.Ratings)
		if next.Ratings == nil {
			next.Ratings = make(map[string]uint32, 1)
		}
		next.Ratings[slug] = elo
		return &next
	}
	l.leader = refresh(l.leader)
	for i, g := range l.guests {
		l.guests[i] = refresh(g)
	}
	if !changed {
		return nil
	}
	return l.broadcaster
}
