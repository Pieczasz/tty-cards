package repository

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"uuid"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pgregory.net/rapid"
)

// A duplicate seat would move one player's rating twice and then collide on the
// match_participants primary key, rolling the whole match back.
func TestCheckDistinctPlayers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ids     []uuid.UUID
		wantErr string
	}{
		{name: "empty", ids: nil},
		{name: "distinct", ids: []uuid.UUID{testutil.UID(3), testutil.UID(1), testutil.UID(2)}},
		{name: "adjacent duplicate", ids: []uuid.UUID{testutil.UID(1), testutil.UID(1)}, wantErr: "duplicate user id " + testutil.UID(1).String()},
		{name: "distant duplicate", ids: []uuid.UUID{testutil.UID(4), testutil.UID(7), testutil.UID(9), testutil.UID(4)}, wantErr: "duplicate user id " + testutil.UID(4).String()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkDistinctPlayers(tt.ids)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

// Each finalize names itself in its errors: the interrupted path used to report a
// failure as a ranked one, and the lobby then wrapped that same prefix a second time.
func TestFinalize_ErrorsNameTheirOwnPath(t *testing.T) {
	t.Parallel()
	// A duplicate seat is refused before the database is touched, so no pool is needed.
	repo := NewMatchRepository(nil)
	dup := []uuid.UUID{testutil.UID(1), testutil.UID(1)}

	tests := []struct {
		name string
		call func() error
		want string
	}{
		{
			name: "ranked",
			call: func() error { return repo.FinalizeRankedMatch(t.Context(), db.GameRef{Slug: "x"}, dup, nil) },
			want: "finalize ranked match: duplicate user id",
		},
		{
			name: "interrupted",
			call: func() error {
				return repo.FinalizeInterruptedMatch(t.Context(), db.GameRef{Slug: "x"}, dup, nil, nil)
			},
			want: "finalize interrupted match: duplicate user id",
		},
		{
			name: "casual",
			call: func() error { return repo.RecordCasualMatch(t.Context(), db.GameRef{Slug: "x"}, dup) },
			want: "record casual match: duplicate user id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.call()
			require.Error(t, err)
			assert.True(t, strings.HasPrefix(err.Error(), tt.want), "got %q", err)
		})
	}
}

// likePrefix has to escape the anonymised prefix's underscore, or LIKE reads it as a
// wildcard and a chosen name such as "deletedX..." would count as erased.
func TestLikePrefix(t *testing.T) {
	t.Parallel()
	assert.Equal(t, `deleted\_%`, likePrefix(db.AnonymisedPrefix))
	assert.Equal(t, `a\%b\\c%`, likePrefix(`a%b\c`))
}

// placeAt is what decides a seat's recorded placement, and a wrong answer here is a
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
			assert.Equal(t, tt.want, placeAt(tt.places, tt.index))
		})
	}
}

// worstPairCount is the anti-farm cap's whole decision: the busiest pair at the table,
// not the busiest seat and not the number of repeated tables.
func TestWorstPairCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		seats map[uint][]uuid.UUID
		want  int
	}{
		{name: "nothing recent", seats: nil, want: 0},
		{name: "a lone seat forms no pair", seats: map[uint][]uuid.UUID{1: {testutil.UID(7)}}, want: 0},
		{name: "one meeting", seats: map[uint][]uuid.UUID{1: {testutil.UID(7), testutil.UID(8)}}, want: 1},
		{
			name: "the same pair padded with a different third each time",
			seats: map[uint][]uuid.UUID{
				1: {testutil.UID(7), testutil.UID(8), testutil.UID(9)},
				2: {testutil.UID(7), testutil.UID(8), testutil.UID(10)},
				3: {testutil.UID(7), testutil.UID(8), testutil.UID(11)},
			},
			want: 3,
		},
		{
			name: "a busy seat with different partners is not a busy pair",
			seats: map[uint][]uuid.UUID{
				1: {testutil.UID(7), testutil.UID(8)},
				2: {testutil.UID(7), testutil.UID(9)},
				3: {testutil.UID(7), testutil.UID(10)},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, worstPairCount(tt.seats))
		})
	}
}

// The advisory key is what two overlapping finalizes agree on, so it has to depend on
// the seat alone - and two different seats must not collapse onto one lock.
func TestSeatAdvisoryKey(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		a := uuid.New()
		b := uuid.New()
		if a == b {
			return
		}
		assert.NotEqual(t, seatAdvisoryKey(a), seatAdvisoryKey(b),
			"two seats collapsed onto one advisory lock")
	})
}

// Two seats can fold onto one key. Locking in user-id order then took the same key at
// two different points, so two finalizes sharing those seats could grab the locks in
// opposite orders and deadlock. The order - and the dedupe - has to be by key.
func TestSeatLockKeys(t *testing.T) {
	t.Parallel()
	var swapped uuid.UUID // high word 1, low word 0: folds to the same key as UID(1)
	swapped[7] = 1
	require.Equal(t, seatAdvisoryKey(testutil.UID(1)), seatAdvisoryKey(swapped), "the fixture must collide")

	keys := seatLockKeys([]uuid.UUID{testutil.UID(9), swapped, testutil.UID(1)})

	assert.Len(t, keys, 2, "a colliding pair must be locked once")
	assert.True(t, slices.IsSorted(keys), "locks must be taken in key order: %v", keys)
}

// A lost registration race arrives as 23505 from the driver, and the only honest way
// to tell it from a real failure is the code.
func TestIsUniqueViolation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil},
		{name: "plain error", err: errors.New("boom")},
		{name: "another pg error", err: &pgconn.PgError{Code: "23503"}},
		{name: "unique violation", err: &pgconn.PgError{Code: pgUniqueViolationCode}, want: true},
		{
			name: "wrapped unique violation",
			err:  errors.Join(errors.New("create user"), &pgconn.PgError{Code: pgUniqueViolationCode}),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, isUniqueViolation(tt.err))
		})
	}
}

func TestProvisionalThresholdIsPositive(t *testing.T) {
	t.Parallel()
	// A zero threshold would make every fresh account established on sight, which is
	// the whole exploit the provisional rule exists to close.
	require.Positive(t, provisionalMatches)
	require.Positive(t, maxSamePairingPerDay)
}
