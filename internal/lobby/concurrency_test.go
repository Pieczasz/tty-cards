package lobby

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/ratelimit"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unlimitedJoins swaps the per-player join rate limiter for one that never
// throttles, so a fan-out of concurrent joins is not masked by rate limiting.
func unlimitedJoins(m *Manager) {
	m.joinLimiter = ratelimit.New(1_000_000, time.Hour)
}

// runWithTimeout runs fn in a goroutine and fails the test loudly if it does not
// finish within d, so a deadlock in the subsystem fails instead of hanging the
// whole `go test` run forever.
func runWithTimeout(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("timed out after %s waiting for concurrent work to finish (possible deadlock)", d)
	}
}

// guestRef pairs a joiner with the per-goroutine outcome of its join attempt.
type guestRef struct {
	p      *game.Player
	joined bool
}

// TestConcurrent_JoinUpToCapacity fans out many simultaneous joins against a
// single lobby and asserts capacity is respected exactly: no over-join, no
// double-join, and every rejected joiner gets a "full" error.
func TestConcurrent_JoinUpToCapacity(t *testing.T) {
	t.Parallel()

	const (
		maxPlayers = 6  // leader + 5 guest slots
		joiners    = 64 // far more than free slots
	)
	freeSlots := maxPlayers - 1 // leader already occupies one slot

	m := newTestManager(t, nil)
	unlimitedJoins(m)
	leader := mockPlayer("leader", testutil.UID(1))
	l, err := m.New(leader, WithMaxPlayers(maxPlayers), WithCardGame("TestGame"))
	require.NoError(t, err)

	guests := make([]*guestRef, joiners)
	for i := range guests {
		guests[i] = &guestRef{p: mockPlayer(fmt.Sprintf("g%d", i), testutil.UID(uint64(i+2)))}
	}

	var (
		successes atomic.Int64
		fullErrs  atomic.Int64
		otherErrs atomic.Int64
		start     = make(chan struct{})
		wg        sync.WaitGroup
	)

	for _, g := range guests {
		wg.Add(1)
		go func(g *guestRef) {
			defer wg.Done()
			<-start // release all goroutines together to maximise contention
			err := joinErr(m.JoinLobbyByCode(l.Code(), g.p))
			switch {
			case err == nil:
				g.joined = true
				successes.Add(1)
			case strings.Contains(err.Error(), "full"):
				fullErrs.Add(1)
			default:
				otherErrs.Add(1)
			}
		}(g)
	}

	runWithTimeout(t, 10*time.Second, func() {
		close(start)
		wg.Wait()
	})

	assert.Zero(t, otherErrs.Load(), "unexpected non-full errors from rejected joiners")
	assert.Equal(t, int64(freeSlots), successes.Load(), "exactly the free slots should be filled")
	assert.Equal(t, int64(joiners-freeSlots), fullErrs.Load(), "all remaining joiners must be rejected as full")

	// Roster is exactly full and contains no duplicates.
	assert.Equal(t, maxPlayers, l.CurrentPlayers(), "lobby must end exactly at capacity")

	seen := map[string]bool{leader.ID: true}
	joinedCount := 0
	for _, g := range guests {
		if g.joined {
			joinedCount++
			require.True(t, l.HasPlayer(g.p), "successful joiner must be a member")
			require.False(t, seen[g.p.ID], "player joined more than once (double-join)")
			seen[g.p.ID] = true
			require.Equal(t, l, m.FindLobbyByPlayer(g.p), "playerLobby mapping must point at the lobby")
		} else {
			assert.False(t, l.HasPlayer(g.p), "rejected joiner must not be a member")
			assert.Nil(t, m.FindLobbyByPlayer(g.p), "rejected joiner must not have a playerLobby entry")
		}
	}
	assert.Equal(t, freeSlots, joinedCount)
}

// TestConcurrent_LeaderAndGuestsLeaveSimultaneously drains a full lobby by having
// the leader and every guest call LeaveLobby at the same time. The lobby must end
// up removed with no ghost membership left in the manager, and nothing may panic.
func TestConcurrent_LeaderAndGuestsLeaveSimultaneously(t *testing.T) {
	t.Parallel()

	const players = 8 // leader + 7 guests

	m := newTestManager(t, nil)
	unlimitedJoins(m)
	leader := mockPlayer("leader", testutil.UID(1))
	l, err := m.New(leader, WithMaxPlayers(players), WithCardGame("TestGame"))
	require.NoError(t, err)

	all := []*game.Player{leader}
	for i := 1; i < players; i++ {
		g := mockPlayer(fmt.Sprintf("g%d", i), testutil.UID(uint64(i+1)))
		require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), g)))
		all = append(all, g)
	}
	require.Equal(t, players, l.CurrentPlayers())

	var (
		start = make(chan struct{})
		wg    sync.WaitGroup
	)
	for _, p := range all {
		wg.Add(1)
		go func(p *game.Player) {
			defer wg.Done()
			<-start
			m.LeaveLobby(p)
		}(p)
	}

	runWithTimeout(t, 10*time.Second, func() {
		close(start)
		wg.Wait()
	})

	// Lobby must be gone from the manager entirely.
	_, err = m.FindLobbyByCode(l.Code())
	require.ErrorContains(t, err, "lobby not found", "empty lobby must be removed")

	// No ghost membership: the manager's playerLobby map is the authoritative
	// membership index, and no player may still resolve to a lobby through it.
	// (The removed lobby object's own roster slice is intentionally left as-is by
	// RemoveLobby since the object is discarded, so it is not a membership signal.)
	for _, p := range all {
		assert.Nil(t, m.FindLobbyByPlayer(p), "player %q left a ghost playerLobby entry", p.ID)
	}
}

