package systemtest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/gametest"
	"github.com/Pieczasz/tty-cards/internal/game/ginrummy"
	"github.com/Pieczasz/tty-cards/internal/game/poker"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	ginrummyview "github.com/Pieczasz/tty-cards/internal/tui/views/gameview/ginrummy"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ginSlug       = "gin_rummy"
	ginName       = "Gin Rummy"
	ginClockSteps = 5000
)

// ginRepo records casual and ranked finishes. The shared ranked recorder leaves
// RecordCasualMatch on a nil interface, which a casual gin match would panic on.
type ginRepo struct {
	*rankedFinalizeRecorder
	casual atomic.Int32
}

func newGinRepo() *ginRepo {
	return &ginRepo{rankedFinalizeRecorder: newRankedFinalizeRecorder()}
}

func (r *ginRepo) RecordCasualMatch(ctx context.Context, _ db.GameRef, _ []uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.casual.Add(1)
	r.fire()
	return nil
}

func (r *ginRepo) FinalizeInterruptedMatch(
	context.Context, db.GameRef, []uuid.UUID, []int, []uuid.UUID,
) (map[uuid.UUID]uint32, error) {
	return map[uuid.UUID]uint32{}, nil
}

func releaseLobby(t *testing.T, manager *lobby.Manager, code string) {
	t.Helper()
	t.Cleanup(func() {
		manager.RemoveLobby(code)
		require.True(t, manager.WaitForFinalizers(5*time.Second), "watchers must drain")
	})
}

func assertGinStandings(t *testing.T, engine *game.Engine, players []*game.Player) {
	t.Helper()
	standings := engine.Standings()
	require.Len(t, standings, len(players), "every seat places, including anyone who left")
	got := make(map[string]bool, len(standings))
	for _, s := range standings {
		require.NotNil(t, s.Player)
		require.Positive(t, s.Place)
		got[s.Player.ID] = true
	}
	for _, p := range players {
		assert.Truef(t, got[p.ID], "standings missing %s", p.Name)
	}
}

func ginExtra(t *testing.T, engine *game.Engine) (hand int, over, match bool) {
	t.Helper()
	engine.WithState(func(s *game.State) {
		extra, ok := s.Extra.(*ginrummy.State)
		require.True(t, ok)
		hand = extra.HandNumber
		over = extra.HandComplete()
		match = extra.MatchComplete
	})
	return hand, over, match
}

func clockStep(t *testing.T, engine *game.Engine) {
	t.Helper()
	id := engine.CurrentPlayerID()
	require.NotEmpty(t, id)
	var act game.Action
	engine.WithState(func(s *game.State) { act = s.Rules.TimeoutAction(s) })
	require.NotNil(t, act)
	require.NoError(t, engine.SubmitAction(id, act))
}

func playUntilBetweenHands(t *testing.T, engine *game.Engine) {
	t.Helper()
	for step := range ginClockSteps {
		if engine.IsFinished() {
			t.Fatalf("match ended at step %d before a between-hands window", step)
		}
		_, over, match := ginExtra(t, engine)
		if over && !match {
			return
		}
		clockStep(t, engine)
	}
	t.Fatalf("no between-hands window within %d steps", ginClockSteps)
}

func otherSeatID(players []*game.Player, id string) string {
	for _, p := range players {
		if p.ID != id {
			return p.ID
		}
	}
	return ""
}

// Three full clock matches: one gin deal is a thin sample, and the auto-player
// mostly walls, so the match end is several hands out.
func TestGinRummyCasualHeadsUpPlaysOut(t *testing.T) {
	t.Parallel()
	for match := 1; match <= 3; match++ {
		t.Run(fmt.Sprintf("match_%d", match), func(t *testing.T) {
			t.Parallel()
			repo := newGinRepo()
			manager := lobby.NewManager(t.Context(), repo, catalog.NewRegistry())
			l, engine, players := startFilledLobby(t, manager, ginSlug, 2, false)
			releaseLobby(t, manager, l.Code())

			playOutByClock(t, engine, ginClockSteps)

			require.Equal(t, game.EndReasonWin, engine.EndReason())
			assertGinStandings(t, engine, players)
			repo.awaitFinalize(t)
			require.Empty(t, repo.calls(), "a casual match must not move Elo")
			assert.Equal(t, int32(1), repo.casual.Load())
		})
	}
}

func TestGinRummyRankedHeadsUpFinalizesOnce(t *testing.T) {
	t.Parallel()
	repo := newRankedFinalizeRecorder()
	manager := lobby.NewManager(t.Context(), repo, catalog.NewRegistry())
	l, engine, players := startFilledLobby(t, manager, ginSlug, 2, true)
	releaseLobby(t, manager, l.Code())

	playOutByClock(t, engine, ginClockSteps)

	require.Equal(t, game.EndReasonWin, engine.EndReason())
	assertGinStandings(t, engine, players)
	repo.awaitFinalize(t)
	require.True(t, manager.WaitForFinalizers(5*time.Second))

	finalized := repo.calls()
	require.Len(t, finalized, 1, "a ranked match finalizes once")
	assert.Equal(t, ginName, finalized[0].gameName)
	assert.ElementsMatch(t, []uuid.UUID{players[0].UserID, players[1].UserID}, finalized[0].userIDs)
}

