// Package poker is No-Limit Texas Hold'em played as a match of HandsPerMatch hands:
// chips carry across hands, side pots are cut for every short all-in, and the biggest
// stack when the hands run out wins.
package poker

import (
	"errors"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
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

// Rules implements No-Limit Texas Hold'em over a HandsPerMatch-hand match.
type Rules struct{}

var (
	_ game.Rules               = (*Rules)(nil)
	_ game.PlayerLeaveHandler  = (*Rules)(nil)
	_ game.TurnTimeoutHandler  = (*Rules)(nil)
	_ game.TurnDurationHandler = (*Rules)(nil)
	// Without it, deleting StandingScore still compiles and the engine silently
	// splits every draw by seat order.
	_ game.StandingScorer = (*Rules)(nil)
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
	extra, ok := state.Extra.(*State)
	if !ok {
		return nil
	}
	if extra.HandComplete() {
		if extra.MatchComplete {
			return nil
		}
		return ActionNextHand{}
	}
	if state.CurrentTurn < 0 || state.CurrentTurn >= len(state.Players) {
		return nil
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

// dealTurnTimeout is how long the incoming dealer has to start the next hand. Dealing
// is a decision about whether to keep playing rather than a move made under pressure,
// so it gets longer than a betting turn - but it stays bounded, because an absent
// dealer is the one seat that can freeze the match for everybody else.
const dealTurnTimeout = time.Minute

// TurnTimeout gives the between-hands deal its own clock and leaves every betting
// turn on the engine's.
func (r *Rules) TurnTimeout(state *game.State) time.Duration {
	extra, ok := state.Extra.(*State)
	if !ok || !extra.HandComplete() || extra.MatchComplete {
		return 0
	}
	return dealTurnTimeout
}

func (r *Rules) MinPlayers() int { return 2 }
func (r *Rules) MaxPlayers() int { return 9 }

func (r *Rules) InitialDeck() []deck.Card { return deck.Standard() }

// InitialDealCount is zero because a match deals a fresh hand every round, not
// once at the start: beginHand owns the deal so there is a single code path for
// it, hand one included.
func (r *Rules) InitialDealCount() int {
	return 0
}

func (r *Rules) OnGameStart(state *game.State) error {
	nPlayers := len(state.Players)
	if nPlayers == 0 {
		return errors.New("cannot start a hand with no players")
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
	extra, ok := state.Extra.(*State)
	if !ok {
		return false
	}
	return extra.MatchComplete
}

func (r *Rules) Standings(state *game.State) []*game.Player {
	extra, ok := state.Extra.(*State)
	if !ok {
		return nil
	}
	return rankPlayers(state, extra)
}

// StandingScore is the group a player lands in once resultLevel stops separating
// them - two players who busted on the same hand are a draw, not places i and i+1
// split by whose ID sorts first. Only equality is read, so the group index is enough;
// chips alone would not be, since it would tie two busts from different hands.
func (r *Rules) StandingScore(state *game.State, p *game.Player) int {
	extra, ok := state.Extra.(*State)
	if !ok {
		return 0
	}
	ranked := rankPlayers(state, extra)
	level := resultLevel(state, extra)
	group := 0
	for i, q := range ranked {
		if i > 0 && level(ranked[i-1], q) != 0 {
			group++
		}
		if q.ID == p.ID {
			return group
		}
	}
	return group
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
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}
	if _, isNextHand := action.(ActionNextHand); isNextHand {
		//nolint:wrapcheck // player-facing prose; the engine already prefixes it
		return game.ValidateNextHand(extra.HandComplete(), extra.MatchComplete)
	}
	if extra.HandComplete() {
		return errHandOver
	}

	p := state.Players[state.CurrentTurn]
	seat := extra.Seats[p.ID]
	if seat.Folded || seat.AllIn {
		return errors.New("player cannot act")
	}

	toCall := extra.ToCall(p.ID)

	switch action := action.(type) {
	case ActionFold:
		return nil
	case ActionCheck:
		if toCall > 0 {
			return errors.New("cannot check, must call or raise")
		}
		return nil
	case ActionCall:
		if toCall == 0 {
			return errors.New("nothing to call")
		}
		return nil
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
		return nil
	default:
		return errUnknownAction
	}
}

var (
	errHandOver      = errors.New("the hand is over")
	errUnknownAction = errors.New("unknown action")
)

func (r *Rules) ApplyAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}
	if _, isNextHand := action.(ActionNextHand); isNextHand {
		// Dealing happens in AfterAction, the only hook that can report a bad deal.
		return nil
	}
	p := state.Players[state.CurrentTurn]
	seat := extra.Seats[p.ID]

	switch action := action.(type) {
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
	}
	seat.Acted = true
	seat.LastBetLevel = extra.CurrentBet
	return nil
}

func (r *Rules) AfterAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}
	if _, isNextHand := action.(ActionNextHand); isNextHand {
		return beginHandOrFinish(state, extra, nextFundedSeat(state, extra, extra.DealerIndex))
	}
	return afterBettingAction(state, extra)
}
