package game

// Event is one message on a table's feed. PlayerID names the seat it is about, when
// there is one: the actor, the winner, or the seat that timed out, idled or left.
type Event struct {
	Type     EventType
	PlayerID string
	Reason   EndReason
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

// EndReason says why a game ended.
type EndReason uint8

// The reasons a table ends.
const (
	EndReasonUnknown EndReason = iota
	EndReasonWin
	EndReasonRulesError
	EndReasonForfeit
	EndReasonAbandoned
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
