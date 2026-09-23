package db

import (
	"strings"
	"testing"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

func TestValidateUsername_Valid(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		gen := rapid.StringMatching(`^[A-Za-z0-9_]{1,16}$`).Filter(func(s string) bool {
			return !strings.HasPrefix(s, AnonymisedPrefix)
		})
		username := gen.Draw(t, "username")

		err := ValidateUsername(username)
		assert.NoError(t, err)
	})
}

func TestValidateUsername_InvalidLength(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		gen := rapid.StringMatching(`^.{17,50}$`)
		username := gen.Draw(t, "username")

		err := ValidateUsername(username)
		assert.ErrorContains(t, err, "username cannot exceed 16 characters")
	})
}

func TestValidateUsername_InvalidCharacters(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		gen := rapid.StringMatching(`^.*[^A-Za-z0-9_].*$`).Filter(func(s string) bool {
			return len(s) > 0 && len(s) <= 16
		})
		username := gen.Draw(t, "username")

		err := ValidateUsername(username)
		assert.ErrorContains(t, err, "username can only contain English letters, numbers, and underscores")
	})
}

// FuzzValidateUsername covers a trust boundary: the username comes straight from the
// SSH client via s.User(). Validation must never be looser than the chosen-name cap
// (16 chars, letters/digits/underscore), or a 40-char squat could occupy the unique
// index that erasure needs.
func FuzzValidateUsername(f *testing.F) {
	f.Add("alice")
	f.Add("")
	f.Add(strings.Repeat("a", 17))
	f.Add("a\x00b")
	f.Add("аdmin") // Cyrillic 'а'
	f.Add("日本語")
	f.Add("has space")
	f.Add("dash-not-allowed")
	f.Add("deleted_1")
	f.Add("deleted_")

	f.Fuzz(func(t *testing.T, name string) {
		if ValidateUsername(name) != nil {
			return // rejected: nothing more to prove
		}
		assert.NotEmpty(t, name, "an accepted username must not be empty")
		assert.LessOrEqual(t, len(name), maxUsernameLength, "an accepted username must fit the chosen-name cap")
		assert.False(t, strings.HasPrefix(name, AnonymisedPrefix),
			"the erasure prefix is not a player-chosen name")
		for _, r := range name {
			isAllowed := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '_'
			assert.True(t, isAllowed, "accepted username contains disallowed rune %q", r)
		}
	})
}

func TestValidateUsername_RejectsAnonymisedPrefix(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"deleted_", "deleted_1", "deleted_42"} {
		assert.ErrorContains(t, ValidateUsername(name), "username is reserved", name)
	}
}

// The anonymised name is written into users.username, so it has to clear the column
// CHECK (charset, and deleted_ plus 32 hex). ValidateUsername must refuse it: that
// prefix is how erasure occupies the unique index, and a player who registers it
// first bricks Art. 17 for that id.
func TestAnonymisedUsername(t *testing.T) {
	t.Parallel()

	ids := []uuid.UUID{uuid.Nil(), uuid.MustParse("00000000-0000-4000-8000-00000000002a"), uuid.New(), uuid.New()}
	for _, id := range ids {
		name := AnonymisedUsername(id)
		assert.Len(t, name, anonymisedUsernameLength, "id %s produced %q", id, name)
		assert.Regexp(t, `^deleted_[0-9a-f]{32}$`, name, "id %s produced %q", id, name)
		require.Error(t, ValidateUsername(name), "id %s produced a registerable %q", id, name)
	}

	id := uuid.MustParse("550e8400-e29b-41d4-a716-446655440000")
	assert.Equal(t, "deleted_550e8400e29b41d4a716446655440000", AnonymisedUsername(id))

	// Distinct ids must stay distinct names, or two erased accounts collide on the
	// unique index and the second player cannot be erased at all.
	seen := make(map[string]uuid.UUID, len(ids))
	for _, id := range ids {
		name := AnonymisedUsername(id)
		_, clash := seen[name]
		assert.False(t, clash, "ids %s and %s both anonymise to %q", seen[name], id, name)
		seen[name] = id
	}
}
