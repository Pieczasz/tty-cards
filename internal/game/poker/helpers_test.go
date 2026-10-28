package poker

import (
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/gametest"
)

// withExtra runs fn on the engine's poker state under the engine lock. What fn reads
// is copied out by fn itself: the pointer is only valid while the lock is held.
func withExtra(t gametest.TB, e *game.Engine, fn func(*State)) {
	t.Helper()
	e.WithState(func(s *game.State) { fn(gametest.Extra[*State](t, s)) })
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
func readExtra[T any](t gametest.TB, e *game.Engine, fn func(*State) T) T {
	t.Helper()
	var out T
	withExtra(t, e, func(s *State) { out = fn(s) })
	return out
}
