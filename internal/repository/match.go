package repository

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/elo"
	"github.com/Pieczasz/tty-cards/internal/observability"
	"github.com/Pieczasz/tty-cards/internal/rating"

	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var tracer = otel.Tracer("tty-cards/repository")

// recordSpanResult marks a span failed on error: recording the error without the
// status leaves the one failed span reading as successful. Ending it stays at the
// call site so the End is visible on every path.
func recordSpanResult(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}

const (
	pgSerializationFailureCode = "40001"
	pgDeadlockDetectedCode     = "40P01"
	// txAttempts bounds how often an aborted write is run again. The lock
	// order makes an abort rare, so a third in a row means something is wrong.
	txAttempts = 3
)

// isRetryableAbort is a transaction Postgres rolled back to break a conflict. Both
// codes are raised before commit, so nothing was written and running it again is
// safe; a dropped connection is not on the list, because it may have committed.
func isRetryableAbort(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		(pgErr.Code == pgSerializationFailureCode || pgErr.Code == pgDeadlockDetectedCode)
}

// transactRetrying runs fn as one transaction, again when Postgres rolled it back to
// break a conflict. Finalizes, casual results and erasures all run through it: without
// it an abort loses a finished match, or an erasure the player already confirmed. fn
// must keep no state across attempts it does not reset itself.
func transactRetrying(ctx context.Context, conn *gorm.DB, fn func(tx *gorm.DB) error) error {
	for attempt := 1; ; attempt++ {
		err := conn.WithContext(ctx).Transaction(fn)
		if attempt == txAttempts || !isRetryableAbort(err) {
			return err //nolint:wrapcheck // the caller names the write
		}
		slog.WarnContext(ctx, "transaction aborted by postgres; retrying", "error", err, "attempt", attempt)
	}
}

// A duplicate seat would move that player's rating twice, then collide on the
// match_participants primary key and roll the whole match back.
func checkDistinctPlayers(userIDs []uuid.UUID) error {
	seen := make(map[uuid.UUID]struct{}, len(userIDs))
	for _, id := range userIDs {
		if _, dup := seen[id]; dup {
			return fmt.Errorf("duplicate user id %s in match standings", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// MatchRepository is the GORM implementation of db.MatchRepository.
type MatchRepository struct {
	db *gorm.DB
}

var _ db.MatchRepository = (*MatchRepository)(nil)

// NewMatchRepository is the GORM db.MatchRepository over conn.
func NewMatchRepository(conn *gorm.DB) *MatchRepository {
	return &MatchRepository{db: conn}
}

// getOrCreateGame takes the handle rather than q.db so a caller inside a transaction
// reuses its connection: going back to the pool holds one while waiting for a second, so
// DBMaxOpenConns concurrent finalizes deadlock until they time out - and the game
// row would outlive a rollback.
//
// DoUpdates rather than DoNothing, for the same reason seedRankingRows revives its rows:
// a soft-deleted game still occupies the unique slug, so DO NOTHING would leave ID zero
// and the default-scoped reload could not see the row - every finalize for that game
// would fail forever. Writing the name on the way through is also how a renamed game
// reaches the leaderboard without its ratings moving.
//
// The plain read comes first because the upsert is a row write: DO UPDATE locks the
// game row until commit, so every finalize for one game - casual ones included -
// queued behind the one before it. Only a miss, a rename or a soft-deleted row needs
// the write.
func getOrCreateGame(tx *gorm.DB, ref db.GameRef) (*db.Game, error) {
	var existing db.Game
	err := tx.Unscoped().Where("slug = ?", ref.Slug).Limit(1).Find(&existing).Error
	if err != nil {
		return nil, fmt.Errorf("find game: %w", err)
	}
	if existing.ID != 0 && existing.Name == ref.Name && !existing.DeletedAt.Valid {
		return &existing, nil
	}

	game := db.Game{Slug: ref.Slug, Name: ref.Name}
	if err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "slug"}},
		DoUpdates: clause.Assignments(map[string]any{
			"deleted_at": nil,
			"name":       ref.Name,
			"updated_at": time.Now(),
		}),
	}).Create(&game).Error; err != nil {
		return nil, fmt.Errorf("create game: %w", err)
	}
	// No id check: DO UPDATE always returns the row, and a zero would fail the
	// matches.game_id foreign key on the very next statement anyway.
	return &game, nil
}

