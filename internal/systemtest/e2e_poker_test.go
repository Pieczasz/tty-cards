package systemtest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/gametest"
	"github.com/Pieczasz/tty-cards/internal/game/poker"
	"github.com/Pieczasz/tty-cards/internal/lobby"

	"github.com/stretchr/testify/require"

	"uuid"
)

// pokerRepo is the ranked recorder plus the casual and interrupted writes a finished
// table always makes. The recorder leaves those on a nil interface, which panics the
// watcher.
type pokerRepo struct {
	*rankedFinalizeRecorder
}

func newPokerRepo() *pokerRepo {
	return &pokerRepo{rankedFinalizeRecorder: newRankedFinalizeRecorder()}
}

func (pokerRepo) RecordCasualMatch(context.Context, db.GameRef, []uuid.UUID) error {
	return nil
}

func (pokerRepo) FinalizeInterruptedMatch(
	context.Context, db.GameRef, []uuid.UUID, []int, []uuid.UUID,
) (map[uuid.UUID]uint32, error) {
	return map[uuid.UUID]uint32{}, nil
}

func newPokerManager(t *testing.T) (*lobby.Manager, *pokerRepo) {
	t.Helper()
	repo := newPokerRepo()
	return lobby.NewManager(t.Context(), repo, catalog.NewRegistry()), repo
}

type seatView struct {
	allIn  bool
	folded bool
}

type tableView struct {
	pool         uint
	handComplete bool
	seats        map[string]seatView
}

func readTable(t *testing.T, engine *game.Engine) tableView {
	t.Helper()
	var view tableView
	engine.WithState(func(state *game.State) {
		extra, ok := state.Extra.(*poker.State)
		require.True(t, ok)
		view.pool = extra.Pool
		view.handComplete = extra.HandComplete()
		view.seats = make(map[string]seatView, len(extra.Seats))
		for id, seat := range extra.Seats {
			view.seats[id] = seatView{allIn: seat.AllIn, folded: seat.Folded}
		}
	})
	return view
}

func pokerPlayer(t *testing.T, players []*game.Player, id string) *game.Player {
	t.Helper()
	for _, p := range players {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("no seated player %s", id)
	return nil
}

func leaveSeat(t *testing.T, manager *lobby.Manager, table *lobby.Lobby, players []*game.Player, id string) {
	t.Helper()
	p := pokerPlayer(t, players, id)
	manager.LeaveLobby(p)
	require.False(t, table.HasPlayer(p), "leaving must drop %s from the roster", id)
}

// requireSettled is the chip invariant for a match that reached a real end: nothing
// created or destroyed, and nothing left sitting in the pot.
func requireSettled(t *testing.T, engine *game.Engine, before uint, players []*game.Player) {
	t.Helper()
	require.Equal(t, before, chipsInPlay(t, engine), "chips must be conserved")
	require.Zero(t, readTable(t, engine).pool, "a finished hand must not leave chips in the pot")
	ids := standingIDs(engine)
	require.NotEmpty(t, ids)
	for _, p := range players {
		require.Contains(t, ids, p.ID, "every seat is ranked, including anyone who left")
	}
}

func finishBy(t *testing.T, engine *game.Engine, clock bool) {
	t.Helper()
	if clock {
		playOutByClock(t, engine)
		return
	}
	playOutMatch(t, engine)
}

func runFullMatch(t *testing.T, seats int, ranked, clock bool) {
	t.Helper()
	manager, repo := newPokerManager(t)
	_, engine, players := startFilledLobby(t, manager, pokerSlug, seats, ranked)
	before := chipsInPlay(t, engine)
	require.Equal(t, uint(seats)*poker.DefaultStack, before)

	finishBy(t, engine, clock)

	require.Equal(t, game.EndReasonWin, engine.EndReason())
	requireSettled(t, engine, before, players)
	if ranked {
		repo.awaitFinalize(t)
		calls := repo.calls()
		require.Len(t, calls, 1)
		require.Equal(t, pokerGame, calls[0].gameName)
		require.Len(t, calls[0].userIDs, len(players))
		for _, p := range players {
			require.Contains(t, calls[0].userIDs, p.UserID)
		}
	}
	require.True(t, manager.WaitForFinalizers(5*time.Second))
}

func TestPokerE2E_CasualSeats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		seats int
		clock bool
	}{
		{name: "heads_up_clock", seats: 2, clock: true},
		{name: "heads_up_actions", seats: 2},
		{name: "four_clock", seats: 4, clock: true},
		{name: "five_actions", seats: 5},
		{name: "six_clock", seats: 6, clock: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runFullMatch(t, tt.seats, false, tt.clock)
		})
	}
}

