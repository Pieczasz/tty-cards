package systemtest

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/poker"
	unorules "github.com/Pieczasz/tty-cards/internal/game/uno"
	"github.com/Pieczasz/tty-cards/internal/lobby"

	"github.com/stretchr/testify/require"
)

const (
	unoSlug = "uno"
	unoGame = "Uno"
)

// unoManager is a catalog-backed lobby manager. A nil repo is an untyped nil: a nil
// *rankedFinalizeRecorder would box into a non-nil interface and panic on casual writes.
func unoManager(t *testing.T, repo db.MatchRepository) *lobby.Manager {
	t.Helper()
	m := lobby.NewManager(t.Context(), repo, catalog.NewRegistry())
	t.Cleanup(func() {
		if !m.WaitForFinalizers(20 * time.Second) {
			t.Errorf("uno watchers did not drain")
		}
	})
	return m
}

func unoLimits(t *testing.T) (minPlayers, maxPlayers int) {
	t.Helper()
	mod, ok := catalog.NewRegistry().Module(unoSlug)
	require.True(t, ok, "catalog must register uno")
	require.GreaterOrEqual(t, mod.MinPlayers, 2)
	require.GreaterOrEqual(t, mod.MaxPlayers, mod.MinPlayers)
	return mod.MinPlayers, min(mod.MaxPlayers, 6)
}

func TestUno_CasualTablesFinish(t *testing.T) {
	t.Parallel()
	minPlayers, maxPlayers := unoLimits(t)
	for n := minPlayers; n <= maxPlayers; n++ {
		t.Run(fmt.Sprintf("%d players", n), func(t *testing.T) {
			t.Parallel()
			m := unoManager(t, nil)
			_, engine, players := startFilledLobby(t, m, unoSlug, n, false)
			cards := unoCards(engine)
			require.Positive(t, cards)
			playOutByClock(t, engine)
			assertUnoMatch(t, engine, players, cards)
		})
	}
}

func TestUno_RankedLeaveFinalizesLeaver(t *testing.T) {
	t.Parallel()
	repo := newRankedFinalizeRecorder()
	m := unoManager(t, repo)
	_, maxPlayers := unoLimits(t)
	n := min(4, maxPlayers)

	l, engine, players := startFilledLobby(t, m, unoSlug, n, true)
	cards := unoCards(engine)
	clockUno(t, engine, 5)
	require.False(t, engine.IsFinished(), "five draws must not end a fresh uno hand")

	leaver := players[1]
	m.LeaveLobby(leaver)
	require.False(t, l.HasPlayer(leaver))
	require.Equal(t, cards, unoCards(engine), "a leave returns the hand to the stock")

	playOutByClock(t, engine)
	assertUnoMatch(t, engine, players, cards)
	requireLeft(t, engine, leaver.ID)

	repo.awaitFinalize(t)
	finalized := repo.calls()
	require.Len(t, finalized, 1)
	require.Equal(t, unoGame, finalized[0].gameName)
	require.Len(t, finalized[0].userIDs, len(players))
	require.Contains(t, finalized[0].userIDs, leaver.UserID)
	for _, p := range players {
		require.Contains(t, finalized[0].userIDs, p.UserID)
	}
}

func TestUno_IllegalActionsThenClock(t *testing.T) {
	t.Parallel()
	m := unoManager(t, nil)
	_, engine, players := startFilledLobby(t, m, unoSlug, 3, false)
	cards := unoCards(engine)

	current := engine.CurrentPlayerID()
	require.NotEmpty(t, current)
	before := unoSnap(engine)

	require.ErrorIs(t, engine.SubmitAction(current, poker.ActionFold{}), game.ErrUnknownAction)
	require.ErrorContains(t, engine.SubmitAction(current, unorules.ActionPlayCard{
		Card: deck.Card{Rank: deck.Ace, Suit: deck.Spades},
	}), "you don't have that card")

	var wild deck.Card
	var held bool
	engine.WithState(func(s *game.State) {
		for _, c := range s.Players[s.CurrentTurn].Cards {
			if unorules.IsWild(c) {
				wild, held = c, true
				return
			}
		}
	})
	if held {
		require.ErrorContains(t, engine.SubmitAction(current, unorules.ActionPlayCard{
			Card: wild, ChosenSuit: deck.NoSuit,
		}), "must choose a valid color")
	}

	other := players[0]
	if other.ID == current {
		other = players[1]
	}
	require.ErrorContains(t, engine.SubmitAction(other.ID, unorules.ActionDrawCard{}), "wait for your turn")
	require.Equal(t, before, unoSnap(engine))
	require.False(t, engine.IsFinished())

	playOutByClock(t, engine)
	assertUnoMatch(t, engine, players, cards)
}

