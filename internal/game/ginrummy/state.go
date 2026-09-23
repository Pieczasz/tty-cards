package ginrummy

import (
	"slices"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
)

type Phase uint8

const (
	AwaitingDraw Phase = iota
	AwaitingDiscard
	HandOver
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
	maxHands        = 50
	handOverTimeout = time.Minute
)

type State struct {
	HandPhase Phase
	// TakenUpcard is the card drawn from the discard pile this turn, which may not
	// be discarded straight back. Without it two players can trade the same upcard
	// forever: discard draws never touch the stock, so the wall never arrives and
	// the hand has no termination path. Nil after a stock draw or once the turn ends.
	TakenUpcard *deck.Card

	// FirstActor is the seat (0 or 1) that acted first in the current hand.
	// Alternates every hand. Used to park the cursor between hands.
	FirstActor int

	HandNumber int

	// TurnsThisHand counts completed draw-and-discard turns, bounded by maxHandTurns.
	TurnsThisHand int

	// CumulativeScores: higher is better. Standings sorts descending.
	CumulativeScores map[string]int

	HandComplete  bool
	MatchComplete bool

	// LastHandResult: settle-up summary for the hand that just ended.
	LastHandResult *HandResult
}

// HandResult is the settle-up summary shown between hands.
type HandResult struct {
	KnockerMelds          [][]deck.Card
	KnockerDeadwood       []deck.Card
	KnockerDeadwoodPoints int

	OpponentMelds          [][]deck.Card
	OpponentDeadwood       []deck.Card
	OpponentDeadwoodPoints int
	LaidOffCards           []deck.Card

	Gin        bool
	Undercut   bool
	Wall       bool
	ScoreDelta int
	Winner     string // player ID credited; empty on Wall
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
