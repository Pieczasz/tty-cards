// Package game is the rules engine: the Engine that owns a table's State under one
// lock, the Rules contract a card game implements, the turn clock, and the event feed
// views read. It knows nothing about the database, the TUI or routes.
package game

import (
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/Pieczasz/tty-cards/internal/broadcaster"
	"github.com/Pieczasz/tty-cards/internal/deck"
)

var (
	errClosed = errors.New("game is closed")
)

type Engine struct {
	mu          sync.Mutex
	state       *State
	broadcaster *broadcaster.Broadcaster[Event]
	closed      bool
	clock       turnClock
	endReason   EndReason
	// winner is the engine's to set, at the end: no rules hook can reach it.
	winner *Player
	// onIdleRemoved is set once at construction, so it is read without the lock.
	onIdleRemoved func(playerID string)
}

// turnClock is the engine's per-turn timer and idle count, guarded by Engine.mu.
type turnClock struct {
	defaultLength time.Duration
	seq           uint64
	timer         *time.Timer
	deadline      time.Time
	missed        map[string]int
	playerID      string
	length        time.Duration
	missCharged   bool
}

// EngineOption configures NewEngine.
type EngineOption func(*Engine)

// WithLogger is the logger the engine and its rules write through (State.Logger).
// The lobby passes one carrying its code and the game slug; nil keeps slog.Default().
func WithLogger(l *slog.Logger) EngineOption {
	return func(e *Engine) {
		e.state.logger = l
	}
}

// WithTurnTimeout sets the default turn length. Zero or less is the clock's one off
// switch: every rules set has a TimeoutAction, so the engine never decides on its
// own that a table goes without one.
func WithTurnTimeout(d time.Duration) EngineOption {
	return func(e *Engine) {
		e.clock.defaultLength = d
	}
}

// WithOnIdleRemoved is told each seat the turn clock takes for idling - unlike
// EventPlayerIdle on the latest-wins feed, it cannot be dropped. It runs on the timer
// goroutine after the engine lock is released, so it may take locks that order before
// Engine.mu; RemovePlayer never calls it, because its caller may hold them.
func WithOnIdleRemoved(fn func(playerID string)) EngineOption {
	return func(e *Engine) {
		e.onIdleRemoved = fn
	}
}

func NewEngine(rules Rules, players []*Player, opts ...EngineOption) *Engine {
	seats := make([]*Player, len(players))
	for i, p := range players {
		if p != nil {
			seat := *p
			seat.Cards = nil
			// Ratings are the lobby's, read off its own roster; a map shared with it
			// would be one the lobby rewrites while the table runs.
			seat.Ratings = nil
			seats[i] = &seat
		}
	}
	e := &Engine{
		state:       NewState(rules, seats, nil),
		broadcaster: broadcaster.New[Event](len(players) + 8),
		clock: turnClock{
			defaultLength: DefaultTurnTimeout,
			missed:        make(map[string]int, len(players)),
		},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Start opens the table: it picks the first seat and lets the rules set the game up,
// the deal included. A table whose rules refuse it stays Waiting.
func (e *Engine) Start() (err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// OnGameStart is rules code running under the lock on the caller's goroutine,
	// ToggleReady's, which is counted in the drain. The table never reached Playing, so
	// it is refused rather than ended: it stays Waiting and the lobby can try again.
	defer func() {
		if r := recover(); r != nil {
			e.state.Logger().Error("rules panicked starting the table",
				"panic", r, "stack", string(debug.Stack()))
			err = errors.New("the game hit an internal error and could not start")
		}
	}()

	if e.closed {
		return errClosed
	}
	if e.state.Phase != Waiting {
		return errors.New("game already started")
	}
	if len(e.state.Players) == 0 {
		return errors.New("cannot start game with no players")
	}

	e.state.CurrentTurn = rand.IntN(len(e.state.Players)) //nolint:gosec // G404: not a secret

	if err := e.state.Rules.OnGameStart(e.state); err != nil {
		return fmt.Errorf("set up game: %w", err)
	}
	e.state.Phase = Playing
	e.settleTurnLocked()
	e.broadcaster.Broadcast(Event{Type: EventGameStarted})
	return nil
}

// SubmitAction applies action for playerID, who must be on turn. Acting for yourself
// clears your missed-turn count.
func (e *Engine) SubmitAction(playerID string, action Action) (err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			e.endOnRulesPanicLocked(r)
			err = errors.New("the game hit an internal error and has ended")
		}
	}()
	current, err := e.checkTurnLocked(playerID, action)
	if err != nil {
		return err
	}
	delete(e.clock.missed, playerID)
	return e.applyActionLocked(current, action)
}

