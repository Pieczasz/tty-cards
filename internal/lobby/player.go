package lobby

import (
	"maps"

	"github.com/Pieczasz/tty-cards/internal/broadcaster"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
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

// withRatings is p carrying ratings (slug to rating): a copy when any of them differs,
// p itself otherwise.
//
// A copy, never a write through the pointer: snapshots, sessions and browse rows
// hold the seated *game.Player and its Ratings map without a lock.
func withRatings(p *game.Player, ratings map[string]uint32) *game.Player {
	next := p
	for slug, rating := range ratings {
		if next.Ratings[slug] == rating {
			continue
		}
		if next == p {
			cp := *p
			cp.Ratings = maps.Clone(p.Ratings)
			if cp.Ratings == nil {
				cp.Ratings = make(map[string]uint32, len(ratings))
			}
			next = &cp
		}
		next.Ratings[slug] = rating
	}
	return next
}

// refreshRatings replaces each seat written (player ID to slug to rating) names with
// a copy carrying those ratings, and returns the feed to announce it on, or nil when
// no seat changed.
func (l *Lobby) refreshRatings(written map[string]map[string]uint32) *broadcaster.Broadcaster[Event] {
	l.mu.Lock()
	defer l.mu.Unlock()
	changed := false
	refresh := func(p *game.Player) *game.Player {
		next := withRatings(p, written[p.ID])
		changed = changed || next != p
		return next
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
