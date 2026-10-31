package lobby

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Pieczasz/tty-cards/internal/game"
)

// graceHold is one armed disconnect leave. gen is the generation the timer
// callback was armed with; a newer arm makes the old callback a no-op. done is
// closed when that callback finishes, so a reconnect blocked on the expiry can wait
// it out instead of sitting down into a seat that is about to disappear.
type graceHold struct {
	gen   uint64
	timer *time.Timer
	done  chan struct{}
	once  sync.Once
}

func (h *graceHold) finish() {
	if h == nil || h.done == nil {
		return
	}
	h.once.Do(func() { close(h.done) })
}

// graceTimers holds mid-game seats after a dropped session. Caller must hold
// Manager.mu around every method: the maps are not independently locked.
type graceTimers struct {
	seq      uint64
	pending  map[string]*graceHold
	expiring map[string]*graceHold
}

func newGraceTimers() graceTimers {
	return graceTimers{
		pending:  make(map[string]*graceHold),
		expiring: make(map[string]*graceHold),
	}
}

func (d *graceTimers) clear(id string) {
	if h, ok := d.pending[id]; ok {
		// Stop failed means the callback is already running and will finish the hold.
		// Closing here would let a waiter resume before the leave has run.
		if h.timer.Stop() {
			h.finish()
		}
		delete(d.pending, id)
	}
	delete(d.expiring, id)
}

func (d *graceTimers) arm(id string, wait time.Duration, fire func(gen uint64)) {
	if old, ok := d.pending[id]; ok && old.timer.Stop() {
		old.finish()
	}
	d.seq++
	h := &graceHold{gen: d.seq, done: make(chan struct{})}
	h.timer = time.AfterFunc(wait, func() {
		fire(h.gen)
		h.finish()
	})
	d.pending[id] = h
}

// claimExpire moves a pending leave of gen into the expiring set. gen 0 claims
// whatever is pending (tests only). A callback or release that saw an older arm
// does not match and must not drop the hold a later drop armed.
func (d *graceTimers) claimExpire(id string, gen uint64) *graceHold {
	h, ok := d.pending[id]
	if !ok || (gen != 0 && h.gen != gen) {
		return nil
	}
	h.timer.Stop()
	delete(d.pending, id)
	d.expiring[id] = h
	return h
}

// tryCancel stops a pending leave for reconnect. blocked means expire already
// owns the seat (or the timer callback is in flight); done is closed when that
// expiry finishes.
func (d *graceTimers) tryCancel(id string) (cancelled, blocked bool, done <-chan struct{}) {
	if h, ok := d.expiring[id]; ok {
		return false, true, h.done
	}
	h, ok := d.pending[id]
	if !ok {
		return false, false, nil
	}
	if !h.timer.Stop() {
		return false, true, h.done
	}
	delete(d.pending, id)
	h.finish()
	return true, false, nil
}

// DisconnectPlayer is what a dropped session calls instead of LeaveLobby: a seat
// in a running game is kept for disconnectGrace so the player can reconnect, while
// a seat in a waiting lobby is given up immediately (nothing is lost by leaving).
// During shutdown the grace is skipped so the drain still forfeits cleanly. ctx is
// the dropped session's, for its log line only: it is cancelled by now, so the
// grace timer runs on the manager's own context.
func (m *Manager) DisconnectPlayer(ctx context.Context, p *game.Player) {
	if p == nil {
		return
	}

	m.mu.Lock()
	// Read under m.mu, not before it: BeginShutdown sets the flag and then takes this
	// lock to drain the pending graces, so a disconnect that saw "not shutting down"
	// outside the lock could arm its timer after the drain had already walked the map
	// - a seat held for a reconnect to a process that is exiting.
	if m.shuttingDown.Load() {
		m.mu.Unlock()
		m.LeaveLobby(p)
		return
	}
	l, ok := m.playerLobby[p.ID]
	if !ok || l == nil {
		m.mu.Unlock()
		return
	}
	l.mu.Lock()
	playing := l.state == inGame
	if playing {
		// The session is gone, so its event channels must close now - but the seat
		// stays, and the engine's turn clock plays for it until they return.
		l.unsubscribePlayerLocked(p.ID)
	} else {
		// The leave below runs after both locks are released. A ready flag left
		// standing in that gap lets the last ready-up start the game with this seat
		// in it, and the leave would then forfeit it mid-game with no grace.
		delete(l.ready, p.ID)
	}
	l.mu.Unlock()
	if !playing {
		m.mu.Unlock()
		m.LeaveLobby(p)
		return
	}
	m.grace.arm(p.ID, disconnectGrace, func(gen uint64) { m.expireLeaveGen(p, gen) })
	m.mu.Unlock()
	slog.InfoContext(ctx, "session dropped mid-game, holding the seat",
		"player_id", p.ID, "grace", disconnectGrace.String())
}

