package lobby

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/Pieczasz/terminal-card/internal/broadcaster"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/elo"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/observability"
)

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

type Event struct {
	Type    string
	Payload any
}

// Lobby event type constants shared with TUI subscribers.
const (
	EventPlayersUpdated  = "PLAYERS_UPDATED"
	EventSettingsUpdated = "SETTINGS_UPDATED"
	EventLobbyClosed     = "LOBBY_CLOSED"
	EventGameStarted     = "GAME_STARTED"
)

type state uint

const (
	Waiting state = iota
	Closed
	InGame
)

type Option func(*options)

type options struct {
	cardGame   string
	maxPlayers int
	isPrivate  bool
	isRanked   bool
}

func WithCardGame(name string) Option {
	return func(o *options) {
		o.cardGame = name
	}
}

func WithMaxPlayers(limit int) Option {
	return func(o *options) {
		o.maxPlayers = limit
	}
}

func WithPrivate(isPrivate bool) Option {
	return func(o *options) {
		o.isPrivate = isPrivate
	}
}

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

// SetCardGame updates the selected game. Only the leader may change it while waiting.
func (l *Lobby) SetCardGame(actor *game.Player, name string) error {
	return l.withLeaderSettings(actor, func() error {
		l.options.cardGame = name
		return nil
	})
}

// withLeaderSettings runs mutate under l.mu once the actor is confirmed as leader of a
// Waiting lobby, then broadcasts SETTINGS_UPDATED.
func (l *Lobby) withLeaderSettings(actor *game.Player, mutate func() error) error {
	l.mu.Lock()
	if !l.leader.Equal(actor) {
		l.mu.Unlock()
		return errors.New("only the leader can change settings")
	}
	if l.state != Waiting {
		l.mu.Unlock()
		return errors.New("cannot change settings while a game is in progress")
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

// detachPlayerLocked mutates roster for a leaving player. Caller holds l.mu.
// Returns false in ok if the player was not in the lobby.
func (l *Lobby) detachPlayerLocked(p *game.Player) (
	engine *game.Engine,
	bc *broadcaster.Broadcaster[Event],
	eventType string,
	shouldClose, ok bool,
) {
	engine = l.activeEngine
	bc = l.broadcaster

	if l.leader.Equal(p) {
		if len(l.guests) > 0 {
			l.leader = l.guests[0]
			l.guests = l.guests[1:]
			delete(l.ready, p.ID)
			return engine, bc, EventPlayersUpdated, false, true
		}
		l.setStateLocked(Closed)
		return engine, bc, EventLobbyClosed, true, true
	}

	if idx := slices.IndexFunc(l.guests, func(g *game.Player) bool { return g.Equal(p) }); idx != -1 {
		l.removeGuestAtLocked(idx)
		return engine, bc, EventPlayersUpdated, false, true
	}
	return nil, nil, "", false, false
}

// removeGuestAtLocked removes guests[idx] and clears ready/subs. Caller holds l.mu.
func (l *Lobby) removeGuestAtLocked(idx int) {
	g := l.guests[idx]
	l.unsubscribePlayerLocked(g.ID)
	l.guests = slices.Delete(l.guests, idx, idx+1)
	delete(l.ready, g.ID)
}

func notifyEngineAndBroadcast(engine *game.Engine, bc *broadcaster.Broadcaster[Event], playerID, eventType string) {
	if engine != nil {
		engine.RemovePlayer(playerID)
	}
	if bc != nil && eventType != "" {
		bc.Broadcast(Event{Type: eventType})
	}
}

// Subscribe registers a lobby event channel for playerID so disconnect can unsubscribe.
// An error means the caller receives nothing and must say so rather than sitting on a
// silent channel.
func (l *Lobby) Subscribe(playerID string) (<-chan Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.broadcaster == nil || l.state == Closed {
		return nil, errors.New("lobby is closed")
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

func (l *Lobby) ToggleReady(p *game.Player, registry *game.Registry) error {
	l.mu.Lock()
	if l.state == InGame {
		// releaseFinishedGame takes l.mu itself, then m.mu via releaseHeldSeats.
		// Calling it while we hold l.mu deadlocks.
		l.mu.Unlock()
		l.releaseFinishedGame()
		l.mu.Lock()
		if l.state == InGame {
			l.mu.Unlock()
			return errors.New("game is already in progress")
		}
	}
	defer l.mu.Unlock()

	if !l.hasPlayerLocked(p) {
		return errors.New("player not in lobby")
	}

	l.ready[p.ID] = !l.ready[p.ID]

	if !l.allReadyLocked() {
		l.broadcastLocked(Event{Type: EventPlayersUpdated})
		return nil
	}

	if _, err := l.startGameLocked(registry); err != nil {
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

func (l *Lobby) Code() string { return l.code }

func (l *Lobby) Broadcaster() *broadcaster.Broadcaster[Event] {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.broadcaster
}

func (l *Lobby) GameName() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.options.cardGame
}

func (l *Lobby) MaxPlayers() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.options.maxPlayers
}

func (l *Lobby) IsRanked() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.options.isRanked
}

func (l *Lobby) IsPrivate() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.options.isPrivate
}

// ActiveGame is the running engine, or nil; a reconnecting view lands back through it.
func (l *Lobby) ActiveGame() *game.Engine {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != InGame {
		return nil
	}
	return l.activeEngine
}

func (l *Lobby) IsWaiting() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state == Waiting
}

func (l *Lobby) Leader() *game.Player {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.leader
}

func (l *Lobby) Guests() []*game.Player {
	l.mu.RLock()
	defer l.mu.RUnlock()
	guests := slices.Clone(l.guests)
	return guests
}

func (l *Lobby) CurrentPlayers() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return 1 + len(l.guests)
}

