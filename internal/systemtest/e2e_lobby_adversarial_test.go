package systemtest

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/broadcaster"
	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/game/poker"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/require"
)

// junkAction is a move no catalog game defines, the sort of payload a hostile
// client can submit once it has a seat id.
type junkAction struct{}

func (junkAction) Name() string { return "junk" }

func TestE2E_LobbyJoinRefusals(t *testing.T) {
	t.Parallel()

	t.Run("bad_codes", func(t *testing.T) {
		t.Parallel()
		manager := newAdversarialManager(t)
		host := nPlayers(1, 1)[0]
		l := mustCreate(t, manager, host, "poker", 4, false)
		guest := nPlayers(2, 1)[0]

		for _, code := range []string{
			"",
			"abc",
			"ABCDEFGHI",
			"abcd1234",
			"ABCD123!",
			"ABCD 234",
			"../ABCD1",
			"';DROP T",
			"ÅÅÅÅÅÅÅÅ",
		} {
			err := joinErr(manager.JoinLobbyByCode(code, guest))
			require.ErrorIs(t, err, lobby.ErrRefused, "code %q", code)
			require.ErrorContains(t, err, "invalid lobby code", "code %q", code)
			_, findErr := manager.FindLobbyByCode(code)
			require.ErrorIs(t, findErr, lobby.ErrRefused, "code %q", code)
		}

		err := joinErr(manager.JoinLobbyByCode("ZZZZZZZZ", guest))
		require.ErrorContains(t, err, "not found")
		require.Nil(t, manager.FindLobbyByPlayer(guest))
		require.Equal(t, 1, l.Snapshot().Players())

		// The real code is case-sensitive: a lowercased copy is not a join.
		lower := strings.ToLower(l.Code())
		require.ErrorContains(t, joinErr(manager.JoinLobbyByCode(lower, guest)), "invalid lobby code")
		require.NoError(t, joinErr(manager.JoinLobbyByCode(l.Code(), guest)))
		require.True(t, l.HasPlayer(guest))
	})

	t.Run("full_and_double_join", func(t *testing.T) {
		t.Parallel()
		manager := newAdversarialManager(t)
		ps := nPlayers(1, 4)
		l := mustCreate(t, manager, ps[0], "poker", 2, false)
		require.NoError(t, joinErr(manager.JoinLobbyByCode(l.Code(), ps[1])))

		err := joinErr(manager.JoinLobbyByCode(l.Code(), ps[2]))
		require.ErrorIs(t, err, lobby.ErrRefused)
		require.ErrorContains(t, err, "full")
		require.Nil(t, manager.FindLobbyByPlayer(ps[2]))
		require.Equal(t, 2, l.Snapshot().Players())

		err = joinErr(manager.JoinLobbyByCode(l.Code(), ps[1]))
		require.ErrorIs(t, err, lobby.ErrRefused)
		require.ErrorContains(t, err, "already in a lobby")
		require.Equal(t, 2, l.Snapshot().Players())
		require.Len(t, l.Snapshot().Guests, 1)
	})

	t.Run("private_without_code", func(t *testing.T) {
		t.Parallel()
		manager := newAdversarialManager(t)
		host := nPlayers(1, 1)[0]
		guest := nPlayers(2, 1)[0]
		// Private is the default: browse is not a way in.
		l := mustCreate(t, manager, host, "poker", 4, true)
		require.True(t, l.Snapshot().Private)
		require.NotContains(t, browseCodes(manager, guest), l.Code())
		require.Empty(t, codesFor(manager, lobby.BrowseFilter{Slug: "poker"}))

		require.ErrorContains(t, joinErr(manager.JoinLobbyByCode("", guest)), "invalid lobby code")
		require.Nil(t, manager.FindLobbyByPlayer(guest))

		require.NoError(t, joinErr(manager.JoinLobbyByCode(l.Code(), guest)))
		require.True(t, l.HasPlayer(guest))
		require.NotContains(t, browseCodes(manager, guest), l.Code(), "the code path must not publish the table")
	})

	t.Run("non_leader_settings", func(t *testing.T) {
		t.Parallel()
		manager := newAdversarialManager(t)
		ps := nPlayers(1, 3)
		l := mustCreate(t, manager, ps[0], "poker", 3, true)
		require.NoError(t, joinErr(manager.JoinLobbyByCode(l.Code(), ps[1])))

		require.NoError(t, l.SetMaxPlayers(ps[0], 4))
		require.NoError(t, l.SetPrivate(ps[0], false))
		require.NoError(t, l.SetRanked(ps[0], true))

		for _, actor := range []*game.Player{ps[1], ps[2]} {
			require.ErrorIs(t, l.SetMaxPlayers(actor, 2), lobby.ErrRefused)
			require.ErrorIs(t, l.SetPrivate(actor, true), lobby.ErrRefused)
			require.ErrorIs(t, l.SetRanked(actor, false), lobby.ErrRefused)
		}
		require.ErrorContains(t, l.SetPrivate(ps[1], true), "only the leader")

		snap := l.Snapshot()
		require.Equal(t, 4, snap.Limit)
		require.False(t, snap.Private)
		require.True(t, snap.Ranked)
		require.Error(t, l.SetMaxPlayers(ps[0], 1))
		require.Error(t, l.SetMaxPlayers(ps[0], 100))
		require.Equal(t, 4, l.Snapshot().Limit)

		require.ErrorContains(t, manager.Kick(ps[0], ps[0]), "yourself")
		require.ErrorContains(t, manager.Kick(ps[1], ps[0]), "only the leader")
		require.Error(t, manager.Kick(ps[0], ps[2]))
		require.NoError(t, manager.Kick(ps[0], ps[1]))
		require.Nil(t, manager.FindLobbyByPlayer(ps[1]))
		require.False(t, l.HasPlayer(ps[1]))
		require.ErrorContains(t, manager.ToggleReady(ps[1]), "not in lobby")

		next, err := manager.CreateLobby(ps[1], lobby.WithCardGame("uno"), lobby.WithMaxPlayers(2))
		require.NoError(t, err)
		require.Same(t, next, manager.FindLobbyByPlayer(ps[1]))
	})

	t.Run("join_budget", func(t *testing.T) {
		t.Parallel()
		manager := newAdversarialManager(t)
		prober := nPlayers(1, 1)[0]
		var notFound, limited int
		for range 15 {
			err := joinErr(manager.JoinLobbyByCode("ZZZZZZZZ", prober))
			require.ErrorIs(t, err, lobby.ErrRefused)
			if strings.Contains(err.Error(), "too many") {
				limited++
				continue
			}
			require.ErrorContains(t, err, "not found")
			notFound++
		}
		require.Equal(t, 10, notFound, "ten well-formed misses are the whole join budget")
		require.Equal(t, 5, limited)

		// A typo never spends that budget: it is rejected before the limiter.
		require.ErrorContains(t, joinErr(manager.JoinLobbyByCode("nope", prober)), "invalid lobby code")

		host := nPlayers(2, 1)[0]
		l := mustCreate(t, manager, host, "poker", 2, false)
		require.ErrorContains(t, joinErr(manager.JoinLobbyByCode(l.Code(), prober)), "too many")
		require.False(t, l.HasPlayer(prober))
		require.Nil(t, manager.FindLobbyByPlayer(prober))
	})

	t.Run("create_bounds", func(t *testing.T) {
		t.Parallel()
		manager := newAdversarialManager(t)
		p := nPlayers(1, 1)[0]
		for _, opts := range [][]lobby.Option{
			nil,
			{lobby.WithCardGame("not-a-game")},
			{lobby.WithCardGame("poker"), lobby.WithMaxPlayers(1)},
			{lobby.WithCardGame("hearts"), lobby.WithMaxPlayers(2)},
			{lobby.WithCardGame("poker"), lobby.WithMaxPlayers(100)},
		} {
			_, err := manager.CreateLobby(p, opts...)
			require.ErrorIs(t, err, lobby.ErrRefused)
			require.Nil(t, manager.FindLobbyByPlayer(p), "a refused create must not keep the seat")
		}
		playing, open := manager.Stats()
		require.Zero(t, playing)
		require.Zero(t, open)
	})
}