func TestUno_LeaveDuringReverseOrSkip(t *testing.T) {
	t.Parallel()
	_, tableCap := unoLimits(t)
	require.GreaterOrEqual(t, tableCap, 4, "the leave needs a bystander and two seats left playing")

	t.Run("reverse", func(t *testing.T) {
		t.Parallel()
		// Direction, not the rank just played: heads-up reverse is a skip and stays clockwise.
		leaveAfter(t, tableCap, []deck.Rank{unorules.Reverse, unorules.Skip, unorules.DrawTwo},
			func(_ deck.Rank, dir int8, _ deck.Rank) bool { return dir < 0 })
	})
	t.Run("skip", func(t *testing.T) {
		t.Parallel()
		leaveAfter(t, tableCap, []deck.Rank{unorules.Skip, unorules.DrawTwo, unorules.WildDrawFour, unorules.Reverse},
			func(played deck.Rank, _ int8, opening deck.Rank) bool {
				return unoSkips(played) || unoSkips(opening)
			})
	})
}

func TestUno_ConcurrentWrongSeat(t *testing.T) {
	t.Parallel()
	m := unoManager(t, nil)
	_, engine, players := startFilledLobby(t, m, unoSlug, 4, false)
	cards := unoCards(engine)

	var wg sync.WaitGroup
	junk := unorules.ActionPlayCard{Card: deck.Card{Rank: deck.Ace, Suit: deck.Spades}}
	for _, p := range players {
		wg.Go(func() {
			for range 200 {
				if engine.IsFinished() {
					return
				}
				current := engine.CurrentPlayerID()
				if p.ID != current {
					_ = engine.SubmitAction(p.ID, unorules.ActionDrawCard{})
					_ = engine.SubmitAction(p.ID, junk)
				}
				_ = engine.SubmitAction(p.ID, poker.ActionFold{})
				_ = engine.SubmitAction("not-a-seat", unorules.ActionDrawCard{})
			}
		})
	}
	for range 30 {
		if engine.IsFinished() {
			break
		}
		id := engine.CurrentPlayerID()
		if id == "" {
			break
		}
		var act game.Action
		engine.WithState(func(s *game.State) { act = s.Rules.TimeoutAction(s) })
		_ = engine.SubmitAction(id, act)
	}
	wg.Wait()

	if !engine.IsFinished() {
		playOutByClock(t, engine)
	}
	assertUnoMatch(t, engine, players, cards)
}

func TestUno_RepeatedMatches(t *testing.T) {
	t.Parallel()
	m := unoManager(t, nil)
	minPlayers, maxPlayers := unoLimits(t)
	n := max(min(4, maxPlayers), minPlayers)

	l, engine, players := startFilledLobby(t, m, unoSlug, n, false)
	events, err := l.Subscribe(players[0].ID)
	require.NoError(t, err)
	t.Cleanup(func() { l.Unsubscribe(players[0].ID, events) })

	for match := range 5 {
		if match > 0 {
			engine = readyRematch(t, m, l, events, players, engine)
		}
		cards := unoCards(engine)
		require.Positive(t, cards)
		playOutByClock(t, engine)
		assertUnoMatch(t, engine, players, cards)
	}
}