func (l *Lobby) HasPlayer(p *game.Player) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.hasPlayerLocked(p)
}

func (l *Lobby) IsReady(p *game.Player) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.ready[p.ID]
}

func (l *Lobby) IsLeader(p *game.Player) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.leader.Equal(p)
}

func playerEloForGame(p *game.Player, gameName string) uint32 {
	if p == nil {
		return elo.ToUint32(elo.DefaultRating)
	}
	if rating, ok := p.Ratings[gameName]; ok {
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
	totalElo := playerEloForGame(l.leader, gameName)
	count := uint32(1)
	for _, g := range l.guests {
		totalElo += playerEloForGame(g, gameName)
		count++
	}
	return totalElo / count
}

func (l *Lobby) addGuest(p *game.Player) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.state != Waiting {
		return errors.New("lobby is not accepting players")
	}

	if 1+len(l.guests) >= l.options.maxPlayers {
		return errors.New("this lobby is full")
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
func (l *Lobby) startGameLocked(registry *game.Registry) (*game.Engine, error) {
	if l.state != Waiting {
		return nil, errors.New("lobby is not in waiting state")
	}
	if !l.allReadyLocked() {
		return nil, errors.New("not all players are ready")
	}
	if l.options.cardGame == "" {
		return nil, errors.New("no card game selected")
	}

	rules, err := registry.Create(l.options.cardGame)
	if err != nil {
		return nil, fmt.Errorf("failed to create game rules: %w", err)
	}

	totalPlayers := len(l.guests) + 1
	if totalPlayers < rules.MinPlayers() {
		return nil, fmt.Errorf("need at least %d players to start", rules.MinPlayers())
	}
	if totalPlayers > rules.MaxPlayers() {
		return nil, errors.New("too many players for this game")
	}

	players := slices.Concat([]*game.Player{l.leader}, l.guests)
	engine := game.NewEngine(rules, players, rules.InitialDeck())

	if err := engine.Start(); err != nil {
		return nil, fmt.Errorf("failed to start game engine: %w", err)
	}

	// Before watchGameLocked, which snapshots it for the finalize.
	l.startedAt = time.Now()
	// The slug, not the display name, is what the match is persisted under. Create
	// above already proved the module is registered.
	mod, _ := registry.Module(l.options.cardGame)
	l.watchGameLocked(engine, db.GameRef{Slug: mod.Slug, Name: l.options.cardGame})

	l.setStateLocked(InGame)
	l.activeEngine = engine
	clear(l.ready)

	observability.GameStarted(context.Background(), l.options.cardGame, l.options.isRanked)
	if !l.createdAt.IsZero() {
		observability.LobbyStarted(context.Background(), l.options.cardGame, time.Since(l.createdAt))
	}

	l.broadcastLocked(Event{
		Type:    EventGameStarted,
		Payload: engine,
	})

	return engine, nil
}

// watchGameLocked starts the goroutine that persists the result and counts engine
// events. It is the only thing that persists a match, so a failed subscribe costs the
// players their history and Elo - the engine's len(players)+8 broadcaster exists so
// that cannot happen, and it is logged loudly if it ever does. Caller holds l.mu.
//
// It subscribes whether or not a match repository is configured: this goroutine is
// also the only consumer of EventPlayerIdle, so skipping it leaves an idle-removed
// seat on the roster with the engine no longer holding it, and the table can never
// reach all-ready again. finalizeFinishedGame already no-ops without a repository.
//
// The finalize snapshot is taken here, not when the game ends: by then the lobby may
// have reopened and been reconfigured, and the result would be written under the new
// ranked flag, the new game, and the next hand's start time.
func (l *Lobby) watchGameLocked(engine *game.Engine, ref db.GameRef) {
	if l.manager == nil {
		return
	}
	ch, err := engine.Broadcaster().Subscribe()
	if err != nil {
		observability.SubscribeFailure(l.manager.shutdownCtx(), "game")
		slog.ErrorContext(l.manager.shutdownCtx(),
			"cannot watch game for completion; result will not be persisted",
			"error", err, "lobby", l.code, "game", ref.Slug)
		return
	}
	req := finalizeRequest{
		lobbyCode: l.code,
		game:      ref,
		isRanked:  l.options.isRanked,
		startedAt: l.startedAt,
	}
	go func() {
		defer engine.Broadcaster().Unsubscribe(ch)
		l.handleBroadcasterEvents(ch, engine, req)
	}()
}

func (l *Lobby) handleBroadcasterEvents(ch <-chan game.Event, engine *game.Engine, req finalizeRequest) {
	ctx := l.manager.shutdownCtx()
	gameName := req.game.Name
	defer func() {
		if n := engine.Broadcaster().Dropped(); n > 0 {
			observability.BroadcastDropped(ctx, "game", n)
		}
	}()

	for event := range ch {
		switch event.Type {
		case game.EventTurnTimedOut:
			observability.TurnTimedOut(ctx, gameName)
		case game.EventPlayerIdle:
			observability.PlayerIdleRemoved(ctx, gameName)
			// The engine took the seat, so the roster follows, or a player kicked for
			// idling reconnects into a lobby whose game no longer has them. Equal falls
			// back to ID, so a zero-UserID stub still matches.
			l.manager.LeaveLobby(&game.Player{ID: event.PlayerID})
		case game.EventGameEnded:
			// Reopen before persist: a 15s write must not pin InGame while the TUI
			// is already back in the lobby and ready-ing the next hand.
			l.releaseFinishedGame()
			l.requestFinalize(engine, event.Reason, req)
			return
		default:
			// Turn and action events are the views' business; the lobby counts nothing.
		}
	}

	// The feed ending is not proof the match did not finish: the broadcaster is
	// latest-wins and can drop EventGameEnded, and RemoveLobby closes the feed from
	// under this goroutine.
	if engine.IsFinished() {
		l.releaseFinishedGame()
		l.requestFinalize(engine, game.EndReasonUnknown, req)
	}
}

// requestFinalize hands the finished table to Manager for persistence. req was
// snapshotted by watchGameLocked when this game started, so a lobby that has since
// reopened cannot rewrite what the finished match is recorded as.
func (l *Lobby) requestFinalize(engine *game.Engine, reason game.EndReason, req finalizeRequest) {
	if l.manager == nil {
		return
	}
	l.manager.finalizeFinishedGame(req, engine, reason)
}

// releaseFinishedGameLocked returns a finished lobby to Waiting and hands back the
// engine to close. Caller holds l.mu; closing and releaseHeldSeats are the unlocked
// caller's job (lock order is manager then lobby).
func (l *Lobby) releaseFinishedGameLocked() *game.Engine {
	if l.state != InGame || l.activeEngine == nil || !l.activeEngine.IsFinished() {
		return nil
	}
	finished := l.activeEngine
	l.setStateLocked(Waiting)
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