func TestE2E_JoinRaces(t *testing.T) {
	t.Parallel()

	t.Run("one_seat_under_stampede", func(t *testing.T) {
		t.Parallel()
		manager := newAdversarialManager(t)
		host := nPlayers(1, 1)[0]
		l := mustCreate(t, manager, host, "poker", 4, false)
		guest := nPlayers(2, 1)[0]

		const stampede = 16
		var wg sync.WaitGroup
		okN := make(chan int, stampede)
		start := make(chan struct{})
		for range stampede {
			wg.Go(func() {
				<-start
				if joinErr(manager.JoinLobbyByCode(l.Code(), guest)) == nil {
					okN <- 1
				}
			})
		}
		close(start)
		waitOrFail(t, &wg, 10*time.Second)
		close(okN)
		n := 0
		for range okN {
			n++
		}
		require.Equal(t, 1, n, "one player gets one seat")
		require.Equal(t, 2, l.Snapshot().Players())
		require.Len(t, l.Snapshot().Guests, 1)
		require.Same(t, l, manager.FindLobbyByPlayer(guest))
		assertSettled(t, manager, []string{l.Code()}, []*game.Player{host, guest})
	})

	t.Run("two_tables_one_player", func(t *testing.T) {
		t.Parallel()
		manager := newAdversarialManager(t)
		hosts := nPlayers(1, 2)
		a := mustCreate(t, manager, hosts[0], "poker", 4, false)
		b := mustCreate(t, manager, hosts[1], "uno", 4, false)

		for i := range 32 {
			p := nPlayers(10+i, 1)[0]
			var wg sync.WaitGroup
			var mu sync.Mutex
			var got []*lobby.Lobby
			wg.Add(2)
			for _, code := range []string{a.Code(), b.Code()} {
				go func(code string) {
					defer wg.Done()
					joined, err := manager.JoinLobbyByCode(code, p)
					if err != nil {
						return
					}
					mu.Lock()
					got = append(got, joined)
					mu.Unlock()
				}(code)
			}
			waitOrFail(t, &wg, 5*time.Second)
			require.Lenf(t, got, 1, "iter %d: a player sits at one table", i)
			require.Same(t, got[0], manager.FindLobbyByPlayer(p))
			other := a
			if got[0] == a {
				other = b
			}
			require.False(t, other.HasPlayer(p))
			manager.LeaveLobby(p)
			require.Nil(t, manager.FindLobbyByPlayer(p))
			require.False(t, a.HasPlayer(p))
			require.False(t, b.HasPlayer(p))
		}
		assertSettled(t, manager, []string{a.Code(), b.Code()}, hosts)
	})
}

