package systemtest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/poker"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"

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
) (map[uuid.UUID]uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.finalized = append(r.finalized, finalizedMatch{
		gameName: ref.Name,
		userIDs:  slices.Clone(orderedUserIDs),
	})
	r.mu.Unlock()

	r.fire()
	return map[uuid.UUID]uint32{}, nil
}

func (r *rankedFinalizeRecorder) calls() []finalizedMatch {
	r.mu.Lock()
	defer r.mu.Unlock()

	calls := make([]finalizedMatch, len(r.finalized))
	for i, call := range r.finalized {
		calls[i] = finalizedMatch{
			gameName: call.gameName,
			userIDs:  slices.Clone(call.userIDs),
		}
	}
	return calls
}

func newPlayer(id uuid.UUID, name string) *game.Player {
	return lobby.NewPlayer(&db.User{ID: id, Username: name})
}

// awaitGameStart waits for l's start event and returns the engine it opened. The event
// itself only hands out seats, so the engine is read off the lobby, server side.
func awaitGameStart(t *testing.T, l *lobby.Lobby, events <-chan lobby.Event) *game.Engine {
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
			engine := l.ActiveGame()
			require.NotNil(t, engine, "GAME_STARTED must leave the lobby in game")
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
		total = extra.Pool
		for _, seat := range extra.Seats {
			total += seat.Chips
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

func playerByID(t *testing.T, players []*game.Player, id string) *game.Player {
	t.Helper()
	for _, p := range players {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no seated player %s", id)
	return nil
}

// clockPlayOutSteps bounds TimeoutAction auto-play. Gin/hearts need thousands;
// see gametest.clockOnlySteps.
const clockPlayOutSteps = 5000

// playOutByClock drives any catalog game to the end with TimeoutAction alone -
// the same path a silent seat takes.
func playOutByClock(t *testing.T, engine *game.Engine) {
	t.Helper()
	for step := range clockPlayOutSteps {
		if engine.IsFinished() {
			return
		}
		id := engine.CurrentPlayerID()
		require.NotEmpty(t, id, "step %d: finished tables have no actor, unfinished ones must", step)
		var act game.Action
		engine.WithState(func(s *game.State) {
			act = s.Rules.TimeoutAction(s)
		})
		require.NotNil(t, act, "step %d: TimeoutAction returned nil", step)
		require.NoError(t, engine.SubmitAction(id, act), "step %d: %s", step, act.Name())
	}
	require.True(t, engine.IsFinished(), "match did not end within %d auto-play steps", clockPlayOutSteps)
}

// startFilledLobby creates a table (lobby default privacy), seats n players
// (leader + guests), readies everyone, and returns the live engine after
// GAME_STARTED.
func startFilledLobby(
	t *testing.T, manager *lobby.Manager, slug string, n int, ranked bool,
) (*lobby.Lobby, *game.Engine, []*game.Player) {
	t.Helper()
	require.GreaterOrEqual(t, n, 2)
	players := make([]*game.Player, n)
	for i := range n {
		players[i] = newPlayer(testutil.UID(i+1), fmt.Sprintf("p%d", i+1))
	}
	l, err := manager.CreateLobby(players[0],
		lobby.WithCardGame(slug),
		lobby.WithMaxPlayers(n),
		lobby.WithRanked(ranked),
	)
	require.NoError(t, err)
	for _, g := range players[1:] {
		require.NoError(t, joinErr(manager.JoinLobbyByCode(t.Context(), l.Code(), g)))
	}
	events, err := l.Subscribe(players[0].ID)
	require.NoError(t, err)
	t.Cleanup(func() { l.Unsubscribe(players[0].ID, events) })
	for _, p := range players {
		require.NoError(t, manager.ToggleReady(p))
	}
	return l, awaitGameStart(t, l, events), players
}
