package rating

import (
	"slices"
	"testing"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// uid is a readable fixed id; a zero id would read as "no seat".
func uid(n byte) uuid.UUID {
	var id uuid.UUID
	id[15] = n
	return id
}

func established(elo uint32) Standing { return Standing{Elo: elo, MatchesPlayed: ProvisionalMatches} }

func TestRate(t *testing.T) {
	t.Parallel()
	a, b, c, d := uid(1), uid(2), uid(3), uid(4)

	tests := []struct {
		name       string
		table      Table
		want       []Write
		wantDamped bool
	}{
		{
			name: "an even pair moves sixteen each way",
			table: Table{
				Seats:   []Seat{{a, 1}, {b, 2}},
				Current: map[uuid.UUID]Standing{a: established(1500), b: established(1500)},
			},
			want: []Write{{a, 1516, 16}, {b, 1484, -16}},
		},
		{
			name: "beating a provisional seat pays the established one nothing",
			table: Table{
				Seats: []Seat{{a, 1}, {b, 2}},
				Current: map[uuid.UUID]Standing{
					a: established(1500), b: {Elo: 1500, MatchesPlayed: ProvisionalMatches - 1},
				},
			},
			want: []Write{{a, 1500, 0}, {b, 1484, -16}},
		},
		{
			name: "losing to a provisional seat still costs the established one",
			table: Table{
				Seats: []Seat{{a, 1}, {b, 2}},
				Current: map[uuid.UUID]Standing{
					a: {Elo: 1500, MatchesPlayed: 0}, b: established(1500),
				},
			},
			want: []Write{{a, 1516, 16}, {b, 1484, -16}},
		},
		{
			name: "two provisional seats rate like any pair",
			table: Table{
				Seats:   []Seat{{a, 1}, {b, 2}},
				Current: map[uuid.UUID]Standing{a: {Elo: 1500}, b: {Elo: 1500}},
			},
			want: []Write{{a, 1516, 16}, {b, 1484, -16}},
		},
		{
			name: "an even draw moves nobody",
			table: Table{
				Seats:   []Seat{{a, 1}, {b, 1}},
				Current: map[uuid.UUID]Standing{a: established(1500), b: established(1500)},
			},
			want: []Write{{a, 1500, 0}, {b, 1500, 0}},
		},
		{
			name: "one pairing under the cap still pays",
			table: Table{
				Seats:          []Seat{{a, 1}, {b, 2}},
				Current:        map[uuid.UUID]Standing{a: established(1500), b: established(1500)},
				RecentPairings: MaxSamePairingPerDay - 1,
			},
			want: []Write{{a, 1516, 16}, {b, 1484, -16}},
		},
		{
			name: "a table at the cap is damped: every row written, none moved",
			table: Table{
				Seats:          []Seat{{a, 1}, {b, 2}},
				Current:        map[uuid.UUID]Standing{a: established(1500), b: established(1500)},
				RecentPairings: MaxSamePairingPerDay,
			},
			want:       []Write{{a, 1500, 0}, {b, 1500, 0}},
			wantDamped: true,
		},
		{
			name: "an interrupted match writes only its leaver, and only down",
			table: Table{
				Seats: []Seat{{a, 1}, {b, 2}, {c, 3}},
				// The seated winner has no row yet: it rates as a new player and is not seeded.
				Current: map[uuid.UUID]Standing{b: established(1500), c: established(1500)},
				Scope:   LeaversOnly([]uuid.UUID{c}),
			},
			want: []Write{{c, 1484, -16}},
		},
		{
			name: "a leaver who comes out ahead of a tied fellow leaver keeps their rating",
			table: Table{
				// b and d tie behind the seated a; the draw pays the lower-rated d.
				Seats: []Seat{{a, 1}, {b, 2}, {d, 2}},
				Current: map[uuid.UUID]Standing{
					a: established(1500), b: established(1600), d: established(1400),
				},
				Scope: LeaversOnly([]uuid.UUID{b, d}),
			},
			want: []Write{{b, 1571, -29}, {d, 1400, 0}},
		},
		{
			name: "an interrupted match with nobody left to charge is not rated",
			table: Table{
				Seats:   []Seat{{a, 1}, {b, 2}},
				Current: map[uuid.UUID]Standing{a: established(1500), b: established(1500)},
				Scope:   LeaversOnly([]uuid.UUID{c}),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Rate(tt.table)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.Writes)
			assert.Equal(t, tt.wantDamped, got.Damped)
			assert.Equal(t, len(tt.want) > 0, got.Rated())
		})
	}
}