func TestE2E_SettingsFlipWhileJoining(t *testing.T) {
	t.Parallel()
	manager := newAdversarialManager(t)
	roster := nPlayers(1, 9)
	leader := roster[0]
	l := mustCreate(t, manager, leader, "poker", 6, false)
	code := l.Code()

	var wg sync.WaitGroup
	start := make(chan struct{})
	for _, g := range roster[1:] {
		wg.Add(1)
		go func(p *game.Player) {
			defer wg.Done()
			<-start
			_ = joinErr(manager.JoinLobbyByCode(code, p))
			_ = joinErr(manager.JoinLobbyByCode(code, p))
			_ = l.SetPrivate(p, true)
			_ = l.SetRanked(p, true)
			_ = l.SetMaxPlayers(p, 9)
		}(g)
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for _, limit := range []int{2, 4, 6, 9, 3} {
			_ = l.SetMaxPlayers(leader, limit)
			_ = l.SetPrivate(leader, true)
			_ = l.SetRanked(leader, true)
			_ = l.SetPrivate(leader, false)
			_ = l.SetRanked(leader, false)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range 40 {
			_ = manager.BrowseLobbies(leader, lobby.BrowseFilter{Slug: "poker", Mode: lobby.BrowseCasual})
			_ = manager.BrowseLobbies(leader, lobby.BrowseFilter{Mode: lobby.BrowseRanked, OnlyWithRoom: true})
			_, _ = manager.Stats()
		}
	}()
	close(start)
	waitOrFail(t, &wg, 10*time.Second)

	snap := l.Snapshot()
	require.False(t, snap.InGame)
	require.False(t, snap.Private)
	require.False(t, snap.Ranked)
	require.True(t, snap.IsLeader(leader))
	require.LessOrEqual(t, snap.Players(), snap.Limit)
	require.GreaterOrEqual(t, snap.Players(), 1)
	require.Contains(t, browseCodes(manager, nil), code)
	assertSettled(t, manager, []string{code}, roster)

	if snap.Players() > 1 {
		limit := snap.Limit
		require.Error(t, l.SetMaxPlayers(leader, snap.Players()-1))
		require.Equal(t, limit, l.Snapshot().Limit)
	}
}

func TestE2E_LeaderLeavePromotesAndCodeDies(t *testing.T) {
	t.Parallel()
	manager := newAdversarialManager(t)

	// The guest who is already seated keeps the table alive, so a join racing
	// the leader's leave still lands on that table with the guest in charge.
	for i := range 20 {
		ps := nPlayers(100+i*3, 3)
		l := mustCreate(t, manager, ps[0], "poker", 4, false)
		require.NoError(t, joinErr(manager.JoinLobbyByCode(l.Code(), ps[1])))
		var wg sync.WaitGroup
		var joined error
		wg.Add(2)
		go func() {
			defer wg.Done()
			manager.LeaveLobby(ps[0])
		}()
		go func() {
			defer wg.Done()
			joined = joinErr(manager.JoinLobbyByCode(l.Code(), ps[2]))
		}()
		waitOrFail(t, &wg, 5*time.Second)
		require.NoError(t, joined, "iter %d", i)

		require.Nil(t, manager.FindLobbyByPlayer(ps[0]))
		require.True(t, l.Snapshot().IsLeader(ps[1]), "iter %d", i)
		require.False(t, l.Snapshot().IsLeader(ps[2]))
		require.Same(t, l, manager.FindLobbyByPlayer(ps[1]))
		require.Same(t, l, manager.FindLobbyByPlayer(ps[2]))
		require.Equal(t, 2, l.Snapshot().Players())
		manager.LeaveLobby(ps[1])
		manager.LeaveLobby(ps[2])
		_, err := manager.FindLobbyByCode(l.Code())
		require.ErrorContains(t, err, "not found")
	}

	ps := nPlayers(1, 3)
	l := mustCreate(t, manager, ps[0], "poker", 4, false)
	code := l.Code()
	require.NoError(t, joinErr(manager.JoinLobbyByCode(code, ps[1])))
	require.NoError(t, joinErr(manager.JoinLobbyByCode(code, ps[2])))
	require.NoError(t, l.SetRanked(ps[0], true))

	manager.LeaveLobby(ps[0])
	require.True(t, l.Snapshot().IsLeader(ps[1]))
	require.False(t, l.Snapshot().IsLeader(ps[0]))
	require.False(t, l.Snapshot().IsLeader(ps[2]))
	require.Nil(t, manager.FindLobbyByPlayer(ps[0]))
	require.True(t, l.Snapshot().Ranked, "promotion keeps the table's settings")
	require.NoError(t, l.SetPrivate(ps[1], false))
	require.ErrorIs(t, l.SetPrivate(ps[2], true), lobby.ErrRefused)
	require.ErrorIs(t, l.SetRanked(ps[0], false), lobby.ErrRefused)
	require.Contains(t, browseCodes(manager, nil), code)

	manager.LeaveLobby(ps[1])
	require.True(t, l.Snapshot().IsLeader(ps[2]))
	manager.LeaveLobby(ps[2])
	manager.LeaveLobby(ps[2])

	_, err := manager.FindLobbyByCode(code)
	require.ErrorContains(t, err, "not found")
	require.NotContains(t, browseCodes(manager, nil), code)
	playing, open := manager.Stats()
	require.Zero(t, playing)
	require.Zero(t, open)
	for _, p := range ps {
		require.Nil(t, manager.FindLobbyByPlayer(p))
		require.Error(t, joinErr(manager.JoinLobbyByCode(code, p)))
	}

	fresh := mustCreate(t, manager, ps[0], "uno", 2, false)
	require.NotEqual(t, code, fresh.Code())
	_, err = manager.FindLobbyByCode(code)
	require.ErrorContains(t, err, "not found")
	require.Equal(t, 1, fresh.Snapshot().Players())
}

func TestE2E_BrowseFiltersWhileTablesStart(t *testing.T) {
	t.Parallel()
	manager := newAdversarialManager(t)
	ps := nPlayers(1, 6)

	casual := mustCreate(t, manager, ps[0], "poker", 4, false)
	ranked := mustCreate(t, manager, ps[1], "poker", 2, false)
	require.NoError(t, ranked.SetRanked(ps[1], true))
	require.NoError(t, joinErr(manager.JoinLobbyByCode(ranked.Code(), ps[2])))
	uno := mustCreate(t, manager, ps[3], "uno", 4, false)
	require.NoError(t, joinErr(manager.JoinLobbyByCode(uno.Code(), ps[4])))
	hearts := mustCreate(t, manager, ps[5], "hearts", 4, true)
	t.Cleanup(func() {
		releaseTables(t, manager, casual.Code(), ranked.Code(), uno.Code(), hearts.Code())
	})

	requireBrowse(t, manager, lobby.BrowseFilter{Slug: "poker"}, casual.Code(), ranked.Code())
	requireBrowse(t, manager, lobby.BrowseFilter{Slug: "uno"}, uno.Code())
	requireBrowse(t, manager, lobby.BrowseFilter{Slug: "hearts"})
	requireBrowse(t, manager, lobby.BrowseFilter{Slug: "gin_rummy"})
	requireBrowse(t, manager, lobby.BrowseFilter{Mode: lobby.BrowseRanked}, ranked.Code())
	requireBrowse(t, manager, lobby.BrowseFilter{Mode: lobby.BrowseCasual}, casual.Code(), uno.Code())
	requireBrowse(t, manager, lobby.BrowseFilter{OnlyWithRoom: true}, casual.Code(), uno.Code())
	requireBrowse(t, manager, lobby.BrowseFilter{Mode: lobby.BrowseRanked, OnlyWithRoom: true})
	requireBrowse(t, manager, lobby.BrowseFilter{Slug: "poker", Mode: lobby.BrowseCasual}, casual.Code())

	rankedEntries := manager.BrowseLobbies(nil, lobby.BrowseFilter{Mode: lobby.BrowseRanked})
	require.Len(t, rankedEntries, 1)
	require.Equal(t, rankedEntries[0].Players, rankedEntries[0].MaxPlayers)
	require.True(t, rankedEntries[0].Ranked)
	require.Equal(t, "poker", rankedEntries[0].Slug)

	for _, e := range manager.BrowseLobbies(nil, lobby.BrowseFilter{OnlyWithRoom: true, Mode: lobby.BrowseCasual}) {
		require.Less(t, e.Players, e.MaxPlayers)
		require.False(t, e.Ranked)
	}
	require.NotContains(t, browseCodes(manager, nil), hearts.Code())

	events, err := uno.Subscribe(ps[3].ID)
	require.NoError(t, err)
	t.Cleanup(func() { uno.Unsubscribe(ps[3].ID, events) })
	require.NoError(t, manager.ToggleReady(ps[3]))

	var browseWG sync.WaitGroup
	browseWG.Add(1)
	defer browseWG.Wait()
	go func() {
		defer browseWG.Done()
		for range 100 {
			_ = manager.BrowseLobbies(ps[3], lobby.BrowseFilter{})
			_ = manager.GamesOnOffer()
		}
	}()

	require.NoError(t, manager.ToggleReady(ps[4]))
	_ = awaitGameStart(t, uno, events)
	browseWG.Wait()

	for range 20 {
		require.NotContains(t, browseCodes(manager, nil), uno.Code())
		require.NotContains(t, codesFor(manager, lobby.BrowseFilter{Slug: "uno"}), uno.Code())
	}
	requireBrowse(t, manager, lobby.BrowseFilter{Slug: "poker"}, casual.Code(), ranked.Code())
	requireBrowse(t, manager, lobby.BrowseFilter{OnlyWithRoom: true, Mode: lobby.BrowseCasual}, casual.Code())

	offered := map[string]bool{}
	for _, mod := range manager.GamesOnOffer() {
		offered[mod.Slug] = true
	}
	require.True(t, offered["poker"])
	require.False(t, offered["uno"], "a slug whose only table has started is not on offer")
	require.False(t, offered["hearts"])

	late := nPlayers(20, 1)[0]
	require.ErrorContains(t, joinErr(manager.JoinLobbyByCode(uno.Code(), late)), "not accepting")
	require.Nil(t, manager.FindLobbyByPlayer(late))
	require.Equal(t, 2, uno.Snapshot().Players())
	require.True(t, uno.Snapshot().InGame)

	require.NoError(t, casual.SetPrivate(ps[0], true))
	require.NotContains(t, browseCodes(manager, nil), casual.Code())
	require.NoError(t, casual.SetPrivate(ps[0], false))
	require.Contains(t, browseCodes(manager, nil), casual.Code())
}

func TestE2E_ConcurrentLobbyTraffic(t *testing.T) {
	t.Parallel()
	manager := newAdversarialManager(t)
	hosts := nPlayers(1, 3)
	floaters := nPlayers(10, 12)
	everyone := make([]*game.Player, 0, len(hosts)+len(floaters))
	everyone = append(everyone, hosts...)
	everyone = append(everyone, floaters...)

	codes := make([]string, len(hosts))
	for i, h := range hosts {
		codes[i] = mustCreate(t, manager, h, "poker", 4, false).Code()
	}
	t.Cleanup(func() { releaseTables(t, manager, codes...) })

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(seed+1), 0xC0FFEE))
			for range 40 {
				p := everyone[rng.IntN(len(everyone))]
				code := codes[rng.IntN(len(codes))]
				switch rng.IntN(5) {
				case 0:
					_, _ = manager.JoinLobbyByCode(code, p)
				case 1:
					manager.LeaveLobby(p)
				case 2:
					_ = manager.ToggleReady(p)
				case 3:
					manager.DisconnectPlayer(p)
					manager.ResumePlayer(p)
				default:
					_ = manager.BrowseLobbies(p, lobby.BrowseFilter{Slug: "poker", OnlyWithRoom: true})
					_, _ = manager.Stats()
					_ = manager.GamesOnOffer()
				}
			}
		}(g)
	}
	waitOrFail(t, &wg, 20*time.Second)
	assertSettled(t, manager, codes, everyone)
}

