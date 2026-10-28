package testutil_test

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fixtures promise the seats lobby.NewPlayer would make. If the lobby changes how
// it keys a seat, every test built on these would quietly stop describing a real table.
func TestPlayersSeatAsTheLobbyDoes(t *testing.T) {
	t.Parallel()
	players := testutil.Players(3)
	require.Len(t, players, 3)
	for i, p := range players {
		n := i + 1
		seated := lobby.NewPlayer(&db.User{ID: testutil.UID(n), Username: p.Name})
		assert.Equal(t, seated.ID, p.ID, "seat %d", n)
		assert.Equal(t, seated.UserID, p.UserID, "seat %d", n)
		assert.Equal(t, testutil.SeatID(n), p.ID, "seat %d", n)
	}
	assert.Equal(t, []string{"p1", "p2", "p3"}, []string{players[0].Name, players[1].Name, players[2].Name})
	assert.Equal(t, uuid.Nil(), testutil.UID(0))
	assert.NotEqual(t, testutil.UID(1), testutil.UID(2))
}
