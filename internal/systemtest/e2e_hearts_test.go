package systemtest

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/crazyeight"
	"github.com/Pieczasz/tty-cards/internal/game/gametest"
	"github.com/Pieczasz/tty-cards/internal/game/ginrummy"
	"github.com/Pieczasz/tty-cards/internal/game/hearts"
	"github.com/Pieczasz/tty-cards/internal/game/poker"
	"github.com/Pieczasz/tty-cards/internal/game/uno"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"uuid"

	"github.com/stretchr/testify/require"
)

const (
	heartsSlug = "hearts"
	heartsGame = "Hearts"
	// heartsClockSteps is the auto-play budget. A quiet table reaches 100 in well
	// under a thousand moves; the ceiling is the same one the rules contract uses.
	heartsClockSteps = 5000
	// heartsTarget is the published match length: someone has to reach it.
	heartsTarget = 100
	// heartsHandPoints is every penalty card in a hand. Moon shots pay it out
	// three times, so a finished match total is a multiple of it, never a leftover.
	heartsHandPoints = 26
)

// heartsRepo records every match write. Ranked wins go through the shared recorder;
// casual and interrupted matches have no method there, and calling the embedded nil
// repository would panic the lobby watcher.
type heartsRepo struct {
	*rankedFinalizeRecorder

	mu     sync.Mutex
	writes []heartsWrite
}

type heartsWrite struct {
	kind    string
	game    string
	userIDs []uuid.UUID
	places  []int
	leavers []uuid.UUID
}

func newHeartsRepo() *heartsRepo {
	return &heartsRepo{rankedFinalizeRecorder: newRankedFinalizeRecorder()}
}

func (r *heartsRepo) RecordCasualMatch(ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.note("casual", ref, orderedUserIDs, nil, nil)
	r.fire()
	return nil
}

func (r *heartsRepo) FinalizeRankedMatch(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID, places []int,
) (map[uuid.UUID]uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.note("ranked", ref, orderedUserIDs, places, nil)
	return r.rankedFinalizeRecorder.FinalizeRankedMatch(ctx, ref, orderedUserIDs, places)
}

func (r *heartsRepo) FinalizeInterruptedMatch(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID, places []int, leavers []uuid.UUID,
) (map[uuid.UUID]uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.note("interrupted", ref, orderedUserIDs, places, leavers)
	r.fire()
	return map[uuid.UUID]uint32{}, nil
}

func (r *heartsRepo) note(kind string, ref db.GameRef, userIDs []uuid.UUID, places []int, leavers []uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, heartsWrite{
		kind:    kind,
		game:    ref.Name,
		userIDs: slices.Clone(userIDs),
		places:  slices.Clone(places),
		leavers: slices.Clone(leavers),
	})
}

func (r *heartsRepo) ofKind(kind string) []heartsWrite {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]heartsWrite, 0, len(r.writes))
	for _, w := range r.writes {
		if w.kind != kind {
			continue
		}
		out = append(out, heartsWrite{
			kind:    w.kind,
			game:    w.game,
			userIDs: slices.Clone(w.userIDs),
			places:  slices.Clone(w.places),
			leavers: slices.Clone(w.leavers),
		})
	}
	return out
}

func heartsManager(t *testing.T, repo db.MatchRepository) *lobby.Manager {
	t.Helper()
	return lobby.NewManager(t.Context(), repo, catalog.NewRegistry())
}

// keepLobby closes the table when the test ends, including on failure, so a clock
// still armed for a pass cannot fire into the next test.
func keepLobby(t *testing.T, manager *lobby.Manager, l *lobby.Lobby) {
	t.Helper()
	t.Cleanup(func() {
		manager.RemoveLobby(l.Code())
		require.True(t, manager.WaitForFinalizers(15*time.Second))
	})
}

