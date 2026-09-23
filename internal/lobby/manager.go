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

	"github.com/Pieczasz/terminal-card/internal/broadcaster"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/observability"
	"github.com/Pieczasz/terminal-card/internal/ratelimit"
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
	cachedPublicLobbies []*Lobby
	cacheLastUpdated    time.Time
	// cacheDirty is set by anything that changes which lobbies are public, a lobby
	// marking itself in-game included. It is atomic rather than guarded by m.mu: a
	// lobby sets it while holding its own lock, and reaching for the manager lock
	// there would invert the documented manager-then-lobby order.
	cacheDirty  atomic.Bool
	matchRepo   db.MatchRepository
	appCtx      context.Context
	joinLimiter *ratelimit.SlidingWindow
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
	grace graceTimers
}

// disconnectGrace is how long a mid-game seat survives its session. The engine's
// turn clock auto-plays for the absent player the whole time, and its idle removal
// (3 missed turns) is the harder backstop, so this mostly decides how long a lobby
// keeps a seat for someone who never comes back between hands.
const disconnectGrace = 90 * time.Second

// NewManager is a Manager whose finished matches are written to matchRepo, or
// nowhere when it is nil. ctx is the process lifetime, not a request.
func NewManager(ctx context.Context, matchRepo db.MatchRepository) *Manager {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Manager{
		lobbies:     make(map[string]*Lobby),
		playerLobby: make(map[string]*Lobby),
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
			n, err := rand.Int(rand.Reader, charsetLen)
			if err != nil {
				return "", fmt.Errorf("generate lobby code: %w", err)
			}
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

// CreateLobby opens a table with leader in seat 0. The defaults are 4 seats, private
// and casual; WithCardGame is required.
func (m *Manager) CreateLobby(leader *game.Player, opts ...Option) (*Lobby, error) {
	options := setupDefaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	if options.cardGame == "" {
		return nil, errNoCardGame
	}
	if options.maxPlayers < 2 {
		return nil, errors.New("max players must be at least 2")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.playerInLobbyLocked(leader) {
		return nil, ErrAlreadyInLobby
	}

	code, err := m.generateLobbyCodeLocked()
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

// JoinLobbyByCode seats p as a guest and returns the lobby it joined, so the caller
// does not look the code up a second time - by when the table may be gone.
func (m *Manager) JoinLobbyByCode(code string, p *game.Player) (*Lobby, error) {
	ctx := m.shutdownCtx()
	if !validCode(code) {
		observability.LobbyJoin(ctx, "invalid_code")
		return nil, ErrInvalidCode
	}
	if !m.joinLimiter.Allow("join:" + p.ID) {
		observability.LobbyJoin(ctx, "rate_limited")
		observability.RateLimitReject(ctx, "lobby_join")
		return nil, ErrJoinRateLimited
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.playerInLobbyLocked(p) {
		observability.LobbyJoin(ctx, "already_in_lobby")
		return nil, ErrAlreadyInLobby
	}

	lobby, exists := m.lobbies[code]
	if !exists {
		observability.LobbyJoin(ctx, "not_found")
		return nil, ErrLobbyNotFound
	}

	if err := lobby.addGuest(p); err != nil {
		observability.LobbyJoin(ctx, "refused")
		return nil, err
	}
	m.playerLobby[p.ID] = lobby
	observability.LobbyJoin(ctx, "ok")
	return lobby, nil
}

// FindLobbyByPlayer is the lobby p sits at, or nil.
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

// FindLobbyByCode is the open lobby holding code.
func (m *Manager) FindLobbyByCode(code string) (*Lobby, error) {
	if !validCode(code) {
		return nil, ErrInvalidCode
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	lobby, exists := m.lobbies[code]
	if !exists {
		return nil, ErrLobbyNotFound
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

	m.mu.Lock()
	m.grace.clear(p.ID)
	l, ok := m.playerLobby[p.ID]
	if !ok || l == nil {
		m.mu.Unlock()
		return
	}

	l.mu.Lock()
	l.unsubscribePlayerLocked(p.ID)
	leave, found := l.detachPlayerLocked(p)
	code := l.code
	l.mu.Unlock()
	if found {
		delete(m.playerLobby, p.ID)
	}
	m.mu.Unlock()

	if !found {
		return
	}
	leave.notify(p.ID)
	if leave.closeLobby {
		m.RemoveLobby(code)
	}
}

func (m *Manager) shutdownCtx() context.Context {
	return m.appCtx
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
// still indexed to it.
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
