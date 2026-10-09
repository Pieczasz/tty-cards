package poker

import (
	"slices"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
)

// SeatView is one seat at the table as one viewer may see it: the money and markers
// everyone sees, and hole cards only where the rules turn them up.
type SeatView struct {
	PlayerID string
	Name     string
	Chips    uint
	Bet      uint
	Folded   bool
	AllIn    bool
	IsDealer bool
	IsSB     bool
	IsBB     bool
	IsTurn   bool
	// IsHero marks the viewer's own seat.
	IsHero   bool
	HandSize int
	Hole     []deck.Card
}

// SeatsFor is every seat as viewer may see it, copied out of the live state. Hole
// cards are the viewer's own, or anyone's still live once the hand is shown down;
// everyone else shows a hand size and nothing more. A pot nobody contested is won
// face-down: with hands left to play, showing those cards would hand the table a free
// read, and a last pot won face-down keeps its cards hidden too. Which cards a seat
// may see is a rule of poker, not of rendering, so it lives here rather than in a
// view.
//
// A view calls it inside Frame, which has no recover, so a state that is not poker's
// has no seats rather than a panic.
func SeatsFor(state *game.State, viewer string) []SeatView {
	extra, ok := state.Extra.(*State)
	if !ok {
		return nil
	}
	seats := make([]SeatView, 0, len(state.Players))
	for i, p := range state.Players {
		if p == nil {
			continue
		}
		var money Seat
		if ls := extra.Seats[p.ID]; ls != nil {
			money = *ls
		}
		s := SeatView{
			PlayerID: p.ID,
			Name:     p.DisplayName(),
			Chips:    money.Chips,
			Bet:      money.Bet,
			Folded:   money.Folded,
			AllIn:    money.AllIn,
			IsDealer: i == extra.DealerIndex,
			IsSB:     i == extra.SBIndex,
			IsBB:     i == extra.BBIndex,
			IsTurn:   state.Phase == game.Playing && state.CurrentTurn == i && !extra.HandComplete(),
			IsHero:   p.ID == viewer,
			HandSize: len(p.Cards),
		}
		if s.IsHero || (extra.ReachedShowdown && !s.Folded) {
			s.Hole = slices.Clone(p.Cards)
		}
		seats = append(seats, s)
	}
	return seats
}