// The seed runs before the read, so a written seat without a row means the row went
// away underneath the finalize. Rating it as new would reset it to 1500.
func TestRate_AWrittenSeatWithoutARowIsAnError(t *testing.T) {
	t.Parallel()
	_, err := Rate(Table{
		Seats:   []Seat{{uid(1), 1}, {uid(2), 2}},
		Current: map[uuid.UUID]Standing{uid(1): established(1500)},
	})
	assert.ErrorContains(t, err, "no ranking row for user "+uid(2).String())
}

func TestScopeWritten(t *testing.T) {
	t.Parallel()
	seats := []uuid.UUID{uid(1), uid(2), uid(3)}

	assert.Equal(t, seats, EverySeat().Written(seats))
	assert.False(t, EverySeat().LossOnly())

	leavers := LeaversOnly([]uuid.UUID{uid(3), uid(1), uid(9)})
	assert.True(t, leavers.LossOnly())
	assert.Equal(t, []uuid.UUID{uid(1), uid(3)}, leavers.Written(seats), "seat order, seated players and strangers dropped")
	assert.Equal(t, []uuid.UUID{uid(1), uid(2), uid(3)}, seats, "Written must not edit the caller's slice")
	assert.Empty(t, LeaversOnly(nil).Written(seats), "an interrupted match with no leavers writes nobody")
}

func TestSeats(t *testing.T) {
	t.Parallel()
	ids := []uuid.UUID{uid(1), uid(2), uid(3), uid(4)}
	erased := func(id uuid.UUID) bool { return id == uid(2) }

	got := Seats(ids, []int{1, 2, 2, 4}, erased)

	assert.Equal(t, []Seat{{uid(1), 1}, {uid(3), 2}, {uid(4), 4}}, got, "an erased seat must not shift the others' places")
	assert.Equal(t, []uuid.UUID{uid(1), uid(3), uid(4)}, IDs(got))
	assert.Equal(t, []Seat{{uid(1), 1}, {uid(2), 2}}, Seats(ids[:2], nil, func(uuid.UUID) bool { return false }))
}

// PlaceAt is what decides a seat's recorded placement, and a wrong answer here is a
// wrong Elo transfer: the index is the strict finish order when no ties were reported.
func TestPlaceAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		places []int
		index  int
		want   int
	}{
		{name: "no tie information", places: nil, index: 2, want: 3},
		{name: "explicit places", places: []int{1, 1, 3}, index: 1, want: 1},
		{name: "index past the slice", places: []int{1}, index: 4, want: 5},
		{name: "a zero place is no place", places: []int{0, 2}, index: 0, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, PlaceAt(tt.places, tt.index))
		})
	}
}

// WorstPairCount is the anti-farm cap's whole decision: the busiest pair at the table,
// not the busiest seat and not the number of repeated tables.
func TestWorstPairCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		tables [][]uuid.UUID
		want   int
	}{
		{name: "nothing recent", tables: nil, want: 0},
		{name: "a lone seat forms no pair", tables: [][]uuid.UUID{{uid(7)}}, want: 0},
		{name: "one meeting", tables: [][]uuid.UUID{{uid(7), uid(8)}}, want: 1},
		{
			name: "the same pair padded with a different third each time",
			tables: [][]uuid.UUID{
				{uid(7), uid(8), uid(9)},
				{uid(8), uid(10), uid(7)},
				{uid(11), uid(7), uid(8)},
			},
			want: 3,
		},
		{
			name: "a busy seat with different partners is not a busy pair",
			tables: [][]uuid.UUID{
				{uid(7), uid(8)},
				{uid(7), uid(9)},
				{uid(7), uid(10)},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, WorstPairCount(tt.tables))
		})
	}
}

func TestThresholdsArePositive(t *testing.T) {
	t.Parallel()
	// A zero threshold would make every fresh account established on sight, which is
	// the whole exploit the provisional rule exists to close; a zero cap damps all.
	require.Positive(t, ProvisionalMatches)
	require.Positive(t, MaxSamePairingPerDay)
}