func TestHearts_CasualClockMatches(t *testing.T) {
	t.Parallel()

	repo := newHeartsRepo()
	manager := heartsManager(t, repo)
	l, engine, players := startFilledLobby(t, manager, heartsSlug, 4, false)
	keepLobby(t, manager, l)
	events := subscribeLobby(t, l, players[0].ID)

	require.False(t, l.Snapshot().Ranked)
	require.Equal(t, heartsSlug, l.Snapshot().Slug)
	require.Equal(t, heartsGame, l.Snapshot().GameName)
	require.Equal(t, 4, l.Snapshot().Players())

	const matches = 5
	for match := 1; match <= matches; match++ {
		if match > 1 {
			for _, p := range players {
				require.NoError(t, manager.ToggleReady(p), "match %d ready", match)
			}
			engine = awaitGameStart(t, l, events)
		}
		playOutByClock(t, engine, heartsClockSteps)
		assertClockWin(t, engine, players)

		repo.awaitFinalize(t)
		casual := repo.ofKind("casual")
		require.Len(t, casual, match, "match %d", match)
		require.Equal(t, heartsGame, casual[match-1].game)
		require.Equal(t, standingUserIDs(engine.Standings()), casual[match-1].userIDs)
		require.False(t, l.Snapshot().InGame, "a finished match reopens the table")
		require.Equal(t, 4, l.Snapshot().Players())
	}
	require.Empty(t, repo.ofKind("ranked"))
	require.Empty(t, repo.ofKind("interrupted"))
	require.Empty(t, repo.calls())
}

func TestHearts_RankedMatchFinalizesOnce(t *testing.T) {
	t.Parallel()

	repo := newHeartsRepo()
	manager := heartsManager(t, repo)
	l, engine, players := startFilledLobby(t, manager, heartsSlug, 4, true)
	keepLobby(t, manager, l)
	require.True(t, l.Snapshot().Ranked)

	playOutByClock(t, engine, heartsClockSteps)
	assertClockWin(t, engine, players)

	repo.awaitFinalize(t)
	ranked := repo.calls()
	require.Len(t, ranked, 1, "a ranked hearts match finalizes once")
	require.Equal(t, heartsGame, ranked[0].gameName)
	require.Equal(t, standingUserIDs(engine.Standings()), ranked[0].userIDs)
	require.Len(t, ranked[0].userIDs, 4)
	require.Len(t, repo.ofKind("ranked"), 1)
	require.Empty(t, repo.ofKind("casual"))
	require.Empty(t, repo.ofKind("interrupted"))
}

func TestHearts_LeaveMidPassAndMidTrick(t *testing.T) {
	t.Parallel()

	t.Run("mid-pass", func(t *testing.T) {
		t.Parallel()
		leaveHeartsDuring(t,
			func(extra *hearts.State) bool {
				return extra.Phase == hearts.PhasePassing && len(extra.PendingPasses) == 1
			},
			func(extra *hearts.State, id string) bool {
				_, passed := extra.PendingPasses[id]
				return !passed
			},
		)
	})

	t.Run("mid-trick", func(t *testing.T) {
		t.Parallel()
		leaveHeartsDuring(t,
			func(extra *hearts.State) bool {
				return extra.Phase == hearts.PhaseTrickPlay && !extra.TrickComplete && len(extra.TrickCards) == 2
			},
			func(extra *hearts.State, id string) bool {
				_, played := extra.TrickCards[id]
				return !played
			},
		)
	})
}