// RemovePlayer takes playerID's seat, running the rules' PlayerLeaveHandler, and ends
// the table when the leave decides it. An unknown seat or a finished table is a no-op.
// A rules panic ends the table rather than escaping: the lobby calls this from its
// disconnect-grace timer, where nothing above would recover it.
func (e *Engine) RemovePlayer(playerID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			e.endOnRulesPanicLocked(r)
		}
	}()
	e.removePlayerLocked(playerID)
}

// Close stops the turn clock and releases the broadcaster. Safe to call repeatedly; the
// closed flag stops a concurrently-resolved timeout re-arming a timer afterwards.
func (e *Engine) Close() {
	e.mu.Lock()
	e.closed = true
	e.stopTurnTimerLocked()
	e.mu.Unlock()
	e.broadcaster.Close()
}

// Subscribe joins the table's event feed without handing out the broadcaster, which
// would let the caller Broadcast or Close the feed for every seat.
func (e *Engine) Subscribe() (<-chan Event, error) {
	ch, err := e.broadcaster.Subscribe()
	if err != nil {
		return nil, fmt.Errorf("subscribe to game events: %w", err)
	}
	return ch, nil
}

// Unsubscribe leaves the feed Subscribe joined. Safe after Close.
func (e *Engine) Unsubscribe(ch <-chan Event) {
	e.broadcaster.Unsubscribe(ch)
}

// Dropped is how many events the latest-wins feed has discarded for slow readers.
func (e *Engine) Dropped() int64 {
	return e.broadcaster.Dropped()
}

// SubscriberCount is how many feeds Subscribe has open, which is how a test proves a
// view gave its slot back. The broadcaster itself stays private.
func (e *Engine) SubscriberCount() int {
	return e.broadcaster.Len()
}

// Snapshot is the table's public state at this moment.
func (e *Engine) Snapshot() StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshotLocked()
}

// Frame reads everything a view renders in one lock hold. fn may be nil; it receives
// the live *State under the contract BoundEngine.Frame documents.
func (e *Engine) Frame(playerID string, fn func(*State)) (StateSnapshot, []deck.Card, time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()

	snap := e.snapshotLocked()
	var hand []deck.Card
	for _, p := range e.state.Players {
		if p.ID == playerID {
			hand = slices.Clone(p.Cards)
			break
		}
	}
	var remaining time.Duration
	if !e.clock.deadline.IsZero() {
		remaining = max(time.Until(e.clock.deadline), 0)
	}
	if fn != nil {
		fn(e.state)
	}
	return snap, hand, remaining
}

// CurrentPlayerID is a test seam; views read the seat on turn from their Frame snapshot.
func (e *Engine) CurrentPlayerID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	current := e.currentPlayerLocked()
	if current == nil {
		return ""
	}
	return current.ID
}

// EndReason is why the table ended, or EndReasonUnknown while it has not. It is kept
// for a reader that missed EventGameEnded: an end on a closed engine broadcasts
// nothing.
func (e *Engine) EndReason() EndReason {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.endReason
}

// IsFinished reports whether the table has ended, for any reason.
func (e *Engine) IsFinished() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.Phase == Finished
}

// Standings is the rules' finishing order, best first, in one lock hold, each seat a
// copy of its identity and marked if it left.
//
// A rules panic returns nil, which finalize drops as unrecordable: the lobby's
// finalize goroutine has nothing above it to recover.
func (e *Engine) Standings() (standings []Standing) {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			e.state.Logger().Error("rules panicked computing standings", "panic", r, "stack", string(debug.Stack()))
			standings = nil
		}
	}()

	ranked := e.state.Rules.Standings(e.state)
	standings = make([]Standing, len(ranked))
	for i, s := range ranked {
		standings[i] = Standing{Place: s.Place}
		if p := s.Player; p != nil {
			standings[i].Player = &Player{ID: p.ID, UserID: p.UserID, Name: p.Name}
			standings[i].Left = slices.ContainsFunc(e.state.LeftPlayers, func(l *Player) bool { return l.ID == p.ID })
		}
	}
	return standings
}

