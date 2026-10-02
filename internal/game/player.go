package game

import (
	"uuid"

	"github.com/Pieczasz/tty-cards/internal/deck"
)

// Player is a seat at a table.
type Player struct {
	ID      string
	UserID  uuid.UUID
	Name    string
	Ratings map[string]uint32
	Cards   []deck.Card
}

// Equal identifies players by their account where both have one, so the same user on
// a second connection is the same player rather than a new seat.
func (p *Player) Equal(other *Player) bool {
	if p == nil || other == nil {
		return false
	}
	if p.UserID == uuid.Nil() || other.UserID == uuid.Nil() {
		return p.ID != "" && p.ID == other.ID
	}
	return p.UserID == other.UserID
}

// DisplayName falls back to the player ID so a seat is never nameless on screen.
func (p *Player) DisplayName() string {
	if p == nil {
		return ""
	}
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}
