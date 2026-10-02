package poker

import (
	"log/slog"

	"github.com/Pieczasz/terminal-card/internal/game"
)

// OnPlayerLeave folds the departing player. Turn and seat resolution runs in
// AfterPlayerRemoved, once the seats have actually shifted.
//
// An all-in player is left alone: they have no decisions left to make, so folding
// them would forfeit chips they had no way to protect. They stay in the pot they are
// committed to (see contenders) and are shown down like anybody else.
func (r *Rules) OnPlayerLeave(state *game.State, playerID string) {
	extra, ok := state.Extra.(*State)
	if !ok || extra.HandComplete() {
		return
	}
	seat := extra.Seats[playerID]
	if seat == nil || seat.AllIn {
		return
	}
	seat.Folded = true
	seat.Acted = true
}

// AfterPlayerRemoved reindexes the button/blinds and picks the next actor from
// post-removal seats, so a fold-on-disconnect never leaves a stale pre-removal
// index on turn.
func (r *Rules) AfterPlayerRemoved(state *game.State, removedIndex int) {
	extra, ok := state.Extra.(*State)
	if !ok {
		return
	}
	n := len(state.Players)
	if n == 0 {
		return
	}
	extra.DealerIndex = adjustSeatIndex(extra.DealerIndex, removedIndex, n)
	extra.SBIndex = adjustSeatIndex(extra.SBIndex, removedIndex, n)
	extra.BBIndex = adjustSeatIndex(extra.BBIndex, removedIndex, n)

	if extra.HandComplete() {
		// The player who was due to deal may be the one who just left, so the turn
		// is re-parked rather than left pointing at an empty seat.
		finishHand(state, extra)
		return
	}

	// A hand is only ever handed to a seat while two players still contest it, and
	// one leave drops that by at most one, so the pot always has a claimant here. The
	// seats have shifted, so the search for the next actor starts on the cursor itself.
	if err := resolveAfterChange(state, extra, game.SeatAt(state.CurrentTurn-1, n)); err != nil {
		// The hook cannot report it; the hand is already unwound and closed.
		slog.Error("poker cannot finish the hand after a leave",
			"hand", extra.HandNumber, "phase", extra.Phase.String(), "error", err)
	}
}

// adjustSeatIndex maps a seat marker to its new index after the player
// removed leaves. When the marker's own holder leaves, it moves back to the
// previous seat rather than silently landing on whoever shifted into the slot.
func adjustSeatIndex(seat, removed, nAfter int) int {
	if nAfter <= 0 {
		return 0
	}
	switch {
	case seat > removed:
		seat--
	case seat == removed:
		seat = game.SeatAt(removed-1, nAfter)

	}
	return min(max(seat, 0), nAfter-1)
}
