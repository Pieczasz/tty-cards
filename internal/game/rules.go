package game

import (
	"cmp"
	"errors"
	"slices"
	"time"
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
//
// OnGameStart sets a table up, the deal included: the engine hands it the seats and
// the first seat on turn, and nothing else.
type Rules interface {
	OnGameStart(state *State) error

	ValidateAction(state *State, action Action) error
	// ApplyAction plays an action ValidateAction accepted, the whole move: an error
	// ends the table as a rules error.
	ApplyAction(state *State, action Action) error
	CheckWinCondition(state *State) bool
	// Standings ranks everyone who sat down, best first, each with a 1-based Place
	// that players who drew share. The leavers come after every seated player and
	// never share a place with one (RankByScore does both); Left is the engine's to
	// fill in.
	Standings(state *State) []Standing

	// TimeoutAction is the move played for a seat whose turn ran out; it must be one
	// ValidateAction accepts, and nil means there is none, which takes the seat. It
	// is not optional: a table without it would freeze on a player who walked away.
	// WithTurnTimeout(0) is how a table goes without a clock.
	TimeoutAction(state *State) Action
}

// TurnDurationHandler stretches a particular turn. Zero keeps the engine's default,
// and no value can resurrect a clock WithTurnTimeout disabled.
type TurnDurationHandler interface {
	TurnDuration(state *State) time.Duration
}

// PlayerLeaveHandler lets a rules set settle a seat that leaves mid-game.
// OnPlayerLeave runs while the seat is still in State.Players; AfterPlayerRemoved runs
// once it is gone and the later seats have shifted down, with the index it had, and
// says what the leave did to the match. Rules without it are LeaveSettled.
type PlayerLeaveHandler interface {
	OnPlayerLeave(state *State, playerID string)
	AfterPlayerRemoved(state *State, removedIndex int) LeaveOutcome
}

// LeaveOutcome is what a leave did to the match, so OverrideNextTurn only ever means
// who acts next.
type LeaveOutcome uint8

// The ways a leave can leave a match.
const (
	// LeaveSettled leaves the rest to the engine: a win, a forfeit to a lone
	// remaining seat, or the next turn.
	LeaveSettled LeaveOutcome = iota
	// LeaveKeepsHandOpen is a lone remaining seat that still has a hand to play out -
	// poker's all-in leaver still contests the pot - so it does not win by forfeit.
	LeaveKeepsHandOpen
	// LeaveInterruptsMatch is a match that cannot go on without the seat, which the
	// seats still playing did not finish: it ends EndReasonInterrupted, and finalize
	// charges only the leavers.
	LeaveInterruptsMatch
)

// Standing is one player's finishing position.
type Standing struct {
	// Player is the seat. From Engine.Standings it is a copy of the seat's identity -
	// ID, UserID and Name - and nothing the table still writes, so it is safe to read
	// once the engine lock is gone.
	Player *Player
	// Place is 1-based. Players who drew share one.
	Place int
	// Left marks a seat that walked out before the end; it places below every seat
	// still at the table.
	Left bool
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

// RankByScore is Standings for a game ranked on one number, lowest first; negate the
// score to rank highest first. Equal scores share a place and keep seat order. The
// leavers rank below every seated player, the last to leave first on a tie, and share
// a place only among themselves: a leaver's score was measured against a table they
// left, so a tie with a seated player would turn a rage-quit into a rated draw, while
// splitting two leavers would move Elo between people who both quit.
func RankByScore(state *State, score func(*Player) int) []Standing {
	left := slices.Clone(state.LeftPlayers)
	slices.Reverse(left)
	seated := rankGroup(slices.Clone(state.Players), score, 1)
	return append(seated, rankGroup(left, score, len(seated)+1)...)
}

// rankGroup sorts players by score and places them from first on.
func rankGroup(players []*Player, score func(*Player) int, first int) []Standing {
	slices.SortStableFunc(players, func(a, b *Player) int { return cmp.Compare(score(a), score(b)) })
	out := make([]Standing, len(players))
	for i, p := range players {
		out[i] = Standing{Player: p, Place: first + i}
		if i > 0 && score(p) == score(players[i-1]) {
			out[i].Place = out[i-1].Place
		}
	}
	return out
}
