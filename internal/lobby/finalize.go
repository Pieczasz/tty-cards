package lobby

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/observability"

	"uuid"
)

// finalizeRequest is the lobby-side snapshot a finished game needs to persist.
// Lobby gathers it under its lock; Manager owns the write.
type finalizeRequest struct {
	lobbyCode string
	game      db.GameRef
	isRanked  bool
	startedAt time.Time
}

// finalizeFinishedGame persists the result of a game that just ended. registered is
// the caller's registerFinalizer result, taken before anything else: every statement
// between observing the end and that call is a window for shutdown to begin, and a
// refusal then drops a finished match with nothing left for WaitForFinalizers to
// wait on. A true registration is released here on every path.
func (m *Manager) finalizeFinishedGame(req finalizeRequest, engine *game.Engine, reason game.EndReason, registered bool) {
	if m == nil {
		return
	}
	if registered {
		defer m.finalizing.Done()
	}
	if m.matchRepo == nil {
		return
	}
	parentCtx := m.shutdownCtx()

	if !registered {
		slog.ErrorContext(parentCtx, "finished match dropped; shutdown stopped new finalizers",
			"lobby", req.lobbyCode, "game", req.game.Slug, "ranked", req.isRanked)
		observability.MatchFinalize(parentCtx, "dropped", req.isRanked)
		return
	}

	if !req.startedAt.IsZero() {
		observability.GameFinished(parentCtx, req.game.Name, req.isRanked, endReasonLabel(reason), time.Since(req.startedAt))
	}
	if req.game.Slug == "" {
		// Every other bail-out says so; this one used to drop a finished match in
		// silence. A lobby cannot start a game the registry does not hold, so an empty
		// slug means the snapshot and the registry disagree.
		slog.ErrorContext(parentCtx, "finished match dropped; the lobby recorded no game",
			"lobby", req.lobbyCode, "ranked", req.isRanked)
		observability.MatchFinalize(parentCtx, "dropped", req.isRanked)
		return
	}

	ctx, cancel := context.WithTimeout(parentCtx, rankedFinalizeTimeout)
	defer cancel()
	m.persistFinishedMatch(ctx, engine, reason, req)
}

// unratedReason says why a ranked match is being recorded without moving Elo. Only the
// three reasons persistFinishedMatch strips the rating for reach it.
func unratedReason(reason game.EndReason) string {
	switch reason {
	case game.EndReasonRulesError:
		return "rules error ended the match; recording without Elo"
	case game.EndReasonAbandoned:
		return "every seat left the match; recording without Elo"
	case game.EndReasonWin, game.EndReasonForfeit, game.EndReasonInterrupted, game.EndReasonUnknown:
	}
	return "server is shutting down; recording the ranked match without Elo"
}

func endReasonLabel(reason game.EndReason) string {
	switch reason {
	case game.EndReasonWin:
		return "win"
	case game.EndReasonRulesError:
		return "rules_error"
	case game.EndReasonForfeit:
		return "forfeit"
	case game.EndReasonAbandoned:
		return "abandoned"
	case game.EndReasonInterrupted:
		return "interrupted"
	case game.EndReasonUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

func (m *Manager) persistFinishedMatch(
	ctx context.Context, engine *game.Engine, reason game.EndReason, req finalizeRequest,
) {
	standings, places := engine.StandingsWithPlaces()
	if reason == game.EndReasonAbandoned && len(standings) == 0 {
		// Nobody was left to record, so there is no history to write - but it is still
		// a finished match that produced no row, which is what the counter tracks.
		slog.WarnContext(ctx, "abandoned match had no standings; nothing recorded",
			"lobby", req.lobbyCode, "game", req.game.Slug, "ranked", req.isRanked)
		observability.MatchFinalize(ctx, "dropped", req.isRanked)
		return
	}

	userIDs := make([]uuid.UUID, 0, len(standings))
	for i, p := range standings {
		if p == nil || p.UserID == uuid.Nil() {
			slog.ErrorContext(ctx, "standing player has no database user; match not recorded",
				"lobby", req.lobbyCode, "game", req.game.Slug, "ranked", req.isRanked, "player_index", i)
			observability.MatchFinalize(ctx, "dropped", req.isRanked)
			return
		}
		userIDs = append(userIDs, p.UserID)
	}

	// A match the deploy interrupted has no honest winner: SSH teardown order, not
	// play, decided who was left holding cards. Rules errors are the same class -
	// half-applied state must not move the ladder. So is a table every seat left:
	// standings are then reverse leave order, so rating it pays the last to quit.
	rated := req.isRanked && !m.isShuttingDown() &&
		reason != game.EndReasonRulesError && reason != game.EndReasonAbandoned
	if req.isRanked && !rated {
		slog.WarnContext(ctx, unratedReason(reason), "lobby", req.lobbyCode, "game", req.game.Slug)
	}

	if err := m.recordFinishedMatch(ctx, req.game, userIDs, places, rated); err != nil {
		slog.ErrorContext(ctx, "failed to record finished match",
			"error", err, "lobby", req.lobbyCode, "game", req.game.Slug, "ranked", rated)
		observability.MatchFinalize(ctx, "error", req.isRanked)
		return
	}
	observability.MatchFinalize(ctx, "ok", req.isRanked)
}

func (m *Manager) recordFinishedMatch(
	ctx context.Context, ref db.GameRef, userIDs []uuid.UUID, places []int, isRanked bool,
) error {
	if isRanked {
		if err := m.matchRepo.FinalizeRankedMatch(ctx, ref, userIDs, places); err != nil {
			return fmt.Errorf("finalize ranked match: %w", err)
		}
		return nil
	}
	if err := m.matchRepo.RecordCasualMatch(ctx, ref, userIDs); err != nil {
		return fmt.Errorf("record casual match: %w", err)
	}
	return nil
}