// expireLeaveGen gives up the seat if gen is still the pending hold. A stale
// timer (gen from an earlier arm) returns without touching the newer one.
//
// Once the drain has stopped new games it leaves the hold pending: the match is
// still rated until BeginShutdown, and the closed listener keeps the player from
// reconnecting, so a forfeit here would be rated against someone who cannot come
// back. BeginShutdown gives the seat up afterwards, unrated.
func (m *Manager) expireLeaveGen(p *game.Player, gen uint64) {
	defer m.recoverGoroutine("disconnect grace")
	if p == nil || m.watchers.Closed() {
		return
	}
	m.mu.Lock()
	h := m.grace.claimExpire(p.ID, gen)
	m.mu.Unlock()
	if h == nil {
		return
	}
	defer h.finish()
	slog.InfoContext(m.appCtx, "disconnect grace expired, giving up the seat", "player_id", p.ID)
	m.LeaveLobby(p)
}

// ResumePlayer cancels a pending disconnect leave, so a reconnecting session keeps
// its seat. Routing back to the table is the caller's: the seat stays indexed, and
// FindLobbyByPlayer finds it. An expiry already running is not cancelled; this
// waits until that leave has finished, so the caller does not sit the player back
// down into a seat that is on its way out. ctx is the reconnecting session's, so
// its log line joins that session's trace.
func (m *Manager) ResumePlayer(ctx context.Context, p *game.Player) {
	if p == nil {
		return
	}
	m.mu.Lock()
	cancelled, blocked, done := m.grace.tryCancel(p.ID)
	m.mu.Unlock()
	if cancelled {
		slog.InfoContext(ctx, "player reconnected inside the grace window", "player_id", p.ID)
		return
	}
	if blocked && done != nil {
		<-done
	}
}

// releaseHeldSeats gives up every seat this table is holding for a dropped session.
// The hold only makes sense mid-hand: once the game is over the lobby is Waiting, and
// DisconnectPlayer gives a Waiting seat up at once. Leaving it armed keeps the player
// out of every other table - and this one unable to reach all-ready - until the timer
// fires, up to disconnectGrace later.
func (m *Manager) releaseHeldSeats(l *Lobby) {
	m.mu.Lock()
	held := m.heldSeatsLocked(l)
	m.mu.Unlock()
	if m.heldSeatsCollected != nil {
		m.heldSeatsCollected()
	}
	// expireLeaveGen, not LeaveLobby: it claims the grace the way the timer would, so
	// a ResumePlayer racing it is refused rather than resuming a seat already gone.
	// The generation seen under m.mu, not 0: a hold armed after the lock was released
	// belongs to a newer game and is not this release's to expire.
	for id, gen := range held {
		m.expireLeaveGen(&game.Player{ID: id}, gen)
	}
}

// heldSeatsLocked maps every seat held for a reconnect at l, or anywhere when l is
// nil, to the generation of its hold. Caller holds m.mu.
func (m *Manager) heldSeatsLocked(l *Lobby) map[string]uint64 {
	held := make(map[string]uint64, len(m.grace.pending))
	for id, h := range m.grace.pending {
		if l == nil || m.playerLobby[id] == l {
			held[id] = h.gen
		}
	}
	return held
}
