// Package db is the persistence contract: the GORM models, the repository interfaces
// the rest of the server depends on, the sentinel errors callers compare against, and
// the embedded SQL migrations that own the schema. The implementations live in
// internal/repository.
package db

import (
	"context"

	"uuid"
)

// Authenticator is what the SSH layer needs to turn a key into a player: look the key
// up, register it on first sight, and stamp the login.
type Authenticator interface {
	// LoadUserByFingerprint returns the account owning the key, with its rankings and
	// their games loaded. A key nobody owns, or whose account is soft-deleted, yields
	// nil, nil, nil.
	LoadUserByFingerprint(ctx context.Context, fingerprint string) (*User, *PublicSSHKey, error)
	// RegisterUserWithSSHKey creates an account and its first key. A refusal the player
	// can act on wraps ErrInvalidUsername, ErrUsernameTaken or ErrKeyAlreadyRegistered.
	RegisterUserWithSSHKey(ctx context.Context, username, fingerprint string) (*User, *PublicSSHKey, error)
	// UpdateUserActivity stamps the account's last-seen and the key's last-used times.
	UpdateUserActivity(ctx context.Context, user *User, key *PublicSSHKey) error
}

// Profiles is one player's own data: what the profile screen shows, and erasing it.
type Profiles interface {
	// UserProfile returns the account with its keys, rankings and their games, or
	// ErrUserNotFound for an id with no live user (never registered, or soft-deleted).
	UserProfile(ctx context.Context, userID uuid.UUID) (*User, error)
	// UserMatchHistory returns the player's newest limit participations, each with its
	// match and game.
	UserMatchHistory(ctx context.Context, userID uuid.UUID, limit int) ([]MatchParticipant, error)
	// DeleteAccount erases a player at their own request (GDPR art. 17): every public
	// key and every ranking row of theirs is hard-deleted, and the users row is
	// anonymised to AnonymisedUsername rather than removed - match_participants
	// references it, and the other players at those tables have history that must keep
	// resolving to a name. It returns ErrUserNotFound for an id that was never a user,
	// and is idempotent for one that was.
	DeleteAccount(ctx context.Context, userID uuid.UUID) error
}

// Leaderboard is the public ranking read.
type Leaderboard interface {
	// BestPlayers returns the top rankings by Elo, skipping any whose user or game is
	// soft-deleted. An empty gameSlug means every game; a non-empty slug filters to
	// that games.slug (unknown slugs yield empty). Results are cached per gameSlug and
	// may be up to 5 minutes stale, so a caller that has just written a ranking will
	// not see it here.
	BestPlayers(ctx context.Context, gameSlug string, limit int) ([]Ranking, error)
}

// UserRepository is every account operation. A consumer should ask for the smallest
// of Authenticator, Profiles and Leaderboard that covers what it calls.
type UserRepository interface {
	Authenticator
	Profiles
	Leaderboard
}

// MatchRepository persists finished matches. Each method is one transaction, so a
// result is written whole or not at all.
type MatchRepository interface {
	// RecordCasualMatch writes history for an unranked result, creating the game row
	// on first sight. orderedUserIDs is the finish order.
	RecordCasualMatch(ctx context.Context, ref GameRef, orderedUserIDs []uuid.UUID) error
	// FinalizeRankedMatch moves Elo and writes history. places is parallel to
	// orderedUserIDs and 1-based; equal entries are scored as a draw, which still moves
	// rating from the higher-rated of the pair to the lower. A nil places means a
	// strict finish order. It returns the rating it stored for each user whose ranking
	// row it wrote - a seat it skipped (erased, or charged nothing) is absent - so a
	// caller can show the new ratings without reading them back.
	FinalizeRankedMatch(ctx context.Context, ref GameRef, orderedUserIDs []uuid.UUID, places []int) (map[uuid.UUID]uint32, error)
	// FinalizeInterruptedMatch settles a ranked match that a leave ended early for
	// everyone (game.EndReasonInterrupted). Elo is computed as for FinalizeRankedMatch,
	// with the leavers already ranked last, but only a leaver's loss is written: every
	// other seat keeps its rating and its matches_played, so a friend quitting cannot
	// bank a lead, and a losing player cannot quit for free. leavers are user ids from
	// orderedUserIDs; anything else is ignored. With no leaver left to charge (erased,
	// or not at the table) the match is recorded as casual: it moved no rating, so it
	// must not count toward the 24h pairing cap. It returns what FinalizeRankedMatch
	// does: here, only the leavers' new ratings.
	FinalizeInterruptedMatch(
		ctx context.Context, ref GameRef, orderedUserIDs []uuid.UUID, places []int, leavers []uuid.UUID,
	) (map[uuid.UUID]uint32, error)
}
