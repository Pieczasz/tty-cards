package lobby

import (
	"testing"

	"github.com/Pieczasz/terminal-card/internal/testutil"

	"github.com/stretchr/testify/require"
)

func TestLobby_EveryPlayerGetsAFeedAfterTheTableGrows(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	seats := testutil.Players(10)
	leader := seats[0]
	l, err := m.CreateLobby(leader, WithMaxPlayers(2), WithCardGame("Uno"))
	require.NoError(t, err)

	require.NoError(t, l.SetMaxPlayers(leader, 10, 2, 10))
	for _, guest := range seats[1:] {
		require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
	}
	require.Equal(t, 10, l.CurrentPlayers())

	for _, p := range seats {
		_, err := l.Subscribe(p.ID)
		require.NoErrorf(t, err, "seat %s was refused a lobby feed", p.ID)
	}

	_, err = l.Subscribe(leader.ID)
	require.NoError(t, err, "a reconnecting player must not be refused")
}
