package systemtest

import (
	"context"
	"fmt"
	"runtime"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/lobby"

	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/require"
)

func heapInUse() uint64 {
	for range 3 {
		runtime.GC()
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

// Reads the heap, so it must have the process to itself.
//
//nolint:paralleltest // a shared heap measurement cannot run alongside other tests
func TestCapacity_MemoryPerTable(t *testing.T) {
	// A heap measurement is a sizing exercise, not a gate: it needs the process to
	// itself, it allocates 200 live tables, and its answer moves with the allocator.
	// -short is what CI runs on every push, and this belongs nowhere near it.
	if testing.Short() {
		t.Skip("heap measurement; run without -short")
	}

	const tables = 200
	const seatsPerTable = 6

	// A real repository, not nil: a nil db.MatchRepository is a shape the server can
	// never have, so measuring against one measures something that does not exist -
	// and any path that starts touching it turns this into a nil dereference.
	manager := lobby.NewManager(context.Background(), newRankedFinalizeRecorder())
	registry := realRegistry(t)

	warm := openTable(t, manager, registry, 0, seatsPerTable)
	require.NotNil(t, warm)

	before := heapInUse()
	held := make([]*lobby.Lobby, 0, tables)
	for i := 1; i <= tables; i++ {
		held = append(held, openTable(t, manager, registry, i, seatsPerTable))
	}
	after := heapInUse()
	runtime.KeepAlive(held)

	perTable := float64(after-before) / float64(tables)
	t.Logf("measurement retained per %d-seat table in play: %.1f KiB (%.1f KiB per seat)",
		seatsPerTable, perTable/1024, perTable/float64(seatsPerTable)/1024)
	t.Logf("measurement %d tables held %.1f MiB of heap", tables, float64(after-before)/(1<<20))

	require.Less(t, perTable, float64(2<<20), "a table must not retain megabytes")
}

func openTable(t *testing.T, manager *lobby.Manager, registry *game.Registry, idx, n int) *lobby.Lobby {
	t.Helper()
	leader := benchPlayer(idx, 0)
	l, err := manager.CreateLobby(leader,
		lobby.WithCardGame("Poker"),
		lobby.WithMaxPlayers(9),
		lobby.WithPrivate(false),
	)
	require.NoError(t, err)
	t.Cleanup(func() { manager.RemoveLobby(l.Code()) })

	guests := make([]*game.Player, 0, n-1)
	for i := 1; i < n; i++ {
		g := benchPlayer(idx, i)
		require.NoError(t, joinErr(manager.JoinLobbyByCode(l.Code(), g)))
		guests = append(guests, g)
	}

	require.NoError(t, l.ToggleReady(leader, registry))
	for _, g := range guests {
		require.NoError(t, l.ToggleReady(g, registry))
	}
	return l
}

func benchPlayer(table, seat int) *game.Player {
	id := fmt.Sprintf("t%d-s%d", table, seat)
	return &game.Player{ID: id, UserID: testutil.UID(byte(table*100 + seat + 1)), Name: id}
}
