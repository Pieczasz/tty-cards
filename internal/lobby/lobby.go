// Package lobby seats players at tables, starts a game once every seat is ready, and
// hands the finished match to a db.MatchRepository. It is the only place a db.User
// becomes a game.Player and the only writer of match results.
//
// Lock order is Manager.mu, then Lobby.mu, then the engine's own lock.
package lobby

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/Pieczasz/terminal-card/internal/broadcaster"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/elo"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/observability"
)

// Lobby is one table: a leader, the guests who joined, the settings the leader chose,
// and the game running on it once everyone is ready. All methods are safe for
// concurrent use.
type Lobby struct {
	mu           sync.RWMutex
	manager      *Manager
	broadcaster  *broadcaster.Broadcaster[Event]
	leader       *game.Player
	guests       []*game.Player
	options      *options
	code         string
	state        state
	ready        map[string]bool
	activeEngine *game.Engine
	playerSubs   map[string][]<-chan Event
	// createdAt and startedAt give the two durations worth watching: how long a table
	// waited for players, and how long a hand ran.
	createdAt time.Time
	startedAt time.Time
}

// Event is one change a lobby publishes to its subscribers.
type Event struct {
	Type EventType
	// Engine is the game that just started. It is set on EventGameStarted only.
	Engine *game.Engine
}

// EventType is what changed. The zero value is no event, so a departure that
// announces nothing carries it.
type EventType uint8

const (
	EventPlayersUpdated EventType = iota + 1
	EventSettingsUpdated
	EventLobbyClosed
	EventGameStarted
)

// String is the event's stable label for logs.
func (t EventType) String() string {
	switch t {
	case EventPlayersUpdated:
		return "players_updated"
	case EventSettingsUpdated:
		return "settings_updated"
	case EventLobbyClosed:
		return "lobby_closed"
	case EventGameStarted:
		return "game_started"
	}
	return "none"
}

type state uint

const (
	waiting state = iota
	closed
	inGame
)

// Option configures a lobby at CreateLobby.
type Option func(*options)

type options struct {
	cardGame   string
	maxPlayers int
	isPrivate  bool
	isRanked   bool
}

// WithCardGame picks the game by its display name, the game.Registry key.
func WithCardGame(name string) Option {
	return func(o *options) {
		o.cardGame = name
	}
}

// WithMaxPlayers caps the table, leader included. The default is 4.
func WithMaxPlayers(limit int) Option {
	return func(o *options) {
		o.maxPlayers = limit
	}
}

// WithPrivate keeps the table out of BrowseLobbies. Lobbies are private by default.
func WithPrivate(isPrivate bool) Option {
	return func(o *options) {
		o.isPrivate = isPrivate
	}
}

// WithRanked makes a finished match move Elo. Lobbies are casual by default.
func WithRanked(isRanked bool) Option {
	return func(o *options) {
		o.isRanked = isRanked
	}
}

func setupDefaultOptions() *options {
	return &options{
		maxPlayers: 4,
		isPrivate:  true,
		isRanked:   false,
	}
}

func (l *Lobby) setStateLocked(s state) {
	l.state = s
	l.manager.invalidatePublicCache()
}

func (l *Lobby) broadcastLocked(event Event) {
	if l.broadcaster != nil {
		l.broadcaster.Broadcast(event)
	}
}

// SetPrivate updates lobby visibility. Only the current leader may change settings.
func (l *Lobby) SetPrivate(actor *game.Player, isPrivate bool) error {
	return l.withLeaderSettings(actor, func() error {
		l.options.isPrivate = isPrivate
		return nil
	})
}

// SetRanked updates whether the lobby writes Elo on finish. Leader-only while waiting.
func (l *Lobby) SetRanked(actor *game.Player, isRanked bool) error {
	return l.withLeaderSettings(actor, func() error {
		l.options.isRanked = isRanked
		return nil
	})
}

// SetMaxPlayers updates capacity. Clamped to current roster size and optional game rules bounds.
func (l *Lobby) SetMaxPlayers(actor *game.Player, limit int, rulesMin, rulesMax int) error {
	return l.withLeaderSettings(actor, func() error {
		current := 1 + len(l.guests)
		if limit < current {
			return fmt.Errorf("max players cannot be below current roster (%d)", current)
		}
		if rulesMin > 0 && limit < rulesMin {
			return fmt.Errorf("max players must be at least %d for this game", rulesMin)
		}
		if rulesMax > 0 && limit > rulesMax {
			return fmt.Errorf("max players cannot exceed %d for this game", rulesMax)
		}
		l.options.maxPlayers = limit
		return nil
	})
}

