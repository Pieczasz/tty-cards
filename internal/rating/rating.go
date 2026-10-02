// Package rating is the ladder's policy: which seats a ranked result may move, and to
// what. It decides and the repository stores - the locks, the reads and the writes stay
// there - so every rule here is testable without a database.
package rating

import (
	"fmt"
	"slices"

	"uuid"

	"github.com/Pieczasz/tty-cards/internal/elo"
)

const (
	// ProvisionalMatches is how many ranked results a ranking row needs before
	// beating its owner pays anything. Identity here is a free SSH keypair, so a
	// fresh 1500-rated account is free to mint and farming it must not be
	// profitable until it has a track record of its own.
	ProvisionalMatches = 5

	// MaxSamePairingPerDay is how many ranked matches any two players at a table may
	// already have shared inside a 24h window before the table stops moving rating.
	// The same two accounts trading wins is either farming or a private rivalry;
	// either way the ladder stops paying after three a day.
	MaxSamePairingPerDay = 3
)

// Scope is which seats a ranked finalize writes. Elo is computed over every seat
// either way; the scope only decides whose row moves.
type Scope struct {
	// lossOnly limits the write to leavers, and each of them to a loss. It is set even
	// with no leavers: an interrupted match moves no seated rating whether or not anyone
	// is left to charge.
	lossOnly bool
	leavers  map[uuid.UUID]bool
}

// EverySeat is an ordinary ranked result: every seat is written.
func EverySeat() Scope { return Scope{} }

// LeaversOnly is an interrupted match: only the leavers are written, and only down.
func LeaversOnly(leavers []uuid.UUID) Scope {
	scope := Scope{lossOnly: true, leavers: make(map[uuid.UUID]bool, len(leavers))}
	for _, id := range leavers {
		scope.leavers[id] = true
	}
	return scope
}

// LossOnly is whether this is an interrupted match's scope.
func (s Scope) LossOnly() bool { return s.lossOnly }

// Written is the seats whose ranking row this finalize writes, in seat order.
func (s Scope) Written(seats []uuid.UUID) []uuid.UUID {
	if !s.lossOnly {
		return seats
	}
	return slices.DeleteFunc(slices.Clone(seats), func(id uuid.UUID) bool { return !s.leavers[id] })
}

// stored is the rating a written seat ends on. A loss-only write caps it at the old
// one: a leaver ranked last can still come out ahead of a tied fellow leaver, and
// quitting must never pay.
func (s Scope) stored(newRating float64, old uint32) uint32 {
	rating := elo.ToUint32(newRating)
	if s.lossOnly {
		return min(rating, old)
	}
	return rating
}

// Seat is one rated seat: who, and the place they finished in.
type Seat struct {
	UserID uuid.UUID
	Place  int
}

// Seats pairs each standing with its place and drops the seats erased reports, their
// places kept so ties still line up. An account erased while the match ran keeps its
// history row, which is other players' history, and nothing else: rating it would put
// it back on the leaderboard under its anonymised name.
func Seats(userIDs []uuid.UUID, places []int, erased func(uuid.UUID) bool) []Seat {
	seats := make([]Seat, 0, len(userIDs))
	for i, id := range userIDs {
		if !erased(id) {
			seats = append(seats, Seat{UserID: id, Place: PlaceAt(places, i)})
		}
	}
	return seats
}

// IDs is the seats' user ids, in seat order.
func IDs(seats []Seat) []uuid.UUID {
	ids := make([]uuid.UUID, len(seats))
	for i, s := range seats {
		ids[i] = s.UserID
	}
	return ids
}

// PlaceAt is the finishing place of the i-th standing. Callers that have no tie
// information pass nil, which is the strict order the slice already carries.
func PlaceAt(places []int, i int) int {
	if i < len(places) && places[i] > 0 {
		return places[i]
	}
	return i + 1
}

// Standing is a seat's ranking row as the finalize read it.
type Standing struct {
	Elo           uint32
	MatchesPlayed uint64
}

// Table is everything the policy needs to rate one finished match.
type Table struct {
	// Seats are the unerased seats, first place to last.
	Seats []Seat
	// Current holds the ranking rows that exist. A seat missing here is a new player:
	// it rates as elo.DefaultRating and provisional, and it must not be a written seat.
	Current map[uuid.UUID]Standing
	// RecentPairings is WorstPairCount over the last 24h of ranked matches.
	RecentPairings int
	Scope          Scope
}

// Write is one ranking row to store: its new rating and the delta history records.
type Write struct {
	UserID uuid.UUID
	Elo    uint32
	Delta  int
}

// Result is the rating decision for one table.
type Result struct {
	// Writes are the written seats, in seat order. Each is a match played whether or
	// not its rating moved: that is what lets a provisional account graduate.
	Writes []Write
	// Damped is a table that has met too often inside 24h: every write keeps its rating.
	Damped bool
}

// Rated is whether any seat's row is written. A match that writes none is recorded as
// casual, because ranked history is what the pairing cap counts.
func (r Result) Rated() bool { return len(r.Writes) > 0 }

// Rate decides a finished table. Elo is computed over every seat - a leaver's loss is
// measured against the table they left - but only the seats the scope writes get a
// Write. Who gets paid against a provisional seat is elo.Calculate's decision, per
// pair; here a damped table is the only reason a written rating stays put.
func Rate(t Table) (Result, error) {
	written := t.Scope.Written(IDs(t.Seats))
	if len(written) == 0 {
		return Result{}, nil
	}

	newRatings := elo.Calculate(players(t.Seats, t.Current))
	damped := t.RecentPairings >= MaxSamePairingPerDay
	writes := make([]Write, 0, len(written))
	for _, id := range written {
		current, ok := t.Current[id]
		if !ok {
			// A written seat was seeded before the read, so a miss is a row gone
			// underneath the finalize, not a new player; storing the default would
			// reset their rating.
			return Result{}, fmt.Errorf("no ranking row for user %s", id)
		}
		stored := current.Elo
		if !damped {
			stored = t.Scope.stored(newRatings[id.String()], current.Elo)
		}
		writes = append(writes, Write{UserID: id, Elo: stored, Delta: int(stored) - int(current.Elo)})
	}
	return Result{Writes: writes, Damped: damped}, nil
}

func players(seats []Seat, current map[uuid.UUID]Standing) []elo.Player {
	out := make([]elo.Player, 0, len(seats))
	for _, seat := range seats {
		rating, provisional := elo.DefaultRating, true
		if s, ok := current[seat.UserID]; ok {
			rating = float64(s.Elo)
			provisional = s.MatchesPlayed < ProvisionalMatches
		}
		out = append(out, elo.Player{
			ID:          seat.UserID.String(),
			Rating:      rating,
			Place:       seat.Place,
			Provisional: provisional,
		})
	}
	return out
}

// WorstPairCount is the highest co-occurrence count of any pair over tables, each the
// seats of this table that sat in one recent match. Pairs, not seat sets: {A,B},
// {A,B,C} and {A,B,D} are three sets but one pair meeting three times, so rotating an
// alt through the table buys no fresh budget. It sorts each table in place.
func WorstPairCount(tables [][]uuid.UUID) int {
	counts := make(map[[2]uuid.UUID]int, len(tables))
	worst := 0
	for _, shared := range tables {
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