func TestE2E_SubscribeExhaustion(t *testing.T) {
	t.Parallel()

	t.Run("lobby_feed", func(t *testing.T) {
		t.Parallel()
		manager := newAdversarialManager(t)
		host := nPlayers(1, 1)[0]
		l := mustCreate(t, manager, host, "poker", 4, true)
		t.Cleanup(func() { releaseTables(t, manager, l.Code()) })

		// Two feeds for one seat is a reconnect overlapping the old session.
		kept, err := l.Subscribe(host.ID)
		require.NoError(t, err)
		overlap, err := l.Subscribe(host.ID)
		require.NoError(t, err)

		n := 2
		for {
			_, subErr := l.Subscribe(host.ID)
			if subErr != nil {
				require.ErrorIs(t, subErr, broadcaster.ErrAtCapacity)
				break
			}
			n++
			require.Less(t, n, 64, "lobby feed never reported capacity")
		}
		require.GreaterOrEqual(t, n, 2)

		l.Unsubscribe(host.ID, overlap)
		_, err = l.Subscribe(host.ID)
		require.NoError(t, err, "freeing the overlap must admit another feed")

		require.NoError(t, l.SetRanked(host, true))
		deadline := time.After(2 * time.Second)
		for {
			select {
			case ev, ok := <-kept:
				require.True(t, ok, "the seat that subscribed first must stay connected")
				if ev.Type == lobby.EventSettingsUpdated {
					return
				}
			case <-deadline:
				t.Fatal("a feed opened before the cap was hit received no settings event")
			}
		}
	})

	t.Run("engine_feed", func(t *testing.T) {
		t.Parallel()
		manager := newAdversarialManager(t)
		l, engine, _ := startFilledLobby(t, manager, "poker", 2, false)
		t.Cleanup(func() { releaseTables(t, manager, l.Code()) })

		n := 0
		var one <-chan game.Event
		for {
			ch, subErr := engine.Subscribe()
			if subErr != nil {
				require.ErrorIs(t, subErr, broadcaster.ErrAtCapacity)
				break
			}
			if one == nil {
				one = ch
			}
			n++
			require.LessOrEqual(t, n, 2*2+8, "engine feed grew past 2n+8")
		}
		// The lobby watcher already holds one slot of the 2n+8 cap.
		require.GreaterOrEqual(t, n, 2, "a seat and an overlapping reconnect must fit")
		engine.Unsubscribe(one)
		_, err := engine.Subscribe()
		require.NoError(t, err)
		clockSteps(t, engine, 1)
		require.False(t, engine.IsFinished(), "filling the feed must not end the hand")
	})
}