func TestHearts_ForeignActionAndWrongSeatSpam(t *testing.T) {
	t.Parallel()

	manager := heartsManager(t, newHeartsRepo())
	l, engine, players := startFilledLobby(t, manager, heartsSlug, 4, false)
	keepLobby(t, manager, l)

	current := engine.CurrentPlayerID()
	require.NotEmpty(t, current)
	before := heartsProgress(t, engine)

	foreign := []game.Action{
		poker.ActionFold{},
		poker.ActionCheck{},
		poker.ActionCall{},
		poker.ActionAllIn{},
		ginrummy.ActionDrawStock{},
		ginrummy.ActionDrawDiscard{},
		uno.ActionDrawCard{},
		crazyeight.ActionDrawCard{},
		gametest.ForeignAction{},
	}
	for _, act := range foreign {
		err := engine.SubmitAction(current, act)
		require.ErrorIs(t, err, game.ErrUnknownAction, act.Name())
	}
	// A hearts move from the wrong part of the hand is a refusal, not a rules error
	// that ends the table.
	err := engine.SubmitAction(current, hearts.ActionPlayCard{})
	require.Error(t, err)
	require.NotErrorIs(t, err, game.ErrUnknownAction)
	err = engine.SubmitAction(current, hearts.ActionPassCards{})
	require.Error(t, err)
	err = engine.SubmitAction(current, hearts.ActionNextHand{})
	require.Error(t, err)
	require.False(t, engine.IsFinished())
	require.Equal(t, before, heartsProgress(t, engine))

	spamWrongSeats(t, engine, players, current)
	require.Equal(t, before, heartsProgress(t, engine), "refused moves must not change the hand")
	require.False(t, engine.IsFinished())

	id := engine.CurrentPlayerID()
	var act game.Action
	engine.WithState(func(s *game.State) { act = s.Rules.TimeoutAction(s) })
	require.NotNil(t, act)
	require.NoError(t, engine.SubmitAction(id, act), "the seat on turn can still play after the spam")
	require.False(t, engine.IsFinished())
}

func TestHearts_CreateLobbyRequiresFourPlayers(t *testing.T) {
	t.Parallel()

	reg := catalog.NewRegistry()
	mod, ok := reg.Module(heartsSlug)
	require.True(t, ok)
	require.Equal(t, 4, mod.MinPlayers)
	require.Equal(t, 4, mod.MaxPlayers)

	manager := heartsManager(t, newHeartsRepo())
	leader := newPlayer(testutil.UID(1), "alice")
	for _, n := range []int{1, 2, 3, 5, 6, 8, 9} {
		_, err := manager.CreateLobby(leader, lobby.WithCardGame(heartsSlug), lobby.WithMaxPlayers(n))
		require.ErrorIs(t, err, lobby.ErrRefused, "max players %d", n)
		require.ErrorContains(t, err, "between 4 and 4")
	}
	require.Nil(t, manager.FindLobbyByPlayer(leader), "a refused create must not keep the seat")

	l, err := manager.CreateLobby(leader, lobby.WithCardGame(heartsSlug), lobby.WithMaxPlayers(4))
	require.NoError(t, err)
	keepLobby(t, manager, l)
	assertFourSeatTable(t, l)

	require.NoError(t, l.SetMaxPlayers(leader, 4))
	require.ErrorIs(t, l.SetMaxPlayers(leader, 3), lobby.ErrRefused)
	require.ErrorIs(t, l.SetMaxPlayers(leader, 5), lobby.ErrRefused)
	require.Equal(t, 4, l.Snapshot().Limit, "a refused resize leaves the four-seat cap")

	for i := 2; i <= 4; i++ {
		guest := newPlayer(testutil.UID(i), fmt.Sprintf("p%d", i))
		require.NoError(t, joinErr(manager.JoinLobbyByCode(l.Code(), guest)))
	}
	extra := newPlayer(testutil.UID(5), "extra")
	require.ErrorIs(t, joinErr(manager.JoinLobbyByCode(l.Code(), extra)), lobby.ErrRefused)
	require.Equal(t, 4, l.Snapshot().Players())
	require.False(t, l.HasPlayer(extra))

	// Zero is "no preference", which the lobby clamps to the only legal size.
	other := newPlayer(testutil.UID(6), "blair")
	byDefault, err := manager.CreateLobby(other, lobby.WithCardGame(heartsSlug))
	require.NoError(t, err)
	keepLobby(t, manager, byDefault)
	assertFourSeatTable(t, byDefault)

	unset := newPlayer(testutil.UID(7), "casey")
	clamped, err := manager.CreateLobby(unset, lobby.WithCardGame(heartsSlug), lobby.WithMaxPlayers(0))
	require.NoError(t, err)
	keepLobby(t, manager, clamped)
	assertFourSeatTable(t, clamped)
}

