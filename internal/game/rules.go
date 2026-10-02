package game

import (
	"cmp"
	"errors"
	"slices"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
)

var (
	// ErrUnknownAction refuses a move the rules have no case for: another game's
	// action, or client garbage.
	ErrUnknownAction = errors.New("unknown action")
	// ErrHandOver refuses a move between hands, after one is scored and before the
	// next is dealt.
	ErrHandOver = errors.New("the hand is over")
)

// Action is a move a player submits. Each rules set defines its own; Name is what logs
// and metrics carry.
type Action interface {
	Name() string
}

// Rules is one card game, driven by the Engine. Every method runs with the engine lock
// held and receives only *State, so it may mutate the state freely and can never call
// back into the Engine.
type Rules interface {
	MinPlayers() int
	MaxPlayers() int

	InitialDeck() []deck.Card
	InitialDealCount() int

	OnGameStart(state *State) error

	ValidateAction(state *State, action Action) error
	AfterAction(state *State, action Action) error
	// ApplyAction mutates state for an action ValidateAction accepted. An error means
	// the state may be half-applied and the engine finishes the game, so anything
	// checkable up front belongs in ValidateAction.
	ApplyAction(state *State, action Action) error
	CheckWinCondition(state *State) bool
	Standings(state *State) []*Player
}

// TurnTimeoutHandler opts a rules set into the turn clock. TimeoutAction is the move
// played for a seat whose turn ran out; it must be one ValidateAction accepts, and nil
// means there is none, which takes the seat. Rules without it get no clock at all.
type TurnTimeoutHandler interface {
	TimeoutAction(state *State) Action
}

// TurnDurationHandler stretches a particular turn. Zero keeps the engine's default,
// and no value can resurrect a clock WithTurnTimeout disabled.
type TurnDurationHandler interface {
	TurnDuration(state *State) time.Duration
}

// PlayerLeaveHandler lets a rules set settle a seat that leaves mid-game.
// OnPlayerLeave runs while the seat is still in State.Players; AfterPlayerRemoved runs
// once it is gone and the later seats have shifted down, with the index it had.
type PlayerLeaveHandler interface {
	OnPlayerLeave(state *State, playerID string)
	AfterPlayerRemoved(state *State, removedIndex int)
}

// StandingScorer reports the value Standings ordered a player by, so
// Engine.Standings can turn a tie into equal finishing places. Without it a
// draw is split by slice position and the seat that sorted first takes rating off the
// other.
//
// Only equality is read, so sign and direction do not matter. A rules set whose
// ordering is already total can skip this.
type StandingScorer interface {
	StandingScore(state *State, p *Player) int
}

// AnyScoreAtLeast is the match-target check for games played to a score.
func AnyScoreAtLeast(scores map[string]int, target int) bool {
	for _, score := range scores {
		if score >= target {
			return true
		}
	}
	return false
}

// StandingsByScore orders players by score ascending, ties stable by seat order, so a
// rules set's Standings and StandingScore cannot disagree. Negate the score to sort
// descending.
func StandingsByScore(players []*Player, score func(*Player) int) []*Player {
	standings := slices.Clone(players)
	slices.SortStableFunc(standings, func(a, b *Player) int {
		return cmp.Compare(score(a), score(b))
	})
	return standings
}