// TestConcurrent_JoinRacingLastLeave repeatedly interleaves a fresh joiner with
// the sole occupant (leader) leaving. Whatever the interleaving, the manager must
// land in one of two consistent states: the lobby was removed and the joiner is
// not seated, or the joiner is properly seated in a live lobby. No orphan entries
// either way.
func TestConcurrent_JoinRacingLastLeave(t *testing.T) {
	t.Parallel()

	const iterations = 500

	runWithTimeout(t, 30*time.Second, func() {
		for i := range iterations {
			m := newTestManager(t, nil)
			unlimitedJoins(m)

			leader := mockPlayer(fmt.Sprintf("leader-%d", i), testutil.UID(uint64(2*i+1)))
			l, err := m.New(leader, WithMaxPlayers(4), WithCardGame("TestGame"))
			require.NoError(t, err)
			code := l.Code()

			joiner := mockPlayer(fmt.Sprintf("joiner-%d", i), testutil.UID(uint64(2*i+2)))

			var (
				wg      sync.WaitGroup
				gate    = make(chan struct{})
				joinErr error
			)
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-gate
				_, joinErr = m.JoinLobbyByCode(code, joiner)
			}()
			go func() {
				defer wg.Done()
				<-gate
				m.LeaveLobby(leader)
			}()
			close(gate)
			wg.Wait()

			// Leader always left, so it must never be a lingering member.
			assert.Nil(t, m.FindLobbyByPlayer(leader), "iter %d: leader left a ghost entry", i)

			_, findErr := m.FindLobbyByCode(code)
			lobbyGone := findErr != nil

			if joinErr == nil {
				// Joiner was seated: the lobby must still exist and hold them.
				require.False(t, lobbyGone, "iter %d: join succeeded but lobby is gone", i)
				assert.True(t, l.HasPlayer(joiner), "iter %d: seated joiner missing from roster", i)
				assert.Equal(t, l, m.FindLobbyByPlayer(joiner), "iter %d: seated joiner missing mapping", i)
			} else {
				// Joiner was rejected: it must own no membership anywhere.
				assert.Nil(t, m.FindLobbyByPlayer(joiner), "iter %d: rejected joiner left a ghost entry", i)
				assert.False(t, l.HasPlayer(joiner), "iter %d: rejected joiner still a member", i)
			}
		}
	})
}

// TestConcurrent_ToggleReady hammers ToggleReady from many goroutines across all
// members of one lobby. Game rules demand more players than are present so a start
// never fires, keeping the lobby in Waiting while the ready map is mutated under
// contention. The point is race-detector cleanliness plus a consistent end state.
func TestConcurrent_ToggleReady(t *testing.T) {
	t.Parallel()

	const (
		members    = 5
		togglers   = 32
		perGoTurns = 40
	)

	m := newTestManager(t, nil)
	unlimitedJoins(m)
	leader := mockPlayer("leader", testutil.UID(1))
	l, err := m.New(leader, WithMaxPlayers(members), WithCardGame("NeverStarts"))
	require.NoError(t, err)

	roster := []*game.Player{leader}
	for i := 1; i < members; i++ {
		g := mockPlayer(fmt.Sprintf("g%d", i), testutil.UID(uint64(i+1)))
		require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), g)))
		roster = append(roster, g)
	}

	// Rules requiring far more players than present: an all-ready roster fails to
	// start, so the lobby stays in Waiting and the ready map keeps churning.
	mockRules := new(MockRules)
	mockRules.On("MinPlayers").Return(members + 100)
	registry := gameRegistry("NeverStarts", mockRules)

	var (
		start = make(chan struct{})
		wg    sync.WaitGroup
	)
	for g := range togglers {
		target := roster[g%len(roster)]
		wg.Add(1)
		go func(p *game.Player) {
			defer wg.Done()
			<-start
			for range perGoTurns {
				// Error is expected sometimes (e.g. "need at least N players" when the
				// toggle briefly makes everyone ready). We only care that it never
				// races or deadlocks; correctness of the ready map is checked after.
				_ = l.ToggleReady(p, registry)
			}
		}(target)
	}

	runWithTimeout(t, 15*time.Second, func() {
		close(start)
		wg.Wait()
	})

	// The lobby never had enough players to start, so it must still be Waiting,
	// with an intact roster and every ready flag a well-defined bool.
	assert.True(t, isWaiting(l), "lobby must remain in Waiting; a start should never have succeeded")
	assert.Equal(t, members, l.CurrentPlayers(), "roster must be unchanged")
	for _, p := range roster {
		_ = l.IsReady(p) // must not race or panic
		assert.True(t, l.HasPlayer(p), "member %q vanished from roster", p.ID)
	}
}

