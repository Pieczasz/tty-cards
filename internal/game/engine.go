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

	"github.com/Pieczasz/terminal-card/internal/broadcaster"
	"github.com/Pieczasz/terminal-card/internal/deck"
)

// ErrInvalidState means State.Extra is not the type the Rules put there: a wiring bug,
// not a player mistake.
var ErrInvalidState = errors.New("invalid state type")

var (
	// errClosed is any call on an engine Close already ended.
	errClosed = errors.New("game is closed")
	// errNoGame is a call on a nil BoundEngine.
	errNoGame = errors.New("no active game")
	// errStaleTurn is an auto-play that lost its turn between resolveTurnTimeout
	// dropping the lock and the submit re-acquiring it: a non-event, not a failure.
	errStaleTurn = errors.New("turn already settled")
	// errActionRefused is an auto-play ValidateAction refused: a rules bug, logged and
	// re-armed, where an apply failure has already ended the game.
	errActionRefused = errors.New("auto-play refused")
)

// Engine owns one mutex covering its clock fields and the State: they are always read
// together, and a second lock would only add orderings to get wrong.
type Engine struct {
	mu          sync.Mutex
	state       *State
	broadcaster *broadcaster.Broadcaster[Event]
	closed      bool
	clock       turnClock
}

// turnClock is the engine's per-turn timer and idle count, guarded by Engine.mu.
type turnClock struct {
	defaultLength time.Duration
	// seq fences stale timers: every stop bumps it, and a timer only acts for the
	// generation it was armed in.
	seq      uint64
	timer    *time.Timer
	deadline time.Time
	missed   map[string]int
	// The seat and length the running deadline was armed for, and whether that
	// seat-turn has been charged its miss: armTurnTimerLocked's continuation check.
	playerID    string
	length      time.Duration
	missCharged bool
}

// EngineOption configures NewEngine.
type EngineOption func(*Engine)

// WithTurnTimeout sets the default turn length; zero or less disables the clock.
func WithTurnTimeout(d time.Duration) EngineOption {
	return func(e *Engine) {
		e.clock.defaultLength = d
	}
}

// NewEngine seats copies of players, not the values themselves: a lobby hands the same
// *Player to every engine it starts, and shared seats would let the next engine deal
// into hands a finished one's viewers still read under a different lock. Nothing
// compares seats by pointer (the lobby uses Player.Equal), and Ratings stays shared
// because nothing writes it once the seat is taken. NewState keeps aliasing the
// values, which is what rules tests building a State by hand rely on.
func NewEngine(rules Rules, players []*Player, cards []deck.Card, opts ...EngineOption) *Engine {
	seats := make([]*Player, len(players))
	for i, p := range players {
		if p != nil {
			seat := *p
			seat.Cards = nil
			seats[i] = &seat
		}
	}
	e := &Engine{
		state: NewState(rules, seats, cards),
		// The argument is the subscriber cap, not a buffer size (that is fixed):
		// headroom above the seat count for non-player subscribers (the
		// ranked-finalize watcher) and for a reconnect overlapping the seat it
		// replaces. Too small and a real player's Subscribe fails with
		// ErrAtCapacity, leaving their view with no feed at all.
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

// Start deals and opens the table. A failed start leaves the engine half-dealt and
// unusable: the lobby builds a new engine per attempt, and the seats are the engine's
// own copies, so nothing outside it sees the partial deal.
func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return errClosed
	}
	if e.state.Phase != Waiting {
		return errors.New("game already started")
	}
	if len(e.state.Players) == 0 {
		return errors.New("cannot start game with no players")
	}

	e.state.Deck.Shuffle()

	hands := make([][]deck.Card, len(e.state.Players))
	for playerIdx := range e.state.Players {
		cards, ok := e.state.Deck.DrawN(e.state.Rules.InitialDealCount())
		if !ok {
			return errors.New("insufficient number of cards to deal for all players")
		}
		hands[playerIdx] = cards
	}

	for playerIdx, hand := range hands {
		e.state.Players[playerIdx].Cards = hand
	}
	e.state.Phase = Playing
	// math/rand: who acts first is public the moment the table opens, so it is no
	// secret worth crypto/rand, and there is no error to handle.
	e.state.CurrentTurn = rand.IntN(len(e.state.Players)) //nolint:gosec // G404: not a secret, see above

	if err := e.state.Rules.OnGameStart(e.state); err != nil {
		return fmt.Errorf("set up game: %w", err)
	}
	e.settleTurnLocked()
	e.broadcaster.Broadcast(Event{Type: EventGameStarted})
	return nil
}

