package lobby

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/big"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Pieczasz/terminal-card/internal/broadcaster"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/observability"
	"github.com/Pieczasz/terminal-card/internal/ratelimit"
)

const (
	lobbyCodeLength       = 8
	rankedFinalizeTimeout = 15 * time.Second
	joinRateLimitCount    = 10
	joinRateLimitWindow   = time.Second
	// maxLobbySubscribers is deliberately not maxPlayers: that setting is raised by
	// SetMaxPlayers long after the broadcaster exists, and a reconnecting player
	// briefly holds two subscriptions, so a table sized to its seats hands
	// ErrAtCapacity - a lobby view that never updates - to a player who joined
	// legitimately. This is the largest roster any game allows plus the same
	// headroom the engine keeps, which no raise can outgrow.
	maxLobbySubscribers = 10 + 8
)

var lobbyCodePattern = regexp.MustCompile(`^[A-Z0-9]{8}$`)

type Manager struct {
	mu                  sync.RWMutex
	lobbies             map[string]*Lobby
	playerLobby         map[string]*Lobby
	cachedPublicLobbies []*Lobby
	cacheLastUpdated    time.Time
	// cacheDirty is set by anything that changes which lobbies are public, a lobby
	// marking itself in-game included. It is atomic rather than guarded by m.mu: a
	// lobby sets it while holding its own lock, and reaching for the manager lock
	// there would invert the documented manager-then-lobby order.
	cacheDirty  atomic.Bool
	matchRepo   db.MatchRepository
	appCtx      context.Context
	joinLimiter *ratelimit.SlidingWindowLimiter
	// finalizerMu makes accepting a finished-match write and stopping new writes
	// atomic with respect to shutdown. WaitGroup alone permits Add after Wait
	// observes zero.
	finalizerMu       sync.Mutex
	finalizersStopped bool
	finalizing        sync.WaitGroup
	// drained is closed by the one waiter goroutine WaitForFinalizers ever starts.
	// Created lazily and reused, so a timed-out drain does not strand a waiter per
	// call the way a fresh goroutine per invocation did.
	drained chan struct{}
	// shuttingDown is set before sessions are torn down, which is earlier than
	// finalizersStopped: a match ending during the drain must still be recorded, just
	// not rated. See persistFinishedMatch.
	shuttingDown atomic.Bool
	// grace holds mid-game seats after a dropped session (see disconnect.go).
	grace disconnectGrace
}

// DisconnectGrace is how long a mid-game seat survives its session. The engine's
// turn clock auto-plays for the absent player the whole time, and its idle removal
// (3 missed turns) is the harder backstop, so this mostly decides how long a lobby
// keeps a seat for someone who never comes back between hands.
const DisconnectGrace = 90 * time.Second

func NewManager(ctx context.Context, matchRepo db.MatchRepository) *Manager {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Manager{
		lobbies:     make(map[string]*Lobby),
		playerLobby: make(map[string]*Lobby),
		grace:       newDisconnectGrace(),
		matchRepo:   matchRepo,
		appCtx:      ctx,
		joinLimiter: ratelimit.NewSlidingWindowLimiter(joinRateLimitCount, joinRateLimitWindow),
	}
}

const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func (m *Manager) generateLobbyCode() (string, error) {
	var code string
	maxAttempts := 10

	for range maxAttempts {
		rawCode := make([]byte, lobbyCodeLength)
		charsetLen := big.NewInt(int64(len(charset)))
		for i := range rawCode {
			n, err := rand.Int(rand.Reader, charsetLen)
			if err != nil {
				return "", fmt.Errorf("generating lobby code: %w", err)
			}
			rawCode[i] = charset[n.Int64()]
		}
		code = string(rawCode)
		if _, exists := m.lobbies[code]; !exists {
			return code, nil
		}
	}
	slog.Error("failed to generate unique lobby code after maximum attempts", "attempts", maxAttempts)
	return "", errors.New("failed to generate lobby code")
}

