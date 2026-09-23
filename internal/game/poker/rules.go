package poker

import (
	"errors"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
)

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
	if extra.HandComplete {
		if extra.MatchComplete {
			return nil
		}
		return ActionNextHand{}
	}
	if state.CurrentTurn < 0 || state.CurrentTurn >= len(state.Players) {
		return nil
	}
	p := state.Players[state.CurrentTurn]
	if ToCall(extra, p.ID) == 0 {
		return ActionCheck{}
	}
	if largestCallableBet(state, extra, p) <= extra.PlayerBets[p.ID] {
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
	if !ok || !extra.HandComplete || extra.MatchComplete {
		return 0
	}
	return dealTurnTimeout
}

func (r *Rules) MinPlayers() int { return 2 }
func (r *Rules) MaxPlayers() int { return 9 }

func (r *Rules) InitialDeck() []deck.Card {
	return deck.StandardDeck()
}

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
		DealerIndex:      state.CurrentTurn,
		SmallBlind:       DefaultSmallBlind,
		BigBlind:         DefaultBigBlind,
		HandsTotal:       HandsPerMatch,
		Folded:           make(map[string]bool, nPlayers),
		PlayersAllIn:     make(map[string]bool, nPlayers),
		Table:            make([]deck.Card, 0, 5),
		PlayerChips:      make(map[string]uint, nPlayers),
		PlayerBets:       make(map[string]uint, nPlayers),
		TotalContributed: make(map[string]uint, nPlayers),
		ActedThisRound:   make(map[string]bool, nPlayers),
		LastBetLevel:     make(map[string]uint, nPlayers),
	}
	for _, p := range state.Players {
		extra.PlayerChips[p.ID] = DefaultStack
	}
	state.Extra = extra

	return r.beginHandOrFinish(state, extra, extra.DealerIndex)
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

type ActionFold struct{}

func (a ActionFold) Name() string { return "poker.Fold" }

type ActionCheck struct{}

func (a ActionCheck) Name() string { return "poker.Check" }

type ActionCall struct{}

func (a ActionCall) Name() string { return "poker.Call" }

type ActionRaiseTo struct {
	Amount uint
}

func (a ActionRaiseTo) Name() string { return "poker.RaiseTo" }

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
		return validateNextHand(extra)
	}
	if extra.HandComplete || extra.Phase == Showdown {
		return errors.New("hand is over")
	}

	p := state.Players[state.CurrentTurn]
	if extra.Folded[p.ID] || extra.PlayersAllIn[p.ID] {
		return errors.New("player cannot act")
	}

	toCall := ToCall(extra, p.ID)

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
		if extra.PlayerChips[p.ID] == 0 {
			return errors.New("no chips to go all-in")
		}
		// A shove that lands above the current bet is a raise, and a player who is
		// only owed the difference from a sub-minimum all-in has no raise to make.
		if extra.PlayerBets[p.ID]+extra.PlayerChips[p.ID] > extra.CurrentBet {
			return checkBettingReopened(extra, p)
		}
		return nil
	default:
		return errors.New("action not allowed in poker")
	}
}

func validateNextHand(extra *State) error {
	if !extra.HandComplete {
		return errors.New("the hand is still being played")
	}
	if extra.MatchComplete {
		return errors.New("the match is over")
	}
	return nil
}

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

	switch action := action.(type) {
	case ActionFold:
		extra.Folded[p.ID] = true
	case ActionCall:
		commitTo(extra, p, extra.CurrentBet)
	case ActionRaiseTo:
		commitTo(extra, p, action.Amount)
		applyBetIncrease(extra, state, p, extra.PlayerBets[p.ID])
	case ActionAllIn:
		newBet := extra.PlayerBets[p.ID] + extra.PlayerChips[p.ID]
		wasRaise := newBet > extra.CurrentBet
		commitTo(extra, p, newBet)
		if wasRaise {
			applyBetIncrease(extra, state, p, extra.PlayerBets[p.ID])
		}
	}
	extra.ActedThisRound[p.ID] = true
	extra.LastBetLevel[p.ID] = extra.CurrentBet
	return nil
}

func (r *Rules) AfterAction(state *game.State, action game.Action) error {
	extra, ok := state.Extra.(*State)
	if !ok {
		return game.ErrInvalidState
	}
	if _, isNextHand := action.(ActionNextHand); isNextHand {
		return r.beginHandOrFinish(state, extra, nextFundedSeat(state, extra, extra.DealerIndex))
	}
	return r.afterBettingAction(state, extra)
}