// RecordCasualMatch implements db.MatchRepository.
func (q *MatchRepository) RecordCasualMatch(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID,
) (err error) {
	if len(orderedUserIDs) == 0 {
		return nil
	}

	ctx, span := tracer.Start(ctx, "db.RecordCasualMatch",
		trace.WithAttributes(attribute.String("game", ref.Slug), attribute.Int("players", len(orderedUserIDs))))
	defer func() { recordSpanResult(span, err); span.End() }()

	if err = checkDistinctPlayers(orderedUserIDs); err != nil {
		return fmt.Errorf("record casual match: %w", err)
	}

	if err = transactRetrying(ctx, q.db, func(tx *gorm.DB) error {
		game, err := getOrCreateGame(tx, ref)
		if err != nil {
			return err
		}
		// A casual result carries no placements, so history records the finish order.
		return recordMatch(tx, game.ID, orderedUserIDs, nil, nil, false)
	}); err != nil {
		return fmt.Errorf("record casual match: %w", err)
	}
	return nil
}

// FinalizeRankedMatch implements db.MatchRepository.
func (q *MatchRepository) FinalizeRankedMatch(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID, places []int,
) (map[uuid.UUID]uint32, error) {
	return q.finalizeRanked(ctx, ref, orderedUserIDs, places, rating.EverySeat())
}

// FinalizeInterruptedMatch implements db.MatchRepository.
func (q *MatchRepository) FinalizeInterruptedMatch(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID, places []int, leavers []uuid.UUID,
) (map[uuid.UUID]uint32, error) {
	return q.finalizeRanked(ctx, ref, orderedUserIDs, places, rating.LeaversOnly(leavers))
}

// finalizeOp names the finalize for its span and its errors, so an interrupted match
// that fails does not report itself as a ranked one.
func finalizeOp(scope rating.Scope) (span, msg string) {
	if scope.LossOnly() {
		return "db.FinalizeInterruptedMatch", "finalize interrupted match"
	}
	return "db.FinalizeRankedMatch", "finalize ranked match"
}

// finalizeRanked returns the rating it wrote for each seat whose row it wrote.
func (q *MatchRepository) finalizeRanked(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID, places []int, scope rating.Scope,
) (_ map[uuid.UUID]uint32, err error) {
	if len(orderedUserIDs) == 0 {
		return map[uuid.UUID]uint32{}, nil
	}

	spanName, op := finalizeOp(scope)
	ctx, span := tracer.Start(ctx, spanName,
		trace.WithAttributes(attribute.String("game", ref.Slug), attribute.Int("players", len(orderedUserIDs))))
	defer func() { recordSpanResult(span, err); span.End() }()

	if err = checkDistinctPlayers(orderedUserIDs); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}

	var result rating.Result
	if err = transactRetrying(ctx, q.db, func(tx *gorm.DB) error {
		// Per attempt: a retried transaction must not report what a rolled-back one decided.
		result = rating.Result{}
		game, err := getOrCreateGame(tx, ref)
		if err != nil {
			return err
		}
		if result, err = updateRankings(ctx, tx, game.ID, orderedUserIDs, places, scope); err != nil {
			return err
		}
		return recordMatch(tx, game.ID, orderedUserIDs, places, deltasOf(result.Writes), result.Rated())
	}); err != nil {
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	// Counted once committed: a rolled-back finalize damped nothing.
	if result.Damped {
		observability.MatchDamped(ctx, ref.Slug)
	}
	written := make(map[uuid.UUID]uint32, len(result.Writes))
	for _, w := range result.Writes {
		written[w.UserID] = w.Elo
	}
	return written, nil
}

// deltasOf is the rating change each write records in history.
func deltasOf(writes []rating.Write) map[uuid.UUID]int {
	deltas := make(map[uuid.UUID]int, len(writes))
	for _, w := range writes {
		deltas[w.UserID] = w.Delta
	}
	return deltas
}

