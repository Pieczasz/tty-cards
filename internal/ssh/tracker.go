package ssh

import (
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"uuid"
)

// errServerFull is Connect's capacity refusal.
var errServerFull = errors.New("server is at capacity")

// account is one account's slot. Its mu is what orders a session's teardown before
// that account's reconnect; no other account ever waits on it.
type account struct {
	mu sync.Mutex
	// gen and conn are the live session, guarded by mu: the generation its teardown
	// must match, and the handle used to hang up on it when a newer one displaces it.
	// A zero gen is no live session.
	gen  uint64
	conn io.Closer
	// refs counts the callers holding or waiting on mu, guarded by the tracker's mu.
	// The entry leaves the map only once nobody does and no session is live.
	refs int
}

// SessionTracker holds one live session per account and the server-wide session
// cap. Its generations are what let a displaced session's teardown tell that it no
// longer owns the account's slot.
//
// The lock order is an account's mu, then the tracker's: mu guards only the map, the
// generation counter and the cap, and is never held while an account's lock is
// taken, so one account's teardown never stalls another account's login.
type SessionTracker struct {
	mu       sync.Mutex
	accounts map[uuid.UUID]*account
	// live counts accounts with a live session, written only under mu. Count reads it
	// without the lock so the public stats API never queues behind a login.
	live atomic.Int64
	next uint64
	// maxSessions is the player-visible capacity: Connect refuses beyond it with a
	// message, unlike the TCP-level LimitListener, which silently stops accepting.
	// Zero means unlimited.
	maxSessions int
}

// NewSessionTracker returns a tracker refusing sessions past maxSessions.
func NewSessionTracker(maxSessions int) *SessionTracker {
	return &SessionTracker{
		accounts:    make(map[uuid.UUID]*account),
		maxSessions: maxSessions,
	}
}

// acquire takes a reference on userID's entry, creating it when create is set, and
// reports nil when there is none to take.
func (t *SessionTracker) acquire(userID uuid.UUID, create bool) *account {
	t.mu.Lock()
	defer t.mu.Unlock()
	a, ok := t.accounts[userID]
	if !ok {
		if !create {
			return nil
		}
		a = &account{}
		t.accounts[userID] = a
	}
	a.refs++
	return a
}

// unref drops a reference taken by acquire, after the caller unlocked a.mu. With no
// reference left nobody can be writing gen, so it is safe to read here.
func (t *SessionTracker) unref(userID uuid.UUID, a *account) {
	t.mu.Lock()
	defer t.mu.Unlock()
	a.refs--
	if a.refs == 0 && a.gen == 0 {
		delete(t.accounts, userID)
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
	a := t.acquire(userID, true)
	defer t.unref(userID, a)
	// Waits for this account's teardown, if one is running, and only for that.
	a.mu.Lock()
	gen, err := t.claim(a)
	if err != nil {
		a.mu.Unlock()
		return 0, err
	}
	prev := a.conn
	a.gen, a.conn = gen, conn
	a.mu.Unlock()

	// Outside the lock: Close writes to the network, and a wedged peer must not hold
	// this account's next login behind it. The displaced session's own teardown is
	// already harmless - ReleaseWith only frees a slot for the live generation.
	if prev != nil {
		_ = prev.Close()
	}
	return gen, nil
}

// claim mints a generation for a, whose mu the caller holds, counting it against the
// cap when the account had no live session.
func (t *SessionTracker) claim(a *account) (uint64, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if a.gen == 0 {
		if t.maxSessions > 0 && t.live.Load() >= int64(t.maxSessions) {
			return 0, errServerFull
		}
		t.live.Add(1)
	}
	t.next++
	return t.next, nil
}

// Count is the number of accounts with a live session.
func (t *SessionTracker) Count() int {
	return int(t.live.Load())
}

// ReleaseWith runs fn and then frees the slot, both under the account's lock, only if
// gen is still the live generation. Holding the lock across fn is the point: a
// reconnect's Connect waits for the old session's teardown to finish, so the
// teardown can never act on a seat the new session has already resumed. That makes
// the lock order account, then lobby manager; nothing takes them the other way.
// Other accounts' logins do not wait: the lock is this account's alone.
func (t *SessionTracker) ReleaseWith(userID uuid.UUID, gen uint64, fn func()) bool {
	a := t.acquire(userID, false)
	if a == nil {
		return false
	}
	defer t.unref(userID, a)
	a.mu.Lock()
	defer a.mu.Unlock()
	if gen == 0 || a.gen != gen {
		return false
	}
	fn()
	a.gen, a.conn = 0, nil
	t.mu.Lock()
	t.live.Add(-1)
	t.mu.Unlock()
	return true
}