func leaveHeartsDuring(
	t *testing.T,
	ready func(*hearts.State) bool,
	canLeave func(*hearts.State, string) bool,
) {
	t.Helper()

	repo := newHeartsRepo()
	manager := heartsManager(t, repo)
	l, engine, players := startFilledLobby(t, manager, heartsSlug, 4, true)
	keepLobby(t, manager, l)
	require.True(t, l.Snapshot().Ranked)

	playHeartsUntil(t, engine, ready)

	var scores map[string]int
	var leaver *game.Player
	engine.WithState(func(s *game.State) {
		extra := heartsState(t, s)
		scores = liveScores(extra)
		for _, p := range players[1:] {
			if canLeave(extra, p.ID) {
				leaver = p
				break
			}
		}
	})
	require.NotNil(t, leaver, "a guest still in the hand can leave")

	require.NotPanics(t, func() { manager.LeaveLobby(leaver) })
	require.True(t, engine.IsFinished())
	require.Equal(t, game.EndReasonInterrupted, engine.EndReason())
	require.False(t, l.HasPlayer(leaver))
	for _, p := range players {
		if p == leaver {
			continue
		}
		require.True(t, l.HasPlayer(p), p.ID)
	}

	standings := engine.Standings()
	require.Len(t, standings, 4)
	require.Equal(t, leaver.ID, standings[3].Player.ID)
	require.True(t, standings[3].Left)
	var prevScore, prevPlace int
	for i, s := range standings[:3] {
		require.False(t, s.Left)
		require.NotEqual(t, leaver.ID, s.Player.ID)
		require.Less(t, s.Place, standings[3].Place, "a leaver places after everyone still seated")
		sc := scores[s.Player.ID]
		if i == 0 {
			require.Equal(t, 1, s.Place)
		} else {
			require.GreaterOrEqual(t, sc, prevScore)
			if sc == prevScore {
				require.Equal(t, prevPlace, s.Place)
			} else {
				require.Greater(t, s.Place, prevPlace)
			}
		}
		prevScore, prevPlace = sc, s.Place
	}
	// The quit does not buy a better score: same total still ranks last.
	require.Greater(t, standings[3].Place, standings[2].Place)

	repo.awaitFinalize(t)
	require.False(t, l.Snapshot().InGame)
	interrupted := repo.ofKind("interrupted")
	require.Len(t, interrupted, 1)
	require.Equal(t, heartsGame, interrupted[0].game)
	require.Equal(t, standingUserIDs(standings), interrupted[0].userIDs)
	require.Equal(t, standingPlaces(standings), interrupted[0].places)
	require.Equal(t, []uuid.UUID{leaver.UserID}, interrupted[0].leavers)
	require.Empty(t, repo.ofKind("ranked"), "an interrupted match is not a full ranked result")
	require.Empty(t, repo.ofKind("casual"))
	require.Empty(t, repo.calls())
}

func playHeartsUntil(t *testing.T, engine *game.Engine, ready func(*hearts.State) bool) {
	t.Helper()
	for step := range heartsClockSteps {
		var hit bool
		engine.WithState(func(s *game.State) { hit = ready(heartsState(t, s)) })
		if hit {
			return
		}
		require.Falsef(t, engine.IsFinished(), "match ended at step %d before the phase under test", step)
		id := engine.CurrentPlayerID()
		require.NotEmpty(t, id, "step %d", step)
		var act game.Action
		engine.WithState(func(s *game.State) { act = s.Rules.TimeoutAction(s) })
		require.NotNilf(t, act, "step %d: TimeoutAction returned nil", step)
		require.NoErrorf(t, engine.SubmitAction(id, act), "step %d: %s", step, act.Name())
	}
	t.Fatal("hearts phase not reached")
}