func TestPokerE2E_RankedFullMatch(t *testing.T) {
	t.Parallel()
	runFullMatch(t, 6, true, true)
}

func TestPokerE2E_FiveFullMatches(t *testing.T) {
	t.Parallel()
	seats := []int{2, 4, 5, 6, 4}
	for i, n := range seats {
		runFullMatch(t, n, false, i%2 == 0)
	}
}

func TestPokerE2E_MidHandLeave(t *testing.T) {
	t.Parallel()

	t.Run("all_in", func(t *testing.T) {
		t.Parallel()
		manager, _ := newPokerManager(t)
		table, engine, players := startFilledLobby(t, manager, pokerSlug, 4, false)
		before := chipsInPlay(t, engine)

		shover := engine.CurrentPlayerID()
		require.NoError(t, engine.SubmitAction(shover, poker.ActionAllIn{}))
		snap := readTable(t, engine)
		require.True(t, snap.seats[shover].allIn)
		require.False(t, snap.handComplete, "other seats can still act on the shove")
		require.NotZero(t, snap.pool)

		leaveSeat(t, manager, table, players, shover)
		after := readTable(t, engine)
		require.False(t, after.seats[shover].folded, "an all-in seat has nothing left to fold")
		require.False(t, engine.IsFinished())

		finishBy(t, engine, true)
		require.Equal(t, game.EndReasonWin, engine.EndReason())
		requireSettled(t, engine, before, players)
		require.True(t, manager.WaitForFinalizers(5*time.Second))
	})

	t.Run("not_all_in", func(t *testing.T) {
		t.Parallel()
		manager, _ := newPokerManager(t)
		table, engine, players := startFilledLobby(t, manager, pokerSlug, 4, false)
		before := chipsInPlay(t, engine)

		var leaver string
		for _, p := range players {
			if !readTable(t, engine).seats[p.ID].allIn {
				leaver = p.ID
				break
			}
		}
		require.NotEmpty(t, leaver)
		require.False(t, readTable(t, engine).handComplete)

		leaveSeat(t, manager, table, players, leaver)
		require.True(t, readTable(t, engine).seats[leaver].folded, "a seat with chips behind forfeits the hand")
		require.False(t, engine.IsFinished(), "the other seats still have a hand to play")

		finishBy(t, engine, false)
		require.Equal(t, game.EndReasonWin, engine.EndReason())
		requireSettled(t, engine, before, players)
		require.True(t, manager.WaitForFinalizers(5*time.Second))
	})
}

func TestPokerE2E_LastSeatedLeaveWhileAllInLeaversContest(t *testing.T) {
	t.Parallel()

	t.Run("heads_up", func(t *testing.T) {
		t.Parallel()
		leaveLastAgainstAllIn(t, 2)
	})
	t.Run("two_all_in_leavers", func(t *testing.T) {
		t.Parallel()
		leaveLastAgainstAllIn(t, 3)
	})
}

// leaveLastAgainstAllIn shoves until one seat still has a decision, drops every
// all-in seat, then drops the last player still on the roster. The pot has to be
// paid to the all-in leavers: an empty table used to abandon with it still in Pool.
func leaveLastAgainstAllIn(t *testing.T, seats int) {
	t.Helper()
	require.GreaterOrEqual(t, seats, 2)
	manager, _ := newPokerManager(t)
	table, engine, players := startFilledLobby(t, manager, pokerSlug, seats, false)
	before := chipsInPlay(t, engine)

	var allIn []string
	for len(allIn) < seats-1 {
		id := engine.CurrentPlayerID()
		require.NotEmpty(t, id)
		require.NoError(t, engine.SubmitAction(id, poker.ActionAllIn{}))
		require.True(t, readTable(t, engine).seats[id].allIn, "%s must be all-in", id)
		allIn = append(allIn, id)
		if seats > 2 && len(allIn) < seats-1 {
			// The next live seat calls the shove and, on equal stacks, is all-in too.
			caller := engine.CurrentPlayerID()
			require.NoError(t, engine.SubmitAction(caller, poker.ActionCall{}))
			require.True(t, readTable(t, engine).seats[caller].allIn, "%s must be all-in", caller)
			allIn = append(allIn, caller)
		}
	}

	snap := readTable(t, engine)
	require.False(t, snap.handComplete, "the last seat has not answered the shove")
	require.NotZero(t, snap.pool)
	for _, id := range allIn {
		leaveSeat(t, manager, table, players, id)
		require.False(t, readTable(t, engine).seats[id].folded)
	}
	require.False(t, engine.IsFinished(), "all-in leavers keep the hand open for the last seat")
	require.NotZero(t, readTable(t, engine).pool, "the pot is still contested")

	last := engine.CurrentPlayerID()
	require.NotContains(t, allIn, last)
	manager.LeaveLobby(pokerPlayer(t, players, last))

	require.True(t, engine.IsFinished())
	require.Equal(t, game.EndReasonAbandoned, engine.EndReason())
	requireSettled(t, engine, before, players)
	require.True(t, manager.WaitForFinalizers(5*time.Second))
}