func leaveAfter(
	t *testing.T,
	n int,
	prefer []deck.Rank,
	done func(played deck.Rank, dir int8, opening deck.Rank) bool,
) {
	t.Helper()
	m := unoManager(t, nil)
	l, engine, players := startFilledLobby(t, m, unoSlug, n, false)
	cards := unoCards(engine)
	driveUno(t, engine, cards, prefer, done)

	dir := unoDirection(t, engine)
	require.Contains(t, []int8{1, -1}, dir)
	current := engine.CurrentPlayerID()
	require.NotEmpty(t, current)

	// A seat that is not on turn must not move the cursor or the direction.
	if bystander := unoOther(players, current, l); bystander != nil {
		m.LeaveLobby(bystander)
		require.False(t, engine.IsFinished(), "one leave from %d seats must leave the hand in play", n)
		require.Equal(t, current, engine.CurrentPlayerID())
		require.Equal(t, dir, unoDirection(t, engine))
		require.Equal(t, cards, unoCards(engine))
		defer requireLeft(t, engine, bystander.ID)
	}

	ids := unoSeatIDs(engine)
	leaverID := engine.CurrentPlayerID()
	want := unoNeighbor(t, ids, leaverID, dir)
	m.LeaveLobby(playerByID(t, players, leaverID))
	require.False(t, engine.IsFinished())
	require.Equal(t, dir, unoDirection(t, engine), "a leave must not flip or zero the direction")
	require.Equal(t, want, engine.CurrentPlayerID(), "the turn follows the table direction")
	require.Equal(t, cards, unoCards(engine))

	playOutByClock(t, engine)
	assertUnoMatch(t, engine, players, cards)
	requireLeft(t, engine, leaverID)
}

func driveUno(
	t *testing.T,
	engine *game.Engine,
	cards int,
	prefer []deck.Rank,
	done func(played deck.Rank, dir int8, opening deck.Rank) bool,
) {
	t.Helper()
	opening := unoTop(t, engine)
	dir := unoDirection(t, engine)
	require.Contains(t, []int8{1, -1}, dir)
	if done(0, dir, opening) {
		return
	}
	var played deck.Rank
	for step := range 800 {
		require.Falsef(t, engine.IsFinished(), "uno ended at step %d before the scripted action", step)
		played = playUnoBiased(t, engine, prefer)
		dir = unoDirection(t, engine)
		require.Containsf(t, []int8{1, -1}, dir, "step %d left direction %d", step, dir)
		require.Equal(t, cards, unoCards(engine), "step %d", step)
		if done(played, dir, opening) {
			return
		}
	}
	t.Fatalf("uno never reached the scripted action; last rank %d direction %d opening %d", played, dir, opening)
}

// playUnoBiased plays the first legal card in prefer, else the clock's draw. Rank 0
// means a draw: it is not an Uno rank, so callers can tell a lay-down from a pass.
func playUnoBiased(t *testing.T, engine *game.Engine, prefer []deck.Rank) deck.Rank {
	t.Helper()
	id := engine.CurrentPlayerID()
	require.NotEmpty(t, id)
	var act unorules.ActionPlayCard
	var found bool
	engine.WithState(func(s *game.State) {
		hand := s.Players[s.CurrentTurn].Cards
		for _, rank := range prefer {
			for _, c := range hand {
				if c.Rank != rank {
					continue
				}
				candidate := unorules.ActionPlayCard{Card: c, ChosenSuit: unorules.ColorRed}
				if s.Rules.ValidateAction(s, candidate) == nil {
					act, found = candidate, true
					return
				}
			}
		}
	})
	if found {
		require.NoError(t, engine.SubmitAction(id, act))
		return act.Card.Rank
	}
	var timeout game.Action
	engine.WithState(func(s *game.State) { timeout = s.Rules.TimeoutAction(s) })
	require.NotNil(t, timeout)
	require.NoError(t, engine.SubmitAction(id, timeout))
	return 0
}

func clockUno(t *testing.T, engine *game.Engine, steps int) {
	t.Helper()
	for range steps {
		if engine.IsFinished() {
			return
		}
		id := engine.CurrentPlayerID()
		require.NotEmpty(t, id)
		var act game.Action
		engine.WithState(func(s *game.State) { act = s.Rules.TimeoutAction(s) })
		require.NotNil(t, act)
		require.NoError(t, engine.SubmitAction(id, act))
	}
}

func readyRematch(
	t *testing.T,
	m *lobby.Manager,
	l *lobby.Lobby,
	events <-chan lobby.Event,
	players []*game.Player,
	prev *game.Engine,
) *game.Engine {
	t.Helper()
	require.Eventually(t, func() bool { return l.ActiveGame() == nil }, 10*time.Second, 5*time.Millisecond,
		"the finished uno table must reopen before the next hand")
	for _, p := range players {
		require.NoError(t, m.ToggleReady(p))
	}
	engine := awaitGameStart(t, l, events)
	require.NotSame(t, prev, engine)
	require.False(t, engine.IsFinished())
	return engine
}

