package crazyeight

import (
	"fmt"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/crazyeight"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/views"

	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/stretchr/testify/require"
)

// benchTable seats n players and returns the first player's view. Rendering is the
// per-frame cost every client pays, so it is the number that decides table capacity.
func benchTable(b *testing.B, n int) *model {
	b.Helper()
	engine := game.NewEngine(&logic.Rules{}, testutil.Players(n), deck.Standard())
	require.NoError(b, engine.Start())
	b.Cleanup(engine.Close)

	global := router.GlobalContext{
		User:  &db.User{ID: testutil.UID(1), Username: "p1"},
		Width: 120, Height: 40, Theme: styles.NewTheme(true),
	}
	m, ok := New(global, game.Bind(engine, views.SessionPlayerID(global)), "crazy_eights").(*model)
	require.True(b, ok)
	b.Cleanup(m.Close)
	return m
}

func BenchmarkCrazyEightView_Render(b *testing.B) {
	for _, seats := range []int{2, 4} {
		b.Run(fmt.Sprintf("seats=%d", seats), func(b *testing.B) {
			m := benchTable(b, seats)
			b.ReportAllocs()
			for b.Loop() {
				_ = m.View()
			}
		})
	}
}
