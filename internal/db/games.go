package db

import (
	"gorm.io/gorm"
)

// Game is one row per catalog game, keyed by Slug.
type Game struct {
	gorm.Model
	// Slug is the identity a rating hangs off - catalog.Entry.Slug, the same value the
	// TUI derives routes from. Keying on the display name instead meant renaming a game
	// orphaned every ranking row attached to it.
	Slug string
	// Name is the display string, refreshed on every write, so a rename shows up on the
	// leaderboard without moving anything.
	Name string
}

// GameRef is how a caller names the game a finished match was played as. Both halves
// come from the same catalog entry: repository looks the row up by Slug and stores
// Name for display.
type GameRef struct {
	Slug string
	Name string
}