func assertUnoMatch(t *testing.T, engine *game.Engine, players []*game.Player, cards int) {
	t.Helper()
	require.True(t, engine.IsFinished())
	require.Equal(t, game.EndReasonWin, engine.EndReason())
	require.Contains(t, []int8{1, -1}, unoDirection(t, engine))
	require.Equal(t, cards, unoCards(engine))

	standings := engine.Standings()
	require.Len(t, standings, len(players))
	require.Equal(t, 1, standings[0].Place)
	got := make([]string, len(standings))
	for i, s := range standings {
		require.NotNil(t, s.Player)
		got[i] = s.Player.ID
		if i > 0 {
			require.GreaterOrEqual(t, s.Place, standings[i-1].Place)
		}
	}
	for _, p := range players {
		require.Contains(t, got, p.ID)
	}
}

func requireLeft(t *testing.T, engine *game.Engine, id string) {
	t.Helper()
	for _, s := range engine.Standings() {
		if s.Player != nil && s.Player.ID == id {
			require.True(t, s.Left)
			return
		}
	}
	t.Fatalf("leaver %s missing from standings", id)
}

type unoView struct {
	turn          int
	dir           int8
	color         deck.Suit
	deck, discard int
	top           deck.Card
	hands         []int
	current       string
}

func unoSnap(engine *game.Engine) unoView {
	var v unoView
	engine.WithState(func(s *game.State) {
		extra := s.Extra.(*unorules.State)
		v.turn = s.CurrentTurn
		v.dir = extra.Direction
		v.color = extra.CurrentColor
		v.current = ""
		if s.CurrentTurn >= 0 && s.CurrentTurn < len(s.Players) {
			v.current = s.Players[s.CurrentTurn].ID
		}
		if s.Deck != nil {
			v.deck = s.Deck.Size()
		}
		if s.Discard != nil {
			v.discard = s.Discard.Size()
			if top, ok := s.Discard.Peek(); ok {
				v.top = top
			}
		}
		v.hands = make([]int, len(s.Players))
		for i, p := range s.Players {
			v.hands[i] = len(p.Cards)
		}
	})
	return v
}

func unoDirection(t *testing.T, engine *game.Engine) int8 {
	t.Helper()
	var dir int8
	engine.WithState(func(s *game.State) {
		dir = s.Extra.(*unorules.State).Direction
	})
	return dir
}

func unoTop(t *testing.T, engine *game.Engine) deck.Rank {
	t.Helper()
	var rank deck.Rank
	engine.WithState(func(s *game.State) {
		if s.Discard == nil {
			return
		}
		if top, ok := s.Discard.Peek(); ok {
			rank = top.Rank
		}
	})
	return rank
}

func unoCards(engine *game.Engine) int {
	var n int
	engine.WithState(func(s *game.State) {
		if s.Deck != nil {
			n += s.Deck.Size()
		}
		if s.Discard != nil {
			n += s.Discard.Size()
		}
		for _, p := range s.Players {
			n += len(p.Cards)
		}
		for _, p := range s.LeftPlayers {
			n += len(p.Cards)
		}
	})
	return n
}

func unoSeatIDs(engine *game.Engine) []string {
	var ids []string
	engine.WithState(func(s *game.State) {
		ids = make([]string, len(s.Players))
		for i, p := range s.Players {
			ids[i] = p.ID
		}
	})
	return ids
}

// unoNeighbor is the seat the turn moves to when leaver, who is on turn, walks out.
// Clockwise is the next index; counterclockwise is the previous one, wrapping.
func unoNeighbor(t *testing.T, ids []string, leaver string, dir int8) string {
	t.Helper()
	idx := slices.Index(ids, leaver)
	require.GreaterOrEqual(t, idx, 0)
	step := 1
	if dir < 0 {
		step = -1
	}
	return ids[game.SeatAt(idx+step, len(ids))]
}

func unoOther(players []*game.Player, current string, l *lobby.Lobby) *game.Player {
	for _, p := range players {
		if p.ID != current && l.HasPlayer(p) {
			return p
		}
	}
	return nil
}

func unoSkips(rank deck.Rank) bool {
	return rank == unorules.Skip || rank == unorules.DrawTwo || rank == unorules.WildDrawFour
}