func assertClockWin(t *testing.T, engine *game.Engine, players []*game.Player) {
	t.Helper()
	require.True(t, engine.IsFinished())
	require.Equal(t, game.EndReasonWin, engine.EndReason())

	standings := engine.Standings()
	require.Len(t, standings, 4)
	seen := make(map[string]struct{}, len(standings))
	var prevPlace, prevScore int
	engine.WithState(func(s *game.State) {
		extra := heartsState(t, s)
		require.True(t, extra.MatchComplete)
		require.Equal(t, hearts.PhaseHandOver, extra.Phase)
		require.GreaterOrEqual(t, extra.HandNumber, 4, "100 points takes at least four hands")
		sum, maxScore := 0, 0
		for _, sc := range extra.CumulativeScores {
			sum += sc
			maxScore = max(maxScore, sc)
		}
		require.GreaterOrEqual(t, maxScore, heartsTarget)
		require.Positive(t, sum)
		require.Zero(t, sum%heartsHandPoints)
		for i, st := range standings {
			require.NotNil(t, st.Player)
			require.False(t, st.Left)
			sc := extra.CumulativeScores[st.Player.ID]
			if i == 0 {
				require.Equal(t, 1, st.Place)
			} else {
				require.GreaterOrEqual(t, st.Place, prevPlace)
				require.GreaterOrEqual(t, sc, prevScore)
				if sc == prevScore {
					require.Equal(t, prevPlace, st.Place)
				}
			}
			prevPlace, prevScore = st.Place, sc
			seen[st.Player.ID] = struct{}{}
		}
	})
	for _, p := range players {
		_, ok := seen[p.ID]
		require.Truef(t, ok, "standings missing %s", p.ID)
	}
}

type heartsStep struct {
	phase   hearts.Phase
	pending int
	tricks  int
	cards   int
	hand    int
}

func heartsProgress(t *testing.T, engine *game.Engine) heartsStep {
	t.Helper()
	var step heartsStep
	engine.WithState(func(s *game.State) {
		extra := heartsState(t, s)
		step = heartsStep{
			phase:   extra.Phase,
			pending: len(extra.PendingPasses),
			tricks:  extra.TricksPlayed,
			cards:   len(extra.TrickCards),
			hand:    extra.HandNumber,
		}
	})
	return step
}

func heartsState(t *testing.T, s *game.State) *hearts.State {
	t.Helper()
	extra, ok := s.Extra.(*hearts.State)
	require.True(t, ok)
	return extra
}

func liveScores(extra *hearts.State) map[string]int {
	scores := make(map[string]int, len(extra.CumulativeScores))
	for id, total := range extra.CumulativeScores {
		scores[id] = total
		if !extra.HandComplete() {
			scores[id] += extra.HandPoints[id]
		}
	}
	return scores
}

func standingUserIDs(standings []game.Standing) []uuid.UUID {
	ids := make([]uuid.UUID, len(standings))
	for i, s := range standings {
		ids[i] = s.Player.UserID
	}
	return ids
}

func standingPlaces(standings []game.Standing) []int {
	places := make([]int, len(standings))
	for i, s := range standings {
		places[i] = s.Place
	}
	return places
}

func assertFourSeatTable(t *testing.T, l *lobby.Lobby) {
	t.Helper()
	snap := l.Snapshot()
	require.Equal(t, 4, snap.Limit)
	require.Equal(t, 4, snap.MinPlayers)
	require.Equal(t, 4, snap.MaxPlayers)
	require.Equal(t, heartsSlug, snap.Slug)
}

func subscribeLobby(t *testing.T, l *lobby.Lobby, playerID string) <-chan lobby.Event {
	t.Helper()
	events, err := l.Subscribe(playerID)
	require.NoError(t, err)
	t.Cleanup(func() { l.Unsubscribe(playerID, events) })
	return events
}

func spamWrongSeats(t *testing.T, engine *game.Engine, players []*game.Player, current string) {
	t.Helper()
	acts := []game.Action{
		poker.ActionFold{},
		hearts.ActionPlayCard{},
		hearts.ActionNextHand{},
		gametest.ForeignAction{},
	}
	const n = 32
	errCh := make(chan error, n*len(players)*len(acts))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range n {
		for _, p := range players {
			if p.ID == current {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				defer func() {
					if recovered := recover(); recovered != nil {
						errCh <- fmt.Errorf("panic from %s: %v", p.ID, recovered)
					}
				}()
				for _, act := range acts {
					err := engine.SubmitAction(p.ID, act)
					if err == nil || !strings.Contains(err.Error(), "wait for your turn") {
						errCh <- fmt.Errorf("wrong seat %s %s: %v", p.ID, act.Name(), err)
					}
				}
			}()
		}
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
}
