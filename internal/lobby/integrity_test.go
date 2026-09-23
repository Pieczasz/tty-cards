package lobby

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
