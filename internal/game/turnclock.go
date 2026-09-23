package game

import (
	"errors"
	"log/slog"
	"runtime/debug"
	"time"
)

const (
	DefaultTurnTimeout = 30 * time.Second
	MaxMissedTurns     = 3
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
	prevDeadline, prevPlayer, prevLength := e.turnDeadline, e.turnPlayerID, e.turnLength
	e.stopTurnTimerLocked()

	if e.closed || e.turnTimeout <= 0 || e.state.Phase != Playing || len(e.state.Players) == 0 {
		return
	}
	if _, ok := e.state.Rules.(TurnTimeoutHandler); !ok {
		return
	}

	timeout := e.turnTimeout
	if h, ok := e.state.Rules.(TurnDurationHandler); ok {
		if override := h.TurnTimeout(e.state); override > 0 {
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
		e.turnMissCharged = false
	}
	e.turnPlayerID, e.turnLength = playerID, timeout

	seq := e.turnSeq
	e.turnDeadline = time.Now().Add(wait)
	e.turnTimer = time.AfterFunc(wait, func() { e.onTurnTimeout(seq) })
}

func (e *Engine) stopTurnTimerLocked() {
	e.turnSeq++
	if e.turnTimer != nil {
		e.turnTimer.Stop()
		e.turnTimer = nil
	}
	e.turnDeadline = time.Time{}
}

// TurnDeadline is a test seam; views read the remaining time from Frame.
func (e *Engine) TurnDeadline() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.turnDeadline
}

// MissedTurns is a test seam: the idle count is the engine's own business.
func (e *Engine) MissedTurns(playerID string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.missedTurns[playerID]
}

func (e *Engine) onTurnTimeout(seq uint64) {
	// This is a time.AfterFunc goroutine: nothing above it recovers, so a panic in a
	// rules hook here would take the whole process down: every table, for one game's
	// defect. It gets the same treatment as a rules error from SubmitAction, so this
	// table ends unrated and the rest keep playing.
	defer e.recoverRulesPanic()

	playerID, action, takeSeat := e.resolveTurnTimeout(seq)
	if playerID == "" {
		return
	}

	if takeSeat {
		e.removeIfStillIdle(seq, playerID)
		return
	}

	err := e.submitTimedOutAction(playerID, action, seq)
	switch {
	case err == nil, errors.Is(err, errStaleTurn):
		// The player acted for themselves while the lock was dropped: their action
		// armed a fresh clock, so re-arming would hand the next player a double turn.
		return
	case errors.Is(err, errActionRefused):
		// TimeoutAction returned a move ValidateAction refuses: a rules bug. The clock
		// was re-armed under the lock, and each expiry still counts a miss.
		slog.Warn("auto-play for an expired turn was refused",
			"error", err, "player_id", playerID, "action", action.Name())
	default:
		slog.Error("auto-play ended the table on a rules error",
			"error", err, "player_id", playerID, "action", action.Name())
	}
}

// recoverRulesPanic is deferred by the timer goroutine. The locked helpers release
// e.mu in their own defers while the panic unwinds, so it is safe to take here.
func (e *Engine) recoverRulesPanic() {
	r := recover()
	if r == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.endOnRulesPanicLocked(r)
}

// endOnRulesPanicLocked is the shared body of both recovers. It does not check for
// Playing: a panic inside finishGameLocked's Standings call has already set Finished
// without announcing it, and every rules hook runs only on a table that was Playing.
func (e *Engine) endOnRulesPanicLocked(r any) {
	slog.Error("rules panicked; ending the table as a rules error",
		"panic", r, "stack", string(debug.Stack()))
	if !e.closed {
		e.finishAfterPanicLocked()
	}
}

// finishAfterPanicLocked ends the table without asking the rules anything: the state a
// hook panicked on cannot be trusted to rank a winner, and a second panic from
// Standings inside a recover would take the process down. finishGameLocked is the
// normal path and stays untouched.
func (e *Engine) finishAfterPanicLocked() {
	e.state.Phase = Finished
	e.stopTurnTimerLocked()
	e.broadcaster.Broadcast(Event{Type: EventGameEnded, Reason: EndReasonRulesError})
}

func (e *Engine) resolveTurnTimeout(seq uint64) (playerID string, action Action, takeSeat bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if seq != e.turnSeq || e.state.Phase != Playing || len(e.state.Players) == 0 {
		return "", nil, false
	}

	current := e.currentPlayerLocked()
	if current == nil {
		return "", nil, false
	}

	// One miss per seat-turn, not per expiry: a turn that carries on after an
	// auto-play (gin's draw, then its discard) is still the one turn missed.
	if !e.turnMissCharged {
		e.missedTurns[current.ID]++
		e.turnMissCharged = true
	}
	if e.missedTurns[current.ID] >= MaxMissedTurns {
		return current.ID, nil, true
	}

	// armTurnTimerLocked never arms without a TurnTimeoutHandler and Rules is set once
	// at construction, so a missing handler here is a wiring bug. It falls into the
	// "no safe move" path below - the seat goes, the table does not panic.
	var safe Action
	if handler, ok := e.state.Rules.(TurnTimeoutHandler); ok {
		safe = handler.TimeoutAction(e.state)
	}
	if safe == nil {
		// No safe move: the seat goes rather than the table waiting. A full set of
		// misses is what makes that re-checkable in removeIfStillIdle. Reaching this in
		// real play is a rules bug, hence the log.
		slog.Warn("rules returned no safe timeout move; taking the seat",
			"player_id", current.ID, "phase", e.state.Phase)
		e.missedTurns[current.ID] = MaxMissedTurns
		return current.ID, nil, true
	}
	// Broadcast under the same lock hold that charged the miss. Outside it, a player
	// whose action lands in the gap has the miss refunded while the "timed out" they
	// disproved still ships.
	e.broadcaster.Broadcast(Event{Type: EventTurnTimedOut, PlayerID: current.ID})
	return current.ID, safe, false
}

// removeIfStillIdle re-checks the idle decision and removes under one lock hold.
// resolveTurnTimeout had to drop the lock before calling here; without this, a player
// who SubmitAction'd in that window would still be kicked.
//
// turnSeq is the whole re-check: every path that clears a miss count settles the
// cursor too, and settling the cursor bumps turnSeq. So a sequence that still matches
// is proof the count was not cleared behind our back, and no separate count check is
// needed - a rejected action clears nothing and leaves the sequence alone, which is
// how spamming garbage fails to dodge removal.
func (e *Engine) removeIfStillIdle(seq uint64, playerID string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if seq != e.turnSeq {
		// They acted (or the table moved on) inside the window we had to drop the
		// lock for: a newer timer already owns the turn.
		return
	}
	// EventPlayerIdle ends the player's ssh session through the view, so this is the
	// only server-side record that a seat was taken for idling.
	slog.Info("removing idle player",
		"player_id", playerID, "missed_turns", e.missedTurns[playerID])
	e.broadcaster.Broadcast(Event{Type: EventPlayerIdle, PlayerID: playerID})
	e.removePlayerLocked(playerID)
}
