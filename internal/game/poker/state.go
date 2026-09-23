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

// Seat is one player's stack and their part in the hand being played. A player who
// leaves keeps theirs: an all-in leaver still contests the pot (contenders) and every
// leaver is still ranked on the chips they walked away with (rankPlayers).
type Seat struct {
	Chips uint
	// Bet is what the player has committed this street, Contributed this hand; the
	// side pots are cut from the latter.
	Bet         uint
	Contributed uint

	Folded bool
	AllIn  bool
	// Acted is whether the player has acted since the betting was last reopened.
	Acted bool
	// LastBetLevel is the CurrentBet the player last acted on this street. Several
	// short all-ins that together raise it by a full MinRaise reopen the betting for
	// that player, which no single one of them does.
	LastBetLevel uint

	// BustedAtHand is the hand the player ran out of chips on, zero while they still
	// have some. Everyone who busts ends on zero chips, so it is the only thing that
	// separates them: going out later is a better finish.
	BustedAtHand int
}

// resetForHand clears everything a seat carries only for the hand just played.
func (s *Seat) resetForHand() {
	*s = Seat{Chips: s.Chips, BustedAtHand: s.BustedAtHand}
}

// State is the Hold'em match state stored in game.State.Extra.
type State struct {
	DealerIndex int
	SBIndex     int
	BBIndex     int

	// Pool is every chip committed this hand, main and side pots together, until the
	// showdown or a fold-out pays it out.
	Pool       uint
	CurrentBet uint
	MinRaise   uint
	SmallBlind uint
	BigBlind   uint

	Phase Phase

	// Table is the community cards dealt so far.
	Table []deck.Card

	// Seats is keyed by player ID and never loses an entry mid-match; see Seat.
	Seats map[string]*Seat

	Pots    []Pot
	Winners []*game.Player
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

	// handStartChips is what the table held when the current hand was dealt, read
	// only by the chip-conservation tripwire in finishHand.
	handStartChips uint
}

// HandComplete is derived from Phase rather than kept beside it, so the two cannot
// disagree about whether the hand is over: a hand is complete once it reaches the
// showdown, whether it was shown down or won face-down.
func (s *State) HandComplete() bool { return s.Phase == PhaseShowdown }

// ToCall is the chips playerID must add to match CurrentBet.
func (s *State) ToCall(playerID string) uint {
	var bet uint
	if seat := s.Seats[playerID]; seat != nil {
		bet = seat.Bet
	}
	if s.CurrentBet <= bet {
		return 0
	}
	return s.CurrentBet - bet
}

// Phase is the street a hand is on. The zero value is a State nobody has dealt.
type Phase uint8

const (
	PhaseUnknown Phase = iota
	PhasePreFlop
	PhaseFlop
	PhaseTurn
	PhaseRiver
	PhaseShowdown
)

// String is the street label the table view prints and the logs carry.
func (p Phase) String() string {
	switch p {
	case PhasePreFlop:
		return "PREFLOP"
	case PhaseFlop:
		return "FLOP"
	case PhaseTurn:
		return "TURN"
	case PhaseRiver:
		return "RIVER"
	case PhaseShowdown:
		return "SHOWDOWN"
	case PhaseUnknown:
	}
	return "UNKNOWN"
}