// genTable draws a finished table: seats in finish order with ties, ratings across the
// whole scale, a mix of provisional and established rows, and either scope. A seat the
// scope does not write may have no row, as a seated player of an interrupted match can.
func genTable(t *rapid.T) Table {
	n := rapid.IntRange(2, 6).Draw(t, "seats")
	place := 1
	table := Table{Current: make(map[uuid.UUID]Standing, n)}
	var leavers []uuid.UUID
	for i := range n {
		id := uid(byte(i + 1))
		if i > 0 && !rapid.Bool().Draw(t, "tied") {
			place = i + 1
		}
		table.Seats = append(table.Seats, Seat{UserID: id, Place: place})
		if rapid.Bool().Draw(t, "leaver") {
			leavers = append(leavers, id)
		}
	}
	lossOnly := rapid.Bool().Draw(t, "lossOnly")
	table.Scope = EverySeat()
	if lossOnly {
		table.Scope = LeaversOnly(leavers)
	}
	for _, seat := range table.Seats {
		if lossOnly && !slices.Contains(leavers, seat.UserID) && rapid.Bool().Draw(t, "no row") {
			continue
		}
		table.Current[seat.UserID] = Standing{
			Elo:           rapid.Uint32Range(100, 4000).Draw(t, "elo"),
			MatchesPlayed: rapid.Uint64Range(0, 2*ProvisionalMatches).Draw(t, "played"),
		}
	}
	table.RecentPairings = rapid.IntRange(0, MaxSamePairingPerDay+2).Draw(t, "pairings")
	return table
}

func writtenIDs(writes []Write) []uuid.UUID {
	ids := make([]uuid.UUID, len(writes))
	for i, w := range writes {
		ids[i] = w.UserID
	}
	return ids
}

// The invariants the ladder rests on, over any table: the scope alone picks who is
// written, a delta is exactly what was stored, an interrupted match never pays, a
// damped table never moves, and nothing leaves the rating scale.
func TestRate_Properties(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		table := genTable(t)

		got, err := Rate(table)
		require.NoError(t, err)

		written := table.Scope.Written(IDs(table.Seats))
		assert.Equal(t, written, writtenIDs(got.Writes), "the writes are not the scope's seats")
		assert.Equal(t, len(written) > 0, got.Rated())
		for _, w := range got.Writes {
			old := table.Current[w.UserID].Elo
			assert.Equal(t, int(w.Elo)-int(old), w.Delta, "the delta is not what was stored")
			assert.GreaterOrEqual(t, w.Elo, uint32(100))
			assert.LessOrEqual(t, w.Elo, uint32(4000))
			if table.Scope.LossOnly() {
				assert.LessOrEqual(t, w.Elo, old, "a loss-only write raised a rating")
			}
			if got.Damped {
				assert.Zero(t, w.Delta, "a damped table moved a rating")
			}
		}
		assert.Equal(t, got.Rated() && table.RecentPairings >= MaxSamePairingPerDay, got.Damped)
	})
}

// Among established players the transfers are zero-sum, so what the stored ratings
// gain and lose can differ only by rounding: at most half a point per seat.
func TestRate_EstablishedTableIsConservedUpToRounding(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		table := genTable(t)
		table.Scope, table.RecentPairings = EverySeat(), 0
		for _, seat := range table.Seats {
			table.Current[seat.UserID] = established(rapid.Uint32Range(100, 4000).Draw(t, "elo"))
		}

		got, err := Rate(table)
		require.NoError(t, err)

		sum := 0
		for _, w := range got.Writes {
			sum += w.Delta
		}
		assert.LessOrEqual(t, 2*max(sum, -sum), len(got.Writes), "ratings minted or burned: %+v", got.Writes)
	})
}

// A draw between two established players is symmetric: what one gains the other
// loses, and it is the lower-rated side that gains.
func TestRate_ADrawIsSymmetric(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		a, b := uid(1), uid(2)
		ra := rapid.Uint32Range(100, 4000).Draw(t, "a")
		rb := rapid.Uint32Range(100, 4000).Draw(t, "b")

		got, err := Rate(Table{
			Seats:   []Seat{{a, 1}, {b, 1}},
			Current: map[uuid.UUID]Standing{a: established(ra), b: established(rb)},
		})
		require.NoError(t, err)

		da, db := got.Writes[0].Delta, got.Writes[1].Delta
		assert.Equal(t, -da, db)
		if ra < rb {
			assert.GreaterOrEqual(t, da, 0, "the lower-rated side lost a draw")
		}
	})
}
