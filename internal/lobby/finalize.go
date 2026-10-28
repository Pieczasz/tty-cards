package lobby

import (
	"context"
	"log/slog"
	"time"

	"github.com/Pieczasz/tty-cards/internal/broadcaster"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/observability"

	"uuid"
)

// finalizeRequest is the lobby-side snapshot a finished game needs to persist.
// Lobby gathers it under its lock; Manager owns the write.
type finalizeRequest struct {
	lobbyCode string
	game      db.GameRef
	isRanked  bool
	startedAt time.Time
	// teardownBegun is whether BeginTeardown had run when the game ended. It is read once,
	// at the end: reopening the table can wait on m.mu, and a teardown that begins in
	// that wait must not turn a hand that ended during the drain into an unrated one.
	teardownBegun bool
}

// logAttrs is the identity every finalize log line carries, so one match's lines can
// be found together.
func (r finalizeRequest) logAttrs() []any {
	return []any{"lobby", r.lobbyCode, "game", r.game.Slug, "ranked", r.isRanked}
}

// finalizeFinishedGame persists the result of a game that just ended. It runs on the
// game's watcher, which WaitForFinalizers waits for.
func (m *Manager) finalizeFinishedGame(req finalizeRequest, engine *game.Engine, reason game.EndReason) {
	parentCtx := m.appCtx

	if req.game.Slug == "" {
		// Every other bail-out says so; this one used to drop a finished match in
		// silence. A lobby cannot start a game the registry does not hold, so an empty
		// slug means the snapshot and the registry disagree.
		slog.ErrorContext(parentCtx, "finished match dropped; the lobby recorded no game",
			"lobby", req.lobbyCode, "ranked", req.isRanked)
		observability.MatchFinalize(parentCtx, "dropped", req.isRanked)
		return
	}

	// The write is detached from the app context on purpose: WaitForFinalizers exists so
	// a match that ends during the drain is still recorded, and a parent cancelled at
	// shutdown would abort exactly those writes. finalizeTimeout is its only deadline.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), finalizeTimeout)
	defer cancel()
	m.persistFinishedMatch(ctx, engine, reason, req)
}

// unratedReason is why a ranked match ending for reason is recorded without moving
// Elo, or "" when it is rated.
//
// A match the deploy interrupted has no honest winner: SSH teardown order, not play,
// decided who was left holding cards. Rules errors are the same class - half-applied
// state must not move the ladder. So is a table every seat left: standings are then
// reverse leave order, so rating it pays the last to quit.
func unratedReason(reason game.EndReason, teardownBegun bool) string {
	switch {
	case reason == game.EndReasonRulesError:
		return "rules error ended the match; recording without Elo"
	case reason == game.EndReasonAbandoned:
		return "every seat left the match; recording without Elo"
	case teardownBegun:
		return "server is hanging up on its sessions; recording the ranked match without Elo"
	}
	return ""
}

// matchResult is what a finished engine leaves to be written.
type matchResult struct {
	ref     db.GameRef
	userIDs []uuid.UUID
	places  []int
	// leavers is set for an interrupted match only: the seats that are charged.
	leavers     []uuid.UUID
	interrupted bool
}

func (m *Manager) persistFinishedMatch(
	ctx context.Context, engine *game.Engine, reason game.EndReason, req finalizeRequest,
) {
	standings := engine.Standings()
	if len(standings) == 0 {
		// Nobody to record (every seat left, or Standings panicked and was recovered),
		// so there is no history to write - but it is still a finished match that
		// produced no row, which is what the counter tracks.
		slog.WarnContext(ctx, "finished match had no standings; nothing recorded",
			append(req.logAttrs(), "reason", reason.String())...)
		observability.MatchFinalize(ctx, "dropped", req.isRanked)
		return
	}

	res := matchResult{ref: req.game, interrupted: reason == game.EndReasonInterrupted}
	res.userIDs = make([]uuid.UUID, 0, len(standings))
	res.places = make([]int, 0, len(standings))
	for i, s := range standings {
		p := s.Player
		if p == nil || p.UserID == uuid.Nil() {
			slog.ErrorContext(ctx, "standing player has no database user; match not recorded",
				append(req.logAttrs(), "player_index", i)...)
			observability.MatchFinalize(ctx, "dropped", req.isRanked)
			return
		}
		res.userIDs = append(res.userIDs, p.UserID)
		res.places = append(res.places, s.Place)
	}
	if res.interrupted {
		res.leavers = leaverIDs(standings)
	}

	// An interrupted match stays rated, but only against its leavers: see
	// recordFinishedMatch.
	rated := req.isRanked
	if why := unratedReason(reason, req.teardownBegun); rated && why != "" {
		rated = false
		slog.WarnContext(ctx, why, req.logAttrs()...)
	}

	written, err := m.recordFinishedMatch(ctx, res, rated)
	if err != nil {
		// The ids and places are what an operator needs to replay the write by hand; the
		// id is already the player_id every log line carries (docs/data-inventory.md).
		slog.ErrorContext(ctx, "record finished match",
			append(req.logAttrs(), "error", err, "rated", rated,
				"user_ids", res.userIDs, "places", res.places)...)
		observability.MatchFinalize(ctx, "error", req.isRanked)
		return
	}
	observability.MatchFinalize(ctx, "ok", req.isRanked)
	m.refreshRatings(req.game.Slug, written)
}