func TestPokerE2E_IllegalActionsThenTimeout(t *testing.T) {
	t.Parallel()

	manager, _ := newPokerManager(t)
	_, engine, players := startFilledLobby(t, manager, pokerSlug, 4, false)
	before := chipsInPlay(t, engine)
	actor := engine.CurrentPlayerID()

	var other string
	for _, p := range players {
		if p.ID != actor {
			other = p.ID
			break
		}
	}
	require.NotEmpty(t, other)

	require.ErrorContains(t, engine.SubmitAction(other, poker.ActionFold{}), "wait for your turn")
	require.ErrorContains(t, engine.SubmitAction(other, poker.ActionRaiseTo{Amount: 500}), "wait for your turn")
	require.ErrorIs(t, engine.SubmitAction(actor, gametest.ForeignAction{}), game.ErrUnknownAction)
	require.Error(t, engine.SubmitAction(actor, poker.ActionCheck{}))

	var current, lo, hi uint
	var ok bool
	engine.WithState(func(state *game.State) {
		extra, isPoker := state.Extra.(*poker.State)
		require.True(t, isPoker)
		current = extra.CurrentBet
		lo, hi, ok = poker.RaiseBounds(state, actor)
	})
	require.Error(t, engine.SubmitAction(actor, poker.ActionRaiseTo{Amount: current}))
	require.Error(t, engine.SubmitAction(actor, poker.ActionRaiseTo{Amount: hi + 1}))
	if ok && lo > 0 {
		require.Error(t, engine.SubmitAction(actor, poker.ActionRaiseTo{Amount: lo - 1}))
	}

	require.Equal(t, before, chipsInPlay(t, engine), "a refused action must not move chips")
	require.Equal(t, actor, engine.CurrentPlayerID(), "a refused action must not pass the turn")

	finishBy(t, engine, true)
	require.Equal(t, game.EndReasonWin, engine.EndReason())
	requireSettled(t, engine, before, players)
	require.True(t, manager.WaitForFinalizers(5*time.Second))
}

func TestPokerE2E_WrongSeatAndLeaveRace(t *testing.T) {
	t.Parallel()

	manager, _ := newPokerManager(t)
	table, engine, players := startFilledLobby(t, manager, pokerSlug, 6, false)
	before := chipsInPlay(t, engine)
	actor := engine.CurrentPlayerID()

	var wrong []*game.Player
	for _, p := range players {
		if p.ID != actor {
			wrong = append(wrong, p)
		}
	}
	require.NotEmpty(t, wrong)

	var wg sync.WaitGroup
	start := make(chan struct{})
	panics := make(chan any, len(wrong)+2)
	recoverPanic := func() {
		if r := recover(); r != nil {
			panics <- r
		}
	}
	for _, p := range wrong {
		wg.Go(func() {
			defer recoverPanic()
			<-start
			for range 40 {
				_ = engine.SubmitAction(p.ID, poker.ActionFold{})
				_ = engine.SubmitAction(p.ID, poker.ActionRaiseTo{Amount: 1})
				_ = engine.SubmitAction(p.ID, gametest.ForeignAction{})
			}
		})
	}
	leavers := wrong[:2]
	for _, p := range leavers {
		wg.Go(func() {
			defer recoverPanic()
			<-start
			manager.LeaveLobby(p)
		})
	}
	close(start)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("wrong-seat actions raced with LeaveLobby and hung")
	}
	var recovered []any
	close(panics)
	for r := range panics {
		recovered = append(recovered, r)
	}
	require.Empty(t, recovered)
	for _, p := range leavers {
		require.False(t, table.HasPlayer(p))
	}

	if !engine.IsFinished() {
		finishBy(t, engine, true)
	}
	require.Equal(t, game.EndReasonWin, engine.EndReason())
	requireSettled(t, engine, before, players)
	require.True(t, manager.WaitForFinalizers(5*time.Second))
}