// WithState runs fn with the engine lock held. fn must not call back into the engine:
// every Engine method takes the same lock, so it would deadlock. It is how a test
// reads or rigs a table; views read through Frame, and finalize through Standings.
func (e *Engine) WithState(fn func(state *State)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(e.state)
}

// checkTurnLocked is everything an action is refused on before it touches the state:
// a live table, playerID on turn, and the rules' ValidateAction. It returns the seat on
// turn.
func (e *Engine) checkTurnLocked(playerID string, action Action) (*Player, error) {
	if e.closed {
		return nil, errClosed
	}
	if e.state.Phase != Playing {
		return nil, errors.New("game not in playing phase")
	}
	current := e.currentPlayerLocked()
	if current == nil || current.ID != playerID {
		return nil, errors.New("wait for your turn to perform an action")
	}
	// The rules' own refusal ("must follow suit") is written for the player, and the
	// views show it as it is.
	if err := e.state.Rules.ValidateAction(e.state, action); err != nil {
		return nil, err //nolint:wrapcheck // the rules' message is the one the player reads
	}
	return current, nil
}

func (e *Engine) applyActionLocked(current *Player, action Action) error {
	// ApplyAction is the whole move, the rules' own state machine included (poker
	// settles bets, deals the next street, picks the next actor), so it runs before
	// any broadcast and clients never see a half-applied move. A failure may have left
	// the state half-applied, so, like a rules panic, it ends the table without asking
	// the rules to rank anyone on it - and ends it, with the broadcast, or every client
	// sits on a frame that never updates and the lobby never records the match.
	if err := e.state.Rules.ApplyAction(e.state, action); err != nil {
		e.endGameLocked(nil, EndReasonRulesError)
		return fmt.Errorf("apply action: %w", err)
	}

	// One cue per settled move - EventTurnAdvanced, or EventGameEnded on a win: every
	// view re-reads its whole frame on any event, so a second one only doubles that.
	if e.state.Rules.CheckWinCondition(e.state) {
		e.finishGameLocked(current, EndReasonWin)
		return nil
	}

	e.advanceTurnLocked()
	e.broadcaster.Broadcast(Event{Type: EventTurnAdvanced})
	return nil
}

// finishGameLocked settles the winner from the rules standings and ends the table;
// fallback names the winner when the rules rank nobody. Caller holds e.mu.
func (e *Engine) finishGameLocked(fallback *Player, reason EndReason) {
	// Finished and the clock stopped before the rules are asked anything: a panic in
	// Standings then leaves a table that cannot auto-play, for endOnRulesPanicLocked
	// to announce.
	e.state.Phase = Finished
	e.stopTurnTimerLocked()

	winner := fallback
	switch standings := e.state.Rules.Standings(e.state); {
	case len(standings) > 0:
		winner = standings[0].Player
	case fallback == nil && len(e.state.Players) > 0:
		winner = e.state.Players[0]
	}
	e.endGameLocked(winner, reason)
}

// endGameLocked is the one way a table ends: Finished, the clock stopped (one left
// running would auto-play into a finished game), the winner recorded and the end
// announced. Caller holds e.mu.
func (e *Engine) endGameLocked(winner *Player, reason EndReason) {
	e.state.Phase = Finished
	e.stopTurnTimerLocked()
	e.winner = winner
	e.endReason = reason

	winnerID := ""
	if winner != nil {
		winnerID = winner.ID
	}
	e.broadcaster.Broadcast(Event{
		Type:     EventGameEnded,
		PlayerID: winnerID,
		Reason:   reason,
	})
}

