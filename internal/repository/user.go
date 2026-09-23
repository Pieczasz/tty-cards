package repository

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"

	"uuid"

	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	pgUniqueViolationCode = "23505"
	bestPlayersCacheSize  = 200
	bestPlayersCacheTTL   = 5 * time.Minute
	// bestPlayersQueryTimeout bounds a shared leaderboard read. The query runs detached
	// from whichever caller started it, so one closing screen does not fail everyone
	// queued behind it - and something still has to stop it.
	bestPlayersQueryTimeout = 10 * time.Second
)

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolationCode
}

type bestPlayersCacheEntry struct {
	rankings []db.Ranking
	at       time.Time
}

type gormUserRepository struct {
	db                    *gorm.DB
	bestPlayersCache      map[string]bestPlayersCacheEntry
	bestPlayersCacheMutex sync.RWMutex
	// bestPlayersGen is bumped by every erasure, under bestPlayersCacheMutex. A read
	// stores its rows only if the generation it started under is still current: one
	// that fetched before an erasure committed would otherwise re-cache the erased
	// account for the whole TTL.
	bestPlayersGen uint64
	// bestPlayersFlight folds concurrent misses into one query. A cold board is read
	// by every lobby screen and the website at once.
	bestPlayersFlight singleflight.Group
}

func NewUserRepository(database *gorm.DB) db.UserRepository {
	return &gormUserRepository{
		db:               database,
		bestPlayersCache: make(map[string]bestPlayersCacheEntry),
	}
}

func (q *gormUserRepository) LoadUserByFingerprint(
	ctx context.Context, fingerprint string,
) (_ *db.User, _ *db.PublicKey, err error) {
	ctx, span := tracer.Start(ctx, "db.LoadUserByFingerprint")
	defer func() { recordSpanResult(span, err); span.End() }()

	var dbKey db.PublicKey
	err = q.db.WithContext(ctx).Where("fingerprint = ?", fingerprint).
		Preload("User").
		Preload("User.Rankings").
		Preload("User.Rankings.Game").
		First(&dbKey).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("load user by fingerprint: %w", err)
	}

	// The Preload applies deleted_at IS NULL while the public_keys row still matches, so
	// a soft-deleted account arrives as a zero-valued association rather than a miss -
	// handing that back authenticates the key as the zero user.
	if dbKey.User.ID == uuid.Nil() {
		return nil, nil, nil
	}
	return &dbKey.User, &dbKey, nil
}

func (q *gormUserRepository) RegisterUserWithKey(
	ctx context.Context, username, fingerprint string,
) (_ *db.User, _ *db.PublicKey, err error) {
	ctx, span := tracer.Start(ctx, "db.RegisterUserWithKey")
	defer func() { recordSpanResult(span, err); span.End() }()

	if err := db.ValidateUsername(username); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", db.ErrInvalidUsername, err)
	}

	var currentUser db.User
	var dbKey db.PublicKey

	err = q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Case-folded, matching idx_users_username_lower (decision D-4), which is also
		// what catches a concurrent registration this read cannot see. Unscoped: the
		// index covers soft-deleted rows too, so a hidden one still owns its name.
		var existingUser db.User
		err := tx.Unscoped().Where("lower(username) = lower(?)", username).First(&existingUser).Error
		if err == nil {
			return db.ErrUsernameTaken
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("check username: %w", err)
		}

		var existingKey db.PublicKey
		err = tx.Where("fingerprint = ?", fingerprint).First(&existingKey).Error
		if err == nil {
			return db.ErrKeyAlreadyRegistered
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("check fingerprint: %w", err)
		}

		currentUser = db.User{
			Username:   username,
			LastSeenAt: time.Now(),
		}
		if err := tx.Create(&currentUser).Error; err != nil {
			if isUniqueViolation(err) { // lost a concurrent registration race
				return db.ErrUsernameTaken
			}
			return fmt.Errorf("create user: %w", err)
		}

		dbKey = db.PublicKey{
			Fingerprint: fingerprint,
			Name:        "auto-generated key",
			UserID:      currentUser.ID,
			LastUsedAt:  time.Now(),
		}
		if err := tx.Create(&dbKey).Error; err != nil {
			if isUniqueViolation(err) {
				return db.ErrKeyAlreadyRegistered
			}
			return fmt.Errorf("create public key: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("register user transaction: %w", err)
	}

	return &currentUser, &dbKey, nil
}

// Read lock only: the database query must never run while the mutex is held. On a
// miss it returns the generation the caller's query will run under.
func (q *gormUserRepository) cachedBestPlayers(gameSlug string, limit int) ([]db.Ranking, uint64, bool) {
	q.bestPlayersCacheMutex.RLock()
	defer q.bestPlayersCacheMutex.RUnlock()

	entry, ok := q.bestPlayersCache[gameSlug]
	if !ok || time.Since(entry.at) >= bestPlayersCacheTTL {
		return nil, q.bestPlayersGen, false
	}
	return slices.Clone(entry.rankings[:min(limit, len(entry.rankings))]), q.bestPlayersGen, true
}

