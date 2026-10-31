// Package poker is No-Limit Texas Hold'em played as a match of HandsPerMatch hands:
// chips carry across hands, side pots are cut for every short all-in, and the biggest
// stack when the hands run out wins.
package poker

import (
	"errors"
	"fmt"
	"time"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
)

// The stack every player sits down with and the blinds, which stay fixed for the
// whole match.
const (
	DefaultStack      uint = 1000
	DefaultSmallBlind uint = 25
	DefaultBigBlind   uint = 50
	// HandsPerMatch is how many hands a match runs for. Chips carry across them
	// and the biggest stack at the end wins, so one cold hand costs a player
	// position rather than the whole match.
	HandsPerMatch = 10
)

// The seat range a poker table deals for.
const (
	MinPlayers = 2
	MaxPlayers = 9
)

// Rules implements No-Limit Texas Hold'em over a HandsPerMatch-hand match.
type Rules struct{}

var (
	_ game.Rules               = (*Rules)(nil)
	_ game.PlayerLeaveHandler  = (*Rules)(nil)
	_ game.TurnDurationHandler = (*Rules)(nil)
)

// TimeoutAction never risks chips on an absent player's behalf: it checks when that
// is free and folds when it is not, which is what every real poker client does with
// a player who has stopped responding. A call is also free when no opponent can put
// in more than the player already has out: everything past that is refunded at
// showdown, and folding would forfeit a bet the player had already covered.
//
// Between hands it deals the next one instead. The player holding the button is the
// only one who can, so an absent dealer would otherwise freeze the match for
// everyone still playing.
func (r *Rules) TimeoutAction(state *game.State) game.Action {
	extra := extraOf(state)
	if extra.HandComplete() {
		if extra.MatchComplete {
			return nil
		}
		return ActionNextHand{}
	}
	p := state.Players[state.CurrentTurn]
	if extra.ToCall(p.ID) == 0 {
		return ActionCheck{}
	}
	if largestCallableBet(state, extra, p) <= extra.Seats[p.ID].Bet {
		return ActionCall{}
	}
	return ActionFold{}
}

// TurnDuration gives the between-hands deal its own clock and leaves every betting
// turn on the engine's.
func (r *Rules) TurnDuration(state *game.State) time.Duration {
	extra := extraOf(state)
	if !extra.HandComplete() || extra.MatchComplete {
		return 0
	}
	return game.NextHandTurnDuration
}

func (r *Rules) OnGameStart(state *game.State) error {
	nPlayers := len(state.Players)
	if nPlayers == 0 {
		return errors.New("cannot start a hand with no players")
	}
	if nPlayers > MaxPlayers {
		return fmt.Errorf("poker seats at most %d players, got %d", MaxPlayers, nPlayers)
	}

	extra := &State{
		// The engine seats the first turn at random; that seat takes the button.
		DealerIndex: state.CurrentTurn,
		SmallBlind:  DefaultSmallBlind,
		BigBlind:    DefaultBigBlind,
		HandsTotal:  HandsPerMatch,
		Table:       make([]deck.Card, 0, BoardSize),
		Seats:       make(map[string]*Seat, nPlayers),
	}
	for _, p := range state.Players {
		extra.Seats[p.ID] = &Seat{Chips: DefaultStack}
	}
	state.Extra = extra

	return beginHandOrFinish(state, extra, extra.DealerIndex)
}

func (r *Rules) CheckWinCondition(state *game.State) bool {
	return extraOf(state).MatchComplete
}

func (r *Rules) Standings(state *game.State) []game.Standing {
	return rankPlayers(state, extraOf(state))
}

// ActionFold gives up the hand and everything already committed to it.
type ActionFold struct{}

func (a ActionFold) Name() string { return "poker.Fold" }

// ActionCheck passes the action on when there is nothing to call.
type ActionCheck struct{}

func (a ActionCheck) Name() string { return "poker.Check" }

// ActionCall matches the current bet, or goes all-in for less on a short stack.
type ActionCall struct{}

