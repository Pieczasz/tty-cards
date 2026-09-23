package lobby

import (
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func isWaiting(l *Lobby) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.state == Waiting
}

// A guest readied up for the table they saw. Once the leader changes what the table
// is - ranked, capacity, visibility - that consent no longer covers it, and the
// leader's own ready must not start a match the guests never agreed to.
func TestLobby_SettingChangeUnreadiesTheTable(t *testing.T) {
	t.Parallel()

	changes := map[string]func(l *Lobby, leader *game.Player) error{
		"ranked":      func(l *Lobby, leader *game.Player) error { return l.SetRanked(leader, true) },
		"private":     func(l *Lobby, leader *game.Player) error { return l.SetPrivate(leader, false) },
		"max_players": func(l *Lobby, leader *game.Player) error { return l.SetMaxPlayers(leader, 3, 0, 0) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, l, registry := newTestLobby(t, 4)
			leader := l.Leader()
			guest := mockPlayer("p2", testutil.UID(2))
			require.NoError(t, m.JoinLobbyByCode(l.Code(), guest))
			require.NoError(t, l.ToggleReady(guest, registry))

			ch, err := l.Subscribe("watcher")
			require.NoError(t, err)
			require.NoError(t, change(l, leader))

			assert.False(t, l.IsReady(guest), "the guest's ready survived a setting change")
			assert.Contains(t, drainEventTypes(ch), EventPlayersUpdated, "the rosters were not told")

			require.NoError(t, l.ToggleReady(leader, registry))
			assert.Equal(t, Waiting, l.state, "the leader started a match the guest never readied for")
		})
	}
}

// The start is only ever checked on a ready toggle, so a roster change that left
// everyone else ready used to strand the table: all-ready, and nothing to start it.
// The same rule as a setting change applies - the table changed, so nobody is ready.
func TestLobby_RosterChangeUnreadiesTheTable(t *testing.T) {
	t.Parallel()

	removals := map[string]func(m *Manager, leader, holdout *game.Player) error{
		"the holdout leaves":    func(m *Manager, _, holdout *game.Player) error { m.LeaveLobby(holdout); return nil },
		"the holdout is kicked": func(m *Manager, leader, holdout *game.Player) error { return m.Kick(leader, holdout) },
	}
	for name, remove := range removals {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, l, registry := newTestLobby(t, 4)
			leader := l.Leader()
			b := mockPlayer("p2", testutil.UID(2))
			c := mockPlayer("p3", testutil.UID(3))
			holdout := mockPlayer("p4", testutil.UID(4))
			for _, p := range []*game.Player{b, c, holdout} {
				require.NoError(t, m.JoinLobbyByCode(l.Code(), p))
			}
			for _, p := range []*game.Player{leader, b, c} {
				require.NoError(t, l.ToggleReady(p, registry))
			}

			require.NoError(t, remove(m, leader, holdout))

			for _, p := range []*game.Player{leader, b, c} {
				assert.False(t, l.IsReady(p), "%s is still ready after the roster changed", p.ID)
			}
			assert.Equal(t, Waiting, l.state)
		})
	}

	t.Run("the leader leaves", func(t *testing.T) {
		t.Parallel()
		m, l, registry := newTestLobby(t, 4)
		leader := l.Leader()
		b := mockPlayer("p2", testutil.UID(2))
		c := mockPlayer("p3", testutil.UID(3))
		require.NoError(t, m.JoinLobbyByCode(l.Code(), b))
		require.NoError(t, m.JoinLobbyByCode(l.Code(), c))
		require.NoError(t, l.ToggleReady(b, registry))

		m.LeaveLobby(leader)

		assert.False(t, l.IsReady(b), "the promoted table kept a ready from before the change")
	})
}

// LeaveLobby drops the index entry and releases m.mu before it calls RemoveLobby, so a
// leader who closes a table can already sit at a new one by the time the old one is
// removed. RemoveLobby must not take the new table's mapping with it.
func TestRemoveLobby_KeepsANewerLobbysMapping(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	p := mockPlayer("p1", testutil.UID(1))
	old, err := m.New(p, WithCardGame("Mock"))
	require.NoError(t, err)

	// The window inside LeaveLobby: the entry is gone, RemoveLobby has not run yet.
	m.mu.Lock()
	delete(m.playerLobby, p.ID)
	m.mu.Unlock()
	fresh, err := m.New(p, WithCardGame("Mock"))
	require.NoError(t, err)

	m.RemoveLobby(old.Code())

	assert.Same(t, fresh, m.FindLobbyByPlayer(p), "removing the old table unmapped the new one")
}

// A hold can outlive its hand: releaseFinishedGame reopens the table before
// releaseHeldSeats reaches m.mu, and a kick in that gap unmaps the guest, so the
// release no longer finds them. The timer then fires a DisconnectGrace later and
// LeaveLobby takes the player out of whatever table they sit at by then.
func TestKick_ClearsTheTargetsGraceHold(t *testing.T) {
	t.Parallel()
	m, l, _ := newTestLobby(t, 4)
	guest := mockPlayer("p2", testutil.UID(2))
	require.NoError(t, m.JoinLobbyByCode(l.Code(), guest))

	m.mu.Lock()
	m.grace.arm(guest.ID, time.Hour, func() {})
	m.mu.Unlock()

	require.NoError(t, m.Kick(l.Leader(), guest))

	m.mu.RLock()
	defer m.mu.RUnlock()
	assert.NotContains(t, m.grace.pending, guest.ID, "the kicked guest's grace timer is still armed")
}
