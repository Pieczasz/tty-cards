package db

import (
	"uuid"

	"gorm.io/gorm"
)

// Match is one finished game. Ranked is whether it moved Elo.
type Match struct {
	gorm.Model
	GameID       uint
	Ranked       bool
	Game         Game               `gorm:"foreignKey:GameID"`
	Participants []MatchParticipant `gorm:"foreignKey:MatchID"`
}

// MatchParticipant is one seat's result in a Match.
type MatchParticipant struct {
	MatchID   uint      `gorm:"primaryKey;autoIncrement:false"`
	UserID    uuid.UUID `gorm:"primaryKey;autoIncrement:false;serializer:stduuid"`
	Placement int       // 1 for first place/winner, 2 for second, etc.
	EloDelta  int       // How much elo they gained/lost

	User  User  `gorm:"foreignKey:UserID"`
	Match Match `gorm:"foreignKey:MatchID"`
}
