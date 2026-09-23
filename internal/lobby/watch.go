package lobby

import (
	"log/slog"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/observability"
)

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
	ch, err := engine.Subscribe()
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
		defer engine.Unsubscribe(ch)
		l.handleGameEvents(ch, engine, req)
	}()
}

func (l *Lobby) handleGameEvents(ch <-chan game.Event, engine *game.Engine, req finalizeRequest) {
	ctx := l.manager.shutdownCtx()
	slug := req.game.Slug
	defer func() {
		if n := engine.Dropped(); n > 0 {
			observability.BroadcastDropped(ctx, "game", n)
		}
	}()

	for event := range ch {
		switch event.Type {
		case game.EventTurnTimedOut:
			observability.TurnTimedOut(ctx, slug)
		case game.EventPlayerIdle:
			observability.PlayerIdleRemoved(ctx, slug)
			// The engine took the seat, so the roster follows, or a player kicked for
			// idling reconnects into a lobby whose game no longer has them. Equal falls
			// back to ID, so a zero-UserID stub still matches.
			l.manager.LeaveLobby(&game.Player{ID: event.PlayerID})
		case game.EventGameEnded:
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
		l.requestFinalize(engine, game.EndReasonUnknown, req)
	}
}

// requestFinalize reopens the finished table and hands it to Manager for persistence.
// req was snapshotted by watchGameLocked when this game started, so a lobby that has
// since reopened cannot rewrite what the finished match is recorded as.
//
// Register, reopen, persist - in that order. Reopening comes before the write, so a
// 15s write does not pin InGame while the TUI is already back in the lobby; but it
// waits on m.mu in releaseHeldSeats, and a shutdown that began inside that wait would
// have refused the registration and dropped the match.
//
// The registration is taken before anything else: every statement between observing
// the end and that call is a window for shutdown to begin, and a refusal then drops a
// finished match with nothing left for WaitForFinalizers to wait on.
func (l *Lobby) requestFinalize(engine *game.Engine, reason game.EndReason, req finalizeRequest) {
	registered := l.manager.registerFinalizer()
	l.releaseFinishedGame()
	if !registered {
		l.manager.dropFinishedMatch(req)
		return
	}
	l.manager.finalizeFinishedGame(req, engine, reason)
}
