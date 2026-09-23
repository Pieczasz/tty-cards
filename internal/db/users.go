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

// Nothing calls AutoMigrate - internal/db/migrations owns the schema - so
// uniqueIndex/not null/default/check/type tags would be decoration that reads like
// enforcement, and one of them had already drifted from the SQL. They are gone; the
// tags that remain (primaryKey, foreignKey, autoIncrement) are the ones GORM actually
// uses to build queries. TestSchemaNullabilityMatchesStructs derives what the SQL must
// guarantee from these structs, so the drift is caught by CI rather than by a tag.
type User struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;serializer:stduuid"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  gorm.DeletedAt
	LastSeenAt time.Time
	Username   string
	PublicKeys []PublicKey
	Rankings   []Ranking
}

func (u *User) BeforeCreate(_ *gorm.DB) error {
	if u.ID == uuid.Nil() {
		u.ID = uuid.NewV7()
	}
	return nil
}

type PublicKey struct {
	gorm.Model
	Fingerprint string
	Name        string
	LastUsedAt  time.Time
	UserID      uuid.UUID `gorm:"serializer:stduuid"`
	User        User      `gorm:"foreignKey:UserID"`
}

type Ranking struct {
	UserID uuid.UUID `gorm:"primaryKey;serializer:stduuid"`
	GameID uint      `gorm:"primaryKey"`

	Elo uint32

	// MatchesPlayed is the ranked track record this row has earned. Below
	// repository.provisionalMatches the account is provisional and beating it pays
	// nobody - a free SSH keypair is a free 1500-rated opponent otherwise.
	MatchesPlayed uint64

	User User `gorm:"foreignKey:UserID"`
	Game Game `gorm:"foreignKey:GameID"`

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt
}

// maxUsernameLength is the chosen-name cap ValidateUsername enforces. The column is
// wider so AnonymisedUsername (deleted_ + 32 hex) still fits; the CHECK refuses any
// other 17-40 character string.
const maxUsernameLength = 16

const anonymisedUsernameLength = 40 // deleted_ + 32 hex

// AnonymisedUsername is the name an erased account is left under. It has to satisfy
// the column CHECK - charset, and either <=16 chars or deleted_ plus 32 hex - because
// the row stays: other players' match history still points at it.
func AnonymisedUsername(userID uuid.UUID) string {
	return AnonymisedPrefix + hex.EncodeToString(userID[:])
}

// AnonymisedPrefix starts every AnonymisedUsername. ValidateUsername refuses it, so a
// name carrying it is an erased account and never a chosen one.
const AnonymisedPrefix = "deleted_"

// ValidateUsername is the chosen-name rule: at most 16 of [A-Za-z0-9_], and not the
// erasure prefix.
func ValidateUsername(username string) error {
	if len(username) > maxUsernameLength {
		return fmt.Errorf("username cannot exceed %d characters", maxUsernameLength)
	}
	if !usernamePattern.MatchString(username) {
		return errors.New("username can only contain English letters, numbers, and underscores")
	}
	if strings.HasPrefix(username, AnonymisedPrefix) {
		return errors.New("username is reserved")
	}
	return nil
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
