package repository

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"uuid"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/elo"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var tracer = otel.Tracer("terminal-card/repository")

// recordSpanResult marks a span failed on error: recording the error without the
// status leaves the one failed span reading as successful. Ending it stays at the
// call site so the End is visible on every path.
func recordSpanResult(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
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
// DBMaxOpenConnections concurrent finalizes deadlock until they time out - and the game
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

// RecordCasualMatch writes an unranked result's history; see db.MatchRepository.
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

	if err = q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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

// FinalizeRankedMatch creates/looks up the game, updates rankings, and records the match
// in a single database transaction so ELO and history cannot diverge.
func (q *MatchRepository) FinalizeRankedMatch(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID, places []int,
) error {
	return q.finalizeRanked(ctx, ref, orderedUserIDs, places, everySeat())
}

// FinalizeInterruptedMatch is FinalizeRankedMatch with only the leavers' losses
// written; see the interface for the policy.
func (q *MatchRepository) FinalizeInterruptedMatch(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID, places []int, leavers []uuid.UUID,
) error {
	return q.finalizeRanked(ctx, ref, orderedUserIDs, places, leaversOnly(leavers))
}

// ratingScope is which seats a ranked finalize writes. Elo is computed over every seat
// either way; the scope only decides whose row moves.
type ratingScope struct {
	// lossOnly limits the write to leavers, and each of them to a loss. It is set even
	// with no leavers: an interrupted match moves no seated rating whether or not anyone
	// is left to charge.
	lossOnly bool
	leavers  map[uuid.UUID]bool
}

func everySeat() ratingScope { return ratingScope{} }

func leaversOnly(leavers []uuid.UUID) ratingScope {
	scope := ratingScope{lossOnly: true, leavers: make(map[uuid.UUID]bool, len(leavers))}
	for _, id := range leavers {
		scope.leavers[id] = true
	}
	return scope
}

// op names the finalize for its span and its errors, so an interrupted match that
// fails does not report itself as a ranked one.
func (s ratingScope) op() (span, msg string) {
	if s.lossOnly {
		return "db.FinalizeInterruptedMatch", "finalize interrupted match"
	}
	return "db.FinalizeRankedMatch", "finalize ranked match"
}

// written is the seats whose ranking row this finalize writes.
func (s ratingScope) written(seats []uuid.UUID) []uuid.UUID {
	if !s.lossOnly {
		return seats
	}
	return slices.DeleteFunc(slices.Clone(seats), func(id uuid.UUID) bool { return !s.leavers[id] })
}

// stored is the rating a written seat ends on. A loss-only write caps it at the old
// one: a leaver ranked last can still come out ahead of a tied fellow leaver, and
// quitting must never pay.
func (s ratingScope) stored(newRating float64, old uint32) uint32 {
	rating := elo.ToUint32(newRating)
	if s.lossOnly {
		return min(rating, old)
	}
	return rating
}

func (q *MatchRepository) finalizeRanked(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID, places []int, scope ratingScope,
) (err error) {
	if len(orderedUserIDs) == 0 {
		return nil
	}

	spanName, op := scope.op()
	ctx, span := tracer.Start(ctx, spanName,
		trace.WithAttributes(attribute.String("game", ref.Slug), attribute.Int("players", len(orderedUserIDs))))
	defer func() { recordSpanResult(span, err); span.End() }()

	if err = checkDistinctPlayers(orderedUserIDs); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}

	if err = q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		game, err := getOrCreateGame(tx, ref)
		if err != nil {
			return err
		}

		deltas, err := updateRankings(ctx, tx, game.ID, orderedUserIDs, places, scope)
		if err != nil {
			return err
		}
		return recordMatch(tx, game.ID, orderedUserIDs, places, deltas, true)
	}); err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	return nil
}