// removePlayerLocked is the body of RemovePlayer. Caller must hold e.mu.
func (e *Engine) removePlayerLocked(playerID string) {
	if e.state.Phase == Finished {
		return
	}

	playerIndex := slices.IndexFunc(e.state.Players, func(p *Player) bool {
		return p.ID == playerID
	})
	if playerIndex == -1 {
		return
	}

	// The leave hooks read the Extra OnGameStart writes: on a table that never
	// started a seat leaving is only a seat leaving.
	h, hooked := e.state.Rules.(PlayerLeaveHandler)
	hooked = hooked && e.state.Phase == Playing
	if hooked {
		h.OnPlayerLeave(e.state, playerID)
	}

	removedPlayer := e.state.Players[playerIndex]
	e.state.LeftPlayers = append(e.state.LeftPlayers, removedPlayer)

	e.state.Players = slices.Delete(e.state.Players, playerIndex, playerIndex+1)
	delete(e.clock.missed, playerID)

	if e.state.CurrentTurn > playerIndex {
		e.state.CurrentTurn--
	}
	// Before AfterPlayerRemoved, and it has to stay that way: the poker hook indexes
	// Players by the clamped cursor and no longer guards the range itself.
	e.clampTurnLocked()

	outcome := LeaveSettled
	if hooked {
		outcome = h.AfterPlayerRemoved(e.state, playerIndex)
	}

	e.broadcaster.Broadcast(Event{Type: EventPlayerLeft, PlayerID: playerID})
	e.settleAfterLeaveLocked(outcome)
}

// settleAfterLeaveLocked decides what a leave leaves behind: an abandoned table, an
// interrupted match, a win, a forfeit to the last seat, or the next turn. Caller holds
// e.mu.
func (e *Engine) settleAfterLeaveLocked(outcome LeaveOutcome) {
	// A table that never started cannot be won: "any hand empty wins" would report a
	// bogus win over undealt hands.
	if e.state.Phase != Playing {
		return
	}

	switch {
	case len(e.state.Players) == 0:
		e.endGameLocked(nil, EndReasonAbandoned)
	case outcome == LeaveInterruptsMatch:
		e.finishGameLocked(nil, EndReasonInterrupted)
	case e.state.Rules.CheckWinCondition(e.state):
		e.finishGameLocked(nil, EndReasonWin)
	case len(e.state.Players) == 1 && outcome != LeaveKeepsHandOpen:
		e.endGameLocked(e.state.Players[0], EndReasonForfeit)
	default:
		e.settleTurnLocked()
		e.broadcaster.Broadcast(Event{Type: EventTurnAdvanced})
	}
}

func (e *Engine) snapshotLocked() StateSnapshot {
	state := e.state
	var snap StateSnapshot
	snap.Phase = state.Phase
	snap.EndReason = e.endReason
	if state.Deck != nil {
		snap.DeckSize = state.Deck.Size()
	}
	if state.Discard != nil {
		if top, ok := state.Discard.Peek(); ok {
			snap.TopDiscard = top
		}
	}
	if e.winner != nil {
		snap.WinnerName = e.winner.DisplayName()
	}
	if current := e.currentPlayerLocked(); current != nil {
		snap.CurrentPlayerName = current.DisplayName()
		snap.CurrentPlayerID = current.ID
	}
	snap.Players = make([]PlayerSnapshot, 0, len(state.Players))
	for _, p := range state.Players {
		snap.Players = append(snap.Players, PlayerSnapshot{
			ID:       p.ID,
			Name:     p.DisplayName(),
			HandSize: len(p.Cards),
		})
	}
	return snap
}

// currentPlayerLocked is the seat State.CurrentTurn points at, or nil. Caller holds e.mu.
func (e *Engine) currentPlayerLocked() *Player {
	if e.state.CurrentTurn < 0 || e.state.CurrentTurn >= len(e.state.Players) {
		return nil
	}
	return e.state.Players[e.state.CurrentTurn]
}

// advanceTurnLocked moves the cursor on after an accepted action: State.OverrideNextTurn
// wins, else the next seat.
func (e *Engine) advanceTurnLocked() {
	if e.state.OverrideNextTurn == nil {
		e.state.CurrentTurn++
	}
	e.settleTurnLocked()
}

// settleTurnLocked honors State.OverrideNextTurn, else leaves State.CurrentTurn where it
// is, then clamps the cursor and arms the clock for the seat it lands on.
func (e *Engine) settleTurnLocked() {
	if e.state.OverrideNextTurn != nil {
		e.state.CurrentTurn = *e.state.OverrideNextTurn
		e.state.OverrideNextTurn = nil
	}
	e.clampTurnLocked()
	e.armTurnTimerLocked()
}

// clampTurnLocked forces State.CurrentTurn into [0, len(Players)) so a stale index
// from a leave handler or a rules override cannot name a seat that is gone.
func (e *Engine) clampTurnLocked() {
	e.state.CurrentTurn = SeatAt(e.state.CurrentTurn, len(e.state.Players))
}
