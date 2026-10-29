package db

import (
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"uuid"

	"gorm.io/gorm"
)

const (
	AnonymisedPrefix         = "deleted_"
	anonymisedUsernameLength = 40 // deleted_ + 32 hex

	maxUsernameLength = 16
)

// User is a player account.
type User struct {
	ID            uuid.UUID `gorm:"type:uuid;primaryKey;serializer:stduuid"`
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     gorm.DeletedAt
	LastSeenAt    time.Time
	Username      string
	PublicSSHKeys []PublicSSHKey
	Rankings      []Ranking
}

// BeforeCreate assigns a UUIDv7 id when the caller did not.
func (u *User) BeforeCreate(_ *gorm.DB) error {
	if u.ID == uuid.Nil() {
		u.ID = uuid.NewV7()
	}
	return nil
}

// PublicSSHKey is one SSH key an account signs in with, by its SHA256 fingerprint.
type PublicSSHKey struct {
	gorm.Model
	Fingerprint string
	Name        string
	LastUsedAt  time.Time
	UserID      uuid.UUID `gorm:"serializer:stduuid"`
	User        User      `gorm:"foreignKey:UserID"`
}

// TableName keeps the SQL table name; the Go type is not the schema.
func (PublicSSHKey) TableName() string { return "public_keys" }

// Ranking is one account's rating in one game.
type Ranking struct {
	UserID uuid.UUID `gorm:"primaryKey;serializer:stduuid"`
	GameID uint      `gorm:"primaryKey"`

	Elo           uint32
	MatchesPlayed uint64

	User User `gorm:"foreignKey:UserID"`
	Game Game `gorm:"foreignKey:GameID"`

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt
}

// AnonymisedUsername is the name an erased account is left under.
func AnonymisedUsername(userID uuid.UUID) string {
	return AnonymisedPrefix + hex.EncodeToString(userID[:])
}

// ValidateUsername is the chosen-name rule: at most 16 of [A-Za-z0-9_], not the
// erasure prefix and not a reserved name, both in any case - names are unique
// case-insensitively, so "Admin" is the same claim as "admin". It only gates
// registration: an existing account keeps its name.
func ValidateUsername(username string) error {
	if len(username) > maxUsernameLength {
		return fmt.Errorf("username cannot exceed %d characters", maxUsernameLength)
	}
	if !usernamePattern.MatchString(username) {
		return errors.New("username can only contain English letters, numbers, and underscores")
	}
	folded := strings.ToLower(username)
	if strings.HasPrefix(folded, AnonymisedPrefix) || reservedUsernames[folded] {
		return errors.New("username is reserved")
	}
	return nil
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// reservedUsernames read as staff or as the server itself on a public leaderboard.
// Exact names only, lower case: admin_fan impersonates nobody.
var reservedUsernames = map[string]bool{
	"admin": true, "administrator": true, "root": true, "mod": true, "moderator": true,
	"system": true, "support": true, "staff": true, "tty_cards": true, "ttycards": true,
	"server": true, "official": true,
}
