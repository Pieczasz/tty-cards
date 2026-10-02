package ssh

import (
	"errors"
	"io"
	"sync"

	"uuid"
)

// ErrServerFull is Connect's capacity refusal.
var ErrServerFull = errors.New("server is at capacity")

// trackedSession is the live session for an account: the generation its teardown
// must match, and the handle used to hang up on it when a newer one displaces it.
type trackedSession struct {
	gen  uint64
	conn io.Closer
}

// SessionTracker holds one live session per account and the server-wide session
// cap. Its generations are what let a displaced session's teardown tell that it no
// longer owns the account's slot.
type SessionTracker struct {
	mu     sync.Mutex
	active map[uuid.UUID]trackedSession
	next   uint64
	// maxSessions is the player-visible capacity: Connect refuses beyond it with a
	// message, unlike the TCP-level LimitListener, which silently stops accepting.
	// Zero means unlimited.
	maxSessions int
}

// NewSessionTracker returns a tracker refusing sessions past maxSessions (zero is
// unlimited).
func NewSessionTracker(maxSessions int) *SessionTracker {
	return &SessionTracker{
		active:      make(map[uuid.UUID]trackedSession),
		maxSessions: maxSessions,
	}
}

// Connect registers userID and returns a generation. A second Connect for the same
// account displaces the first: half-open TCP otherwise blocks reconnect for the whole
// mid-game grace window. ReleaseWith with a stale generation is a no-op.
//
// conn is the connection the displaced session is hung up on. Without closing it,
// the account keeps every connection it ever opened until each one's TCP dies, so the
// per-account limit becomes advisory: Count still counts accounts, but each one can
// hold any number of live TUIs and lobby subscriptions.
func (t *SessionTracker) Connect(userID uuid.UUID, conn io.Closer) (uint64, error) {
	t.mu.Lock()
	t.next++
	gen := t.next
	prev, exists := t.active[userID]
	if !exists && t.maxSessions > 0 && len(t.active) >= t.maxSessions {
		t.mu.Unlock()
		return 0, ErrServerFull
	}
	t.active[userID] = trackedSession{gen: gen, conn: conn}
	t.mu.Unlock()

	// Outside the lock: Close writes to the network, and a wedged peer must not hold
	// every other account's Connect behind it. The displaced session's own teardown
	// is already harmless - ReleaseWith only frees a slot for the live generation.
	if exists && prev.conn != nil {
		_ = prev.conn.Close()
	}
	return gen, nil
}

// Count is the number of accounts with a live session.
func (t *SessionTracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.active)
}

// ReleaseWith runs fn and then frees the slot, both under the tracker lock, only if
// gen is still the live generation. Holding the lock across fn is the point: a
// reconnect's Connect waits for the old session's teardown to finish, so the
// teardown can never act on a seat the new session has already resumed. That makes
// the lock order tracker, then lobby manager; nothing takes them the other way.
func (t *SessionTracker) ReleaseWith(userID uuid.UUID, gen uint64, fn func()) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active[userID].gen != gen {
		return false
	}
	fn()
	delete(t.active, userID)
	return true
}
