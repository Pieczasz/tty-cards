package db

import (
	"gorm.io/gorm"
)

// Game is one row per catalog game, keyed by Slug.
type Game struct {
	gorm.Model
	Slug string
	Name string
}

// GameRef names the game a match is recorded under. Finalize looks the games row up
// by Slug and creates it on first use, so a renamed slug starts a fresh ladder.
type GameRef struct {
	Slug string
	Name string
}