// seedRankingRows gives fetchRankings something to lock. A first-time (user_id, game_id)
// has no row, so FOR UPDATE locks nothing and applyRatings has nothing to update. Row
// order does not matter here: every writer of a user's rankings already holds that
// user's seat lock (lockPairing or lockSeat, invariant 7), so two transactions that
// share a user never interleave on these rows.
//
// Soft-deleted rankings are revived first: DO NOTHING leaves them invisible to the
// default scope, and a missing row used to abort the whole table's finalize.
func seedRankingRows(tx *gorm.DB, gameID uint, userIDs []uuid.UUID) error {
	if err := tx.Unscoped().Model(&db.Ranking{}).
		Where("user_id IN ? AND game_id = ? AND deleted_at IS NOT NULL", uuidStrings(userIDs), gameID).
		Update("deleted_at", nil).Error; err != nil {
		return fmt.Errorf("revive rankings: %w", err)
	}
	seeds := make([]db.Ranking, 0, len(userIDs))
	for _, userID := range userIDs {
		seeds = append(seeds, db.Ranking{UserID: userID, GameID: gameID, Elo: elo.ToUint32(elo.DefaultRating)})
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}, {Name: "game_id"}},
		DoNothing: true,
	}).Create(&seeds).Error; err != nil {
		return fmt.Errorf("seed rankings: %w", err)
	}
	return nil
}

// updateRankings rates the table: rating.Rate decides, this reads what it needs under
// the locks and stores what it decided, which it returns.
func updateRankings(
	ctx context.Context, tx *gorm.DB, gameID uint, orderedUserIDs []uuid.UUID, places []int, scope rating.Scope,
) (rating.Result, error) {
	// Serialize same-pairing finalizes across every game: ranking row locks are
	// per (user, game), so A-B farming Poker and Hearts concurrently would both
	// see an undamped count without this.
	if err := lockPairing(tx, orderedUserIDs); err != nil {
		return rating.Result{}, err
	}

	// Read under the seat locks eraseUser also takes, so an erasure either
	// committed before this or waits for this transaction to finish.
	seats, err := unerasedSeats(tx, orderedUserIDs, places)
	if err != nil {
		return rating.Result{}, err
	}
	seatIDs := rating.IDs(seats)
	written := scope.Written(seatIDs)
	if len(written) == 0 {
		return rating.Result{}, nil
	}

	// Only rows about to be written are seeded: a seated player with no ranking yet
	// would otherwise land on the leaderboard from a match that did not count for them.
	// rating.Rate reads a missing row as the starting rating anyway.
	if err := seedRankingRows(tx, gameID, written); err != nil {
		return rating.Result{}, err
	}

	rankingMap, err := fetchRankings(tx, gameID, seatIDs)
	if err != nil {
		return rating.Result{}, fmt.Errorf("fetch rankings: %w", err)
	}
	pairings, err := repeatedPairCountLast24h(tx, seatIDs)
	if err != nil {
		return rating.Result{}, err
	}

	result, err := rating.Rate(rating.Table{
		Seats: seats, Current: standings(rankingMap), RecentPairings: pairings, Scope: scope,
	})
	if err != nil {
		return rating.Result{}, fmt.Errorf("rate game %d: %w", gameID, err)
	}
	if result.Damped {
		// The anti-farm cap doing its job, not a fault: Info, not Warn.
		slog.InfoContext(ctx, "ranked match damped: these players have already met inside 24h",
			"user_ids", seatIDs, "game_id", gameID, "recent_pairings", pairings)
	}
	if err := applyRatings(tx, rankingMap, result.Writes); err != nil {
		return rating.Result{}, err
	}
	return result, nil
}

// standings is the ranking rows as rating.Rate reads them.
func standings(rankingMap map[uuid.UUID]*db.Ranking) map[uuid.UUID]rating.Standing {
	out := make(map[uuid.UUID]rating.Standing, len(rankingMap))
	for id, r := range rankingMap {
		out[id] = rating.Standing{Elo: r.Elo, MatchesPlayed: r.MatchesPlayed}
	}
	return out
}

// applyRatings stores each write. rating.Rate only writes seats it found a row for, so
// every write has one here.
func applyRatings(tx *gorm.DB, rankingMap map[uuid.UUID]*db.Ranking, writes []rating.Write) error {
	for _, w := range writes {
		if err := writeRanking(tx, rankingMap[w.UserID], w.Elo); err != nil {
			return err
		}
	}
	return nil
}

