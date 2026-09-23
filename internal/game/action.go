package game

import "github.com/Pieczasz/terminal-card/internal/deck"

type Action interface {
	Name() string
}

type Event struct {
	Type     EventType
	PlayerID string
	// Reason qualifies EventGameEnded; every other event leaves it zero.
	Reason EndReason
}

type EventType uint8

const (
	EventUnknown EventType = iota
	EventTurnAdvanced
	EventActionApplied
	EventGameEnded
	EventGameStarted
	EventTurnTimedOut
	EventPlayerIdle
	EventPlayerLeft
)

// EndReason says why a game ended, so an observer can tell a win from a table the
// rules broke or everyone walked out on - the three look identical from the event
// type alone.
type EndReason uint8

const (
	EndReasonUnknown EndReason = iota
	EndReasonWin
	EndReasonRulesError
	// EndReasonForfeit is last-player-standing: everyone else left mid-game.
	EndReasonForfeit
	// EndReasonAbandoned is a table every seat left.
	EndReasonAbandoned
	// EndReasonInterrupted is a match that one seat's leave ended early for everyone
	// else (Hearts cannot continue three-handed). The seats still playing are not
	// rated on a result nobody finished; the leaver still takes the loss, or quitting
	// a losing match would be free.
	EndReasonInterrupted
)

type PlayerSnapshot struct {
	ID       string
	Username string
	HandSize int
}

type StateSnapshot struct {
	Phase Phase
	// CurrentPlayer is a display name; CurrentPlayerID is what identifies the seat.
	// Two players can share a name, so never decide whose turn it is from the former.
	CurrentPlayer   string
	CurrentPlayerID string
	TopDiscard      deck.Card
	DeckSize        int
	Players         []PlayerSnapshot
	Winner          string
}