// withLeaderSettings runs mutate under l.mu once the actor is confirmed as leader of a
// Waiting lobby, then broadcasts SETTINGS_UPDATED.
func (l *Lobby) withLeaderSettings(actor *game.Player, mutate func() error) error {
	l.mu.Lock()
	if !l.leader.Equal(actor) {
		l.mu.Unlock()
		return fmt.Errorf("%w change settings", ErrNotLeader)
	}
	if l.state != waiting {
		l.mu.Unlock()
		return errSettingsLocked
	}
	if err := mutate(); err != nil {
		l.mu.Unlock()
		return err
	}
	// A ready was consent to the table as it was. Keeping it lets the leader flip a
	// setting after everyone readied and start a match nobody agreed to.
	clear(l.ready)
	// Visibility is one of these settings: a cached browse would keep offering a table
	// that just went private.
	l.manager.invalidatePublicCache()
	bc := l.broadcaster
	l.mu.Unlock()
	if bc != nil {
		bc.Broadcast(Event{Type: EventSettingsUpdated})
		bc.Broadcast(Event{Type: EventPlayersUpdated})
	}
	return nil
}

// departure is what a roster change leaves to do once every lock is dropped: take the
// seat out of the running game, announce the change, and close the table when nobody
// is left.
type departure struct {
	engine     *game.Engine
	bc         *broadcaster.Broadcaster[Event]
	event      EventType
	closeLobby bool
}

// notify takes playerID out of the engine and publishes the event. Run it with no
// lock held: the engine takes its own.
func (d departure) notify(playerID string) {
	if d.engine != nil {
		d.engine.RemovePlayer(playerID)
	}
	if d.bc != nil && d.event != 0 {
		d.bc.Broadcast(Event{Type: d.event})
	}
}

// detachPlayerLocked mutates roster for a leaving player. Caller holds l.mu.
// ok is false if the player was not in the lobby.
func (l *Lobby) detachPlayerLocked(p *game.Player) (leave departure, ok bool) {
	leave = departure{engine: l.activeEngine, bc: l.broadcaster}

	if l.leader.Equal(p) {
		if len(l.guests) > 0 {
			l.leader = l.guests[0]
			l.guests = l.guests[1:]
			// Same rule as removeGuestAtLocked: the table changed, so nobody is ready.
			clear(l.ready)
			leave.event = EventPlayersUpdated
			return leave, true
		}
		l.setStateLocked(closed)
		leave.event = EventLobbyClosed
		leave.closeLobby = true
		return leave, true
	}

	if idx := slices.IndexFunc(l.guests, func(g *game.Player) bool { return g.Equal(p) }); idx != -1 {
		l.removeGuestAtLocked(idx)
		leave.event = EventPlayersUpdated
		return leave, true
	}
	return departure{}, false
}

// removeGuestAtLocked removes guests[idx] and its subs, and un-readies the table.
// Caller holds l.mu.
//
// A start is only checked on a ready toggle, so dropping the one unready seat used to
// leave a table all-ready with nothing to start it. A ready was also consent to the
// table as it was - the same rule withLeaderSettings applies.
func (l *Lobby) removeGuestAtLocked(idx int) {
	g := l.guests[idx]
	l.unsubscribePlayerLocked(g.ID)
	l.guests = slices.Delete(l.guests, idx, idx+1)
	clear(l.ready)
}

// Subscribe registers a lobby event channel for playerID so disconnect can unsubscribe.
// An error means the caller receives nothing and must say so rather than sitting on a
// silent channel.
func (l *Lobby) Subscribe(playerID string) (<-chan Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.broadcaster == nil || l.state == closed {
		return nil, ErrLobbyClosed
	}
	ch, err := l.broadcaster.Subscribe()
	if err != nil {
		return nil, fmt.Errorf("subscribe to lobby events: %w", err)
	}
	if l.playerSubs == nil {
		l.playerSubs = make(map[string][]<-chan Event)
	}
	l.playerSubs[playerID] = append(l.playerSubs[playerID], ch)
	return ch, nil
}

// Unsubscribe removes a single channel previously returned by Subscribe.
func (l *Lobby) Unsubscribe(playerID string, ch <-chan Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.broadcaster == nil || ch == nil {
		return
	}
	l.broadcaster.Unsubscribe(ch)
	if playerID == "" || l.playerSubs == nil {
		return
	}
	subs := l.playerSubs[playerID]
	for i, sub := range subs {
		if sub == ch {
			l.playerSubs[playerID] = slices.Delete(subs, i, i+1)
			if len(l.playerSubs[playerID]) == 0 {
				delete(l.playerSubs, playerID)
			}
			return
		}
	}
}