func TestE2E_ReconnectOverlap(t *testing.T) {
	t.Parallel()
	manager := newAdversarialManager(t)
	l, _, players := startFilledLobby(t, manager, "poker", 2, false)
	t.Cleanup(func() { releaseTables(t, manager, l.Code()) })
	guest := players[1]
	other := nPlayers(50, 1)[0]

	for range 25 {
		manager.DisconnectPlayer(guest)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			manager.ResumePlayer(guest)
		}()
		go func() {
			defer wg.Done()
			manager.ResumePlayer(guest)
		}()
		waitOrFail(t, &wg, 5*time.Second)
		require.Same(t, l, manager.FindLobbyByPlayer(guest))
		require.True(t, l.HasPlayer(guest))
		require.Equal(t, 2, l.Snapshot().Players())
	}

	for range 25 {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			manager.DisconnectPlayer(guest)
		}()
		go func() {
			defer wg.Done()
			manager.ResumePlayer(guest)
		}()
		waitOrFail(t, &wg, 5*time.Second)
		require.Same(t, l, manager.FindLobbyByPlayer(guest))
		require.Equal(t, 2, l.Snapshot().Players())
		require.False(t, l.HasPlayer(other))
	}

	require.Error(t, joinErr(manager.JoinLobbyByCode(l.Code(), guest)))
	_, err := manager.CreateLobby(guest, lobby.WithCardGame("uno"))
	require.ErrorIs(t, err, lobby.ErrRefused)
	require.Same(t, l, manager.FindLobbyByPlayer(guest))
	assertSettled(t, manager, []string{l.Code()}, players)
}

