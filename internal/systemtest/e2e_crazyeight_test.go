package systemtest

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/poker"
	"github.com/Pieczasz/tty-cards/internal/lobby"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const crazyEightsSlug = "crazy_eights"

// crazyEightsSeatCap is the largest table this e2e fills. A game whose MaxPlayers
// exceeds six is still only sat to six.
func crazyEightsSeatCap(maxPlayers int) int {
	if maxPlayers > 6 {
		return 6
	}
	return maxPlayers
}

func crazyEightsModule(t *testing.T) game.Module {
	t.Helper()
	mod, ok := catalog.NewRegistry().Module(crazyEightsSlug)
	require.True(t, ok, "catalog is missing crazy eights")
	return mod
}

// startCrazyEights seats n players and deals. Casual matches pass a nil repository:
// that is the server with nothing to persist, and the watcher still returns.
func startCrazyEights(
	t *testing.T, ranked bool, n int,
) (*lobby.Manager, *lobby.Lobby, *game.Engine, []*game.Player, *rankedFinalizeRecorder) {
	t.Helper()
	var rec *rankedFinalizeRecorder
	var match db.MatchRepository
	if ranked {
		rec = newRankedFinalizeRecorder()
		match = rec
	}
	manager := lobby.NewManager(t.Context(), match, catalog.NewRegistry())
	l, engine, players := startFilledLobby(t, manager, crazyEightsSlug, n, ranked)
	t.Cleanup(func() {
		for _, p := range players {
			manager.LeaveLobby(p)
		}
	})
	return manager, l, engine, players, rec
}

func advanceByTimeout(t *testing.T, engine *game.Engine, steps int) {
	t.Helper()
	for range steps {
		require.False(t, engine.IsFinished(), "ended before %d auto-play steps", steps)
		id := engine.CurrentPlayerID()
		require.NotEmpty(t, id)
		var act game.Action
		engine.WithState(func(s *game.State) { act = s.Rules.TimeoutAction(s) })
		require.NotNil(t, act)
		require.NoError(t, engine.SubmitAction(id, act))
	}
}

// assertCrazyEightsDeck checks the shedding invariant: the 52-card stock is still
// exactly the standard deck, spread across the stock, the discard, and every hand,
// including cards a leaver was holding when they walked.
func assertCrazyEightsDeck(t *testing.T, engine *game.Engine) {
	t.Helper()
	got := map[deck.Card]int{}
	engine.WithState(func(s *game.State) {
		add := func(cards []deck.Card) {
			for _, c := range cards {
				got[c]++
			}
		}
		if s.Deck != nil {
			add(s.Deck.Cards())
		}
		if s.Discard != nil {
			add(s.Discard.Cards())
		}
		for _, p := range s.Players {
			add(p.Cards)
		}
		for _, p := range s.LeftPlayers {
			add(p.Cards)
		}
	})
	want := map[deck.Card]int{}
	for _, c := range deck.Standard() {
		want[c]++
	}
	require.Equal(t, want, got, "crazy eights must conserve the standard deck")
}

func assertStandingsCover(t *testing.T, engine *game.Engine, stayed []*game.Player) {
	t.Helper()
	standings := engine.Standings()
	require.NotEmpty(t, standings)
	byID := map[string]game.Standing{}
	for _, s := range standings {
		require.NotNil(t, s.Player)
		byID[s.Player.ID] = s
	}
	for _, p := range stayed {
		s, ok := byID[p.ID]
		require.Truef(t, ok, "standings missing %s", p.ID)
		assert.Falsef(t, s.Left, "%s stayed and must not be marked left", p.Name)
	}
}

func finishCrazyEights(t *testing.T, manager *lobby.Manager, engine *game.Engine, stayed []*game.Player) {
	t.Helper()
	playOutByClock(t, engine)
	require.True(t, engine.IsFinished())
	require.Equal(t, game.EndReasonWin, engine.EndReason())
	assertCrazyEightsDeck(t, engine)
	assertStandingsCover(t, engine, stayed)
	require.Len(t, engine.Standings(), len(stayed))
	require.True(t, manager.WaitForFinalizers(5*time.Second))
}

func TestCrazyEights_CasualMinAndMaxSeats(t *testing.T) {
	t.Parallel()
	mod := crazyEightsModule(t)
	for _, n := range []int{mod.MinPlayers, crazyEightsSeatCap(mod.MaxPlayers)} {
		t.Run(fmt.Sprintf("%d_seats", n), func(t *testing.T) {
			t.Parallel()
			manager, _, engine, players, _ := startCrazyEights(t, false, n)
			assertCrazyEightsDeck(t, engine)
			finishCrazyEights(t, manager, engine, players)
		})
	}
}