// seatInvariant is the one thing that must hold after any interleaving of disconnect,
// reconnect and expiry: the roster and the manager's index agree, and no timer is left
// armed for a seat that is already gone. A seat held with no pending timer is a resumed
// player; a seat gone with no index entry is an expired one. Never both, never neither.
func seatInvariant(t *testing.T, m *Manager, l *Lobby, p *game.Player, iter int) {
	t.Helper()
	m.mu.RLock()
	_, pending := m.grace.pending[p.ID]
	indexed := m.playerLobby[p.ID]
	m.mu.RUnlock()

	// A closed lobby keeps its roster slice - RemoveLobby discards the object rather
	// than emptying it - so the state, not the slice, is what says whether a seat
	// exists. The manager's index is the authoritative membership record either way.
	l.mu.RLock()
	seated := l.state != Closed && l.hasPlayerLocked(p)
	l.mu.RUnlock()

	assert.False(t, pending, "iter %d: a grace timer is still armed after both paths settled", iter)
	if seated {
		assert.Equal(t, l, indexed, "iter %d: seat held but the index does not point at the lobby", iter)
		return
	}
	assert.Nil(t, indexed, "iter %d: the seat is gone but the index still claims it", iter)
}

// The grace timer firing and the player reconnecting are the same seat from two
// goroutines. Whoever loses must lose completely: a resume that hands back a lobby the
// expiry has already emptied drops the player into a table that does not hold them.
func TestConcurrent_GraceExpiryRacesReconnect(t *testing.T) {
	t.Parallel()

	const iterations = 200
	runWithTimeout(t, 30*time.Second, func() {
		for i := range iterations {
			m, l, _, guest := startedGame(t)
			m.DisconnectPlayer(guest)

			var (
				wg      sync.WaitGroup
				gate    = make(chan struct{})
				resumed *Lobby
			)
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-gate
				m.expireLeave(guest)
			}()
			go func() {
				defer wg.Done()
				<-gate
				resumed = m.ResumePlayer(guest)
			}()
			close(gate)
			wg.Wait()

			seatInvariant(t, m, l, guest, i)
			if resumed != nil {
				assert.True(t, l.HasPlayer(guest), "iter %d: resumed into a seat that was given up", i)
				assert.Equal(t, l, resumed, "iter %d: resumed into the wrong table", i)
			}
		}
	})
}

// Two sessions for one account can both reach ResumePlayer - a takeover overlaps the
// old session's teardown. Exactly one may cancel the grace, and neither may leave one
// armed or come back with a lobby the player is not in.
func TestConcurrent_ReconnectTwice(t *testing.T) {
	t.Parallel()

	const iterations = 200
	runWithTimeout(t, 30*time.Second, func() {
		for i := range iterations {
			m, l, _, guest := startedGame(t)
			m.DisconnectPlayer(guest)

			var (
				wg   sync.WaitGroup
				gate = make(chan struct{})
				got  [2]*Lobby
			)
			for slot := range got {
				wg.Add(1)
				go func(slot int) {
					defer wg.Done()
					<-gate
					got[slot] = m.ResumePlayer(guest)
				}(slot)
			}
			close(gate)
			wg.Wait()

			seatInvariant(t, m, l, guest, i)
			for _, resumed := range got {
				assert.Equal(t, l, resumed, "iter %d: a reconnecting session lost its own table", i)
			}
		}
	})
}