// seedRankingRows gives fetchRankings something to lock. A first-time (user_id, game_id)
// has no row, so FOR UPDATE locks nothing and two concurrent finalizes both insert: one
// hits 23505, rolls back and loses the match. Sorted by user_id because a conflicting
// insert still waits on the inserting transaction, though the ordering that matters is
// fetchRankings' - ON CONFLICT DO NOTHING takes no lock on an existing row.
//
// Soft-deleted rankings are revived first: DO NOTHING leaves them invisible to the
// default scope, and a missing row used to abort the whole table's finalize.
func seedRankingRows(tx *gorm.DB, gameID uint, userIDs []uuid.UUID) error {
	// Sorted for the same reason fetchRankings orders: an UPDATE locks the rows its
	// IN list yields, so two finalizes over overlapping seats would otherwise take
	// the revived rows in opposite orders and Postgres would abort one.
	sorted := sortedUserIDs(userIDs)
	if err := tx.Unscoped().Model(&db.Ranking{}).
		Where("user_id IN ? AND game_id = ? AND deleted_at IS NOT NULL", uuidStrings(sorted), gameID).
		Update("deleted_at", nil).Error; err != nil {
		return fmt.Errorf("revive rankings: %w", err)
	}
	seeds := make([]db.Ranking, 0, len(userIDs))
	for _, userID := range sorted {
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

const (
	// provisionalMatches is how many ranked results a ranking row needs before
	// beating its owner pays anything. Identity here is a free SSH keypair, so a
	// fresh 1500-rated account is free to mint and farming it must not be
	// profitable until it has a track record of its own.
	provisionalMatches = 5

	// maxSamePairingPerDay is how many ranked matches any two players at a table may
	// already have shared inside a 24h window before the table stops moving rating.
	// The same two accounts trading wins is either farming or a private rivalry;
	// either way the ladder stops paying after three a day.
	maxSamePairingPerDay = 3
)

// updateRankings rates the table. Elo is computed over every seat - a leaver's loss is
// measured against the table they left - but only the seats scope writes are stored.
func updateRankings(
	ctx context.Context, tx *gorm.DB, gameID uint, orderedUserIDs []uuid.UUID, places []int, scope ratingScope,
) (map[uuid.UUID]int, error) {
	// Serialize same-pairing finalizes across every game: ranking row locks are
	// per (user, game), so A-B farming Poker and Hearts concurrently would both
	// see an undamped count without this.
	if err := lockPairing(tx, orderedUserIDs); err != nil {
		return nil, err
	}

	// Read under the seat locks eraseUser also takes, so an erasure either
	// committed before this or waits for this transaction to finish.
	seats, seatPlaces, err := unerasedSeats(tx, orderedUserIDs, places)
	if err != nil {
		return nil, err
	}
	written := scope.written(seats)
	if len(written) == 0 {
		return map[uuid.UUID]int{}, nil
	}

	// Only rows about to be written are seeded: a seated player with no ranking yet
	// would otherwise land on the leaderboard from a match that did not count for them.
	// calculateNewElos reads a missing row as the starting rating anyway.
	if err := seedRankingRows(tx, gameID, written); err != nil {
		return nil, err
	}

	rankingMap, err := fetchRankings(tx, gameID, seats)
	if err != nil {
		return nil, fmt.Errorf("fetch rankings: %w", err)
	}

	damped, err := isDamped(ctx, tx, gameID, seats)
	if err != nil {
		return nil, err
	}
	return applyRatings(tx, gameID, written, rankingMap, calculateNewElos(seats, seatPlaces, rankingMap), damped, scope)
}

// isDamped is whether this table has met too often inside 24h for its result to move
// rating (maxSamePairingPerDay).
func isDamped(ctx context.Context, tx *gorm.DB, gameID uint, seats []uuid.UUID) (bool, error) {
	pairings, err := repeatedPairCountLast24h(tx, seats)
	if err != nil {
		return false, err
	}
	damped := pairings >= maxSamePairingPerDay
	if damped {
		slog.WarnContext(ctx, "ranked match damped: these players have already met inside 24h",
			"user_ids", seats, "game_id", gameID, "recent_pairings", pairings)
	}
	return damped, nil
}

// applyRatings writes each written seat's result and returns the deltas history
// records. A damped table keeps every rating and records no delta.
func applyRatings(
	tx *gorm.DB, gameID uint, written []uuid.UUID, rankingMap map[uuid.UUID]*db.Ranking,
	newRatings map[string]float64, damped bool, scope ratingScope,
) (map[uuid.UUID]int, error) {
	deltas := make(map[uuid.UUID]int, len(written))
	for _, userID := range written {
		// Every seat was just seeded, so a miss is a soft-deleted row, not a new player.
		r, ok := rankingMap[userID]
		if !ok {
			return nil, fmt.Errorf("no ranking row for user %s in game %d", userID, gameID)
		}
		// A key mismatch used to fall through to the zero value and store the elo floor.
		newRating, ok := newRatings[userID.String()]
		if !ok {
			return nil, fmt.Errorf("no elo result for user %s", userID)
		}
		// Who gets paid against a provisional seat is elo.Calculate's decision, per
		// pair; here a damped table is the only reason a rating stays put.
		stored := r.Elo
		if !damped {
			stored = scope.stored(newRating, r.Elo)
		}
		delta, err := writeRanking(tx, r, stored)
		if err != nil {
			return nil, err
		}
		if !damped {
			deltas[userID] = delta
		}
	}
	return deltas, nil
}

// writeRanking stores one seat's rating and returns the delta it wrote.
func writeRanking(tx *gorm.DB, r *db.Ranking, stored uint32) (int, error) {
	// The increment rides the row this transaction already holds FOR UPDATE, and
	// happens whether or not the rating moved: a damped or unpaid match is still a
	// match played, and it is what lets a provisional account graduate. On an
	// interrupted match only the leaver's row gets here, so only they are counted.
	// Measured before the update: Updates writes the new values back into r.
	delta := int(stored) - int(r.Elo)
	res := tx.Model(r).Updates(map[string]any{
		"matches_played": gorm.Expr("matches_played + 1"),
		"elo":            stored,
	})
	if res.Error != nil {
		return 0, fmt.Errorf("update ranking: %w", res.Error)
	}
	// This transaction holds the row FOR UPDATE, so zero rows means it is gone
	// underneath us. Carrying on would write the computed elo_delta into history for
	// a rating that never moved.
	if res.RowsAffected == 0 {
		return 0, fmt.Errorf("ranking for user %s in game %d was not updated", r.UserID, r.GameID)
	}
	return delta, nil
}

// unerasedSeats drops the seats whose account was erased while the match ran, with
// their places kept alongside so ties still line up. Seeding an erased seat would put
// the account back on the leaderboard under its anonymised name; it keeps its
// participant row, which is other players' history (decision #22), and nothing else.
//
// The anonymised prefix is safe to match on: ValidateUsername refuses it, so no chosen
// name starts with it. Unscoped because an operator soft-delete does not make the
// account any less erased.
func unerasedSeats(tx *gorm.DB, userIDs []uuid.UUID, places []int) ([]uuid.UUID, []int, error) {
	var erased []string
	if err := tx.Unscoped().Model(&db.User{}).
		Where("id IN ? AND username LIKE ?", uuidStrings(userIDs), likePrefix(db.AnonymisedPrefix)).
		Pluck("id", &erased).Error; err != nil {
		return nil, nil, fmt.Errorf("query erased seats: %w", err)
	}
	seats := make([]uuid.UUID, 0, len(userIDs))
	seatPlaces := make([]int, 0, len(userIDs))
	for i, id := range userIDs {
		if !slices.Contains(erased, id.String()) {
			seats = append(seats, id)
			seatPlaces = append(seatPlaces, placeAt(places, i))
		}
	}
	return seats, seatPlaces, nil
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
// (idx_matches_ranked_created, migration 000007) and joins out to these seats, so it
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

	var rows []db.MatchParticipant
	if err := tx.Where("match_id IN ?", matchIDs).Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("query pairing participants: %w", err)
	}

	here := make(map[uuid.UUID]struct{}, len(userIDs))
	for _, id := range userIDs {
		here[id] = struct{}{}
	}
	// Only the seats sitting at this table matter: a past match's other players are
	// not part of any pair being capped now.
	seats := make(map[uint][]uuid.UUID, len(matchIDs))
	for _, row := range rows {
		if _, ours := here[row.UserID]; ours {
			seats[row.MatchID] = append(seats[row.MatchID], row.UserID)
		}
	}
	return worstPairCount(seats), nil
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

// worstPairCount is the highest co-occurrence count over every pair in seats.
func worstPairCount(seats map[uint][]uuid.UUID) int {
	counts := make(map[[2]uuid.UUID]int, len(seats))
	worst := 0
	for _, shared := range seats {
		slices.SortFunc(shared, uuid.UUID.Compare)
		for i, a := range shared {
			for _, b := range shared[i+1:] {
				counts[[2]uuid.UUID{a, b}]++
				worst = max(worst, counts[[2]uuid.UUID{a, b}])
			}
		}
	}
	return worst
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

func sortedUserIDs(userIDs []uuid.UUID) []uuid.UUID {
	out := slices.Clone(userIDs)
	slices.SortFunc(out, uuid.UUID.Compare)
	return out
}

func uuidStrings(ids []uuid.UUID) []string {
	// Stdlib uuid.UUID has no database/sql Valuer; pgx will not encode it as uuid.
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
			Placement: placeAt(places, i),
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
	// FOR UPDATE: serialize concurrent finalize transactions to avoid lost Elo updates.
	//
	// ORDER BY user_id is what makes that safe rather than deadlock-prone: this is
	// where the row locks are actually taken (the seed's ON CONFLICT DO NOTHING locks
	// nothing when the row already exists, which is the common case), so without a
	// fixed order two finalizes over overlapping seats can lock in opposite orders.
	// Postgres then aborts one, and that match is lost from history and Elo.
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

func calculateNewElos(
	orderedUserIDs []uuid.UUID, places []int, rankingMap map[uuid.UUID]*db.Ranking,
) map[string]float64 {
	players := make([]elo.Player, 0, len(orderedUserIDs))
	for i, userID := range orderedUserIDs {
		rating, provisional := elo.DefaultRating, true
		if r, ok := rankingMap[userID]; ok {
			rating = float64(r.Elo)
			provisional = r.MatchesPlayed < provisionalMatches
		}
		players = append(players, elo.Player{
			ID:          userID.String(),
			Rating:      rating,
			Place:       placeAt(places, i),
			Provisional: provisional,
		})
	}
	return elo.Calculate(players)
}

// placeAt is the finishing place of the i-th standing. Callers that have no tie
// information pass nil, which is the strict order the slice already carries.
func placeAt(places []int, i int) int {
	if i < len(places) && places[i] > 0 {
		return places[i]
	}
	return i + 1
}
