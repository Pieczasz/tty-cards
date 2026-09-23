// Package systemtest drives the server's real components end to end - catalog,
// registry, lobby manager, and game engine - through their public APIs only.
package systemtest

import (
	"context"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/lobby"

	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	pokerGame  = "Poker"
	maxActions = 60
)

func TestSystemRankedGameWithMidGameLeave(t *testing.T) {
	t.Parallel()

	repo := newRankedFinalizeRecorder()
	manager := lobby.NewManager(context.Background(), repo)
	registry := realRegistry(t)

	leader := newPlayer(testutil.UID(1), "alice")
	guests := []*game.Player{newPlayer(testutil.UID(2), "bob"), newPlayer(testutil.UID(3), "carol"), newPlayer(testutil.UID(4), "dave")}

	l, err := manager.CreateLobby(leader,
		lobby.WithCardGame(pokerGame),
		lobby.WithMaxPlayers(2),
		lobby.WithPrivate(true),
	)
	require.NoError(t, err)
	require.Equal(t, leader, l.Leader())

	require.NoError(t, l.SetMaxPlayers(leader, 4, 2, 9))
	require.NoError(t, l.SetPrivate(leader, false))
	require.NoError(t, l.SetRanked(leader, true))
	assert.Equal(t, 4, l.MaxPlayers())
	assert.False(t, l.IsPrivate())
	assert.True(t, l.IsRanked())

	require.Error(t, l.SetMaxPlayers(guests[0], 9, 2, 9), "only the leader may change settings")

	for _, g := range guests {
		require.NoError(t, joinErr(manager.JoinLobbyByCode(l.Code(), g)), "guest %s should join", g.ID)
	}
	require.Equal(t, 4, l.CurrentPlayers())
	assert.Contains(t, browseCodes(manager, leader), l.Code(), "a public waiting lobby is listed")

	events, subErr := l.Subscribe(leader.ID)
	require.NoError(t, subErr)
	t.Cleanup(func() { l.Unsubscribe(leader.ID, events) })

	all := append([]*game.Player{leader}, guests...)
	for _, p := range all {
		require.NoError(t, l.ToggleReady(p, registry))
	}

	engine := awaitGameStart(t, events)
	require.NotNil(t, engine)
	assert.NotNil(t, l.ActiveGame(), "a started lobby is in game")
	assert.NotContains(t, browseCodes(manager, leader), l.Code(),
		"and stops being offered to anyone browsing for a seat")

	startingChips := chipsInPlay(t, engine)
	require.Positive(t, startingChips)

	require.True(t, actOnce(engine), "the first player to act should have a legal move")

	leaver := guests[0]
	manager.LeaveLobby(leaver)
	assert.False(t, l.HasPlayer(leaver), "leaving removes the player from the lobby")
	assert.Equal(t, startingChips, chipsInPlay(t, engine),
		"a mid-hand disconnect must not create or destroy chips")

	playOutMatch(t, engine)

	assert.Equal(t, startingChips, chipsInPlay(t, engine), "chips survive the whole match")
	standings := standingIDs(engine)
	assert.NotEmpty(t, standings, "a finished hand ranks its players")
	assert.Contains(t, standings, leaver.ID, "a player who left still places")

	repo.awaitFinalize(t)
	require.True(t, manager.WaitForFinalizers(5*time.Second), "ranked writes should drain")

	finalized := repo.calls()
	require.Len(t, finalized, 1, "a ranked hand finalizes exactly once")
	assert.Equal(t, pokerGame, finalized[0].gameName)
	assert.Len(t, finalized[0].userIDs, len(all),
		"every player is ranked, including the one who left mid-hand")
}

func TestSystemLobbyRespectsGameBounds(t *testing.T) {
	t.Parallel()

	manager := lobby.NewManager(context.Background(), newRankedFinalizeRecorder())
	registry := realRegistry(t)

	leader := newPlayer(testutil.UID(1), "alice")
	l, err := manager.CreateLobby(leader,
		lobby.WithCardGame(pokerGame),
		lobby.WithMaxPlayers(2),
	)
	require.NoError(t, err)

	err = l.ToggleReady(leader, registry)
	require.ErrorContains(t, err, "at least 2 players")
	assert.True(t, l.IsReady(leader), "the ready flag still toggles")
	assert.Nil(t, l.ActiveGame(), "one ready player cannot start a two-player game")

	require.NoError(t, joinErr(manager.JoinLobbyByCode(l.Code(), newPlayer(testutil.UID(2), "bob"))))
	assert.Error(t, joinErr(manager.JoinLobbyByCode(l.Code(), newPlayer(testutil.UID(3), "carol"))),
		"a full lobby rejects further joins")
}

func TestSystemLeaderLeavingPromotesGuest(t *testing.T) {
	t.Parallel()

	manager := lobby.NewManager(context.Background(), newRankedFinalizeRecorder())
	leader := newPlayer(testutil.UID(1), "alice")
	guest := newPlayer(testutil.UID(2), "bob")

	l, err := manager.CreateLobby(leader, lobby.WithCardGame(pokerGame), lobby.WithMaxPlayers(4))
	require.NoError(t, err)
	require.NoError(t, joinErr(manager.JoinLobbyByCode(l.Code(), guest)))

	manager.LeaveLobby(leader)
	assert.Equal(t, guest, l.Leader(), "the remaining guest takes over")
	assert.True(t, l.IsLeader(guest))

	manager.LeaveLobby(guest)
	_, err = manager.FindLobbyByCode(l.Code())
	assert.Error(t, err, "an empty lobby is cleaned up")
}

func browseCodes(manager *lobby.Manager, p *game.Player) []string {
	entries := manager.BrowseLobbies(p, lobby.BrowseFilter{})
	codes := make([]string, 0, len(entries))
	for _, e := range entries {
		codes = append(codes, e.Code)
	}
	return codes
}

func standingIDs(engine *game.Engine) []string {
	standings, _ := engine.StandingsWithPlaces()
	ids := make([]string, len(standings))
	for i, p := range standings {
		ids[i] = p.ID
	}
	return ids
}
