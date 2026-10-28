package lobby

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/elo"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLobby_Snapshot(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, testutil.NopMatches{})
	leader := mockPlayer("leader", testutil.UID(1))
	leader.Ratings = map[string]uint32{"testgame": 1650}
	guest := mockPlayer("guest", testutil.UID(2))
	l, err := m.CreateLobby(leader, WithCardGame("testgame"), WithMaxPlayers(3), WithPrivate(false), WithRanked(true))
	require.NoError(t, err)
	_, err = m.JoinLobbyByCode(l.Code(), guest)
	require.NoError(t, err)
	require.NoError(t, m.ToggleReady(guest))

	snap := l.Snapshot()

	assert.Equal(t, Snapshot{
		Code: l.Code(), Slug: "testgame", GameName: "TestGame",
		MinPlayers: 1, MaxPlayers: 10, Limit: 3,
		Private: false, Ranked: true, InGame: false,
		Leader: SeatView{Player: leader, Elo: 1650},
		Guests: []SeatView{{Player: guest, Elo: elo.ToUint32(elo.DefaultRating), Ready: true}},
	}, snap)
	assert.Equal(t, 2, snap.Players())
	assert.True(t, snap.IsLeader(leader))
	assert.False(t, snap.IsLeader(guest))
	seat, ok := snap.SeatOf(guest)
	assert.True(t, ok)
	assert.Equal(t, snap.Guests[0], seat)
	_, ok = snap.SeatOf(mockPlayer("stranger", testutil.UID(3)))
	assert.False(t, ok)
}

// A snapshot is a copy: what the lobby does next, and what a view does to the copy,
// stay on their own side.
func TestLobby_SnapshotDoesNotAlias(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, testutil.NopMatches{})
	leader := mockPlayer("leader", testutil.UID(1))
	first := mockPlayer("first", testutil.UID(2))
	l, err := m.CreateLobby(leader, WithCardGame("testgame"), WithMaxPlayers(4))
	require.NoError(t, err)
	_, err = m.JoinLobbyByCode(l.Code(), first)
	require.NoError(t, err)

	before := l.Snapshot()
	before.Guests[0] = SeatView{Player: mockPlayer("forged", testutil.UID(9))}
	_, seated := l.Snapshot().SeatOf(first)
	assert.True(t, seated, "a write to the copy reached the roster")

	before = l.Snapshot()
	_, err = m.JoinLobbyByCode(l.Code(), mockPlayer("second", testutil.UID(3)))
	require.NoError(t, err)
	require.NoError(t, m.ToggleReady(first))
	require.NoError(t, l.SetRanked(leader, true))

	assert.Len(t, before.Guests, 1)
	assert.False(t, before.Guests[0].Ready)
	assert.False(t, before.Ranked)
}

// The torn frame Snapshot exists to prevent: read leader and guests in two holds and
// a promotion between them shows the new leader twice. Under -race this also proves
// the copy is taken under the lock.
func TestLobby_SnapshotIsConsistentUnderChurn(t *testing.T) {
	t.Parallel()

	m := newTestManager(t, testutil.NopMatches{})
	unlimitedJoins(m)
	leader := mockPlayer("leader", testutil.UID(1))
	// The anchor never leaves or readies, so the table neither closes nor starts.
	anchor := mockPlayer("anchor", testutil.UID(2))
	l, err := m.CreateLobby(leader, WithCardGame("testgame"), WithMaxPlayers(10))
	require.NoError(t, err)
	_, err = m.JoinLobbyByCode(l.Code(), anchor)
	require.NoError(t, err)

	churners := []*game.Player{leader}
	for i := range 4 {
		churners = append(churners, mockPlayer(fmt.Sprintf("c%d", i), testutil.UID(uint64(i+3))))
	}

	done := make(chan struct{})
	var writers, readers sync.WaitGroup
	for _, p := range churners {
		writers.Go(func() {
			for range 200 {
				_, _ = m.JoinLobbyByCode(l.Code(), p)
				_ = m.ToggleReady(p)
				m.LeaveLobby(p)
			}
		})
	}
	for range 2 {
		readers.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}
				assertConsistentSnapshot(t, l.Snapshot(), anchor)
			}
		})
	}

	runWithTimeout(t, 10*time.Second, writers.Wait)
	close(done)
	readers.Wait()
}

func assertConsistentSnapshot(t *testing.T, s Snapshot, anchor *game.Player) {
	t.Helper()
	seen := map[string]bool{s.Leader.Player.ID: true}
	for _, g := range s.Guests {
		if seen[g.Player.ID] {
			t.Errorf("%s holds two seats in one snapshot", g.Player.ID)
		}
		seen[g.Player.ID] = true
	}
	if s.Players() > s.Limit {
		t.Errorf("%d seats over a cap of %d", s.Players(), s.Limit)
	}
	if _, ok := s.SeatOf(anchor); !ok {
		t.Error("the anchor, who never left, is missing")
	}
}
