package game

import (
	"runtime/debug"
	"time"
)

const (
	// DefaultTurnTimeout is a turn's length unless WithTurnTimeout or a
	// TurnDurationHandler says otherwise.
	DefaultTurnTimeout = 30 * time.Second
	// MaxMissedTurns is how many turns in a row a seat may miss before it is taken.
	MaxMissedTurns = 3
	// minTurnRemaining is the least a seat keeping the turn is left with, so a turn
	// that carries on (gin's draw then discard, an auto-play, a leave elsewhere) never
	// lands on a clock already at zero.
	minTurnRemaining = 10 * time.Second
)

// armTurnTimerLocked (re)starts the clock for the seat on turn. The same seat with the
// same turn length is the same turn carrying on - gin's draw then discard, a re-armed
// auto-play, somebody else leaving - and keeps its running deadline, floored at
// minTurnRemaining. Anything else is a fresh turn with the full length, and only a
// fresh turn can be charged a new miss.
func (e *Engine) armTurnTimerLocked() {
	prevDeadline, prevPlayer, prevLength := e.clock.deadline, e.clock.playerID, e.clock.length
	e.stopTurnTimerLocked()

	if e.closed || e.clock.defaultLength <= 0 || e.state.Phase != Playing || len(e.state.Players) == 0 {
		return
	}

	timeout := e.clock.defaultLength
	if h, ok := e.state.Rules.(TurnDurationHandler); ok {
		if override := h.TurnDuration(e.state); override > 0 {
			timeout = override
		}
	}

	playerID := ""
	if current := e.currentPlayerLocked(); current != nil {
		playerID = current.ID
	}
	wait := timeout
	if !prevDeadline.IsZero() && playerID == prevPlayer && timeout == prevLength {
		wait = max(time.Until(prevDeadline), min(minTurnRemaining, timeout))
	} else {
		e.clock.missCharged = false
	}
	e.clock.playerID, e.clock.length = playerID, timeout

	seq := e.clock.seq
	e.clock.deadline = time.Now().Add(wait)
	e.clock.timer = time.AfterFunc(wait, func() { e.onTurnTimeout(seq) })
}

func (e *Engine) stopTurnTimerLocked() {
	e.clock.seq++
	if e.clock.timer != nil {
		e.clock.timer.Stop()
		e.clock.timer = nil
	}
	e.clock.deadline = time.Time{}
}

// onTurnTimeout is the timer's callback. A seat taken for idling is reported to
// WithOnIdleRemoved only here, once the engine lock is released.
func (e *Engine) onTurnTimeout(seq uint64) {
	idle := e.expireTurn(seq)
	if idle == "" || e.onIdleRemoved == nil {
		return
	}
	// A time.AfterFunc goroutine, and the hook is the owner's code, run with no lock
	// held: a panic in it must not take the process down for every table.
	defer func() {
		if r := recover(); r != nil {
			e.state.Logger().Error("idle-removed hook panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	e.onIdleRemoved(idle)
}

// expireTurn settles an expired turn in one lock hold: it charges the miss, then
// plays the rules' safe move for the seat or, once the seat has missed MaxMissedTurns
// in a row, takes it. Nothing is decided under one hold and acted on under another,
// so a player's own move either lands before this takes the lock - and moved the
// generation on - or after it has settled the turn. It returns the seat it took for
// idling, or "".
func (e *Engine) expireTurn(seq uint64) (idle string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// This is a time.AfterFunc goroutine: nothing above it recovers, so a panic in a
	// rules hook here would take the whole process down: every table, for one game's
	// defect. Deferred after the Lock, it runs before the unlock, as in SubmitAction.
	defer func() {
		if r := recover(); r != nil {
			e.endOnRulesPanicLocked(r)
		}
	}()

	// A fired AfterFunc cannot be stopped: one that waited on the lock while the turn
	// moved on, the table ended or the engine closed finds a newer generation.
	if seq != e.clock.seq || e.state.Phase != Playing {
		return ""
	}
	current := e.currentPlayerLocked()
	if current == nil {
		return ""
	}

	// One miss per seat-turn, not per expiry: a turn that carries on after an
	// auto-play (gin's draw, then its discard) is still the one turn missed.
	if !e.clock.missCharged {
		e.clock.missed[current.ID]++
		e.clock.missCharged = true
	}
	if e.clock.missed[current.ID] >= MaxMissedTurns {
		return e.takeIdleSeatLocked(current.ID)
	}
	return e.autoPlayLocked(current)
}

// autoPlayLocked plays the rules' safe move for current, whose turn just expired, and
// returns current's ID if it took the seat instead. Caller holds e.mu.
func (e *Engine) autoPlayLocked(current *Player) (idle string) {
	safe := e.state.Rules.TimeoutAction(e.state)
	if safe == nil {
		// No safe move: the seat goes rather than the table waiting. Reaching this in
		// real play is a rules bug, hence the log.
		e.state.Logger().Warn("rules returned no safe timeout move; taking the seat",
			"player_id", current.ID, "phase", e.state.Phase)
		e.clock.missed[current.ID] = MaxMissedTurns
		return e.takeIdleSeatLocked(current.ID)
	}
	e.broadcaster.Broadcast(Event{Type: EventTurnTimedOut, PlayerID: current.ID})

	// The miss count is left alone: this timeout is already counted, and clearing it
	// would mean somebody who never comes back is never removed. The generation fence
	// already proved the table live and current on turn, so only the rules can refuse.
	if err := e.state.Rules.ValidateAction(e.state, safe); err != nil {
		// TimeoutAction returned a move ValidateAction refuses: a rules bug. The
		// re-armed turn is chargeable again, or a rules set that always refuses would
		// never lose the seat.
		e.state.Logger().Warn("auto-play for an expired turn was refused",
			"error", err, "player_id", current.ID, "action", safe.Name())
		e.clock.missCharged = false
		e.armTurnTimerLocked()
		return ""
	}
	if err := e.applyActionLocked(current, safe); err != nil {
		e.state.Logger().Error("auto-play ended the table on a rules error",
			"error", err, "player_id", current.ID, "action", safe.Name())
	}
	return ""
}

// takeIdleSeatLocked removes a seat that stopped playing and returns its ID. Caller
// holds e.mu.
func (e *Engine) takeIdleSeatLocked(playerID string) string {
	// EventPlayerIdle ends the player's ssh session through the view, so this is the
	// only server-side record that a seat was taken for idling.
	e.state.Logger().Info("removing idle player",
		"player_id", playerID, "missed_turns", e.clock.missed[playerID])
	e.broadcaster.Broadcast(Event{Type: EventPlayerIdle, PlayerID: playerID})
	e.removePlayerLocked(playerID)
	return playerID
}

// endOnRulesPanicLocked is the shared body of every recover. It ends the table without
// asking the rules anything: the state a hook panicked on cannot be trusted to rank a
// winner, and a second panic from Standings inside a recover would take the process
// down. It does not check for Playing: a panic inside finishGameLocked's Standings call
// has already set Finished without announcing it, and every rules hook runs only on a
// table that was Playing. Nor for closed: a leave can still reach the rules on a
// closed engine, and the reason has to be recorded even though the broadcast is lost.
func (e *Engine) endOnRulesPanicLocked(r any) {
	e.state.Logger().Error("rules panicked; ending the table as a rules error",
		"panic", r, "stack", string(debug.Stack()))
	e.endGameLocked(nil, EndReasonRulesError)
}
