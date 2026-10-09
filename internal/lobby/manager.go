package lobby

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Pieczasz/tty-cards/internal/broadcaster"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/observability"
	"github.com/Pieczasz/tty-cards/internal/ratelimit"
)

const (
	lobbyCodeLength = 8
	// lobbyCodeAttempts is how many collisions generateLobbyCodeLocked retries.
	lobbyCodeAttempts   = 10
	finalizeTimeout     = 15 * time.Second
	joinRateLimitCount  = 10
	joinRateLimitWindow = time.Second
	// maxLobbySubscribers is deliberately not maxPlayers: that setting is raised by
	// SetMaxPlayers long after the broadcaster exists, and a reconnecting player
	// briefly holds two subscriptions, so a table sized to its seats hands
	// ErrAtCapacity - a lobby view that never updates - to a player who joined
	// legitimately. This is the largest roster any game allows plus the same
	// headroom the engine keeps, which no raise can outgrow.
	maxLobbySubscribers = 10 + 8
)

var lobbyCodePattern = regexp.MustCompile(`^[A-Z0-9]{8}$`)

// Manager owns every lobby on the server and which one each player sits at. It also
// persists finished matches and holds a dropped player's seat for a reconnect. All
// methods are safe for concurrent use.
type Manager struct {
	mu                  sync.RWMutex
	lobbies             map[string]*Lobby
	playerLobby         map[string]*Lobby
	games               *game.Registry
	cachedPublicLobbies []*Lobby
	cacheLastUpdated    time.Time
	// cacheDirty is set by anything that changes which lobbies are public, a lobby
	// marking itself in-game included. It is atomic rather than guarded by m.mu
	// because each lobby shares it (Lobby.cacheDirty) and sets it under its own lock,
	// where taking the manager lock would invert the manager-then-lobby order.
	cacheDirty  atomic.Bool
	matchRepo   db.MatchRepository
	appCtx      context.Context
	joinLimiter *ratelimit.SlidingWindow
	// gateMu makes counting a new game's watcher and closing the gate on new games
	// atomic with respect to shutdown. WaitGroup alone permits Add after Wait
	// observes zero.
	gateMu     sync.Mutex
	gateClosed bool
	// watchers counts every game's watcher goroutine, from the start of its game to
	// the end of its finalize.
	watchers sync.WaitGroup
	// drained is closed by the one waiter goroutine WaitForFinalizers ever starts.
	// Created lazily and reused, so a timed-out drain does not strand a waiter per
	// call the way a fresh goroutine per invocation did.
	drained chan struct{}
	// shuttingDown is set before sessions are torn down: a match ending during the
	// drain must still be recorded, just not rated. See persistFinishedMatch.
	shuttingDown atomic.Bool
	// grace holds mid-game seats after a dropped session (see disconnect.go).
	grace graceTimers
}

// disconnectGrace is how long a mid-game seat survives its session. The engine's
// turn clock auto-plays for the absent player the whole time, and its idle removal
// (3 missed turns) is the harder backstop, so this mostly decides how long a lobby
// keeps a seat for someone who never comes back between hands.
const disconnectGrace = 90 * time.Second

