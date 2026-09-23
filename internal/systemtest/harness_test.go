package systemtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/catalog"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/game/poker"
	"github.com/Pieczasz/terminal-card/internal/lobby"

	"uuid"

	"github.com/stretchr/testify/require"
)

// joinErr keeps the join-and-assert call sites one line: they only care whether the
// join was refused.
func joinErr(_ *lobby.Lobby, err error) error { return err }

type finalizedMatch struct {
	gameName string
	userIDs  []uuid.UUID
}

type finalizeSignal struct {
	signal chan struct{}
}

func newFinalizeSignal() finalizeSignal {
	return finalizeSignal{signal: make(chan struct{}, 8)}
}

func (s finalizeSignal) fire() {
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

func (s finalizeSignal) awaitFinalize(t *testing.T) {
	t.Helper()
	select {
	case <-s.signal:
	case <-time.After(30 * time.Second):
		t.Fatal("ranked finalize never ran")
	}
}

type rankedFinalizeRecorder struct {
	db.MatchRepository
	finalizeSignal

	mu        sync.Mutex
	finalized []finalizedMatch
}

func newRankedFinalizeRecorder() *rankedFinalizeRecorder {
	return &rankedFinalizeRecorder{finalizeSignal: newFinalizeSignal()}
}

func (r *rankedFinalizeRecorder) FinalizeRankedMatch(
	ctx context.Context,
	ref db.GameRef,
	orderedUserIDs []uuid.UUID,
	_ []int,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	r.finalized = append(r.finalized, finalizedMatch{
		gameName: ref.Name,
		userIDs:  append([]uuid.UUID(nil), orderedUserIDs...),
	})
	r.mu.Unlock()

	r.fire()
	return nil
}

func (r *rankedFinalizeRecorder) calls() []finalizedMatch {
	r.mu.Lock()
	defer r.mu.Unlock()

	calls := make([]finalizedMatch, len(r.finalized))
	for i, call := range r.finalized {
		calls[i] = finalizedMatch{
			gameName: call.gameName,
			userIDs:  append([]uuid.UUID(nil), call.userIDs...),
		}
	}
	return calls
}

func realRegistry(t *testing.T) *game.Registry {
	t.Helper()
	registry := catalog.NewRegistry()
	return registry
}

func newPlayer(id uuid.UUID, name string) *game.Player {
	return lobby.NewPlayer(&db.User{ID: id, Username: name})
}

func awaitGameStart(t *testing.T, events <-chan lobby.Event) *game.Engine {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("lobby channel closed before the game started")
			}
			if event.Type != lobby.EventGameStarted {
				continue
			}
			engine, ok := event.Payload.(*game.Engine)
			require.True(t, ok, "GAME_STARTED payload must carry the engine")
			return engine
		case <-deadline:
			t.Fatal("timed out waiting for the game to start")
		}
	}
}

func chipsInPlay(t *testing.T, engine *game.Engine) uint {
	t.Helper()
	var total uint
	engine.WithState(func(state *game.State) {
		extra, ok := state.Extra.(*poker.State)
		require.True(t, ok)
		total = extra.MainPool
		for _, chips := range extra.PlayerChips {
			total += chips
		}
	})
	return total
}

func playOutMatch(t *testing.T, engine *game.Engine) {
	t.Helper()
	for range maxActions * poker.HandsPerMatch {
		if engine.IsFinished() {
			return
		}
		if !actOnce(engine) {
			break
		}
	}
	require.True(t, engine.IsFinished(), "the match must reach its end")
}

func actOnce(engine *game.Engine) bool {
	playerID := engine.CurrentPlayerID()
	for _, action := range []game.Action{
		poker.ActionCheck{},
		poker.ActionCall{},
		poker.ActionFold{},
		poker.ActionNextHand{},
	} {
		if err := engine.SubmitAction(playerID, action); err == nil {
			return true
		}
	}
	return false
}
