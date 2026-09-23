package poker

import (
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
)

// Pot is a main or side pot with players eligible to win it.
type Pot struct {
	Amount   uint
	Eligible []string // player IDs
}

type State struct {
	DealerIndex int
	SBIndex     int
	BBIndex     int

	MainPool   uint
	CurrentBet uint
	MinRaise   uint
	SmallBlind uint
	BigBlind   uint

	Phase RoundPhase

	Folded       map[string]bool
	PlayersAllIn map[string]bool

	Table []deck.Card

	PlayerChips      map[string]uint
	PlayerBets       map[string]uint // chips committed this street
	TotalContributed map[string]uint // chips committed this hand (for side pots)
	ActedThisRound   map[string]bool
	// LastBetLevel is the CurrentBet a player last acted on this street. Several
	// short all-ins that together raise it by a full MinRaise reopen the betting for
	// that player, which no single one of them does.
	LastBetLevel map[string]uint

	Pots         []Pot
	HandComplete bool
	Winners      []*game.Player
	// ReachedShowdown is true only when the hand was actually shown down. A pot
	// nobody contested is won face-down, so the winner's cards must stay hidden -
	// the match has more hands to play and the table would be reading them.
	ReachedShowdown bool

	// A match is HandsTotal hands long and chips carry across them, so a single
	// unlucky hand no longer ends the game. MatchComplete is what the engine
	// checks; HandComplete only pauses the table for the result screen.
	HandNumber    int
	HandsTotal    int
	MatchComplete bool
	// BustedAtHand records the hand a player ran out of chips on. Everyone who
	// busts ends on zero chips, so it is the only thing that separates them: going
	// out later is a better finish.
	BustedAtHand map[string]int

	// handStartChips is what the table held when the current hand was dealt, read
	// only by the chip-conservation tripwire in finishHand.
	handStartChips uint
}

type RoundPhase uint8

const (
	PhaseUnknown RoundPhase = iota
	PreFlop
	Flop
	Turn
	River
	Showdown
)

func (p RoundPhase) String() string {
	switch p {
	case PreFlop:
		return "PREFLOP"
	case Flop:
		return "FLOP"
	case Turn:
		return "TURN"
	case River:
		return "RIVER"
	case Showdown:
		return "SHOWDOWN"
	default:
		return "UNKNOWN"
	}
}