func (a ActionCall) Name() string { return "poker.Call" }

// ActionRaiseTo raises the player's street bet to Amount, a total rather than an
// increment; RaiseBounds is the band ValidateAction accepts.
type ActionRaiseTo struct {
	Amount uint
}

func (a ActionRaiseTo) Name() string { return "poker.RaiseTo" }

// ActionAllIn commits the player's whole stack.
type ActionAllIn struct{}

func (a ActionAllIn) Name() string { return "poker.AllIn" }

// ActionNextHand deals the next hand of the match. Only the incoming dealer, who
// holds the turn while the result screen is up, may submit it.
type ActionNextHand struct{}

func (a ActionNextHand) Name() string { return "poker.NextHand" }

func (r *Rules) ValidateAction(state *game.State, action game.Action) error {
	extra := extraOf(state)
	switch action.(type) {
	case ActionNextHand:
		//nolint:wrapcheck // player-facing prose; the engine already prefixes it
		return game.ValidateNextHand(extra.HandComplete(), extra.MatchComplete)
	case ActionFold, ActionCheck, ActionCall, ActionRaiseTo, ActionAllIn:
		if extra.HandComplete() {
			return game.ErrHandOver
		}
		return validateBet(state, extra, action)
	default:
		return game.ErrUnknownAction
	}
}

// validateBet checks a betting action from the seat on turn in a live hand.
func validateBet(state *game.State, extra *State, action game.Action) error {
	p := state.Players[state.CurrentTurn]
	seat := extra.Seats[p.ID]
	if seat.Folded || seat.AllIn {
		return errors.New("player cannot act")
	}

	toCall := extra.ToCall(p.ID)

	switch action := action.(type) {
	case ActionCheck:
		if toCall > 0 {
			return errors.New("cannot check, must call or raise")
		}
	case ActionCall:
		if toCall == 0 {
			return errors.New("nothing to call")
		}
	case ActionRaiseTo:
		return validateRaiseTo(state, extra, p, action.Amount)
	case ActionAllIn:
		if seat.Chips == 0 {
			return errors.New("no chips to go all-in")
		}
		// A shove that lands above the current bet is a raise, and a player who is
		// only owed the difference from a sub-minimum all-in has no raise to make.
		if seat.Bet+seat.Chips > extra.CurrentBet {
			return checkBettingReopened(extra, seat)
		}
	}
	// A fold is always open to a seat that can act.
	return nil
}

// ApplyAction plays the move and then settles the street around it - the next actor,
// the next street or the hand's end - or deals the next hand.
func (r *Rules) ApplyAction(state *game.State, action game.Action) error {
	extra := extraOf(state)
	if _, isNextHand := action.(ActionNextHand); isNextHand {
		return beginHandOrFinish(state, extra, nextFundedSeat(state, extra, extra.DealerIndex))
	}
	if err := applyBet(state, extra, action); err != nil {
		return err
	}
	return afterBettingAction(state, extra)
}

// applyBet moves what a betting action commits, before the street is settled around
// it.
func applyBet(state *game.State, extra *State, action game.Action) error {
	p := state.Players[state.CurrentTurn]
	seat := extra.Seats[p.ID]

	switch action := action.(type) {
	case ActionCheck:
	case ActionFold:
		seat.Folded = true
	case ActionCall:
		extra.commitTo(seat, extra.CurrentBet)
	case ActionRaiseTo:
		extra.commitTo(seat, action.Amount)
		applyBetIncrease(state, extra, p, seat.Bet)
	case ActionAllIn:
		newBet := seat.Bet + seat.Chips
		wasRaise := newBet > extra.CurrentBet
		extra.commitTo(seat, newBet)
		if wasRaise {
			applyBetIncrease(state, extra, p, seat.Bet)
		}
	default:
		return game.ErrUnknownAction
	}
	seat.Acted = true
	seat.LastBetLevel = extra.CurrentBet
	return nil
}
