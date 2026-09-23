// Package game holds the rules engine: state, seats, turns and the turn clock.
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

// errStaleTurn is an auto-play that lost its turn between resolveTurnTimeout dropping
// the lock and the submit re-acquiring it. Internal: a non-event, not a failure.
var errStaleTurn = errors.New("turn already settled")

// errActionRefused is an auto-play ValidateAction refused: a rules bug, logged and
// re-armed, where an apply failure has already ended the game.
var errActionRefused = errors.New("auto-play refused")

// Engine owns one mutex covering its clock fields and the State: they are always read
// together, and a second lock would only add orderings to get wrong.
type Engine struct {
	mu          sync.Mutex
	state       *State
	broadcaster *broadcaster.Broadcaster[Event]
	closed      bool

	turnTimeout  time.Duration
	turnSeq      uint64
	turnTimer    *time.Timer
	turnDeadline time.Time
	missedTurns  map[string]int
	// The seat and length the running deadline was armed for, and whether that
	// seat-turn has been charged its miss: armTurnTimerLocked's continuation check.
	turnPlayerID    string
	turnLength      time.Duration
	turnMissCharged bool
}

type EngineOption func(*Engine)

func WithTurnTimeout(d time.Duration) EngineOption {
	return func(e *Engine) {
		e.turnTimeout = d
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
		turnTimeout: DefaultTurnTimeout,
		missedTurns: make(map[string]int, len(players)),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

func (e *Engine) Broadcaster() *broadcaster.Broadcaster[Event] {
	return e.broadcaster
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

// WithState runs fn with the engine lock held. fn must not call back into the engine:
// every Engine method takes the same lock, so it would deadlock. A test seam: nothing
// in production calls it, and views read through Frame.
func (e *Engine) WithState(fn func(state *State)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	fn(e.state)
}

func (e *Engine) Snapshot() StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.snapshotLocked()
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
		snap.Winner = state.Winner.DisplayName()
	}
	if current := e.currentPlayerLocked(); current != nil {
		snap.CurrentPlayer = current.DisplayName()
		snap.CurrentPlayerID = current.ID
	}
	snap.Players = make([]PlayerSnapshot, 0, len(state.Players))
	for _, p := range state.Players {
		snap.Players = append(snap.Players, PlayerSnapshot{
			ID:       p.ID,
			Username: p.DisplayName(),
			HandSize: len(p.Cards),
		})
	}
	return snap
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
	if !e.turnDeadline.IsZero() {
		remaining = max(time.Until(e.turnDeadline), 0)
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

func (e *Engine) IsFinished() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.Phase == Finished
}

// StandingsWithPlaces returns standings and 1-based finishing places in one lock hold.
// Players the rules scored equally share a place; everything else counts up strictly.
//
// The *Player values alias live engine state, so only the fields nothing writes after
// the seat was taken - ID, UserID, Name - are safe to read once the lock is gone.
// Cards and anything the rules keep may change under a caller that holds them.
//
// A rules panic returns nil, nil, which finalize drops as unrecordable: the lobby's
// finalize goroutine has nothing above it to recover.
func (e *Engine) StandingsWithPlaces() (standings []*Player, places []int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			slog.Error("rules panicked computing standings", "panic", r, "stack", string(debug.Stack()))
			standings, places = nil, nil
		}
	}()

	standings = e.standingsLocked()
	return standings, e.placesLocked(standings)
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

	places := make([]int, len(standings))
	scorer, ok := e.state.Rules.(StandingScorer)
	for i, p := range standings {
		switch {
		case i == 0:
			places[i] = 1
		case ok && p != nil && standings[i-1] != nil &&
			left[p.ID] == left[standings[i-1].ID] &&
			scorer.StandingScore(e.state, p) == scorer.StandingScore(e.state, standings[i-1]):
			places[i] = places[i-1]
		default:
			places[i] = i + 1
		}
	}
	return places
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

// Start deals and opens the table. A failed start leaves the engine half-dealt and
// unusable: the lobby builds a new engine per attempt, and the seats are the engine's
// own copies, so nothing outside it sees the partial deal.
func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.closed {
		return errors.New("game is closed")
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
		cards, ok := e.state.Deck.DrawNCards(e.state.Rules.InitialDealCount())
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
		return fmt.Errorf("failed to setup game: %w", err)
	}
	e.applyNextTurnLocked(false)

	e.broadcaster.Broadcast(Event{
		Type: EventGameStarted,
	})

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
	return e.submitActionLocked(playerID, action, true)
}

// submitTimedOutAction plays a move resolveTurnTimeout computed for turn generation
// seq. The lock was dropped in between, so a moved-on generation means the player acted
// themselves and applying the stale move would be a double play.
func (e *Engine) submitTimedOutAction(playerID string, action Action, seq uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if seq != e.turnSeq {
		return errStaleTurn
	}
	err := e.submitActionLocked(playerID, action, false)
	if err != nil && e.state.Phase == Playing {
		// Still playing after a failure means ValidateAction refused the move: an apply
		// failure ends the game, and seq rules out a wrong seat. Re-armed on this lock
		// hold, because after it is dropped a player's own move may already have armed
		// the next seat's clock, and re-arming then would reset it. The re-armed turn
		// is chargeable again, or a rules set that always refuses would never lose
		// the seat.
		e.turnMissCharged = false
		e.armTurnTimerLocked()
		return fmt.Errorf("%w: %w", errActionRefused, err)
	}
	return err
}