// SubmitAction applies action for playerID, who must be on turn. Acting for yourself
// clears your missed-turn count.
//
// A rules panic ends the table as a rules error and comes back as an error rather than
// unwinding into the session, whose recover would leave the table running on
// half-applied state. The recover is a direct defer and runs before the unlock, so the
// lock is still held.
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
	// Cleared only on a move the rules accept: clearing on any keypress would let a
	// client dodge the idle check in removeIfStillIdle by spamming rejected actions.
	delete(e.clock.missed, playerID)
	return e.applyActionLocked(current, action)
}

// RemovePlayer takes playerID's seat, running the rules' PlayerLeaveHandler, and ends
// the table when the leave decides it. An unknown seat or a finished table is a no-op.
func (e *Engine) RemovePlayer(playerID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
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

// IsFinished reports whether the table has ended, for any reason.
func (e *Engine) IsFinished() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.Phase == Finished
}

// Standing is one player's finishing position.
type Standing struct {
	Player *Player
	// Place is 1-based. Players the rules scored equally share one.
	Place int
}

// Standings returns the finishing order, best first, in one lock hold. Players the
// rules scored equally share a place; everything else counts up strictly.
//
// The *Player values alias live engine state, so only the fields nothing writes after
// the seat was taken - ID, UserID, Name - are safe to read once the lock is gone.
// Cards and anything the rules keep may change under a caller that holds them.
//
// A rules panic returns nil, which finalize drops as unrecordable: the lobby's
// finalize goroutine has nothing above it to recover.
func (e *Engine) Standings() (standings []Standing) {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("rules panicked computing standings", "panic", r, "stack", string(debug.Stack()))
			standings = nil
		}
	}()

	players := e.standingsLocked()
	places := e.placesLocked(players)
	standings = make([]Standing, len(players))
	for i, p := range players {
		standings[i] = Standing{Player: p, Place: places[i]}
	}
	return standings
}

// WithState runs fn with the engine lock held. fn must not call back into the engine:
// every Engine method takes the same lock, so it would deadlock. A test seam: nothing
// in production calls it, and views read through Frame.
func (e *Engine) WithState(fn func(state *State)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(e.state)
}

// submitTimedOutAction plays a move resolveTurnTimeout computed for turn generation
// seq. The lock was dropped in between, so a moved-on generation means the player acted
// themselves and applying the stale move would be a double play.
func (e *Engine) submitTimedOutAction(playerID string, action Action, seq uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if seq != e.clock.seq {
		return errStaleTurn
	}
	// The miss count is left alone: this timeout is already counted, and clearing it
	// would mean somebody who never comes back is never removed.
	current, err := e.checkTurnLocked(playerID, action)
	if err == nil {
		return e.applyActionLocked(current, action)
	}
	if e.state.Phase != Playing {
		return err
	}
	// Still playing after a refusal means ValidateAction refused the move: seq rules
	// out a wrong seat. Re-armed on this lock hold, because after it is dropped a
	// player's own move may already have armed the next seat's clock, and re-arming
	// then would reset it. The re-armed turn is chargeable again, or a rules set that
	// always refuses would never lose the seat.
	e.clock.missCharged = false
	e.armTurnTimerLocked()
	return fmt.Errorf("%w: %w", errActionRefused, err)
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
	if err := e.state.Rules.ValidateAction(e.state, action); err != nil {
		return nil, fmt.Errorf("you can't perform that action: %w", err)
	}
	return current, nil
}