// writeRanking stores one seat's rating.
func writeRanking(tx *gorm.DB, r *db.Ranking, stored uint32) error {
	// The increment rides the row this transaction already holds FOR UPDATE, and
	// happens whether or not the rating moved: a damped or unpaid match is still a
	// match played, and it is what lets a provisional account graduate. On an
	// interrupted match only the leaver's row gets here, so only they are counted.
	res := tx.Model(r).Updates(map[string]any{
		"matches_played": gorm.Expr("matches_played + 1"),
		"elo":            stored,
	})
	if res.Error != nil {
		return fmt.Errorf("update ranking: %w", res.Error)
	}
	// This transaction holds the row FOR UPDATE, so zero rows means it is gone
	// underneath us. Carrying on would write the computed elo_delta into history for
	// a rating that never moved.
	if res.RowsAffected == 0 {
		return fmt.Errorf("ranking for user %s in game %d was not updated", r.UserID, r.GameID)
	}
	return nil
}

// unerasedSeats is the seats rating.Seats keeps once the accounts erased while the
// match ran are dropped.
//
// The anonymised prefix is safe to match on: ValidateUsername refuses it, so no chosen
// name starts with it. Unscoped because an operator soft-delete does not make the
// account any less erased.
func unerasedSeats(tx *gorm.DB, userIDs []uuid.UUID, places []int) ([]rating.Seat, error) {
	var erased []string
	if err := tx.Unscoped().Model(&db.User{}).
		Where("id IN ? AND username LIKE ?", uuidStrings(userIDs), likePrefix(db.AnonymisedPrefix)).
		Pluck("id", &erased).Error; err != nil {
		return nil, fmt.Errorf("query erased seats: %w", err)
	}
	return rating.Seats(userIDs, places, func(id uuid.UUID) bool {
		return slices.Contains(erased, id.String())
	}), nil
}

// repeatedPairCountLast24h is the most ranked matches any single pair of players at
// this table has already shared inside 24h, across every game.
//
// Pairs, not the exact seat list: {A,B}, {A,B,C} and {A,B,D} are three different
// participant sets, so a cap on set repeats handed each of them its own budget and two
// accounts could farm each other indefinitely by rotating a third alt through the
// table. A and B co-occurring is what the cap is actually about, and switching game
// does not make it legitimate either, so the window spans every game.
//
// The scan drives from the server's ranked matches inside the window
// (idx_matches_ranked_created) and joins out to these seats, so it
// is bounded by the last day's ranked traffic, not by how long these players have been
// playing. The pair counting happens in Go, which is cheap at these volumes. Callers
// must hold lockPairing, or two concurrent finalizes sharing a seat both read an
// undamped count.
func repeatedPairCountLast24h(tx *gorm.DB, userIDs []uuid.UUID) (int, error) {
	var matchIDs []uint
	if err := recentRankedMatches(tx, userIDs, time.Now().Add(-24*time.Hour)).
		Pluck("matches.id", &matchIDs).Error; err != nil {
		return 0, fmt.Errorf("query recent pairings: %w", err)
	}
	if len(matchIDs) == 0 {
		return 0, nil
	}

	// Only the seats sitting at this table matter: a past match's other players are
	// not part of any pair being capped now.
	var rows []db.MatchParticipant
	if err := tx.Select("match_id", "user_id").
		Where("match_id IN ? AND user_id IN ?", matchIDs, uuidStrings(userIDs)).
		Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("query pairing participants: %w", err)
	}

	seats := make(map[uint][]uuid.UUID, len(matchIDs))
	for _, row := range rows {
		seats[row.MatchID] = append(seats[row.MatchID], row.UserID)
	}
	return rating.WorstPairCount(slices.Collect(maps.Values(seats))), nil
}

// recentRankedMatches is the ranked, non-deleted matches since the cutoff that any of
// userIDs sat in. The model's default scope supplies deleted_at IS NULL, which with
// ranked is exactly the partial index's predicate.
func recentRankedMatches(tx *gorm.DB, userIDs []uuid.UUID, since time.Time) *gorm.DB {
	return tx.Model(&db.Match{}).
		Joins("JOIN match_participants ON match_participants.match_id = matches.id").
		Where("matches.ranked AND matches.created_at > ? AND match_participants.user_id IN ?",
			since, uuidStrings(userIDs)).
		Distinct()
}

// lockPairing serializes finalizes that share any seat, taking the locks in key order
// so two overlapping tables cannot grab them in opposite orders and deadlock.
// Ranking row locks are per (user, game), so the same accounts finalizing Poker and
// Hearts at the same moment would both read an undamped pair count without this. One
// lock per seat rather than one per exact set, because the cap is now per pair and two
// different sets can share one.
func lockPairing(tx *gorm.DB, userIDs []uuid.UUID) error {
	for _, key := range seatLockKeys(userIDs) {
		if err := lockKey(tx, key); err != nil {
			return err
		}
	}
	return nil
}

