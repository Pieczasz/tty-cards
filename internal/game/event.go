package game

// Event is one message on a table's feed. PlayerID names the seat it is about, when
// there is one: the actor, the winner, or the seat that timed out, idled or left.
type Event struct {
	Type     EventType
	PlayerID string
	// Reason qualifies EventGameEnded; every other event leaves it zero.
	Reason EndReason
}

// EventType says what happened at the table.
type EventType uint8

// The events a table publishes.
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

// The reasons a table ends.
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

// String is the reason's stable label, the one metrics and logs carry.
func (r EndReason) String() string {
	switch r {
	case EndReasonWin:
		return "win"
	case EndReasonRulesError:
		return "rules_error"
	case EndReasonForfeit:
		return "forfeit"
	case EndReasonAbandoned:
		return "abandoned"
	case EndReasonInterrupted:
		return "interrupted"
	case EndReasonUnknown:
	}
	return "unknown"
}

// String is the event's stable label for logs.
func (t EventType) String() string {
	switch t {
	case EventTurnAdvanced:
		return "turn_advanced"
	case EventActionApplied:
		return "action_applied"
	case EventGameEnded:
		return "game_ended"
	case EventGameStarted:
		return "game_started"
	case EventTurnTimedOut:
		return "turn_timed_out"
	case EventPlayerIdle:
		return "player_idle"
	case EventPlayerLeft:
		return "player_left"
	case EventUnknown:
	}
	return "unknown"
}
