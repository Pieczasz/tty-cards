package game

import (
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
)

// BoundEngine is a session-scoped handle: it only submits as its player and only
// returns that player's hand. Bind refuses a nil engine, so a non-nil BoundEngine
// always has one.
type BoundEngine struct {
	engine   *Engine
	playerID string
}

// Bind is playerID's handle on engine, or nil for a nil engine. Every BoundEngine
// method is safe on nil and reports that there is no game.
func Bind(engine *Engine, playerID string) *BoundEngine {
	if engine == nil {
		return nil
	}
	return &BoundEngine{engine: engine, playerID: playerID}
}

// PlayerID is the seat this handle acts as.
func (b *BoundEngine) PlayerID() string {
	if b == nil {
		return ""
	}
	return b.playerID
}

// Subscribe joins the engine's event feed. The broadcaster itself stays unreachable: a
// view holding it could Broadcast or Close the feed for the whole table.
func (b *BoundEngine) Subscribe() (<-chan Event, error) {
	if b == nil {
		return nil, errNoGame
	}
	return b.engine.Subscribe()
}

// Unsubscribe leaves the feed Subscribe joined.
func (b *BoundEngine) Unsubscribe(events <-chan Event) {
	if b == nil || events == nil {
		return
	}
	b.engine.Unsubscribe(events)
}

// Submit plays action as this handle's player.
func (b *BoundEngine) Submit(action Action) error {
	if b == nil {
		return errNoGame
	}
	return b.engine.SubmitAction(b.playerID, action)
}

// Frame reads the snapshot, this player's hand, the turn clock and (through fn, which
// may be nil) the live *State in one lock hold, so the pieces cannot describe different
// moments.
//
// The *State is unredacted and live: treat it as read-only, copy anything kept past
// the callback, and filter anything reaching the screen.
func (b *BoundEngine) Frame(fn func(*State)) (StateSnapshot, []deck.Card, time.Duration) {
	if b == nil {
		return StateSnapshot{}, nil, 0
	}
	return b.engine.Frame(b.playerID, fn)
}