func (l *Lobby) unsubscribePlayerLocked(playerID string) {
	if playerID == "" || l.broadcaster == nil || l.playerSubs == nil {
		return
	}
	for _, ch := range l.playerSubs[playerID] {
		l.broadcaster.Unsubscribe(ch)
	}
	delete(l.playerSubs, playerID)
}

// ToggleReady flips p's ready flag, and starts the game from registry once every seat
// is ready. A finished game still holding the table is released first.
func (l *Lobby) ToggleReady(p *game.Player, registry *game.Registry) error {
	// Before taking l.mu: releaseFinishedGame takes it itself, then m.mu via
	// releaseHeldSeats. It is a no-op unless a finished game is holding the table.
	l.releaseFinishedGame()

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == inGame {
		return ErrGameInProgress
	}
	if !l.hasPlayerLocked(p) {
		return ErrNotInLobby
	}

	l.ready[p.ID] = !l.ready[p.ID]

	if !l.allReadyLocked() {
		l.broadcastLocked(Event{Type: EventPlayersUpdated})
		return nil
	}

	if err := l.startGameLocked(registry); err != nil {
		// The ready flip is already committed, so the other clients have to see it even
		// though the start failed, or their rosters disagree with the server.
		l.broadcastLocked(Event{Type: EventPlayersUpdated})
		return err
	}
	return nil
}

func (l *Lobby) allReadyLocked() bool {
	if !l.ready[l.leader.ID] {
		return false
	}
	for _, g := range l.guests {
		if !l.ready[g.ID] {
			return false
		}
	}
	return true
}

// Code is the 8-character code players join by.
func (l *Lobby) Code() string { return l.code }

// GameName is the display name of the game the table plays.
func (l *Lobby) GameName() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.options.cardGame
}

// MaxPlayers is the seat cap, leader included.
func (l *Lobby) MaxPlayers() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.options.maxPlayers
}

// IsRanked is whether a finished match here moves Elo.
func (l *Lobby) IsRanked() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.options.isRanked
}

// IsPrivate is whether the table is kept out of BrowseLobbies.
func (l *Lobby) IsPrivate() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.options.isPrivate
}

// ActiveGame is the running engine, or nil; a reconnecting view lands back through it.
func (l *Lobby) ActiveGame() *game.Engine {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.state != inGame {
		return nil
	}
	return l.activeEngine
}

// Leader is the player in seat 0, who owns the settings.
func (l *Lobby) Leader() *game.Player {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.leader
}

// Guests is every seated player but the leader, in join order. The slice is a copy.
func (l *Lobby) Guests() []*game.Player {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return slices.Clone(l.guests)
}

// CurrentPlayers is how many seats are taken, leader included.
func (l *Lobby) CurrentPlayers() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return 1 + len(l.guests)
}

// HasPlayer is whether p is seated here.
func (l *Lobby) HasPlayer(p *game.Player) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.hasPlayerLocked(p)
}

// IsReady is p's ready flag for the next game.
func (l *Lobby) IsReady(p *game.Player) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.ready[p.ID]
}

// IsLeader is whether p holds seat 0.
func (l *Lobby) IsLeader(p *game.Player) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.leader.Equal(p)
}

// Rating is p's rating in gameName as the lobby matches on it: missing and zero both
// read as the starting rating. Views show this, so a seat reads the same everywhere.
func Rating(p *game.Player, gameName string) uint32 {
	if p == nil {
		return elo.ToUint32(elo.DefaultRating)
	}
	return ratingFor(p.Ratings, gameName)
}

// ratingFor is a player's rating in gameName. Missing and zero both mean unrated: no
// stored rating can be zero (elo.MinRating is the floor), so a zero is a map that was
// never filled in, and it is matched at the starting rating like any newcomer.
func ratingFor(ratings map[string]uint32, gameName string) uint32 {
	if rating := ratings[gameName]; rating != 0 {
		return rating
	}
	return elo.ToUint32(elo.DefaultRating)
}

// averageEloLocked is the table's average rating in gameName, or the starting rating
// when the game is unnamed. Caller holds l.mu.
func (l *Lobby) averageEloLocked(gameName string) uint32 {
	if gameName == "" {
		return elo.ToUint32(elo.DefaultRating)
	}
	totalElo := ratingFor(l.leader.Ratings, gameName)
	count := uint32(1)
	for _, g := range l.guests {
		totalElo += ratingFor(g.Ratings, gameName)
		count++
	}
	return totalElo / count
}