func ValidLobbyCode(code string) bool {
	return lobbyCodePattern.MatchString(code)
}

func (m *Manager) playerInLobbyLocked(p *game.Player) bool {
	lobby, exists := m.playerLobby[p.ID]
	if !exists {
		return false
	}
	if lobby.HasPlayer(p) {
		return true
	}
	delete(m.playerLobby, p.ID)
	return false
}

func (m *Manager) New(leader *game.Player, opts ...Option) (*Lobby, error) {
	options := setupDefaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	if options.cardGame == "" {
		return nil, errors.New("card game is required")
	}
	if options.maxPlayers < 2 {
		return nil, errors.New("max players must be at least 2")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.playerInLobbyLocked(leader) {
		return nil, errors.New("player is already in a lobby")
	}

	code, err := m.generateLobbyCode()
	if err != nil {
		return nil, err
	}

	lobby := &Lobby{
		manager:     m,
		leader:      leader,
		guests:      nil,
		options:     options,
		code:        code,
		ready:       make(map[string]bool),
		broadcaster: broadcaster.New[Event](maxLobbySubscribers),
		playerSubs:  make(map[string][]<-chan Event),
		createdAt:   time.Now(),
	}

	m.lobbies[code] = lobby
	m.playerLobby[leader.ID] = lobby
	m.invalidatePublicCache()
	return lobby, nil
}

func (m *Manager) JoinLobbyByCode(code string, p *game.Player) error {
	ctx := m.shutdownCtx()
	if !ValidLobbyCode(code) {
		observability.LobbyJoin(ctx, "invalid_code")
		return errors.New("invalid lobby code")
	}
	if m.joinLimiter != nil && !m.joinLimiter.Allow("join:"+p.ID) {
		observability.LobbyJoin(ctx, "rate_limited")
		observability.RateLimitReject(ctx, "lobby_join")
		return errors.New("too many join attempts, please try again later")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.playerInLobbyLocked(p) {
		observability.LobbyJoin(ctx, "already_in_lobby")
		return errors.New("player is already in a lobby")
	}

	lobby, exists := m.lobbies[code]
	if !exists {
		observability.LobbyJoin(ctx, "not_found")
		return errors.New("lobby not found")
	}

	if err := lobby.addGuest(p); err != nil {
		observability.LobbyJoin(ctx, "refused")
		return err
	}
	m.playerLobby[p.ID] = lobby
	observability.LobbyJoin(ctx, "ok")
	return nil
}

func (m *Manager) FindLobbyByPlayer(p *game.Player) *Lobby {
	m.mu.RLock()
	lobby, exists := m.playerLobby[p.ID]
	m.mu.RUnlock()

	if !exists {
		return nil
	}
	if lobby.HasPlayer(p) {
		return lobby
	}
	m.mu.Lock()
	if m.playerLobby[p.ID] == lobby {
		delete(m.playerLobby, p.ID)
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) FindLobbyByCode(code string) (*Lobby, error) {
	if !ValidLobbyCode(code) {
		return nil, errors.New("invalid lobby code")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	lobby, exists := m.lobbies[code]
	if !exists {
		return nil, errors.New("lobby not found")
	}
	return lobby, nil
}

// DisconnectPlayer is what a dropped session calls instead of LeaveLobby: a seat
// in a running game is kept for DisconnectGrace so the player can reconnect, while
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
	inGame := l.state == InGame
	if inGame {
		// The session is gone, so its event channels must close now - but the seat
		// stays, and the engine's turn clock plays for it until they return.
		l.unsubscribePlayerLocked(p.ID)
	}
	l.mu.Unlock()
	if !inGame {
		m.mu.Unlock()
		m.LeaveLobby(p)
		return
	}
	m.grace.arm(p.ID, DisconnectGrace, func() { m.expireLeave(p) })
	m.mu.Unlock()
	slog.Info("session dropped mid-game, holding the seat",
		"player_id", p.ID, "grace", DisconnectGrace.String())
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
	slog.Info("disconnect grace expired, giving up the seat", "player_id", p.ID)
	m.LeaveLobby(p)
}

// ResumePlayer cancels a pending disconnect leave and returns the lobby the player
// still occupies, or nil. A reconnecting session calls it before routing, so the
// player lands back at their table instead of a fresh home screen.
//
// A takeover (second SSH session while the first is half-open) finds the seat still
// mapped with no pending leave: return that lobby so unclean disconnects can resume.
func (m *Manager) ResumePlayer(p *game.Player) *Lobby {
	if p == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cancelled, blocked := m.grace.tryCancel(p.ID); blocked {
		return nil
	} else if cancelled {
		slog.Info("player reconnected inside the grace window", "player_id", p.ID)
	}
	// Same re-validation FindLobbyByPlayer and New do: a stale index entry would
	// route the reconnect into a lobby whose roster no longer holds them.
	if !m.playerInLobbyLocked(p) {
		return nil
	}
	return m.playerLobby[p.ID]
}

// LeaveLobby drops a player from their lobby. The roster mutation and the
// playerLobby index update happen under one hold of m.mu (taking l.mu inside it, per
// the documented manager-then-lobby order), so the two can never disagree about
// where a player is. The engine and broadcast calls run with both locks dropped, the
// way Kick does it.
func (m *Manager) LeaveLobby(p *game.Player) {
	if p == nil {
		return
	}

	m.mu.Lock()
	m.grace.clear(p.ID)
	l, ok := m.playerLobby[p.ID]
	if !ok || l == nil {
		m.mu.Unlock()
		return
	}

	l.mu.Lock()
	l.unsubscribePlayerLocked(p.ID)
	engine, bc, eventType, shouldClose, found := l.detachPlayerLocked(p)
	code := l.code
	l.mu.Unlock()
	if found {
		delete(m.playerLobby, p.ID)
	}
	m.mu.Unlock()

	if !found {
		return
	}
	notifyEngineAndBroadcast(engine, bc, p.ID, eventType)
	if shouldClose {
		m.RemoveLobby(code)
	}
}

func (m *Manager) shutdownCtx() context.Context {
	if m == nil || m.appCtx == nil {
		return context.Background()
	}
	return m.appCtx
}

// registerFinalizer accepts a finished-match write unless shutdown has started.
func (m *Manager) registerFinalizer() bool {
	m.finalizerMu.Lock()
	defer m.finalizerMu.Unlock()
	if m.finalizersStopped {
		return false
	}
	m.finalizing.Add(1)
	return true
}

// BeginShutdown marks the process as going away without stopping finished-match
// writes: a hand that ends while sessions are torn down still belongs in the
// players' history, it just must not move anyone's rating.
//
// Seats still held for a reconnect are given up here. Their timers would fire long
// after the drain, so the lobby would never be removed and its engine never closed -
// the player is not coming back to a process that is exiting.
func (m *Manager) BeginShutdown() {
	if m == nil {
		return
	}
	m.shuttingDown.Store(true)
	m.mu.Lock()
	held := make([]*game.Player, 0, len(m.grace.pending))
	for id := range m.grace.pending {
		held = append(held, &game.Player{ID: id})
	}
	m.mu.Unlock()
	// LeaveLobby stops the timer under m.mu before touching the roster.
	for _, p := range held {
		m.LeaveLobby(p)
	}
}

// releaseHeldSeats gives up every seat this table is holding for a dropped session.
// The hold only makes sense mid-hand: once the game is over the lobby is Waiting, and
// DisconnectPlayer gives a Waiting seat up at once. Leaving it armed keeps the player
// out of every other table - and this one unable to reach all-ready - until the timer
// fires, up to DisconnectGrace later.
func (m *Manager) releaseHeldSeats(l *Lobby) {
	if m == nil || l == nil {
		return
	}
	m.mu.Lock()
	held := make([]*game.Player, 0, len(m.grace.pending))
	for id := range m.grace.pending {
		if m.playerLobby[id] == l {
			held = append(held, &game.Player{ID: id})
		}
	}
	m.mu.Unlock()
	// expireLeave, not LeaveLobby: it claims the grace the way the timer would, so a
	// ResumePlayer racing it is refused rather than resuming a seat already gone.
	for _, p := range held {
		m.expireLeave(p)
	}
}

func (m *Manager) isShuttingDown() bool {
	return m != nil && m.shuttingDown.Load()
}

// WaitForFinalizers stops accepting finished-match writes, then blocks until all
// previously registered writes finish or timeout elapses. A non-positive timeout
// waits indefinitely.
//
// The waiter goroutine is started once and reused, so a caller that times out and
// calls again does not strand one waiter per attempt. Because finalizersStopped is
// already set, the group only ever counts down, so that goroutine always exits.
func (m *Manager) WaitForFinalizers(timeout time.Duration) bool {
	if m == nil {
		return true
	}
	m.shuttingDown.Store(true)

	m.finalizerMu.Lock()
	m.finalizersStopped = true
	if m.drained == nil {
		ch := make(chan struct{})
		m.drained = ch
		go func() {
			m.finalizing.Wait()
			close(ch)
		}()
	}
	drained := m.drained
	m.finalizerMu.Unlock()

	if timeout <= 0 {
		<-drained
		return true
	}
	select {
	case <-drained:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Stats counts lobbies by state for the public stats endpoint: how many hands are
// being played, and how many tables are sitting open waiting for players.
//
// The lobby set is copied under m.mu and the lock released before any lobby is
// touched, so each l.mu is taken on its own rather than nested inside m.mu. Reading
// l.state without l.mu would race with a lobby mutating itself.
func (m *Manager) Stats() (inGame, waiting int) {
	if m == nil {
		return 0, 0
	}
	m.mu.RLock()
	lobbies := slices.Collect(maps.Values(m.lobbies))
	m.mu.RUnlock()

	for _, l := range lobbies {
		l.mu.RLock()
		switch l.state {
		case InGame:
			inGame++
		case Waiting:
			waiting++
		case Closed:
		}
		l.mu.RUnlock()
	}
	return inGame, waiting
}

// kickableGuestLocked returns the guest index host may remove. Caller holds l.mu.
func (l *Lobby) kickableGuestLocked(host, target *game.Player) (int, error) {
	switch {
	case l.state == Closed:
		return -1, errors.New("lobby is closed")
	// A leader who can kick mid-hand can farm Elo: drop whoever is winning, let the
	// engine finish the match without them, and take the rating.
	case l.state == InGame:
		return -1, errors.New("cannot kick during a game")
	case !l.leader.Equal(host):
		return -1, errors.New("only the leader can kick players")
	case l.leader.Equal(target):
		return -1, errors.New("cannot kick the lobby leader")
	}
	idx := slices.IndexFunc(l.guests, func(g *game.Player) bool { return g.Equal(target) })
	if idx == -1 {
		return -1, errors.New("player not in lobby")
	}
	return idx, nil
}

// Kick removes a guest from the lobby. Only the current leader may kick guests.
func (m *Manager) Kick(host, target *game.Player) error {
	if host == nil || target == nil {
		return errors.New("host and target are required")
	}
	if host.Equal(target) {
		return errors.New("cannot kick yourself")
	}

	m.mu.Lock()
	l, ok := m.playerLobby[host.ID]
	if !ok || l == nil {
		m.mu.Unlock()
		return errors.New("host is not in a lobby")
	}
	// Hold manager lock while mutating lobby so join/leave cannot interleave.
	l.mu.Lock()
	idx, err := l.kickableGuestLocked(host, target)
	if err != nil {
		l.mu.Unlock()
		m.mu.Unlock()
		return err
	}

	engine := l.activeEngine
	bc := l.broadcaster
	l.removeGuestAtLocked(idx)
	delete(m.playerLobby, target.ID)
	// A hold can outlive its hand for as long as releaseHeldSeats waits on m.mu, and
	// with the mapping gone that release no longer finds this seat: the timer would
	// later take the player out of whatever table they had moved on to.
	m.grace.clear(target.ID)
	l.mu.Unlock()
	m.mu.Unlock()

	notifyEngineAndBroadcast(engine, bc, target.ID, EventPlayersUpdated)
	return nil
}

func (m *Manager) RemoveLobby(code string) {
	m.mu.Lock()
	l, exists := m.lobbies[code]
	if !exists {
		m.mu.Unlock()
		return
	}
	delete(m.lobbies, code)
	m.invalidatePublicCache()

	l.mu.Lock()
	seats := make([]string, 0, 1+len(l.guests))
	if l.leader != nil {
		seats = append(seats, l.leader.ID)
	}
	for _, g := range l.guests {
		seats = append(seats, g.ID)
	}
	engine := l.activeEngine
	l.activeEngine = nil
	bc := l.broadcaster
	l.broadcaster = nil
	l.mu.Unlock()

	// Only entries still pointing here: LeaveLobby unmaps and drops m.mu before it
	// calls this, so the player may already be seated at a newer table.
	for _, id := range seats {
		if m.playerLobby[id] == l {
			delete(m.playerLobby, id)
		}
	}
	m.mu.Unlock()

	if engine != nil {
		engine.Close()
	}
	if bc != nil {
		if n := bc.Dropped(); n > 0 {
			observability.BroadcastDropped(m.shutdownCtx(), "lobby", n)
		}
		bc.Close()
	}
}

// publicLobbyCacheTTL is what keeps a browse off every lobby's own lock. The window
// is short enough that a new table shows up on the next refresh.
const publicLobbyCacheTTL = 2 * time.Second

// invalidatePublicCache makes the next browse re-scan. Callers are the writes that
// change which tables are on offer, so a new or closed table shows up immediately
// rather than a cache window later.
func (m *Manager) invalidatePublicCache() {
	if m != nil {
		m.cacheDirty.Store(true)
	}
}

// getCachedPublicLobbies serves the cache under a read lock and, on a miss, copies
// the lobby set and releases m.mu before touching any l.mu - the same shape as
// Stats. Two simultaneous misses both rescan and the later write wins, so the stored
// list can hold a table that has since gone private or started; browseEntry
// re-checks each one, which is what keeps that out of the browse.
func (m *Manager) getCachedPublicLobbies() []*Lobby {
	m.mu.RLock()
	if !m.cacheDirty.Load() && time.Since(m.cacheLastUpdated) < publicLobbyCacheTTL {
		lobbies := slices.Clone(m.cachedPublicLobbies)
		m.mu.RUnlock()
		return lobbies
	}
	// Cleared inside the same lock hold that snapshots the lobby set, and before it.
	// New and RemoveLobby set the flag while holding m.mu exclusively, so an
	// invalidation either happened before this point - and its lobby is in the
	// snapshot - or lands after, and survives into the next browse. Clearing it after
	// the snapshot instead left a window where a table set the flag, this cleared it,
	// and the snapshot had never seen the table: hidden for the whole TTL.
	m.cacheDirty.Store(false)
	all := slices.Collect(maps.Values(m.lobbies))
	m.mu.RUnlock()

	publicLobbies := make([]*Lobby, 0, len(all))
	for _, l := range all {
		l.mu.RLock()
		if !l.options.isPrivate && l.state == Waiting {
			publicLobbies = append(publicLobbies, l)
		}
		l.mu.RUnlock()
	}

	m.mu.Lock()
	m.cachedPublicLobbies = publicLobbies
	m.cacheLastUpdated = time.Now()
	m.mu.Unlock()

	lobbies := slices.Clone(publicLobbies)
	return lobbies
}
