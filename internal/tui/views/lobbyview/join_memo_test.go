package lobbyview

import (
	"testing"

	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The join browser re-renders on a 2s tick for every player parked on it, so an
// unchanged frame must come from the memo, not a fresh ~6.7k-allocation render.
func TestJoinView_MemoizesUnchangedFrames(t *testing.T) {
	t.Parallel()
	g := router.GlobalContext{Width: 100, Height: 30, LobbyManager: newManager(t)}
	m := NewJoin(g).(*joinModel)

	first := m.View()
	require.NotEmpty(t, m.lastView, "the frame is cached after a render")
	cached := m.lastView
	second := m.View()
	assert.Equal(t, first, second, "unchanged state renders the identical frame")
	assert.Equal(t, cached, m.lastView, "and the cache entry is reused, not rebuilt")

	m.cursor++
	_ = m.View()
	assert.NotEqual(t, cached, m.lastKey, "a cursor move invalidates the memo")
}

func TestJoinRenderKey_CoversTheListContents(t *testing.T) {
	t.Parallel()
	g := router.GlobalContext{
		Width: 100, Height: 30,
		LobbyManager: newManager(t),
	}
	m := NewJoin(g).(*joinModel)
	m.entries = []lobby.BrowseEntry{{Code: "AAAA", GameName: "Poker", Players: 2, MaxPlayers: 4}}
	k1 := m.renderKey()
	m.entries[0].Players = 3
	assert.NotEqual(t, k1, m.renderKey(), "a seat filling up invalidates the memo")
}