// The match can finish while a seat is still being held for a reconnect. The reopened
// table gives that seat up, and a resume landing at the same moment must not resurrect
// it or leave the index pointing at a lobby that has already closed.
func TestConcurrent_MatchEndsDuringTheGraceWindow(t *testing.T) {
	t.Parallel()

	const iterations = 200
	runWithTimeout(t, 30*time.Second, func() {
		for i := range iterations {
			m, l, leader, guest := startedGame(t)
			m.DisconnectPlayer(guest)

			var (
				wg   sync.WaitGroup
				gate = make(chan struct{})
			)
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-gate
				m.LeaveLobby(leader) // last player out finishes the engine
			}()
			go func() {
				defer wg.Done()
				<-gate
				m.ResumePlayer(guest)
			}()
			close(gate)
			wg.Wait()

			// The watcher goroutine does the release, so the settled state is eventual.
			require.Eventually(t, func() bool {
				m.mu.RLock()
				defer m.mu.RUnlock()
				_, pending := m.grace.pending[guest.ID]
				return !pending
			}, 2*time.Second, time.Millisecond, "iter %d: a grace timer outlived the match", i)
			seatInvariant(t, m, l, guest, i)
			assert.Nil(t, m.FindLobbyByPlayer(leader), "iter %d: the leader left a ghost entry", i)
		}
	})
}

// Shutdown walks the pending graces while a session is still dropping into one. The
// drain must not leave a timer behind whichever order they land in.
func TestConcurrent_DisconnectDuringShutdown(t *testing.T) {
	t.Parallel()

	const iterations = 200
	runWithTimeout(t, 30*time.Second, func() {
		for i := range iterations {
			m, l, _, guest := startedGame(t)

			var (
				wg   sync.WaitGroup
				gate = make(chan struct{})
			)
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-gate
				m.DisconnectPlayer(guest)
			}()
			go func() {
				defer wg.Done()
				<-gate
				m.BeginShutdown()
			}()
			close(gate)
			wg.Wait()

			seatInvariant(t, m, l, guest, i)
		}
	})
}

// Kick, leave and join all rewrite the same two maps under the manager lock. A target
// that is kicked while leaving, or joining elsewhere, must end up in exactly one place.
func TestConcurrent_KickRacesLeaveAndJoin(t *testing.T) {
	t.Parallel()

	const iterations = 300
	runWithTimeout(t, 30*time.Second, func() {
		for i := range iterations {
			m := newTestManager(t, nil)
			unlimitedJoins(m)

			host := mockPlayer("host", testutil.UID(1))
			target := mockPlayer("target", testutil.UID(2))
			other := mockPlayer("other", testutil.UID(3))

			table, err := m.New(host, WithMaxPlayers(4), WithCardGame("TestGame"))
			require.NoError(t, err)
			elsewhere, err := m.New(other, WithMaxPlayers(4), WithCardGame("TestGame"))
			require.NoError(t, err)
			require.NoError(t, joinErr(m.JoinLobbyByCode(table.Code(), target)))

			var (
				wg   sync.WaitGroup
				gate = make(chan struct{})
			)
			wg.Add(3)
			go func() { defer wg.Done(); <-gate; _ = m.Kick(host, target) }()
			go func() { defer wg.Done(); <-gate; m.LeaveLobby(target) }()
			go func() { defer wg.Done(); <-gate; _ = joinErr(m.JoinLobbyByCode(elsewhere.Code(), target)) }()
			close(gate)
			wg.Wait()

			// Every order of the three is legal, and which one wins is not the point.
			// What must never happen is the player holding two seats, or the roster and
			// the index disagreeing about which single seat they hold.
			m.mu.RLock()
			indexed := m.playerLobby[target.ID]
			m.mu.RUnlock()

			atTable, atElsewhere := table.HasPlayer(target), elsewhere.HasPlayer(target)
			require.False(t, atTable && atElsewhere, "iter %d: seated at two tables at once", i)
			switch {
			case atTable:
				assert.Equal(t, table, indexed, "iter %d: seated at the first table, indexed elsewhere", i)
			case atElsewhere:
				assert.Equal(t, elsewhere, indexed, "iter %d: seated at the second table, indexed elsewhere", i)
			default:
				assert.Nil(t, indexed, "iter %d: no seat anywhere, but the index still claims one", i)
			}
		}
	})
}

// The browse cache clears its dirty flag before it snapshots the lobby set, never
// after: a table created in between would otherwise set the flag, have it cleared by a
// scan that could not see it, and stay hidden for the whole cache TTL.
func TestConcurrent_BrowseCacheNeverSwallowsAnInvalidation(t *testing.T) {
	t.Parallel()

	const tables = 60
	m := newTestManager(t, nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range tables * 10 {
			m.BrowseLobbies(nil, BrowseFilter{Limit: MaxBrowseLimit})
		}
	}()
	go func() {
		defer wg.Done()
		for i := range tables {
			_, err := m.New(mockPlayer(fmt.Sprintf("p%d", i), testutil.UID(uint64(i+1))),
				WithPrivate(false), WithCardGame("TestGame"))
			assert.NoError(t, err)
		}
	}()
	runWithTimeout(t, 30*time.Second, wg.Wait)

	assert.Len(t, m.BrowseLobbies(nil, BrowseFilter{Limit: MaxBrowseLimit}), tables,
		"a table created while a browse was scanning stayed hidden for the cache TTL")
}