func TestE2E_CatalogGamesTakeClockSteps(t *testing.T) {
	t.Parallel()
	for _, entry := range catalog.Games {
		t.Run(entry.Slug, func(t *testing.T) {
			t.Parallel()
			manager := newAdversarialManager(t)
			// Min seats only: a full short match of gin or hearts is thousands of
			// clock steps, and a handful is enough to prove the table actually deals.
			l, engine, players := startFilledLobby(t, manager, entry.Slug, entry.MinPlayers, false)
			t.Cleanup(func() { releaseTables(t, manager, l.Code()) })
			clockSteps(t, engine, 8)
			require.False(t, engine.IsFinished(), "a few clock steps must not play out the match")
			require.NotNil(t, l.ActiveGame())
			for _, p := range players {
				require.Same(t, l, manager.FindLobbyByPlayer(p))
			}
			require.Equal(t, entry.MinPlayers, l.Snapshot().Players())
		})
	}
}

func TestE2E_CrackPlayingAndSpamCreate(t *testing.T) {
	t.Parallel()

	t.Run("after_the_game_starts", func(t *testing.T) {
		t.Parallel()
		manager := newAdversarialManager(t)
		l, engine, players := startFilledLobby(t, manager, "poker", 3, true)
		t.Cleanup(func() { releaseTables(t, manager, l.Code()) })
		leaver := players[2]
		ranked := l.Snapshot().Ranked
		limit := l.Snapshot().Limit

		manager.LeaveLobby(leaver)
		manager.LeaveLobby(leaver)
		manager.DisconnectPlayer(leaver)
		manager.ResumePlayer(leaver)
		require.False(t, l.HasPlayer(leaver))
		require.Nil(t, manager.FindLobbyByPlayer(leaver))
		require.Nil(t, l.Seat(leaver.ID))
		require.Equal(t, 2, l.Snapshot().Players())

		for range 20 {
			require.Error(t, engine.SubmitAction(leaver.ID, poker.ActionFold{}))
		}
		require.False(t, engine.IsFinished(), "a departed seat's moves must not end the hand")

		for _, p := range players {
			err := manager.ToggleReady(p)
			require.ErrorIs(t, err, lobby.ErrRefused)
			if p == leaver {
				require.ErrorContains(t, err, "not in lobby")
				continue
			}
			require.ErrorContains(t, err, "already in progress")
		}
		require.ErrorContains(t, l.SetRanked(players[0], false), "cannot change settings")
		require.ErrorContains(t, l.SetPrivate(players[0], false), "cannot change settings")
		require.ErrorContains(t, l.SetMaxPlayers(players[0], 2), "cannot change settings")
		require.ErrorIs(t, l.SetRanked(players[1], false), lobby.ErrRefused)
		require.Equal(t, ranked, l.Snapshot().Ranked)
		require.Equal(t, limit, l.Snapshot().Limit)
		require.True(t, l.Snapshot().Private, "an in-game table cannot be published")
		require.True(t, l.Snapshot().InGame)
		require.NotContains(t, browseCodes(manager, nil), l.Code())

		late := nPlayers(40, 1)[0]
		require.ErrorContains(t, joinErr(manager.JoinLobbyByCode(l.Code(), late)), "not accepting")
		require.Nil(t, manager.FindLobbyByPlayer(late))
		require.Equal(t, 2, l.Snapshot().Players())

		require.ErrorContains(t, manager.Kick(players[0], players[1]), "cannot kick during a game")
		require.True(t, l.HasPlayer(players[1]))
		_, err := manager.CreateLobby(players[0], lobby.WithCardGame("poker"))
		require.ErrorContains(t, err, "already in a lobby")

		current := engine.CurrentPlayerID()
		require.NotEmpty(t, current)
		require.NotNil(t, l.Seat(current))
		require.Error(t, engine.SubmitAction(current, nil))
		require.Error(t, engine.SubmitAction(current, junkAction{}))
		require.False(t, engine.IsFinished())
		clockSteps(t, engine, 1)
	})

	t.Run("spam_create", func(t *testing.T) {
		t.Parallel()
		manager := newAdversarialManager(t)
		seated := nPlayers(1, 1)[0]
		first := mustCreate(t, manager, seated, "poker", 2, false)
		for range 30 {
			_, err := manager.CreateLobby(seated, lobby.WithCardGame("gin_rummy"))
			require.ErrorIs(t, err, lobby.ErrRefused)
			require.ErrorContains(t, err, "already in a lobby")
		}
		require.Same(t, first, manager.FindLobbyByPlayer(seated))
		_, open := manager.Stats()
		require.Equal(t, 1, open)
		manager.LeaveLobby(seated)

		const tables = 32
		players := nPlayers(2, tables)
		var wg sync.WaitGroup
		var mu sync.Mutex
		codes := make([]string, 0, tables)
		errs := make([]error, 0)
		for _, p := range players {
			wg.Add(1)
			go func(p *game.Player) {
				defer wg.Done()
				created, err := manager.CreateLobby(p,
					lobby.WithCardGame("crazy_eights"),
					lobby.WithPrivate(false),
					lobby.WithMaxPlayers(2),
				)
				var again error
				if err == nil {
					_, again = manager.CreateLobby(p, lobby.WithCardGame("poker"))
				}
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					errs = append(errs, err)
					return
				}
				if again == nil {
					errs = append(errs, fmt.Errorf("%s opened a second table", p.Name))
					return
				}
				codes = append(codes, created.Code())
			}(p)
		}
		waitOrFail(t, &wg, 15*time.Second)
		require.Empty(t, errs)
		require.Len(t, codes, tables)
		require.Len(t, codesFor(manager, lobby.BrowseFilter{Limit: lobby.MaxBrowseLimit}), tables)
		playing, open := manager.Stats()
		require.Zero(t, playing)
		require.Equal(t, tables, open)
		assertSettled(t, manager, codes, players)

		wg = sync.WaitGroup{}
		for _, p := range players {
			wg.Add(1)
			go func(p *game.Player) {
				defer wg.Done()
				manager.LeaveLobby(p)
				manager.LeaveLobby(p)
			}(p)
		}
		waitOrFail(t, &wg, 10*time.Second)

		playing, open = manager.Stats()
		require.Zero(t, playing)
		require.Zero(t, open)
		require.Empty(t, browseCodes(manager, nil))
		for _, p := range players {
			require.Nil(t, manager.FindLobbyByPlayer(p))
		}
		for _, code := range codes {
			_, err := manager.FindLobbyByCode(code)
			require.ErrorContains(t, err, "not found")
		}
		require.Error(t, joinErr(manager.JoinLobbyByCode(codes[0], players[0])))
		require.Nil(t, manager.FindLobbyByPlayer(players[0]))

		fresh := mustCreate(t, manager, players[0], "poker", 2, false)
		require.Equal(t, 1, fresh.Snapshot().Players())
		if fresh.Code() == codes[0] {
			require.False(t, fresh.HasPlayer(players[1]))
			return
		}
		_, err := manager.FindLobbyByCode(codes[0])
		require.ErrorContains(t, err, "not found")
	})
}