// applyActionLocked plays an action checkTurnLocked accepted for current and moves the
// table on. Any error has already ended the game.
func (e *Engine) applyActionLocked(current *Player, action Action) error {
	if err := e.state.Rules.ApplyAction(e.state, action); err != nil {
		// State may be half-applied, so the game cannot be played on.
		e.finishGameLocked(current, EndReasonRulesError)
		return fmt.Errorf("apply action: %w", err)
	}

	// AfterAction advances the rules' own state machine (poker settles bets, deals the
	// next street, picks the next actor), so it runs before any broadcast and clients
	// never see a half-applied move. It ends the game the same way a win does: without
	// the broadcast every client sits on a frame that never updates and the lobby never
	// records the match.
	if err := e.state.Rules.AfterAction(e.state, action); err != nil {
		e.finishGameLocked(current, EndReasonRulesError)
		return fmt.Errorf("after action: %w", err)
	}

	e.broadcaster.Broadcast(Event{
		Type:     EventActionApplied,
		PlayerID: current.ID,
	})

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
		winner = standings[0]
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
	e.state.Winner = winner

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

	if h, ok := e.state.Rules.(PlayerLeaveHandler); ok {
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

	if h, ok := e.state.Rules.(PlayerLeaveHandler); ok {
		h.AfterPlayerRemoved(e.state, playerIndex)
	}

	e.broadcaster.Broadcast(Event{Type: EventPlayerLeft, PlayerID: playerID})
	e.settleAfterLeaveLocked()
}

// settleAfterLeaveLocked decides what a leave leaves behind: an abandoned table, a win,
// a forfeit to the last seat, or the next turn. Caller holds e.mu.
func (e *Engine) settleAfterLeaveLocked() {
	// A table that never started cannot be won: "any hand empty wins" would report a
	// bogus win over undealt hands.
	if e.state.Phase != Playing {
		return
	}

	switch {
	case len(e.state.Players) == 0:
		e.endGameLocked(nil, EndReasonAbandoned)
	case e.state.Rules.CheckWinCondition(e.state):
		reason := EndReasonWin
		if e.state.Interrupted {
			reason = EndReasonInterrupted
		}
		e.finishGameLocked(nil, reason)
	// A leave handler may keep the hand open (poker all-in leavers still contest the
	// pot). OverrideNextTurn means the last seat still has work.
	case len(e.state.Players) == 1 && e.state.OverrideNextTurn == nil:
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
	if state.Deck != nil {
		snap.DeckSize = state.Deck.Size()
	}
	if state.Discard != nil {
		if top, ok := state.Discard.Peek(); ok {
			snap.TopDiscard = top
		}
	}
	if state.Winner != nil {
		snap.WinnerName = state.Winner.DisplayName()
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

func (e *Engine) standingsLocked() []*Player {
	standings := e.state.Rules.Standings(e.state)

	// State.Players holds no nils (Start deals into every seat, so one would panic
	// there first), but Standings is the rules' own slice and may.
	placed := make(map[string]bool, len(standings))
	for _, p := range standings {
		if p != nil {
			placed[p.ID] = true
		}
	}

	out := make([]*Player, 0, len(standings)+len(e.state.LeftPlayers))
	out = append(out, standings...)
	for _, p := range slices.Backward(e.state.LeftPlayers) {
		if !placed[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

func (e *Engine) placesLocked(standings []*Player) []int {
	// standingsLocked appends LeftPlayers after the seats the rules placed, so a
	// leaver's StandingScore was measured against a state they are no longer in -
	// tying it with a seated player turns a rage-quit into a rated draw. They rank
	// strictly below everyone still at the table. Two leavers with the same score
	// still share a place: splitting them mints Elo between people who both quit.
	left := make(map[string]bool, len(e.state.LeftPlayers))
	for _, p := range e.state.LeftPlayers {
		left[p.ID] = true
	}

	scorer, ok := e.state.Rules.(StandingScorer)
	tied := func(a, b *Player) bool {
		return ok && a != nil && b != nil && left[a.ID] == left[b.ID] &&
			scorer.StandingScore(e.state, a) == scorer.StandingScore(e.state, b)
	}

	places := make([]int, len(standings))
	for i, p := range standings {
		places[i] = i + 1
		if i > 0 && tied(p, standings[i-1]) {
			places[i] = places[i-1]
		}
	}
	return places
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
