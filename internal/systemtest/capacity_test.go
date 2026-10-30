package systemtest

import (
	"fmt"
	"runtime"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"uuid"

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
	manager := lobby.NewManager(t.Context(), newRankedFinalizeRecorder(), catalog.NewRegistry())

	warm := openTable(t, manager, 0, seatsPerTable)
	require.NotNil(t, warm)

	before := heapInUse()
	held := make([]*lobby.Lobby, 0, tables)
	for i := 1; i <= tables; i++ {
		held = append(held, openTable(t, manager, i, seatsPerTable))
	}
	after := heapInUse()
	runtime.KeepAlive(held)

	perTable := float64(after-before) / float64(tables)
	t.Logf("measurement retained per %d-seat table in play: %.1f KiB (%.1f KiB per seat)",
		seatsPerTable, perTable/1024, perTable/float64(seatsPerTable)/1024)
	t.Logf("measurement %d tables held %.1f MiB of heap", tables, float64(after-before)/(1<<20))

	require.Less(t, perTable, float64(2<<20), "a table must not retain megabytes")
}

func openTable(t *testing.T, manager *lobby.Manager, idx, n int) *lobby.Lobby {
	t.Helper()
	leader := benchPlayer(idx, 0)
	l, err := manager.CreateLobby(leader,
		lobby.WithCardGame("poker"),
		lobby.WithMaxPlayers(9),
		lobby.WithPrivate(false),
	)
	require.NoError(t, err)
	t.Cleanup(func() { manager.RemoveLobby(l.Code()) })

	guests := make([]*game.Player, 0, n-1)
	for i := 1; i < n; i++ {
		g := benchPlayer(idx, i)
		require.NoError(t, joinErr(manager.JoinLobbyByCode(t.Context(), l.Code(), g)))
		guests = append(guests, g)
	}

	require.NoError(t, manager.ToggleReady(leader))
	for _, g := range guests {
		require.NoError(t, manager.ToggleReady(g))
	}
	return l
}

func benchPlayer(table, seat int) *game.Player {
	id := fmt.Sprintf("t%d-s%d", table, seat)
	return &game.Player{ID: id, UserID: testutil.UID(table*100 + seat + 1), Name: id}
}

// The capacity tables must have the shape real players give them: one account per
// seat and never uuid.Nil, which the lobby treats as "no account".
func TestBenchPlayer_GivesEverySeatItsOwnAccount(t *testing.T) {
	t.Parallel()
	seen := map[uuid.UUID]string{}
	for table := range 201 {
		for seat := range 6 {
			p := benchPlayer(table, seat)
			require.NotEqual(t, uuid.Nil(), p.UserID, p.ID)
			other, dup := seen[p.UserID]
			require.False(t, dup, "%s shares an account with %s", p.ID, other)
			seen[p.UserID] = p.ID
		}
	}
}