func (l *Lobby) addGuest(p *game.Player) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != waiting {
		return errNotAccepting
	}

	if 1+len(l.guests) >= l.options.maxPlayers {
		return ErrLobbyFull
	}

	if l.leader.Equal(p) {
		return errors.New("player is already the leader of this lobby")
	}
	for _, g := range l.guests {
		if g.Equal(p) {
			return errors.New("player is already in this lobby")
		}
	}

	l.guests = append(l.guests, p)
	l.broadcastLocked(Event{Type: EventPlayersUpdated})
	return nil
}

func (l *Lobby) hasPlayerLocked(p *game.Player) bool {
	if l.leader.Equal(p) {
		return true
	}
	for _, g := range l.guests {
		if g.Equal(p) {
			return true
		}
	}
	return false
}

// startGameLocked starts a match. Caller must hold l.mu.
func (l *Lobby) startGameLocked(registry *game.Registry) error {
	if l.state != waiting {
		return errors.New("lobby is not in waiting state")
	}
	if !l.allReadyLocked() {
		return errors.New("not all players are ready")
	}
	if l.options.cardGame == "" {
		return errors.New("no card game selected")
	}

	rules, err := registry.Create(l.options.cardGame)
	if err != nil {
		return fmt.Errorf("create game rules: %w", err)
	}

	totalPlayers := len(l.guests) + 1
	if totalPlayers < rules.MinPlayers() {
		return fmt.Errorf("need at least %d players to start", rules.MinPlayers())
	}
	if totalPlayers > rules.MaxPlayers() {
		return errTooManyPlayers
	}

	players := slices.Concat([]*game.Player{l.leader}, l.guests)
	engine := game.NewEngine(rules, players, rules.InitialDeck())

	if err := engine.Start(); err != nil {
		return fmt.Errorf("start game engine: %w", err)
	}

	// Before watchGameLocked, which snapshots it for the finalize.
	l.startedAt = time.Now()
	// The slug, not the display name, is what the match is persisted under. Create
	// above already proved the module is registered.
	mod, _ := registry.Module(l.options.cardGame)
	l.watchGameLocked(engine, db.GameRef{Slug: mod.Slug, Name: l.options.cardGame})

	l.setStateLocked(inGame)
	l.activeEngine = engine
	clear(l.ready)

	observability.GameStarted(context.Background(), mod.Slug, l.options.isRanked)
	if !l.createdAt.IsZero() {
		observability.LobbyStarted(context.Background(), mod.Slug, time.Since(l.createdAt))
	}

	l.broadcastLocked(Event{Type: EventGameStarted, Engine: engine})

	return nil
}

// releaseFinishedGameLocked returns a finished lobby to Waiting and hands back the
// engine to close. Caller holds l.mu; closing and releaseHeldSeats are the unlocked
// caller's job (lock order is manager then lobby).
func (l *Lobby) releaseFinishedGameLocked() *game.Engine {
	if l.state != inGame || l.activeEngine == nil || !l.activeEngine.IsFinished() {
		return nil
	}
	finished := l.activeEngine
	l.setStateLocked(waiting)
	l.activeEngine = nil
	clear(l.ready)
	return finished
}

// releaseFinishedGame is releaseFinishedGameLocked for a caller holding no lock, and
// announces the reopened table.
func (l *Lobby) releaseFinishedGame() {
	l.mu.Lock()
	finished := l.releaseFinishedGameLocked()
	bc := l.broadcaster
	l.mu.Unlock()

	if finished == nil {
		return
	}
	finished.Close()
	l.manager.releaseHeldSeats(l)
	if bc != nil {
		bc.Broadcast(Event{Type: EventPlayersUpdated})
	}
}

// kickableGuestLocked returns the guest index host may remove. Caller holds l.mu.
func (l *Lobby) kickableGuestLocked(host, target *game.Player) (int, error) {
	switch {
	case l.state == closed:
		return -1, ErrLobbyClosed
	// A leader who can kick mid-hand can farm Elo: drop whoever is winning, let the
	// engine finish the match without them, and take the rating.
	case l.state == inGame:
		return -1, errKickInGame
	case !l.leader.Equal(host):
		return -1, fmt.Errorf("%w kick players", ErrNotLeader)
	case l.leader.Equal(target):
		return -1, errors.New("cannot kick the lobby leader")
	}
	idx := slices.IndexFunc(l.guests, func(g *game.Player) bool { return g.Equal(target) })
	if idx == -1 {
		return -1, ErrNotInLobby
	}
	return idx, nil
}