func TestGinRummyLeaveMidHandForfeit(t *testing.T) {
	t.Parallel()
	repo := newGinRepo()
	manager := lobby.NewManager(t.Context(), repo, catalog.NewRegistry())
	l, engine, players := startFilledLobby(t, manager, ginSlug, 2, false)
	releaseLobby(t, manager, l.Code())

	stayer, leaver := players[0], players[1]
	require.NotPanics(t, func() { manager.LeaveLobby(leaver) })

	require.True(t, engine.IsFinished())
	require.Equal(t, game.EndReasonForfeit, engine.EndReason())
	assert.False(t, l.HasPlayer(leaver))

	var standings []game.Standing
	require.NotPanics(t, func() { standings = engine.Standings() })
	require.Len(t, standings, 2)
	assert.Equal(t, stayer.ID, standings[0].Player.ID)
	assert.False(t, standings[0].Left)
	assert.Equal(t, leaver.ID, standings[1].Player.ID)
	assert.True(t, standings[1].Left, "the leaver still places, below the seat that stayed")

	var out string
	require.NotPanics(t, func() {
		screen := ginrummyview.New(router.GlobalContext{
			Theme:  styles.NewTheme(true),
			Width:  80,
			Height: 24,
		}, game.Bind(engine, stayer.ID), ginSlug)
		t.Cleanup(func() {
			if c, ok := screen.(router.Closer); ok {
				c.Close()
			}
		})
		out = screen.View().Content
	})
	assert.Contains(t, out, "MATCH COMPLETE")
	assert.Contains(t, out, stayer.Name)
	assert.NotContains(t, out, "Press Esc to go back", "a forfeit shows the scoreboard")

	repo.awaitFinalize(t)
	assert.Equal(t, int32(1), repo.casual.Load(), "the forfeit is still a recorded result")
	assert.Empty(t, repo.calls())
}

func TestGinRummyForeignActionAndWrongSeat(t *testing.T) {
	t.Parallel()
	manager := lobby.NewManager(t.Context(), newGinRepo(), catalog.NewRegistry())
	l, engine, players := startFilledLobby(t, manager, ginSlug, 2, false)
	releaseLobby(t, manager, l.Code())

	actor := engine.CurrentPlayerID()
	wrong := otherSeatID(players, actor)
	require.NotEmpty(t, wrong)

	require.ErrorIs(t, engine.SubmitAction(actor, poker.ActionFold{}), game.ErrUnknownAction)
	require.ErrorIs(t, engine.SubmitAction(actor, gametest.ForeignAction{}), game.ErrUnknownAction)
	require.ErrorContains(t, engine.SubmitAction(wrong, ginrummy.ActionDrawStock{}), "wait for your turn")

	acts := []game.Action{
		ginrummy.ActionDrawStock{},
		ginrummy.ActionDrawDiscard{},
		ginrummy.ActionDiscard{},
		ginrummy.ActionKnock{},
		ginrummy.ActionNextHand{},
		poker.ActionFold{},
		gametest.ForeignAction{},
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("wrong-seat spam panicked: %v", r)
				}
			}()
			for _, act := range acts {
				if err := engine.SubmitAction(wrong, act); err == nil {
					t.Errorf("wrong seat %s accepted", act.Name())
				}
			}
		}()
	}
	wg.Wait()

	require.False(t, engine.IsFinished(), "refused moves must not end the table")
	clockStep(t, engine)
}

func TestGinRummyBetweenHandsRefusesOtherMoves(t *testing.T) {
	t.Parallel()
	manager := lobby.NewManager(t.Context(), newGinRepo(), catalog.NewRegistry())
	l, engine, players := startFilledLobby(t, manager, ginSlug, 2, false)
	releaseLobby(t, manager, l.Code())

	actor := engine.CurrentPlayerID()
	require.ErrorContains(t, engine.SubmitAction(actor, ginrummy.ActionNextHand{}), "still being played")

	handBefore, _, _ := ginExtra(t, engine)
	playUntilBetweenHands(t, engine)
	actor = engine.CurrentPlayerID()
	wrong := otherSeatID(players, actor)

	require.ErrorIs(t, engine.SubmitAction(actor, poker.ActionFold{}), game.ErrUnknownAction)
	require.ErrorIs(t, engine.SubmitAction(actor, ginrummy.ActionDrawStock{}), game.ErrHandOver)
	require.ErrorIs(t, engine.SubmitAction(actor, ginrummy.ActionDrawDiscard{}), game.ErrHandOver)
	require.ErrorIs(t, engine.SubmitAction(actor, ginrummy.ActionDiscard{}), game.ErrHandOver)
	require.ErrorIs(t, engine.SubmitAction(actor, ginrummy.ActionKnock{}), game.ErrHandOver)
	require.ErrorContains(t, engine.SubmitAction(wrong, ginrummy.ActionNextHand{}), "wait for your turn")

	require.NoError(t, engine.SubmitAction(actor, ginrummy.ActionNextHand{}))
	hand, over, match := ginExtra(t, engine)
	assert.Equal(t, handBefore+1, hand)
	assert.False(t, over)
	assert.False(t, match)
	require.ErrorContains(t, engine.SubmitAction(engine.CurrentPlayerID(), ginrummy.ActionNextHand{}),
		"still being played")
}