// refreshRatings puts the ratings a ranked finalize wrote on the seats of those
// players, at whichever table each sits at now - by the time the write commits the
// table has reopened, and a player may have moved on. Matchmaking, the table's
// average and the waiting room all read a seat's ratings, which would otherwise stay
// the ones its session logged in with. A player seated nowhere is not refreshed: the
// next login reads the stored rating.
//
// m.mu is held, read-only, across each lobby's update so no player can change tables
// between the index lookup and the seat write; the broadcasts wait for both locks to
// be released.
func (m *Manager) refreshRatings(slug string, written map[uuid.UUID]uint32) {
	if len(written) == 0 {
		return
	}
	m.mu.RLock()
	tables := make(map[*Lobby]struct{}, len(written))
	for id := range written {
		if l := m.playerLobby[id.String()]; l != nil {
			tables[l] = struct{}{}
		}
	}
	var changed []*broadcaster.Broadcaster[Event]
	for l := range tables {
		if bc := l.refreshRatings(slug, written); bc != nil {
			changed = append(changed, bc)
		}
	}
	m.mu.RUnlock()

	for _, bc := range changed {
		bc.Broadcast(Event{Type: EventPlayersUpdated})
	}
}

// recordFinishedMatch picks the write. An interrupted match (decision D-1) is rated
// only against its leavers: the seats still playing did not finish, so nothing moves
// for them, but quitting a losing match must not be free.
//
// It returns the ratings the write stored, none for a casual one. The repository's
// error already names the write, so it is returned as it is.
//
//nolint:wrapcheck // wrapping it again logged "finalize ranked match: finalize ranked match"
func (m *Manager) recordFinishedMatch(ctx context.Context, res matchResult, rated bool) (map[uuid.UUID]uint32, error) {
	switch {
	case !rated:
		return nil, m.matchRepo.RecordCasualMatch(ctx, res.ref, res.userIDs)
	case res.interrupted:
		return m.matchRepo.FinalizeInterruptedMatch(ctx, res.ref, res.userIDs, res.places, res.leavers)
	default:
		return m.matchRepo.FinalizeRankedMatch(ctx, res.ref, res.userIDs, res.places)
	}
}

// leaverIDs is who left the finished table, as its standings mark them.
func leaverIDs(standings []game.Standing) []uuid.UUID {
	var ids []uuid.UUID
	for _, s := range standings {
		if s.Left {
			ids = append(ids, s.Player.UserID)
		}
	}
	return ids
}

// admitGame counts the watcher of a game ToggleReady may be about to start, unless
// shutdown has closed the gate. A caller that starts no game gives the count back.
func (m *Manager) admitGame() bool {
	m.gateMu.Lock()
	defer m.gateMu.Unlock()
	if m.gateClosed {
		return false
	}
	m.watchers.Add(1)
	return true
}

// BeginShutdown marks the process as going away and stops new games, without
// stopping the ones in play: players may finish them while the server waits for
// sessions to end on their own, and those results are rated as usual. BeginTeardown is
// the point after which they are not.
//
// Seats still held for a reconnect are given up here. Their timers would fire long
// after the drain, so the lobby would never be removed and its engine never closed -
// the player is not coming back to a process that is exiting.
func (m *Manager) BeginShutdown() {
	if m == nil {
		return
	}
	m.shuttingDown.Store(true)
	m.gateMu.Lock()
	m.gateClosed = true
	m.gateMu.Unlock()
	m.mu.Lock()
	held := m.heldSeatsLocked(nil)
	m.mu.Unlock()
	// LeaveLobby stops the timer under m.mu before touching the roster.
	for _, p := range held {
		m.LeaveLobby(p)
	}
}

// BeginTeardown is called right before the server closes the sessions still connected.
// A match a forced hang-up ends has no honest winner, so it is recorded without Elo.
func (m *Manager) BeginTeardown() {
	if m == nil {
		return
	}
	m.tearingDown.Store(true)
}

// WaitForFinalizers stops new games (BeginShutdown normally has already), then blocks
// until every game's watcher - and the finalize it runs when its game ends - has
// finished, or timeout elapses. A non-positive timeout waits indefinitely.
//
// Watchers are counted from game start, not from game end, so a table that a late
// session teardown ends while this waits is still written rather than refused.
//
// The waiter goroutine is started once and reused, so a caller that times out and
// calls again does not strand one waiter per attempt. Because the gate is already
// closed, the group only ever counts down, so that goroutine exits once the last
// game does.
func (m *Manager) WaitForFinalizers(timeout time.Duration) bool {
	if m == nil {
		return true
	}
	m.shuttingDown.Store(true)

	m.gateMu.Lock()
	m.gateClosed = true
	if m.drained == nil {
		ch := make(chan struct{})
		m.drained = ch
		go func() {
			m.watchers.Wait()
			close(ch)
		}()
	}
	drained := m.drained
	m.gateMu.Unlock()

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