// playerPresent is false when playing for an absent player: that timeout is already
// counted, and clearing it would mean somebody who never comes back is never removed.
func (e *Engine) submitActionLocked(playerID string, action Action, playerPresent bool) error {
	if e.closed {
		return errors.New("game is closed")
	}
	if e.state.Phase != Playing {
		return errors.New("game not in playing phase")
	}

	currentPlayer := e.currentPlayerLocked()
	if currentPlayer == nil || currentPlayer.ID != playerID {
		return errors.New("wait for your turn to perform an action")
	}

	if err := e.state.Rules.ValidateAction(e.state, action); err != nil {
		return fmt.Errorf("you can't perform that action: %w", err)
	}

	// Cleared only on a move the rules accept: clearing on any keypress would let a
	// client dodge the idle check in removeIfStillIdle by spamming rejected actions.
	if playerPresent {
		delete(e.missedTurns, playerID)
	}

	if err := e.state.Rules.ApplyAction(e.state, action); err != nil {
		// State may be half-applied, so the game cannot be played on.
		e.finishGameLocked(currentPlayer, EndReasonRulesError)
		return fmt.Errorf("apply action: %w", err)
	}

	// AfterAction advances the rules' own state machine (poker settles bets, deals the
	// next street, picks the next actor), so it runs before any broadcast and clients
	// never see a half-applied move. It ends the game the same way a win does: without
	// the broadcast every client sits on a frame that never updates and the lobby never
	// records the match.
	if err := e.state.Rules.AfterAction(e.state, action); err != nil {
		e.finishGameLocked(currentPlayer, EndReasonRulesError)
		return fmt.Errorf("post-action rules failed: %w", err)
	}

	e.broadcaster.Broadcast(Event{
		Type:     EventActionApplied,
		PlayerID: playerID,
	})

	if e.state.Rules.CheckWinCondition(e.state) {
		e.finishGameLocked(currentPlayer, EndReasonWin)
		return nil
	}

	e.applyNextTurnLocked(true)

	e.broadcaster.Broadcast(Event{
		Type: EventTurnAdvanced,
	})

	return nil
}

// finishGameLocked settles the winner from the rules standings and announces the end of
// the game; fallback names the winner when the rules rank nobody. Caller holds e.mu.
func (e *Engine) finishGameLocked(fallback *Player, reason EndReason) {
	e.state.Phase = Finished
	// A clock left running would auto-play into a finished game.
	e.stopTurnTimerLocked()

	standings := e.state.Rules.Standings(e.state)
	switch {
	case len(standings) > 0:
		e.state.Winner = standings[0]
	case fallback != nil:
		e.state.Winner = fallback
	case len(e.state.Players) > 0:
		e.state.Winner = e.state.Players[0]
	}

	winnerID := ""
	if e.state.Winner != nil {
		winnerID = e.state.Winner.ID
	}
	e.broadcaster.Broadcast(Event{
		Type:     EventGameEnded,
		PlayerID: winnerID,
		Reason:   reason,
	})
}

func (e *Engine) RemovePlayer(playerID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.removePlayerLocked(playerID)
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
	delete(e.missedTurns, playerID)

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

	// A table that never started cannot be won: "any hand empty wins" would report a
	// bogus win over undealt hands.
	if e.state.Phase != Playing {
		return
	}

	if len(e.state.Players) == 0 {
		e.state.Winner = nil
		e.state.Phase = Finished
		e.stopTurnTimerLocked()
		e.broadcaster.Broadcast(Event{Type: EventGameEnded, Reason: EndReasonAbandoned})
		return
	}

	if e.state.Rules.CheckWinCondition(e.state) {
		reason := EndReasonWin
		if e.state.Interrupted {
			reason = EndReasonInterrupted
		}
		e.finishGameLocked(nil, reason)
		return
	}

	if len(e.state.Players) == 1 {
		// A leave handler may keep the hand open (poker all-in leavers still
		// contest the pot). OverrideNextTurn means the last seat still has work.
		if e.state.OverrideNextTurn != nil {
			e.applyNextTurnLocked(false)
			e.broadcaster.Broadcast(Event{Type: EventTurnAdvanced})
			return
		}
		e.state.Phase = Finished
		e.stopTurnTimerLocked()
		e.state.Winner = e.state.Players[0]
		e.broadcaster.Broadcast(Event{
			Type:     EventGameEnded,
			PlayerID: e.state.Winner.ID,
			Reason:   EndReasonForfeit,
		})
		return
	}

	e.applyNextTurnLocked(false)

	e.broadcaster.Broadcast(Event{
		Type: EventTurnAdvanced,
	})
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

// currentPlayerLocked is the seat State.CurrentTurn points at, or nil. Caller holds e.mu.
func (e *Engine) currentPlayerLocked() *Player {
	if e.state.CurrentTurn < 0 || e.state.CurrentTurn >= len(e.state.Players) {
		return nil
	}
	return e.state.Players[e.state.CurrentTurn]
}

func (e *Engine) applyNextTurnLocked(advance bool) {
	switch {
	case e.state.OverrideNextTurn != nil:
		e.state.CurrentTurn = *e.state.OverrideNextTurn
		e.state.OverrideNextTurn = nil
	case advance:
		e.state.CurrentTurn++
	}
	e.clampTurnLocked()
	e.armTurnTimerLocked()
}

// clampTurnLocked forces State.CurrentTurn into [0, len(Players)) so a stale index
// from a leave handler or a rules override cannot name a seat that is gone.
func (e *Engine) clampTurnLocked() {
	n := len(e.state.Players)
	if n <= 0 {
		e.state.CurrentTurn = 0
		return
	}
	e.state.CurrentTurn = ((e.state.CurrentTurn % n) + n) % n
}
