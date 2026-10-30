package ssh

import (
	"sync"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/catalog"
	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	"github.com/stretchr/testify/require"
)

// startedTable seats host and guest at a two-player match that is already playing,
// which is the only state in which a dropped session keeps its seat on a grace timer.
func startedTable(t *testing.T, manager *lobby.Manager, host, guest *game.Player) *lobby.Lobby {
	t.Helper()
	table, err := manager.CreateLobby(host, lobby.WithCardGame(catalog.Games[0].Slug), lobby.WithMaxPlayers(2))
	require.NoError(t, err)
	_, err = manager.JoinLobbyByCode(t.Context(), table.Code(), guest)
	require.NoError(t, err)
	require.NoError(t, manager.ToggleReady(host))
	require.NoError(t, manager.ToggleReady(guest))
	require.NotNil(t, table.ActiveGame())
	return table
}

// A reconnect racing the old session's teardown must never end with a grace timer
// armed on the seat the new session holds: when it fires, 90 seconds later, the
// player who came back forfeits the match they are playing. The teardown that saw
// itself as owner used to disarm nothing and arm the timer after the reconnect had
// already resumed.
func TestReleaseSession_RacingReconnectNeverLeavesTheSeatOnATimer(t *testing.T) {
	t.Parallel()

	for range 200 {
		manager := lobby.NewManager(t.Context(), nil, catalog.NewRegistry())
		user := &db.User{ID: testutil.UID(7), Username: "comeback"}
		player := lobby.NewPlayer(user)
		host := lobby.NewPlayer(&db.User{ID: testutil.UID(8), Username: "host"})
		table := startedTable(t, manager, host, player)

		tracker := NewSessionTracker(0)
		oldGen, err := tracker.Connect(user.ID, nil, nil)
		require.NoError(t, err)
		reg := &sessionRegistry{}
		old := &stubSession{addr: stubAddr{"10.0.0.1:1"}}
		reg.store(old, &sessionState{user: user, gen: oldGen})

		deps := newSessionDeps(t, stubUserRepo{user: user})
		deps.LobbyManager = manager
		deps.Tracker = tracker
		fresh := &stubSession{addr: stubAddr{"10.0.0.1:2"}, pubKey: testPublicKey(t)}
		freshState := &sessionState{traceCtx: t.Context()}
		reg.store(fresh, freshState)
		newModel := sessionModel(deps, reg, testBudget(100, 100, 100))

		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Go(func() { <-start; reg.releaseSession(old, deps) })
		wg.Go(func() { <-start; newModel(fresh) })
		close(start)
		wg.Wait()
		require.NotNil(t, freshState.model, "the reconnect was refused")
		freshState.model.Close()

		// BeginShutdown gives up every seat still held on a grace timer, so a seat that
		// survives it had none armed.
		manager.BeginShutdown()
		require.True(t, table.HasPlayer(player), "the reconnected player's seat was left on a grace timer")
		manager.LeaveLobby(player)
		manager.LeaveLobby(host)
	}
}

// A session the server refuses never becomes the player's, so it must not touch the
// seat held for them: cancelling the grace timer there left the seat held with no
// session and no timer, until the engine's idle removal took it.
func TestSessionModel_ARefusedReconnectLeavesTheGraceTimerArmed(t *testing.T) {
	t.Parallel()

	manager := lobby.NewManager(t.Context(), nil, catalog.NewRegistry())
	user := &db.User{ID: testutil.UID(9), Username: "refused"}
	player := lobby.NewPlayer(user)
	host := lobby.NewPlayer(&db.User{ID: testutil.UID(10), Username: "host"})
	table := startedTable(t, manager, host, player)
	manager.DisconnectPlayer(t.Context(), player)

	deps := newSessionDeps(t, stubUserRepo{user: user})
	deps.LobbyManager = manager
	s := &stubSession{addr: stubAddr{"10.0.0.2:1"}, pubKey: testPublicKey(t)}
	deps.Tracker = fullTracker(t)
	reg := &sessionRegistry{}
	reg.store(s, &sessionState{traceCtx: t.Context()})

	model := sessionModel(deps, reg, testBudget(100, 100, 100))(s)
	require.Nil(t, model, "the full server admitted the session")

	// BeginShutdown gives up every seat still on a grace timer.
	manager.BeginShutdown()
	require.False(t, table.HasPlayer(player), "the refused session cancelled the grace timer")
	manager.LeaveLobby(host)
}