// NewManager is a Manager opening tables for the games in games, whose finished
// matches are written to matchRepo, or nowhere when it is nil. ctx is the process
// lifetime, not a request. A nil games panics, the way game.NewRegistry does on a
// half-declared module: a manager that can open no table is a wiring bug, and it
// should fail the boot rather than every player's first create.
func NewManager(ctx context.Context, matchRepo db.MatchRepository, games *game.Registry) *Manager {
	if games == nil {
		panic("lobby.NewManager: nil game registry")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return &Manager{
		lobbies:     make(map[string]*Lobby),
		playerLobby: make(map[string]*Lobby),
		games:       games,
		grace:       newGraceTimers(),
		matchRepo:   matchRepo,
		appCtx:      ctx,
		joinLimiter: ratelimit.New(joinRateLimitCount, joinRateLimitWindow),
	}
}

const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

var charsetLen = big.NewInt(int64(len(charset)))

// generateLobbyCodeLocked is a fresh code no open lobby holds. Caller holds m.mu.
func (m *Manager) generateLobbyCodeLocked() (string, error) {
	raw := make([]byte, lobbyCodeLength)
	for range lobbyCodeAttempts {
		for i := range raw {
			// crypto/rand cannot fail on Go 1.24+: a broken source crashes the process.
			n, _ := rand.Int(rand.Reader, charsetLen)
			raw[i] = charset[n.Int64()]
		}
		if code := string(raw); m.lobbies[code] == nil {
			return code, nil
		}
	}
	return "", fmt.Errorf("generate lobby code: every one of %d attempts collided", lobbyCodeAttempts)
}

func validCode(code string) bool {
	return lobbyCodePattern.MatchString(code)
}

// CreateLobby opens a table with leader in seat 0. WithCardGame is required and must
// name a game the manager offers; the defaults are 4 seats (clamped to what the game
// deals for), private and casual.
func (m *Manager) CreateLobby(leader *game.Player, opts ...Option) (*Lobby, error) {
	options := setupDefaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	if options.cardGame == "" {
		return nil, errNoCardGame
	}
	mod, ok := m.games.Module(options.cardGame)
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownGame, options.cardGame)
	}
	if options.maxPlayers == 0 {
		options.maxPlayers = min(max(defaultMaxPlayers, mod.MinPlayers), mod.MaxPlayers)
	}
	if options.maxPlayers < mod.MinPlayers || options.maxPlayers > mod.MaxPlayers {
		return nil, refusalf("max players must be between %d and %d for %s",
			mod.MinPlayers, mod.MaxPlayers, mod.Name)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, seated := m.playerLobby[leader.ID]; seated {
		return nil, errAlreadyInLobby
	}

	code, err := m.generateLobbyCodeLocked()
	if err != nil {
		return nil, err
	}

	lobby := &Lobby{
		cacheDirty:  &m.cacheDirty,
		game:        mod,
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

// ToggleReady flips p's ready flag at the table p sits at, and starts the game once
// every seat is ready - unless shutdown has begun, when no new game starts.
func (m *Manager) ToggleReady(p *game.Player) error {
	l := m.FindLobbyByPlayer(p)
	if l == nil {
		return errNotInLobby
	}
	// Before toggleReady takes l.mu: this takes it itself, then m.mu via
	// releaseHeldSeats. It is a no-op unless a finished game is holding the table.
	m.releaseFinishedGame(l)
	// The watcher is counted before the game can start, so a drain that begins at any
	// point after the start waits for it.
	admitted := m.admitGame()
	start, err := l.toggleReady(p, admitted, m.seatHook(l))
	if start.engine == nil {
		if admitted {
			m.watchers.Done()
		}
		return err
	}
	go func() {
		defer m.watchers.Done()
		m.watchGame(l, start)
	}()
	return err
}

// JoinLobbyByCode seats p as a guest and returns the lobby it joined, so the caller
// does not look the code up a second time - by when the table may be gone.
func (m *Manager) JoinLobbyByCode(code string, p *game.Player) (*Lobby, error) {
	ctx := m.appCtx
	if !validCode(code) {
		observability.LobbyJoin(ctx, "invalid_code")
		return nil, errInvalidCode
	}
	if !m.joinLimiter.Allow("join:" + p.ID) {
		observability.LobbyJoin(ctx, "rate_limited")
		observability.RateLimitReject(ctx, "lobby_join")
		return nil, errJoinRateLimited
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, seated := m.playerLobby[p.ID]; seated {
		observability.LobbyJoin(ctx, "already_in_lobby")
		return nil, errAlreadyInLobby
	}

	lobby, exists := m.lobbies[code]
	if !exists {
		observability.LobbyJoin(ctx, "not_found")
		return nil, errLobbyNotFound
	}

	if err := lobby.addGuest(p); err != nil {
		observability.LobbyJoin(ctx, "refused")
		return nil, err
	}
	m.playerLobby[p.ID] = lobby
	observability.LobbyJoin(ctx, "ok")
	return lobby, nil
}

// FindLobbyByPlayer is the lobby p sits at, or nil. The index is the whole answer:
// every roster write updates it in the same hold of m.mu, so it cannot point at a
// table that no longer seats p.
func (m *Manager) FindLobbyByPlayer(p *game.Player) *Lobby {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.playerLobby[p.ID]
}

// FindLobbyByCode is the open lobby holding code.
func (m *Manager) FindLobbyByCode(code string) (*Lobby, error) {
	if !validCode(code) {
		return nil, errInvalidCode
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	lobby, exists := m.lobbies[code]
	if !exists {
		return nil, errLobbyNotFound
	}
	return lobby, nil
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
	m.leave(p, nil)
}

// seatHook keeps l's roster in step with the seats its engine takes for idling. It
// is the engine's lossless WithOnIdleRemoved rather than EventPlayerIdle on the
// latest-wins feed: a dropped event would leave the seat on the roster with the
// engine no longer holding it, and the table could never reach all-ready again.
func (m *Manager) seatHook(l *Lobby) game.EngineOption {
	return game.WithOnIdleRemoved(func(playerID string) { m.dropSeat(l, playerID) })
}

// dropSeat gives up the seat l's engine took from playerID. Only while the player is
// still indexed to l: the hook runs after the engine lock is released, and a player
// who left in that gap and sat down elsewhere must keep the new seat.
func (m *Manager) dropSeat(l *Lobby, playerID string) {
	m.leave(&game.Player{ID: playerID}, l)
}

// leave is LeaveLobby, limited to a player still indexed to from when it is not nil.
func (m *Manager) leave(p *game.Player, from *Lobby) {
	m.mu.Lock()
	l, ok := m.playerLobby[p.ID]
	if from != nil && l != from {
		m.mu.Unlock()
		return
	}
	m.grace.clear(p.ID)
	if !ok || l == nil {
		m.mu.Unlock()
		return
	}

	l.mu.Lock()
	l.unsubscribePlayerLocked(p.ID)
	leave, found := l.detachPlayerLocked(p)
	l.mu.Unlock()
	delete(m.playerLobby, p.ID)
	// Closed in the same hold that emptied it: a table that is out of the index but
	// still open is one a later RemoveLobby has to second-guess.
	var gone closing
	if leave.closeLobby {
		gone = m.removeLobbyLocked(l)
	}
	m.mu.Unlock()

	if !found {
		return
	}
	leave.notify(p.ID)
	gone.close(m.appCtx)
}

// Stats counts lobbies by state for the public stats endpoint: how many hands are
// being played, and how many tables are sitting open waiting for players.
//
// The lobby set is copied under m.mu and the lock released before any lobby is
// touched, so each l.mu is taken on its own rather than nested inside m.mu. Reading
// l.state without l.mu would race with a lobby mutating itself.
func (m *Manager) Stats() (playing, open int) {
	if m == nil {
		return 0, 0
	}
	m.mu.RLock()
	lobbies := slices.Collect(maps.Values(m.lobbies))
	m.mu.RUnlock()

	for _, l := range lobbies {
		l.mu.RLock()
		switch l.state {
		case inGame:
			playing++
		case waiting:
			open++
		case closed:
		}
		l.mu.RUnlock()
	}
	return playing, open
}

// Kick removes a guest from the lobby. Only the current leader may kick guests.
func (m *Manager) Kick(host, target *game.Player) error {
	if host == nil || target == nil {
		return errors.New("host and target are required")
	}
	if host.Equal(target) {
		return errKickSelf
	}

	m.mu.Lock()
	l, ok := m.playerLobby[host.ID]
	if !ok || l == nil {
		m.mu.Unlock()
		return errHostNotInLobby
	}
	// Hold manager lock while mutating lobby so join/leave cannot interleave.
	l.mu.Lock()
	idx, err := l.kickableGuestLocked(host, target)
	if err != nil {
		l.mu.Unlock()
		m.mu.Unlock()
		return err
	}

	kicked := departure{engine: l.activeEngine, bc: l.broadcaster, event: EventPlayersUpdated}
	l.removeGuestAtLocked(idx)
	delete(m.playerLobby, target.ID)
	// A hold can outlive its hand for as long as releaseHeldSeats waits on m.mu, and
	// with the mapping gone that release no longer finds this seat: the timer would
	// later take the player out of whatever table they had moved on to.
	m.grace.clear(target.ID)
	l.mu.Unlock()
	m.mu.Unlock()

	kicked.notify(target.ID)
	return nil
}

// RemoveLobby closes the table under code: its engine, its event feed and every seat
// at it.
func (m *Manager) RemoveLobby(code string) {
	m.mu.Lock()
	var gone closing
	if l, exists := m.lobbies[code]; exists {
		gone = m.removeLobbyLocked(l)
	}
	m.mu.Unlock()
	gone.close(m.appCtx)
}

// closing is what a removed table leaves to close once the locks are released.
type closing struct {
	engine *game.Engine
	bc     *broadcaster.Broadcaster[Event]
}

func (c closing) close(ctx context.Context) {
	if c.engine != nil {
		c.engine.Close()
	}
	if c.bc != nil {
		if n := c.bc.Dropped(); n > 0 {
			observability.BroadcastDropped(ctx, "lobby", n)
		}
		c.bc.Close()
	}
}

// removeLobbyLocked takes l out of the manager and unmaps every seat at it. Every
// seat is indexed to l and nowhere else - roster and index change in one hold of
// m.mu - so the whole roster is unmapped. Caller holds m.mu.
func (m *Manager) removeLobbyLocked(l *Lobby) closing {
	delete(m.lobbies, l.code)
	m.invalidatePublicCache()

	l.mu.Lock()
	defer l.mu.Unlock()
	delete(m.playerLobby, l.leader.ID)
	for _, g := range l.guests {
		delete(m.playerLobby, g.ID)
	}
	gone := closing{engine: l.activeEngine, bc: l.broadcaster}
	l.activeEngine = nil
	l.broadcaster = nil
	return gone
}
