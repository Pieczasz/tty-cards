package poker

import (
	"github.com/Pieczasz/terminal-card/internal/game"

	"github.com/stretchr/testify/require"
)

// testingT is the slice of *testing.T and *rapid.T these helpers need. Threading
// rapid's own T into them is what lets a property failure be reported to rapid,
// which then shrinks the counterexample; reporting to the parent *testing.T instead
// aborts the property goroutine and loses the seed.
type testingT interface {
	require.TestingT
	Helper()
}

// extra is the poker state of a hand-built table, failing the test rather than
// panicking on anything else.
func extra(t testingT, s *game.State) *State {
	t.Helper()
	e, ok := s.Extra.(*State)
	require.True(t, ok, "state.Extra is %T, not *poker.State", s.Extra)
	return e
}

// withExtra runs fn on the engine's poker state under the engine lock. What fn reads
// is copied out by fn itself: the pointer is only valid while the lock is held.
func withExtra(t testingT, e *game.Engine, fn func(*State)) {
	t.Helper()
	e.WithState(func(s *game.State) { fn(extra(t, s)) })
}

// seatsWithChips seats every player in chips with that stack and nothing else set.
func seatsWithChips(chips map[string]uint) map[string]*Seat {
	seats := make(map[string]*Seat, len(chips))
	for id, c := range chips {
		seats[id] = &Seat{Chips: c}
	}
	return seats
}

// stacks is every seat's chips, copied out.
func stacks(extra *State) map[string]uint {
	out := make(map[string]uint, len(extra.Seats))
	for id, seat := range extra.Seats {
		out[id] = seat.Chips
	}
	return out
}

// contributions is every seat's chips committed this hand, copied out.
func contributions(extra *State) map[string]uint {
	out := make(map[string]uint, len(extra.Seats))
	for id, seat := range extra.Seats {
		out[id] = seat.Contributed
	}
	return out
}

// readExtra is fn of the engine's poker state, computed under the engine lock.
func readExtra[T any](t testingT, e *game.Engine, fn func(*State) T) T {
	t.Helper()
	var out T
	withExtra(t, e, func(s *State) { out = fn(s) })
	return out
}