func (q *gormUserRepository) BestPlayers(
	ctx context.Context, limit int, gameSlug string,
) (_ []db.Ranking, err error) {
	ctx, span := tracer.Start(ctx, "db.BestPlayers", trace.WithAttributes(attribute.Int("limit", limit)))
	defer func() { recordSpanResult(span, err); span.End() }()

	limit = max(limit, 0)

	// An entry holds at most bestPlayersCacheSize rows, so a larger ask cannot be served
	// from it and must not be stored into it: later callers would get a truncated board.
	cacheable := limit <= bestPlayersCacheSize
	out, gen, fresh := q.cachedBestPlayers(gameSlug, limit)
	if cacheable && fresh {
		return out, nil
	}

	fetch := bestPlayersCacheSize
	if !cacheable {
		fetch = limit
	}

	// The generation is in the key, so a caller arriving after an erasure starts its
	// own read instead of joining one that began before it.
	key := fmt.Sprintf("%s/%d/%d", gameSlug, fetch, gen)
	shared, err, _ := q.bestPlayersFlight.Do(key, func() (any, error) {
		readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), bestPlayersQueryTimeout)
		defer cancel()
		return q.fetchBestPlayers(readCtx, fetch, gameSlug, gen, cacheable)
	})
	if err != nil {
		return nil, fmt.Errorf("get best players: %w", err)
	}
	rankings, _ := shared.([]db.Ranking)
	span.SetAttributes(attribute.Int("rows", len(rankings)))
	return slices.Clone(rankings[:min(limit, len(rankings))]), nil
}

// fetchBestPlayers is one leaderboard query, stored into the cache when cacheable and
// gen is still current. The rows it returns are shared by every caller of the flight,
// so they are only ever cloned from, never handed out.
func (q *gormUserRepository) fetchBestPlayers(
	ctx context.Context, fetch int, gameSlug string, gen uint64, cacheable bool,
) ([]db.Ranking, error) {
	// Provisional accounts stay on the board: a player's first ranked win should show
	// up, and hiding it made the board empty on a young server. The anti-farm rules
	// live in the finalize path, and they bound a pumped account rather than rule it
	// out: an established player gains nothing from a fresh alt, but alts that have
	// played their five provisional matches among themselves are established too, and
	// each still pays up to maxSamePairingPerDay wins a day. The ceiling is linear in
	// the graduated alts someone is willing to run (decision #10).
	//
	// user_id breaks the tie: equal Elo is common at the starting rating, and without
	// it Postgres is free to return those rows in a different order every time the
	// cache expires, so the board visibly reshuffles between refreshes.
	query := q.db.WithContext(ctx).Preload("User").Preload("Game").
		Order("rankings.elo desc, rankings.user_id").
		Limit(fetch)
	if gameSlug != "" {
		query = query.Joins("JOIN games ON games.id = rankings.game_id AND games.deleted_at IS NULL").
			Where("games.slug = ?", gameSlug)
	}

	var rankings []db.Ranking
	if err := query.Find(&rankings).Error; err != nil {
		return nil, fmt.Errorf("query rankings: %w", err)
	}

	// Not caching an empty result is what bounds this map: gameSlug is caller-controlled,
	// and an unknown game returns no rows, so it never becomes a key.
	if cacheable && len(rankings) > 0 {
		at := time.Now()
		q.bestPlayersCacheMutex.Lock()
		entry, ok := q.bestPlayersCache[gameSlug]
		if q.bestPlayersGen == gen && (!ok || entry.at.Before(at)) {
			q.bestPlayersCache[gameSlug] = bestPlayersCacheEntry{rankings: rankings, at: at}
		}
		q.bestPlayersCacheMutex.Unlock()
	}
	return rankings, nil
}

func (q *gormUserRepository) UserProfile(ctx context.Context, userID uuid.UUID) (_ *db.User, err error) {
	ctx, span := tracer.Start(ctx, "db.UserProfile",
		trace.WithAttributes(attribute.String("user_id", userID.String())))
	defer func() { recordSpanResult(span, err); span.End() }()

	var user db.User
	err = q.db.WithContext(ctx).Preload("PublicKeys").
		Preload("Rankings").
		Preload("Rankings.Game").
		Where("id = ?", userID.String()).
		First(&user).Error
	if err != nil {
		// gorm.ErrRecordNotFound, wrapped: a second "not found" sentinel in this
		// package only gave callers a second thing to compare against.
		return nil, fmt.Errorf("get user profile: %w", err)
	}
	return &user, nil
}

