package ginrummy

import (
	"slices"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
)

// Phase is where the turn, or the hand, is. The zero value is PhaseAwaitingDraw, the
// phase every hand deals into.
type Phase uint8

// The phases of a gin rummy hand.
const (
	PhaseAwaitingDraw Phase = iota
	PhaseAwaitingDiscard
	PhaseHandOver
)

const (
	knockThreshold = 10
	ginBonus       = 25
	undercutBonus  = 25
	targetScore    = 100
	wallStockSize  = 2
	dealCount      = 10
	maxHandTurns   = 100
	// maxHands bounds the match. Two players who never knock never score, so the
	// target alone is not a termination condition: a table that walls every hand
	// redeals forever. At the cap the standings settle on the totals as they are.
	maxHands = 50

	// A meld is three or four of a rank, or a run of three or more in one suit.
	minMeldSize = 3
	maxSetSize  = 4
)

// State is the Gin Rummy match state stored in game.State.Extra.
type State struct {
	Phase Phase
	// TakenUpcard is the card drawn from the discard pile this turn, which may not
	// be discarded straight back. Without it two players can trade the same upcard
	// forever: discard draws never touch the stock, so the wall never arrives and
	// the hand has no termination path. Nil after a stock draw or once the turn ends.
	TakenUpcard *deck.Card

	// FirstActor is the seat (0 or 1) that acts first in the hand being played. It
	// alternates every hand, and closeHand flips it as the hand ends, so during
	// PhaseHandOver it already names the next hand's first actor, the seat
	// ActionNextHand is expected from. A hand that ends the match leaves it unflipped.
	FirstActor int

	HandNumber int

	// TurnsThisHand counts completed draw-and-discard turns, bounded by maxHandTurns.
	TurnsThisHand int

	// CumulativeScores: higher is better. Standings sorts descending.
	CumulativeScores map[string]int

	MatchComplete bool

	// LastHandResult: settle-up summary for the hand that just ended.
	LastHandResult *HandResult
}

// HandComplete is derived from Phase rather than kept beside it, so the two cannot
// disagree about whether the hand is over.
func (s *State) HandComplete() bool { return s.Phase == PhaseHandOver }

// Outcome is how a hand ended. The zero value is a result nobody has settled.
type Outcome uint8

// The ways a gin rummy hand ends.
const (
	OutcomeUnknown Outcome = iota
	// OutcomeKnock is a knock the defender could not undercut.
	OutcomeKnock
	// OutcomeGin is a knock on no deadwood: the bonus, and no layoffs.
	OutcomeGin
	// OutcomeUndercut is a knock the defender matched or beat after laying off.
	OutcomeUndercut
	// OutcomeWall is a hand nobody knocked in before the stock ran down.
	OutcomeWall
)

// String is the outcome's stable label, the one the logs carry.
func (o Outcome) String() string {
	switch o {
	case OutcomeKnock:
		return "knock"
	case OutcomeGin:
		return "gin"
	case OutcomeUndercut:
		return "undercut"
	case OutcomeWall:
		return "wall"
	case OutcomeUnknown:
	}
	return "unknown"
}

// HandResult is the settle-up summary shown between hands.
type HandResult struct {
	Outcome Outcome

	KnockerMelds          [][]deck.Card
	KnockerDeadwood       []deck.Card
	KnockerDeadwoodPoints int

	OpponentMelds          [][]deck.Card
	OpponentDeadwood       []deck.Card
	OpponentDeadwoodPoints int
	LaidOffCards           []deck.Card

	ScoreDelta int
	Winner     string // player ID credited; empty on a wall

	// WallCause is why a hand walled, "stock exhausted" or "turn limit"; empty on
	// any other outcome. A hand traded on the upcard ends with the stock still full,
	// so a screen that always blamed the stock told the player something false.
	WallCause string
}

// Clone deep-copies the result so a view can keep reading it after releasing the
// engine lock. Every other field a view lifts out of State is copied on the way out;
// this one carries slices, so handing the pointer over would be the odd one out.
func (h *HandResult) Clone() *HandResult {
	if h == nil {
		return nil
	}
	out := *h
	out.KnockerMelds = cloneMelds(h.KnockerMelds)
	out.OpponentMelds = cloneMelds(h.OpponentMelds)
	out.KnockerDeadwood = slices.Clone(h.KnockerDeadwood)
	out.OpponentDeadwood = slices.Clone(h.OpponentDeadwood)
	out.LaidOffCards = slices.Clone(h.LaidOffCards)
	return &out
}

func cloneMelds(melds [][]deck.Card) [][]deck.Card {
	if melds == nil {
		return nil
	}
	out := make([][]deck.Card, len(melds))
	for i, meld := range melds {
		out[i] = slices.Clone(meld)
	}
	return out
}

// extraOf is the table's Extra, which OnGameStart put there. Anything else is a wiring bug.
func extraOf(state *game.State) *State {
	return state.Extra.(*State) //nolint:forcetypeassert // wiring bug
}
