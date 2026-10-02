package game

import "errors"

// SetTurn puts seat on turn now and keeps it there once the action settles. It is the
// one way a rules set names who acts next from inside ApplyAction or AfterAction.
func (s *State) SetTurn(seat int) {
	s.CurrentTurn = seat
	s.OverrideTurn(seat)
}

// OverrideTurn names the seat the engine hands the turn to once the action settles.
// The pointer is to this call's own copy, never into the rules' Extra, which could
// move under it.
func (s *State) OverrideTurn(seat int) {
	s.OverrideNextTurn = &seat
}

// SeatAt wraps i onto a table of n seats, negative offsets included, so a
// counter-clockwise step never needs its own +n.
func SeatAt(i, n int) int {
	if n <= 0 {
		return 0
	}
	return ((i % n) + n) % n
}

// NextSeat is the first seat clockwise after from that ok accepts, trying from itself
// last, or -1 when none does.
func NextSeat(from, n int, ok func(seat int) bool) int {
	for step := 1; step <= n; step++ {
		if seat := SeatAt(from+step, n); ok(seat) {
			return seat
		}
	}
	return -1
}

var (
	errHandInPlay = errors.New("the hand is still being played")
	errMatchOver  = errors.New("the match is over")
)

// ValidateNextHand is the check every multi-hand game runs on a request to deal the
// next hand, so the three of them refuse it in the same words.
func ValidateNextHand(handOver, matchOver bool) error {
	if !handOver {
		return errHandInPlay
	}
	if matchOver {
		return errMatchOver
	}
	return nil
}
