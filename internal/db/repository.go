package db

import (
	"context"

	"uuid"
)

type UserRepository interface {
	LoadUserByFingerprint(ctx context.Context, fingerprint string) (*User, *PublicKey, error)
	RegisterUserWithKey(ctx context.Context, username, fingerprint string) (*User, *PublicKey, error)
	// BestPlayers returns the top rankings by Elo. An empty gameSlug means every
	// game; a non-empty slug filters to that games.slug (unknown slugs yield empty).
	// Results are cached per gameSlug and may be up to 5 minutes stale, so a caller
	// that has just written a ranking will not see it here.
	BestPlayers(ctx context.Context, limit int, gameSlug string) ([]Ranking, error)
	UserProfile(ctx context.Context, userID uuid.UUID) (*User, error)
	UpdateUserActivity(ctx context.Context, user *User, key *PublicKey) error
	UserMatchHistory(ctx context.Context, userID uuid.UUID, limit int) ([]MatchParticipant, error)
	// DeleteAccount erases a player at their own request (GDPR art. 17): every public
	// key and every ranking row of theirs is hard-deleted, and the users row is
	// anonymised to AnonymisedUsername rather than removed - match_participants
	// references it, and the other players at those tables have history that must keep
	// resolving to a name. It returns ErrUserNotFound for an id that was never a user,
	// and is idempotent for one that was.
	DeleteAccount(ctx context.Context, userID uuid.UUID) error
}

type MatchRepository interface {
	// RecordCasualMatch writes history for an unranked result in one transaction,
	// creating the game row on first sight. orderedUserIDs is the finish order.
	RecordCasualMatch(ctx context.Context, ref GameRef, orderedUserIDs []uuid.UUID) error
	// FinalizeRankedMatch moves Elo and writes history in one transaction. places is
	// parallel to orderedUserIDs and 1-based; equal entries are scored as a draw, which
	// still moves rating from the higher-rated of the pair to the lower. A nil places
	// means a strict finish order.
	FinalizeRankedMatch(ctx context.Context, ref GameRef, orderedUserIDs []uuid.UUID, places []int) error
	// FinalizeInterruptedMatch settles a ranked match that a leave ended early for
	// everyone (game.EndReasonInterrupted). Elo is computed as for FinalizeRankedMatch,
	// with the leavers already ranked last, but only a leaver's loss is written: every
	// other seat keeps its rating and its matches_played, so a friend quitting cannot
	// bank a lead, and a losing player cannot quit for free. leavers are user ids from
	// orderedUserIDs; anything else is ignored.
	FinalizeInterruptedMatch(
		ctx context.Context, ref GameRef, orderedUserIDs []uuid.UUID, places []int, leavers []uuid.UUID,
	) error
}
