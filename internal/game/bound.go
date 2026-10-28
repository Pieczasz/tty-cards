package game

import (
	"time"

	"github.com/Pieczasz/tty-cards/internal/deck"
)

// BoundEngine is a session-scoped handle: it only submits as its player and only
// returns that player's hand. Bind refuses a nil engine, so a non-nil BoundEngine
// always has one.
type BoundEngine struct {
	engine   *Engine
	playerID string
}

// Bind is playerID's handle on engine, or nil for a nil engine. Only that nil is
// tolerated, and only where a lobby event may carry no engine: the router hands a view
// a non-nil handle or none at all, so BoundEngine's methods do not guard a nil receiver.
func Bind(engine *Engine, playerID string) *BoundEngine {
	if engine == nil {
		return nil
	}
	return &BoundEngine{engine: engine, playerID: playerID}
}

// PlayerID is the seat this handle acts as.
func (b *BoundEngine) PlayerID() string {
	return b.playerID
}

// Subscribe joins the engine's event feed. The broadcaster itself stays unreachable: a
// view holding it could Broadcast or Close the feed for the whole table.
func (b *BoundEngine) Subscribe() (<-chan Event, error) {
	return b.engine.Subscribe()
}

// Unsubscribe leaves the feed Subscribe joined.
func (b *BoundEngine) Unsubscribe(events <-chan Event) {
	if events == nil {
		return
	}
	b.engine.Unsubscribe(events)
}

// Submit plays action as this handle's player.
func (b *BoundEngine) Submit(action Action) error {
	return b.engine.SubmitAction(b.playerID, action)
}

// Frame reads the snapshot, this player's hand, the turn clock and (through fn, which
// may be nil) the live *State in one lock hold, so the pieces cannot describe different
// moments.
//
// The *State is unredacted and live: treat it as read-only, copy anything kept past
// the callback, and filter anything reaching the screen.
func (b *BoundEngine) Frame(fn func(*State)) (StateSnapshot, []deck.Card, time.Duration) {
	return b.engine.Frame(b.playerID, fn)
}