func newAdversarialManager(t *testing.T) *lobby.Manager {
	t.Helper()
	return lobby.NewManager(t.Context(), newRankedFinalizeRecorder(), catalog.NewRegistry())
}

func nPlayers(start, n int) []*game.Player {
	out := make([]*game.Player, n)
	for i := range n {
		id := start + i
		out[i] = newPlayer(testutil.UID(id), fmt.Sprintf("p%d", id))
	}
	return out
}

func mustCreate(t *testing.T, m *lobby.Manager, leader *game.Player, slug string, maxPlayers int, private bool) *lobby.Lobby {
	t.Helper()
	l, err := m.CreateLobby(leader,
		lobby.WithCardGame(slug),
		lobby.WithMaxPlayers(maxPlayers),
		lobby.WithPrivate(private),
	)
	require.NoError(t, err)
	return l
}

func codesFor(m *lobby.Manager, f lobby.BrowseFilter) []string {
	entries := m.BrowseLobbies(nil, f)
	codes := make([]string, len(entries))
	for i, e := range entries {
		codes[i] = e.Code
	}
	return codes
}

func requireBrowse(t *testing.T, m *lobby.Manager, f lobby.BrowseFilter, want ...string) {
	t.Helper()
	got := codesFor(m, f)
	if len(want) == 0 {
		require.Empty(t, got)
		return
	}
	require.ElementsMatch(t, want, got)
}