// seatLockKeys is the lock order: by folded key, deduplicated. Sorting by user id was
// not enough once two seats can fold onto one key - that key then sat at a different
// point in each transaction's order, and two of them could deadlock on it.
func seatLockKeys(userIDs []uuid.UUID) []int64 {
	keys := make([]int64, 0, len(userIDs))
	for _, id := range userIDs {
		keys = append(keys, seatAdvisoryKey(id))
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// lockSeat is the per-user advisory lock both a ranked finalize and an erasure hold.
func lockSeat(tx *gorm.DB, id uuid.UUID) error {
	return lockKey(tx, seatAdvisoryKey(id))
}

// lockKey takes the two-int4 form of the advisory lock, with the 64-bit key split in
// half. Postgres keeps the two forms apart (objsubid 2, not 1), and golang-migrate takes
// the single-bigint form, so a seat key can never collide with a migration's lock.
func lockKey(tx *gorm.DB, key int64) error {
	//nolint:gosec // G115: splitting an advisory key into its two halves, not an id
	hi, lo := int32(key>>32), int32(key)
	if err := tx.Exec("SELECT pg_advisory_xact_lock(?::int4, ?::int4)", hi, lo).Error; err != nil {
		return fmt.Errorf("lock seat key %d: %w", key, err)
	}
	return nil
}

// likePrefix is a LIKE pattern matching strings that start with prefix, its wildcards
// escaped: the anonymised prefix's underscore would otherwise match any character.
func likePrefix(prefix string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix) + "%"
}

// uuidStrings is ids as query arguments. Every id is bound as its text form, the
// same as the stduuid serializer writes: a raw uuid.UUID is a [16]byte with no
// Valuer, which only works where Postgres infers a uuid parameter from the column
// and breaks in an untyped spot such as SELECT ? or a COALESCE.
func uuidStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

func seatAdvisoryKey(userID uuid.UUID) int64 {
	// One lock per seat. Folding 128 bits into a bigint can collide; that only
	// serializes two finalizes, it does not mix ratings.
	hi := binary.BigEndian.Uint64(userID[0:8])
	lo := binary.BigEndian.Uint64(userID[8:16])
	return int64(hi ^ lo) //nolint:gosec // G115: advisory key, not an id
}

// recordMatch writes the match row and its participants.
func recordMatch(
	tx *gorm.DB, gameID uint, orderedUserIDs []uuid.UUID, places []int, eloDeltas map[uuid.UUID]int, ranked bool,
) error {
	match := db.Match{GameID: gameID, Ranked: ranked}
	if err := tx.Create(&match).Error; err != nil {
		return fmt.Errorf("create match: %w", err)
	}

	participants := make([]db.MatchParticipant, len(orderedUserIDs))
	for i, userID := range orderedUserIDs {
		participants[i] = db.MatchParticipant{
			MatchID:   match.ID,
			UserID:    userID,
			Placement: rating.PlaceAt(places, i),
			EloDelta:  eloDeltas[userID],
		}
	}
	// One multi-row insert rather than a round trip per seat.
	if err := tx.Create(&participants).Error; err != nil {
		return fmt.Errorf("create match participants: %w", err)
	}
	return nil
}

func fetchRankings(tx *gorm.DB, gameID uint, userIDs []uuid.UUID) (map[uuid.UUID]*db.Ranking, error) {
	var rankings []db.Ranking
	// The seat locks serialize every transaction that writes these users' rankings
	// (lockPairing or lockSeat, invariant 7), so they, not this, are what stop lost
	// Elo updates and deadlocks. FOR UPDATE with a fixed ORDER BY is a cheap backstop
	// should a writer ever skip them, not the protocol.
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id IN ? AND game_id = ?", uuidStrings(userIDs), gameID).
		Order("user_id").Find(&rankings).Error; err != nil {
		return nil, fmt.Errorf("query rankings: %w", err)
	}

	rankingMap := make(map[uuid.UUID]*db.Ranking)
	for i := range rankings {
		rankingMap[rankings[i].UserID] = &rankings[i]
	}
	return rankingMap, nil
}