func TestCrazyEights_RankedMidGameLeaveFinalizes(t *testing.T) {
	t.Parallel()
	mod := crazyEightsModule(t)
	require.GreaterOrEqual(t, crazyEightsSeatCap(mod.MaxPlayers), 4)
	manager, l, engine, players, repo := startCrazyEights(t, true, 4)

	advanceByTimeout(t, engine, 2)
	leaver := players[1]
	require.False(t, l.Snapshot().IsLeader(leaver))
	manager.LeaveLobby(leaver)
	assert.False(t, l.HasPlayer(leaver))
	assertCrazyEightsDeck(t, engine)
	require.False(t, engine.IsFinished(), "the remaining seats still have a hand")

	playOutByClock(t, engine)
	require.Equal(t, game.EndReasonWin, engine.EndReason())
	assertCrazyEightsDeck(t, engine)

	stayed := []*game.Player{players[0], players[2], players[3]}
	assertStandingsCover(t, engine, stayed)
	var leaverStanding game.Standing
	for _, s := range engine.Standings() {
		if s.Player != nil && s.Player.ID == leaver.ID {
			leaverStanding = s
		}
	}
	require.NotNil(t, leaverStanding.Player, "a player who left still places")
	assert.True(t, leaverStanding.Left)

	repo.awaitFinalize(t)
	require.True(t, manager.WaitForFinalizers(5*time.Second))
	finalized := repo.calls()
	require.Len(t, finalized, 1)
	assert.Equal(t, mod.Name, finalized[0].gameName)
	require.Len(t, finalized[0].userIDs, len(players))
	for _, p := range players {
		assert.Contains(t, finalized[0].userIDs, p.UserID)
	}
}

func TestCrazyEights_ForeignActionDoesNotStick(t *testing.T) {
	t.Parallel()
	manager, _, engine, players, _ := startCrazyEights(t, false, 2)
	onTurn := engine.CurrentPlayerID()
	require.ErrorIs(t, engine.SubmitAction(onTurn, poker.ActionFold{}), game.ErrUnknownAction)
	require.False(t, engine.IsFinished())
	assertCrazyEightsDeck(t, engine)

	off := players[0]
	if off.ID == onTurn {
		off = players[1]
	}
	require.Error(t, engine.SubmitAction(off.ID, poker.ActionFold{}))
	require.Error(t, engine.SubmitAction("not-a-seat", poker.ActionFold{}))
	require.Equal(t, onTurn, engine.CurrentPlayerID(), "a refused move must not advance the turn")

	finishCrazyEights(t, manager, engine, players)
}

func TestCrazyEights_ConcurrentWrongSeatSpam(t *testing.T) {
	t.Parallel()
	manager, _, engine, players, _ := startCrazyEights(t, false, 4)
	onTurn := engine.CurrentPlayerID()

	var wg sync.WaitGroup
	var panics, applied, refused atomic.Int32
	for range 24 {
		for _, p := range players {
			if p.ID == onTurn {
				continue
			}
			wg.Go(func() {
				defer func() {
					if recover() != nil {
						panics.Add(1)
					}
				}()
				if err := engine.SubmitAction(p.ID, poker.ActionFold{}); err != nil {
					refused.Add(1)
					return
				}
				applied.Add(1)
			})
		}
	}
	wg.Wait()

	require.Zero(t, panics.Load())
	require.Zero(t, applied.Load())
	require.Positive(t, refused.Load())
	require.False(t, engine.IsFinished())
	require.Equal(t, onTurn, engine.CurrentPlayerID())
	assertCrazyEightsDeck(t, engine)
	finishCrazyEights(t, manager, engine, players)
}

func TestCrazyEights_DisconnectAndLeaveConservesDeck(t *testing.T) {
	t.Parallel()
	mod := crazyEightsModule(t)
	n := crazyEightsSeatCap(mod.MaxPlayers)
	require.GreaterOrEqual(t, n, 4)
	manager, l, engine, players, _ := startCrazyEights(t, false, n)
	assertCrazyEightsDeck(t, engine)

	gone := map[string]*game.Player{}
	for point, idx := range []int{1, 2, 3} {
		advanceByTimeout(t, engine, 2)
		p := players[idx]
		if point%2 == 0 {
			manager.DisconnectPlayer(p)
			require.Truef(t, l.HasPlayer(p), "point %d: a dropped session keeps the seat", point)
			assertCrazyEightsDeck(t, engine)
			advanceByTimeout(t, engine, 1)
			require.True(t, l.HasPlayer(p), "grace has not given the seat up")
		}
		manager.LeaveLobby(p)
		require.Falsef(t, l.HasPlayer(p), "point %d: leaving drops the seat", point)
		assertCrazyEightsDeck(t, engine)
		require.False(t, engine.IsFinished())
		gone[p.ID] = p
	}

	stayed := make([]*game.Player, 0, len(players)-len(gone))
	for _, p := range players {
		if _, left := gone[p.ID]; !left {
			stayed = append(stayed, p)
		}
	}
	playOutByClock(t, engine)
	require.Equal(t, game.EndReasonWin, engine.EndReason())
	assertCrazyEightsDeck(t, engine)
	assertStandingsCover(t, engine, stayed)
	for _, p := range gone {
		var standing game.Standing
		for _, s := range engine.Standings() {
			if s.Player != nil && s.Player.ID == p.ID {
				standing = s
			}
		}
		require.NotNilf(t, standing.Player, "%s left and must still place", p.Name)
		assert.Truef(t, standing.Left, "%s left", p.Name)
	}
	require.True(t, manager.WaitForFinalizers(5*time.Second))
}

func TestCrazyEights_RepeatedFullMatches(t *testing.T) {
	t.Parallel()
	mod := crazyEightsModule(t)
	capSeats := crazyEightsSeatCap(mod.MaxPlayers)
	span := capSeats - mod.MinPlayers + 1
	require.Positive(t, span)
	for i := range 5 {
		n := mod.MinPlayers + (i % span)
		t.Run(fmt.Sprintf("match_%d_%d_seats", i+1, n), func(t *testing.T) {
			t.Parallel()
			manager, _, engine, players, _ := startCrazyEights(t, false, n)
			finishCrazyEights(t, manager, engine, players)
		})
	}
}