// clockSteps plays n timeout actions, stopping early when the match is already over.
// Eight steps leaves the opening deal without playing out gin or hearts.
func clockSteps(t *testing.T, engine *game.Engine, n int) {
	t.Helper()
	for step := range n {
		if engine.IsFinished() {
			return
		}
		id := engine.CurrentPlayerID()
		require.NotEmpty(t, id, "step %d", step)
		var act game.Action
		engine.WithState(func(s *game.State) {
			act = s.Rules.TimeoutAction(s)
		})
		require.NotNil(t, act, "step %d", step)
		require.NoError(t, engine.SubmitAction(id, act), "step %d: %s", step, act.Name())
	}
}

func releaseTables(t *testing.T, m *lobby.Manager, codes ...string) {
	t.Helper()
	for _, code := range codes {
		m.RemoveLobby(code)
	}
	require.True(t, m.WaitForFinalizers(5*time.Second), "watchers did not drain")
}

func waitOrFail(t *testing.T, wg *sync.WaitGroup, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatal("timed out waiting for lobby operations")
	}
}

// assertSettled is the invariant after concurrent lobby traffic: one leader per
// live table, no seat twice, nobody over the cap, and the index matches the
// rosters with nobody left over.
func assertSettled(t *testing.T, m *lobby.Manager, codes []string, players []*game.Player) {
	t.Helper()
	known := make(map[string]*game.Player, len(players))
	for _, p := range players {
		known[p.ID] = p
	}
	seated := make(map[string]string)
	live := 0
	for _, code := range codes {
		l, err := m.FindLobbyByCode(code)
		if err != nil {
			continue
		}
		live++
		snap := l.Snapshot()
		require.NotNil(t, snap.Leader.Player)
		require.LessOrEqual(t, snap.Players(), snap.Limit, "table %s over cap", code)
		seats := make([]*game.Player, 0, snap.Players())
		seats = append(seats, snap.Leader.Player)
		for _, g := range snap.Guests {
			require.False(t, snap.IsLeader(g.Player), "table %s has two leaders", code)
			seats = append(seats, g.Player)
		}
		seen := make(map[string]struct{}, len(seats))
		for _, p := range seats {
			require.NotNil(t, p)
			_, dup := seen[p.ID]
			require.Falsef(t, dup, "duplicate seat %s at %s", p.ID, code)
			seen[p.ID] = struct{}{}
			require.Contains(t, known, p.ID, "ghost seat %s at %s", p.ID, code)
			prev, ok := seated[p.ID]
			require.Falsef(t, ok, "%s sits at %s and %s", p.ID, prev, code)
			seated[p.ID] = code
			require.Same(t, l, m.FindLobbyByPlayer(p))
			require.True(t, l.HasPlayer(p))
		}
	}
	playing, open := m.Stats()
	require.Equal(t, live, playing+open, "stats do not match the tables still findable by code")
	for _, p := range players {
		l := m.FindLobbyByPlayer(p)
		if l == nil {
			require.NotContains(t, seated, p.ID)
			continue
		}
		require.Equal(t, l.Code(), seated[p.ID])
		require.True(t, l.HasPlayer(p))
	}
}
