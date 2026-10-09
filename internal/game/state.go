package game

import (
	"log/slog"
	"slices"

	"github.com/Pieczasz/tty-cards/internal/deck"
)

type State struct {
	Players          []*Player
	LeftPlayers      []*Player
	CurrentTurn      int
	OverrideNextTurn *int
	Phase            Phase
	Winner           *Player
	Interrupted      bool
	Deck             *deck.Pile
	Discard          *deck.Pile
	Rules            Rules
	Extra            any
	// logger is unexported so the rules can read it but never swap it; the engine
	// sets it from WithLogger.
	logger *slog.Logger
}

// Logger is the table's logger, carrying which lobby and game it is, so a rules line
// such as "hand 7" names the one table it is about. A State built without an engine
// logs to slog.Default().
func (s *State) Logger() *slog.Logger {
	if s.logger == nil {
		return slog.Default()
	}
	return s.logger
}

// Phase is where a table is in its life: dealt yet, in play, or over.
type Phase uint8

// The phases of a table's life.
const (
	Waiting Phase = iota
	Playing
	Finished
)

// NewState is a Waiting table of players over a stock of cards, the stock in the
// order given.
func NewState(rules Rules, players []*Player, cards []deck.Card) *State {
	return &State{
		Players: slices.Clone(players),
		Deck:    deck.New(cards),
		Rules:   rules,
	}
}

// PlayerSnapshot is one seat as a view may show it: a name and a card count, never the
// cards.
type PlayerSnapshot struct {
	ID       string
	Name     string
	HandSize int
}

// StateSnapshot is the public face of a table at one moment, copied out under the
// engine lock so it can be read after the lock is gone.
type StateSnapshot struct {
	Phase             Phase
	CurrentPlayerName string
	CurrentPlayerID   string
	TopDiscard        deck.Card
	DeckSize          int
	Players           []PlayerSnapshot
	WinnerName        string
	EndReason         EndReason
}
