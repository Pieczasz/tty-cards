package lobby

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/observability"
)

// gameStart is a game toggleReady just started, handed to the manager to watch: the
// engine, its feed and the finalize snapshot. The zero value means nothing to watch.
type gameStart struct {
	engine *game.Engine
	feed   <-chan game.Event
	req    finalizeRequest
}

// watchLocked subscribes the manager's watcher to engine before l.mu is released, so
// no seat can act - and no game can end - before the feed is open. A failed subscribe
// is returned: the caller must not open the table, because this watcher is the only
// thing that persists a match. Caller holds l.mu.
//
// The finalize snapshot is taken here, not when the game ends: by then the lobby may
// have reopened and been reconfigured, and the result would be written under the new
// ranked flag, the new game, and the next hand's start time.
func (l *Lobby) watchLocked(engine *game.Engine, ref db.GameRef) (gameStart, error) {
	ch, err := engine.Subscribe()
	if err != nil {
		observability.SubscribeFailure(context.Background(), "game")
		return gameStart{}, err
	}
	return gameStart{engine: engine, feed: ch, req: finalizeRequest{
		lobbyCode: l.code,
		game:      ref,
		isRanked:  l.options.isRanked,
		startedAt: l.startedAt,
	}}, nil
}

// watchGame is the goroutine ToggleReady starts, and counts in m.watchers, for every
// game: it counts engine events, and when the game ends it reopens the table and
// persists the result.
//
// It runs whether or not a match repository is configured: reopening the table and
// giving up the seats it held for a reconnect are its job too, and
// finalizeFinishedGame already no-ops without a repository. Idle-removed seats do
// not reach the roster through it but through seatHook, which no dropped event can
// lose.
func (m *Manager) watchGame(l *Lobby, start gameStart) {
	defer m.recoverGoroutine("watch")
	engine := start.engine
	defer engine.Unsubscribe(start.feed)
	ctx := m.appCtx
	slug := start.req.game.Slug
	defer func() {
		if n := engine.Dropped(); n > 0 {
			observability.BroadcastDropped(ctx, "game", n)
		}
	}()

	for event := range start.feed {
		switch event.Type {
		case game.EventTurnTimedOut:
			observability.TurnTimedOut(ctx, slug)
		case game.EventPlayerIdle:
			observability.PlayerIdleRemoved(ctx, slug)
		case game.EventGameEnded:
			m.requestFinalize(l, engine, event.Reason, start.req)
			return
		default:
			// Turn and leave events are the views' business; the lobby counts nothing.
		}
	}

	// The feed ending is not proof the match did not finish. EventGameEnded is the
	// last event an engine sends, so latest-wins never drops it, but RemoveLobby closes
	// the feed from under this goroutine, and a leave already on its way can still end
	// the game on the closed engine, whose broadcast is then a no-op. The engine kept
	// why it ended.
	if engine.IsFinished() {
		m.requestFinalize(l, engine, engine.EndReason(), start.req)
	}
}

// requestFinalize reopens the finished table and persists its result. req was
// snapshotted by watchLocked when this game started, so a lobby that has since
// reopened cannot rewrite what the finished match is recorded as.
//
// Reopen, then persist: a 15s write must not pin InGame while the TUI is already
// back in the lobby. Both run on the watcher goroutine, which WaitForFinalizers has
// counted since the game started, so a drain beginning anywhere in between still
// waits for the write.
//
// The finish is counted here, where every end passes, so games started minus games
// finished stays true across a drain and on a server with no match repository.
func (m *Manager) requestFinalize(l *Lobby, engine *game.Engine, reason game.EndReason, req finalizeRequest) {
	observability.GameFinished(m.appCtx, req.game.Slug, req.isRanked, reason.String(),
		time.Since(req.startedAt))
	m.releaseFinishedGame(l)
	m.finalizeFinishedGame(req, engine, reason)
}

// recoverGoroutine stops one lobby goroutine's panic from taking the process down.
// A rules panic is the engine's to end; this is the timers and the watcher, which
// have nothing above them.
func (m *Manager) recoverGoroutine(where string) {
	r := recover()
	if r == nil || m == nil {
		return
	}
	slog.ErrorContext(m.appCtx, "lobby goroutine panicked",
		"where", where, "panic", r, "stack", string(debug.Stack()))
}
