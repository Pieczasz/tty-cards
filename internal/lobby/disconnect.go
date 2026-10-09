package lobby

import (
	"log/slog"
	"time"

	"github.com/Pieczasz/tty-cards/internal/game"
)

// graceTimers holds mid-game seats after a dropped session. Caller must hold
// Manager.mu around every method: the maps are not independently locked.
type graceTimers struct {
	pending  map[string]*time.Timer
	expiring map[string]struct{}
}

func newGraceTimers() graceTimers {
	return graceTimers{
		pending:  make(map[string]*time.Timer),
		expiring: make(map[string]struct{}),
	}
}

func (d *graceTimers) clear(id string) {
	if t, ok := d.pending[id]; ok {
		t.Stop()
		delete(d.pending, id)
	}
	delete(d.expiring, id)
}

func (d *graceTimers) arm(id string, wait time.Duration, fire func()) {
	if t, ok := d.pending[id]; ok {
		t.Stop()
	}
	d.pending[id] = time.AfterFunc(wait, fire)
}

// beginExpire moves a pending leave into the expiring set. False if there was
// nothing to expire (already resumed or cleared).
//
// The timer is stopped because releaseHeldSeats claims holds early: left running, it
// fires at the old deadline into whatever hold the player's next drop armed.
func (d *graceTimers) beginExpire(id string) bool {
	t, ok := d.pending[id]
	if !ok {
		return false
	}
	t.Stop()
	delete(d.pending, id)
	d.expiring[id] = struct{}{}
	return true
}

// tryCancel stops a pending leave for reconnect. blocked means expire already
// owns the seat (or the timer callback is in flight).
func (d *graceTimers) tryCancel(id string) (cancelled, blocked bool) {
	if _, ok := d.expiring[id]; ok {
		return false, true
	}
	t, ok := d.pending[id]
	if !ok {
		return false, false
	}
	if !t.Stop() {
		return false, true
	}
	delete(d.pending, id)
	return true, false
}

// DisconnectPlayer is what a dropped session calls instead of LeaveLobby: a seat
// in a running game is kept for disconnectGrace so the player can reconnect, while
// a seat in a waiting lobby is given up immediately (nothing is lost by leaving).
// During shutdown the grace is skipped so the drain still forfeits cleanly.
func (m *Manager) DisconnectPlayer(p *game.Player) {
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
	}
	l.mu.Unlock()
	if !playing {
		m.mu.Unlock()
		m.LeaveLobby(p)
		return
	}
	m.grace.arm(p.ID, disconnectGrace, func() { m.expireLeave(p) })
	m.mu.Unlock()
	slog.InfoContext(m.appCtx, "session dropped mid-game, holding the seat",
		"player_id", p.ID, "grace", disconnectGrace.String())
}

// expireLeave is the grace timer's body, separate so tests can drive the expiry
// without waiting out the window.
func (m *Manager) expireLeave(p *game.Player) {
	m.mu.Lock()
	if !m.grace.beginExpire(p.ID) {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	slog.InfoContext(m.appCtx, "disconnect grace expired, giving up the seat", "player_id", p.ID)
	m.LeaveLobby(p)
}

// ResumePlayer cancels a pending disconnect leave, so a reconnecting session keeps
// its seat. Routing back to the table is the caller's: the seat stays indexed, and
// FindLobbyByPlayer finds it. A grace whose expiry is already running is not
// cancelled - that seat is as good as gone.
func (m *Manager) ResumePlayer(p *game.Player) {
	if p == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancelled, _ := m.grace.tryCancel(p.ID); cancelled {
		slog.InfoContext(m.appCtx, "player reconnected inside the grace window", "player_id", p.ID)
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
	// expireLeave, not LeaveLobby: it claims the grace the way the timer would, so a
	// ResumePlayer racing it is refused rather than resuming a seat already gone.
	for _, p := range held {
		m.expireLeave(p)
	}
}

// heldSeatsLocked is every seat held for a reconnect at l, or anywhere when l is nil.
// Caller holds m.mu.
func (m *Manager) heldSeatsLocked(l *Lobby) []*game.Player {
	held := make([]*game.Player, 0, len(m.grace.pending))
	for id := range m.grace.pending {
		if l == nil || m.playerLobby[id] == l {
			held = append(held, &game.Player{ID: id})
		}
	}
	return held
}
