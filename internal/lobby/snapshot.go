package lobby

import "github.com/Pieczasz/tty-cards/internal/game"

// SeatView is one seated player as a Snapshot shows it.
type SeatView struct {
	// Player is shared with the roster, not copied: nothing writes a seated
	// *game.Player once it is seated (the engine deals into copies of its own).
	Player *game.Player
	// Elo is the player's rating in the table's game, unrated at the starting rating.
	// It is the one they logged in with, not a live read.
	Elo   uint32
	Ready bool
}

// Snapshot is a table as one moment saw it, so a frame drawn from it cannot show a
// promoted leader twice or a ready flag from a roster that has since changed.
type Snapshot struct {
	Code     string
	Slug     string
	GameName string
	// MinPlayers and MaxPlayers are the seats the game deals for; Limit is this
	// table's cap within them, leader included.
	MinPlayers int
	MaxPlayers int
	Limit      int
	Private    bool
	Ranked     bool
	InGame     bool
	Leader     SeatView
	// Guests is every seat but the leader's, in join order.
	Guests []SeatView
}

// Snapshot copies the table under one hold of its lock.
func (l *Lobby) Snapshot() Snapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()

	guests := make([]SeatView, len(l.guests))
	for i, g := range l.guests {
		guests[i] = l.seatViewLocked(g)
	}
	return Snapshot{
		Code:       l.code,
		Slug:       l.game.Slug,
		GameName:   l.game.Name,
		MinPlayers: l.game.MinPlayers,
		MaxPlayers: l.game.MaxPlayers,
		Limit:      l.options.maxPlayers,
		Private:    l.options.isPrivate,
		Ranked:     l.options.isRanked,
		InGame:     l.state == inGame,
		Leader:     l.seatViewLocked(l.leader),
		Guests:     guests,
	}
}

func (l *Lobby) seatViewLocked(p *game.Player) SeatView {
	return SeatView{Player: p, Elo: ratingFor(p.Ratings, l.game.Slug), Ready: l.ready[p.ID]}
}

// Players is how many seats are taken, leader included.
func (s Snapshot) Players() int { return 1 + len(s.Guests) }

// IsLeader is whether p holds seat 0.
func (s Snapshot) IsLeader(p *game.Player) bool { return s.Leader.Player.Equal(p) }

// Seated is whether p holds any seat.
func (s Snapshot) Seated(p *game.Player) bool {
	if s.IsLeader(p) {
		return true
	}
	for _, g := range s.Guests {
		if g.Player.Equal(p) {
			return true
		}
	}
	return false
}
