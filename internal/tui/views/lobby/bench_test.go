package lobby

import (
	"fmt"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/require"
)

func benchGlobal(m *lobby.Manager) router.GlobalContext {
	return router.GlobalContext{
		User: testUser(99, "browser"), LobbyManager: m,
		GameRegistry: testRegistry(), Width: 120, Height: 40,
		Theme: styles.NewTheme(true),
	}
}

// The browser re-renders every two seconds for every player sitting on it, so this is
// a per-second cost across the whole server rather than a per-keypress one.
func BenchmarkJoinView_Render(b *testing.B) {
	m := lobby.NewManager(b.Context(), nil)
	for i := range 20 {
		leader := &game.Player{ID: fmt.Sprintf("h%d", i), UserID: testutil.UID(byte(i + 1)), Name: fmt.Sprintf("h%d", i)}
		_, err := m.CreateLobby(leader, lobby.WithPrivate(false), lobby.WithCardGame(testGameName))
		require.NoError(b, err)
	}
	view, ok := NewJoin(benchGlobal(m)).(*joinModel)
	require.True(b, ok)

	b.ReportAllocs()
	for b.Loop() {
		_ = view.View()
	}
}

// The in-lobby view redraws on every roster and settings event.
func BenchmarkLobbyView_Render(b *testing.B) {
	manager := lobby.NewManager(b.Context(), nil)
	seats := testutil.NamedPlayers("alice", "p2", "p3", "p4")
	l, err := manager.CreateLobby(seats[0], lobby.WithMaxPlayers(4), lobby.WithPrivate(false),
		lobby.WithCardGame(testGameName))
	require.NoError(b, err)
	for _, g := range seats[1:] {
		_, err := manager.JoinLobbyByCode(l.Code(), g)
		require.NoError(b, err)
	}

	global := benchGlobal(manager)
	global.User = testUser(1, "alice")
	view, ok := New(global, l).(*model)
	require.True(b, ok)
	b.Cleanup(view.Close)

	b.ReportAllocs()
	for b.Loop() {
		_ = view.View()
	}
}

func BenchmarkCreateView_Render(b *testing.B) {
	view, ok := NewCreate(benchGlobal(lobby.NewManager(b.Context(), nil))).(*createModel)
	require.True(b, ok)

	b.ReportAllocs()
	for b.Loop() {
		_ = view.View()
	}
}
