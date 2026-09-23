package lobby

import (
	"context"
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
	m := lobby.NewManager(context.Background(), nil)
	for i := range 20 {
		leader := &game.Player{ID: fmt.Sprintf("h%d", i), UserID: testutil.UID(byte(i + 1)), Name: fmt.Sprintf("h%d", i)}
		_, err := m.New(leader, lobby.WithPrivate(false), lobby.WithCardGame(testGameName))
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
	manager := lobby.NewManager(context.Background(), nil)
	leader := &game.Player{ID: testutil.SeatID(1), UserID: testutil.UID(1), Name: "alice"}
	l, err := manager.New(leader, lobby.WithMaxPlayers(4), lobby.WithPrivate(false),
		lobby.WithCardGame(testGameName))
	require.NoError(b, err)
	for i := 2; i <= 4; i++ {
		g := &game.Player{ID: testutil.SeatID(i), UserID: testutil.UID(i), Name: fmt.Sprintf("p%d", i)}
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
	view, ok := NewCreate(benchGlobal(lobby.NewManager(context.Background(), nil))).(*createModel)
	require.True(b, ok)

	b.ReportAllocs()
	for b.Loop() {
		_ = view.View()
	}
}