// UpdateUserActivity stamps both last-seen fields; the caller decides whether a stale
// timestamp matters.
func (q *gormUserRepository) UpdateUserActivity(
	ctx context.Context, user *db.User, key *db.PublicKey,
) (err error) {
	ctx, span := tracer.Start(ctx, "db.UpdateUserActivity")
	defer func() { recordSpanResult(span, err); span.End() }()

	// Omit(clause.Associations) on both: user arrives with Rankings and their Games
	// preloaded and key with its User, and GORM's save hooks upsert every association
	// it can see - so stamping a timestamp on login re-wrote each of that player's
	// ranking rows. "User" alone was not enough, and it was the wrong association.
	if err = q.db.WithContext(ctx).Model(user).Omit(clause.Associations).
		Update("LastSeenAt", time.Now()).Error; err != nil {
		return fmt.Errorf("update last seen: %w", err)
	}
	if err = q.db.WithContext(ctx).Model(key).Omit(clause.Associations).
		Update("LastUsedAt", time.Now()).Error; err != nil {
		return fmt.Errorf("update key last used: %w", err)
	}
	return nil
}

func (q *gormUserRepository) UserMatchHistory(
	ctx context.Context, userID uuid.UUID, limit int,
) (_ []db.MatchParticipant, err error) {
	ctx, span := tracer.Start(ctx, "db.UserMatchHistory",
		trace.WithAttributes(attribute.String("user_id", userID.String()), attribute.Int("limit", limit)))
	defer func() { recordSpanResult(span, err); span.End() }()

	// GORM reads a negative Limit as "no limit", which would stream the whole history.
	limit = max(limit, 0)

	var history []db.MatchParticipant
	err = q.db.WithContext(ctx).Where("user_id = ?", userID.String()).
		Preload("Match").
		Preload("Match.Game").
		Order("match_id desc").
		Limit(limit).
		Find(&history).Error
	if err != nil {
		return nil, fmt.Errorf("get user match history: %w", err)
	}
	span.SetAttributes(attribute.Int("rows", len(history)))
	return history, nil
}

// DeleteAccount is the player-facing erasure. The users row survives on purpose: it
// is the parent of match_participants rows that belong to the *other* players at
// those tables, and their history has to keep resolving to a name.
func (q *gormUserRepository) DeleteAccount(ctx context.Context, userID uuid.UUID) (err error) {
	ctx, span := tracer.Start(ctx, "db.DeleteAccount",
		trace.WithAttributes(attribute.String("user_id", userID.String())))
	defer func() { recordSpanResult(span, err); span.End() }()

	if err = q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return eraseUserLocked(tx, userID)
	}); err != nil {
		// The sentinel is the contract callers compare against, so it is handed back
		// as-is; anything else is a database failure and gets the usual context.
		if errors.Is(err, db.ErrUserNotFound) {
			return db.ErrUserNotFound
		}
		return fmt.Errorf("delete account transaction: %w", err)
	}

	// The board is keyed by game slug and this player may sit on any of them, so the
	// whole map goes: a five-minute TTL is five minutes of an erased name on screen.
	q.bestPlayersCacheMutex.Lock()
	clear(q.bestPlayersCache)
	q.bestPlayersGen++
	q.bestPlayersCacheMutex.Unlock()
	return nil
}

func eraseUserLocked(tx *gorm.DB, userID uuid.UUID) error {
	// The seat lock a ranked finalize holds for this user: without it a finalize that
	// read the name before this commit seeds a ranking for the erased account, and it
	// is back on the leaderboard.
	if err := lockSeat(tx, userID); err != nil {
		return err
	}
	// Unscoped, not a soft delete: the fingerprint column is unique, so a lingering
	// key row would keep the returning player from ever registering again - and a
	// soft-deleted ranking still holds the (user_id, game_id) primary key.
	if err := tx.Unscoped().Where("user_id = ?", userID.String()).Delete(&db.PublicKey{}).Error; err != nil {
		return fmt.Errorf("delete public keys: %w", err)
	}
	if err := tx.Unscoped().Where("user_id = ?", userID.String()).Delete(&db.Ranking{}).Error; err != nil {
		return fmt.Errorf("delete rankings: %w", err)
	}

	// A column update keyed on the id, not Save on a loaded User: the save hooks would
	// walk the associations this transaction has just deleted and write them back.
	//
	// Unscoped: an operator soft-delete hides the row from the default scope, and a
	// scoped update then matched nothing and reported a still-named account as unknown.
	anonymised := tx.Unscoped().Model(&db.User{}).Where("id = ?", userID.String()).Updates(map[string]any{
		"username": db.AnonymisedUsername(userID),
		// NULL rather than a zero timestamp: "never seen" is what an erased row means,
		// and the column is nullable precisely so it can say that.
		"last_seen_at": nil,
	})
	if anonymised.Error != nil {
		return fmt.Errorf("anonymise user: %w", anonymised.Error)
	}
	if anonymised.RowsAffected == 0 {
		return db.ErrUserNotFound
	}
	return nil
}
