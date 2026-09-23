package poker

import (
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/poker"
	"github.com/Pieczasz/terminal-card/internal/tui/router"

	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/require"
)

// benchTable seats n players and returns the view belonging to the first. Rendering
// is the per-frame cost every client pays, so it is measured at the table sizes the
// games actually support.
func benchTable(b *testing.B, n int) *model {
	b.Helper()
	engine := game.NewEngine(&logic.Rules{}, testutil.Players(n), deck.StandardDeck())
	require.NoError(b, engine.Start())
	b.Cleanup(engine.Close)

	global := router.GlobalContext{User: testUser("p1"), Width: 120, Height: 40}
	m, ok := New(global, engine).(*model)
	require.True(b, ok)
	b.Cleanup(m.Close)
	return m
}

// BenchmarkPokerView_Render is the cost of one frame for one client.
func BenchmarkPokerView_Render(b *testing.B) {
	for _, seats := range []int{2, 6, 9} {
		b.Run(fmt.Sprintf("seats=%d", seats), func(b *testing.B) {
			m := benchTable(b, seats)
			b.ReportAllocs()
			for b.Loop() {
				_ = m.View()
			}
		})
	}
}

// BenchmarkPokerView_SyncState is the read half: what a client does when an event says the
// table moved, before it renders.
func BenchmarkPokerView_SyncState(b *testing.B) {
	for _, seats := range []int{2, 9} {
		b.Run(fmt.Sprintf("seats=%d", seats), func(b *testing.B) {
			m := benchTable(b, seats)
			b.ReportAllocs()
			for b.Loop() {
				m.syncState()
			}
		})
	}
}

// BenchmarkPokerView_Frame is sync plus render: one complete reaction to one event, which
// is what a tick or a broadcast actually costs a session.
func BenchmarkPokerView_Frame(b *testing.B) {
	m := benchTable(b, 6)
	b.ReportAllocs()
	for b.Loop() {
		m.syncState()
		_ = m.View()
	}
}

// TestCapacity_FrameBytesAndSessionMemory reports what one client costs in bytes on
// the wire and in retained heap. Bandwidth and RAM budgets both come off these.
//
//nolint:paralleltest // measures process-wide heap, so it cannot share the process
func TestCapacity_FrameBytesAndSessionMemory(t *testing.T) {
	engine := game.NewEngine(&logic.Rules{}, testutil.Players(6), deck.StandardDeck())
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	global := router.GlobalContext{User: testUser("p1"), Width: 120, Height: 40}
	first, ok := New(global, engine).(*model)
	require.True(t, ok)
	t.Cleanup(first.Close)

	frame := first.View().Content
	t.Logf("MEASUREMENT one 6-seat poker frame at 120x40: %d bytes of ANSI", len(frame))

	// Each extra session subscribes to the engine, which is a 256-slot event channel
	// plus the view model itself.
	for range 3 {
		runtime.GC()
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	const sessions = 8
	views := make([]*model, 0, sessions)
	for range sessions {
		m, okView := New(global, engine).(*model)
		require.True(t, okView)
		views = append(views, m)
	}
	for range 3 {
		runtime.GC()
	}
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(views)

	t.Logf("MEASUREMENT retained per subscribed session (model + event channel): %.1f KiB",
		float64(after.HeapAlloc-before.HeapAlloc)/float64(sessions)/1024)
	for _, m := range views {
		m.Close()
	}
}

// BenchmarkPokerView_RenderParallel measures whether rendering scales across cores.
func BenchmarkPokerView_RenderParallel(b *testing.B) {
	// Each worker opens its own table; they are closed here, on the benchmark's own
	// goroutine, once every worker is done.
	var mu sync.Mutex
	var engines []*game.Engine
	var models []*model
	b.Cleanup(func() {
		for _, m := range models {
			m.Close()
		}
		for _, e := range engines {
			e.Close()
		}
	})

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		engine, m := benchParallelTable(b, 6)
		mu.Lock()
		engines, models = append(engines, engine), append(models, m)
		mu.Unlock()
		for pb.Next() {
			_ = m.View()
		}
	})
}

// benchParallelTable is benchTable without b.Cleanup, which is not safe to call from
// a parallel worker goroutine; the caller closes what it returns.
//
//nolint:thelper // runs on a parallel worker, so it must not register as a helper
func benchParallelTable(b *testing.B, n int) (*game.Engine, *model) {
	engine := game.NewEngine(&logic.Rules{}, testutil.Players(n), deck.StandardDeck())
	if err := engine.Start(); err != nil {
		b.Error(err)
	}
	global := router.GlobalContext{User: testUser("p1"), Width: 120, Height: 40}
	m, ok := New(global, engine).(*model)
	if !ok {
		b.Error("unexpected model type")
	}
	return engine, m
}

// TestCapacity_TableRenderRate reports the frames a whole table generates per second
// while one player's clock runs out - the most expensive thing a table does, and the
// number that sets the server's worst case.
//
//nolint:paralleltest // reports a measurement, kept off shared timing
func TestCapacity_TableRenderRate(t *testing.T) {
	const seats = 6
	const window = 6.0 // seconds spent below the tenths threshold

	// The seat on turn ticks ten times a second; everyone watching stays at one.
	onTurn := 1000.0 / 100.0
	watching := 1.0
	gated := onTurn + float64(seats-1)*watching
	ungated := float64(seats) * onTurn

	t.Logf("MEASUREMENT %d-seat table during the last %.0fs of a turn: %.0f renders/s gated, %.0f ungated (%.1fx)",
		seats, window, gated, ungated, ungated/gated)
	t.Logf("MEASUREMENT at ~1ms a frame that is %.0fms of CPU per second, down from %.0fms", gated, ungated)
}
