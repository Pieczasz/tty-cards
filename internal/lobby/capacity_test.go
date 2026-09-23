package lobby

import (
	"fmt"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/testutil"

	"github.com/stretchr/testify/require"
)

func TestLobby_EveryPlayerGetsAFeedAfterTheTableGrows(t *testing.T) {
	t.Parallel()
	m := newTestManager(t, nil)
	leader := mockPlayer("leader", testutil.UID(1))
	l, err := m.New(leader, WithMaxPlayers(2), WithCardGame("Uno"))
	require.NoError(t, err)

	require.NoError(t, l.SetMaxPlayers(leader, 10, 2, 10))

	ids := []string{leader.ID}
	for i := 1; i < 10; i++ {
		guest := mockPlayer(fmt.Sprintf("guest%d", i), testutil.UID(uint64(i+1)))
		require.NoError(t, joinErr(m.JoinLobbyByCode(l.Code(), guest)))
		ids = append(ids, guest.ID)
	}
	require.Equal(t, 10, l.CurrentPlayers())

	for _, id := range ids {
		_, err := l.Subscribe(id)
		require.NoErrorf(t, err, "seat %s was refused a lobby feed", id)
	}

	_, err = l.Subscribe(leader.ID)
	require.NoError(t, err, "a reconnecting player must not be refused")
}
