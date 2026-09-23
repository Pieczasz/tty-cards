package hearts

import (
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
)

// Phase is where a hand is. The zero value is PhasePassing, the phase every hand but
// a hold hand deals into.
type Phase uint8

const (
	PhasePassing Phase = iota
	PhaseTrickPlay
	PhaseHandOver
)

// PassDirection is where a hand's three passed cards go. It rotates left, right,
// across and hold, one hand each.
type PassDirection uint8

const (
	PassLeft PassDirection = iota
	PassRight
	PassAcross
	PassNone

	// passDirectionCount is the length of the rotation.
	passDirectionCount = iota
)

// DefaultTargetScore is the total that ends the match once a player reaches it.
const DefaultTargetScore = 100

const (
	playerCount  = 4
	cardsPerHand = 13
	cardsToPass  = 3
	// penaltyPointsTotal is every point in a hand: the thirteen hearts and the queen.
	penaltyPointsTotal  = cardsPerHand + queenOfSpadesPoints
	queenOfSpadesPoints = 13

	passTurnTimeout     = 45 * time.Second
	handOverTurnTimeout = time.Minute
)

var (
	twoOfClubs    = deck.Card{Rank: deck.Two, Suit: deck.Clubs}
	queenOfSpades = deck.Card{Rank: deck.Queen, Suit: deck.Spades}
)

// String names the direction for logs and the TUI's pass banner, so the two can
// never disagree about what a direction is called.
func (d PassDirection) String() string {
	switch d {
	case PassLeft:
		return "left"
	case PassRight:
		return "right"
	case PassAcross:
		return "across"
	case PassNone:
		return "hold"
	default:
		return "unknown"
	}
}

// State is the Hearts match state stored in game.State.Extra.
type State struct {
	Phase Phase

	PassDirection PassDirection
	// PendingPasses holds each seat's three cards from the moment it passes until
	// every seat has; a seat with an entry has passed.
	PendingPasses map[string][]deck.Card

	LedSuit    deck.Suit
	TrickCards map[string]deck.Card
	// TrickComplete marks TrickCards as a trick that is already won. Clearing it the
	// moment the fourth card lands would empty the table before the engine
	// broadcasts the play, so the cards stay up until somebody leads the next trick.
	TrickComplete bool
	TrickLeader   int
	HeartsBroken  bool
	TricksPlayed  int

	HandPoints       map[string]int
	CumulativeScores map[string]int
	HandNumber       int
	DealerIndex      int
	TargetScore      int
	MatchComplete    bool

	LastTrickWinner string
}

// HandComplete is derived from Phase rather than kept beside it, so the two cannot
// disagree about whether the hand is over.
func (s *State) HandComplete() bool { return s.Phase == PhaseHandOver }

// passed reports whether the seat has already handed over its three cards this hand.
func (s *State) passed(playerID string) bool {
	_, ok := s.PendingPasses[playerID]
	return ok
}

// leadingTrick reports whether the next card played opens a trick. A won trick
// still sitting on the table is not one in progress.
func (s *State) leadingTrick() bool {
	return s.TrickComplete || len(s.TrickCards) == 0
}

// startTrick sweeps a won trick off the table. It runs when the next card is
// played, which is the first moment every client has had the chance to see it.
func (s *State) startTrick() {
	if !s.TrickComplete {
		return
	}
	clear(s.TrickCards)
	s.LedSuit = deck.NoSuit
	s.TrickComplete = false
}

func resetHandState(extra *State) {
	extra.Phase = PhasePassing
	extra.PassDirection = PassLeft
	extra.PendingPasses = nil

	extra.LedSuit = deck.NoSuit
	extra.TrickCards = make(map[string]deck.Card, playerCount)
	extra.TrickComplete = false
	extra.TrickLeader = 0
	extra.HeartsBroken = false
	extra.TricksPlayed = 0
	// Fresh rather than cleared: every read is by index, so an absent seat already
	// reads zero and beginHand does not have to seed one key per player.
	extra.HandPoints = make(map[string]int, playerCount)
	extra.LastTrickWinner = ""
}
