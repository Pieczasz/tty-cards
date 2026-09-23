package gameview

import (
	"slices"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
)

// BaseState is the engine state every game view renders from.
type BaseState struct {
	Phase      game.Phase
	MyTurn     bool
	Hand       []deck.Card
	TopDiscard deck.Card
	// Seats is every player in engine seat order, hero included. Opponents drops the
	// hero and runs clockwise from the hero's left - the seat that acts next comes
	// first - which is the order SplitZones lays the table out in.
	Seats     []game.PlayerSnapshot
	Opponents []game.PlayerSnapshot
	DeckSize  int
	// CurrentPlayerName is for rendering only; decide turns on CurrentPlayerID, since
	// display names are not unique.
	CurrentPlayerName string
	CurrentPlayerID   string
	WinnerName        string
	// TurnRemaining is time left before the engine plays for them; zero means no clock.
	TurnRemaining time.Duration
}

// syncBaseState builds a redacted view via BoundEngine (own hand only), handing the
// live *State to fn in the same lock hold. Identity comes from bound.PlayerID,
// the authenticated session's player, never from a display name.
func syncBaseState(bound *game.BoundEngine, fn func(*game.State)) BaseState {
	var base BaseState
	if bound == nil {
		return base
	}

	snap, hand, remaining := bound.Frame(fn)
	base.Phase = snap.Phase
	base.TopDiscard = snap.TopDiscard
	base.CurrentPlayerName = snap.CurrentPlayerName
	base.CurrentPlayerID = snap.CurrentPlayerID
	base.WinnerName = snap.WinnerName
	base.Hand = hand
	base.TurnRemaining = remaining
	base.Seats = snap.Players
	base.DeckSize = snap.DeckSize

	// MyTurn comes off the same Frame as CurrentPlayerID: seats highlight from the
	// latter while the hand and clock light up from the former, so reads that straddled
	// a turn change put the highlight on one seat and the controls on another.
	heroID := bound.PlayerID()
	base.MyTurn = base.Phase == game.Playing && heroID != "" && snap.CurrentPlayerID == heroID

	base.Opponents = opponentsFrom(snap.Players, heroID)

	return base
}

// opponentsFrom is seats without the hero, rotated to start on the hero's left. A
// session with no seat at the table sees the seats in engine order.
func opponentsFrom(seats []game.PlayerSnapshot, heroID string) []game.PlayerSnapshot {
	hero := slices.IndexFunc(seats, func(p game.PlayerSnapshot) bool { return p.ID == heroID })
	if hero < 0 {
		return slices.Clone(seats)
	}
	return slices.Concat(seats[hero+1:], seats[:hero])
}

// SeatNames maps player ID to display name; Name falls back to the ID itself.
func (b BaseState) SeatNames() map[string]string {
	names := make(map[string]string, len(b.Seats))
	for _, seat := range b.Seats {
		names[seat.ID] = seat.Name
	}
	return names
}

func (b BaseState) SeatOrder() []string {
	order := make([]string, len(b.Seats))
	for i, seat := range b.Seats {
		order[i] = seat.ID
	}
	return order
}
