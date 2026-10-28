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

type GameRef struct {
	Slug string
	Name string
}
