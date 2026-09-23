//go:build integration

package systemtest

import (
	"context"
	"testing"
	"time"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/elo"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/repository"
	"github.com/Pieczasz/terminal-card/internal/testutil"

	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSystemRankedResultReachesLeaderboardAndProfile(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)

	ctx := context.Background()
	userRepo := repository.NewUserRepository(gormDB)
	matchRepo := newSignallingMatchRepo(repository.NewMatchRepository(gormDB))
	manager := lobby.NewManager(ctx, matchRepo)
	registry := realRegistry(t)

	players := make([]*game.Player, 0, 3)
	for _, name := range []string{"alice", "bob", "carol"} {
		user, _, err := userRepo.RegisterUserWithKey(ctx, name, "fingerprint-"+name)
		require.NoError(t, err)
		players = append(players, lobby.NewPlayer(user))
	}
	leader := players[0]

	l, err := manager.New(leader,
		lobby.WithCardGame(pokerGame),
		lobby.WithMaxPlayers(3),
		lobby.WithRanked(true),
	)
	require.NoError(t, err)
	for _, g := range players[1:] {
		require.NoError(t, joinErr(manager.JoinLobbyByCode(l.Code(), g)))
	}

	events, subErr := l.Subscribe(leader.ID)
	require.NoError(t, subErr)
	t.Cleanup(func() { l.Unsubscribe(leader.ID, events) })

	for _, p := range players {
		require.NoError(t, l.ToggleReady(p, registry))
	}
	engine := awaitGameStart(t, events)

	playOutMatch(t, engine)

	matchRepo.awaitFinalize(t)
	require.True(t, manager.WaitForFinalizers(30*time.Second), "ranked write must drain")

	best, err := userRepo.BestPlayers(ctx, "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, best, "a finished ranked game must populate the leaderboard")

	var moved bool
	for _, r := range best {
		if r.Elo != uint32(elo.DefaultRating) {
			moved = true
		}
	}
	assert.True(t, moved, "at least one player's Elo must change after a ranked game")

	for _, p := range players {
		profile, err := userRepo.UserProfile(ctx, p.UserID)
		require.NoError(t, err)
		assert.NotEmpty(t, profile.Rankings, "%s should have a ranking row", profile.Username)

		history, err := userRepo.UserMatchHistory(ctx, p.UserID, 10)
		require.NoError(t, err)
		require.Len(t, history, 1, "%s should have exactly one recorded match", profile.Username)
		assert.Positive(t, history[0].Placement, "placement is 1-based")
		assert.Equal(t, pokerGame, history[0].Match.Game.Name)
		assert.True(t, history[0].Match.Ranked, "history must remember this was a rated game")
	}
}

// A casual lobby records the result in match history but must not touch Elo:
// players still want to see what they played, ratings stay for ranked lobbies.
func TestSystemCasualGameRecordsHistoryWithoutElo(t *testing.T) {
	t.Parallel()
	gormDB := testutil.SetupTestDB(t)

	ctx := context.Background()
	userRepo := repository.NewUserRepository(gormDB)
	matchRepo := repository.NewMatchRepository(gormDB)
	manager := lobby.NewManager(ctx, matchRepo)
	registry := realRegistry(t)

	players := make([]*game.Player, 0, 2)
	for _, name := range []string{"dave", "erin"} {
		user, _, err := userRepo.RegisterUserWithKey(ctx, name, "fingerprint-"+name)
		require.NoError(t, err)
		players = append(players, lobby.NewPlayer(user))
	}

	l, err := manager.New(players[0],
		lobby.WithCardGame(pokerGame),
		lobby.WithMaxPlayers(2),
		lobby.WithRanked(false),
	)
	require.NoError(t, err)
	require.NoError(t, joinErr(manager.JoinLobbyByCode(l.Code(), players[1])))

	events, subErr := l.Subscribe(players[0].ID)
	require.NoError(t, subErr)
	t.Cleanup(func() { l.Unsubscribe(players[0].ID, events) })
	for _, p := range players {
		require.NoError(t, l.ToggleReady(p, registry))
	}
	engine := awaitGameStart(t, events)

	playOutMatch(t, engine)

	// The write lands on the lobby's watcher goroutine, so wait for the history row
	// to appear rather than assuming it is already there.
	for _, p := range players {
		userID := p.UserID
		require.Eventually(t, func() bool {
			history, err := userRepo.UserMatchHistory(ctx, userID, 10)
			return err == nil && len(history) == 1
		}, 10*time.Second, 100*time.Millisecond, "a casual game belongs in match history")

		history, err := userRepo.UserMatchHistory(ctx, userID, 10)
		require.NoError(t, err)
		assert.Positive(t, history[0].Placement, "placement is 1-based")
		assert.Equal(t, pokerGame, history[0].Match.Game.Name)
		assert.False(t, history[0].Match.Ranked, "the profile shows this row as a casual game")
		assert.Zero(t, history[0].EloDelta, "a casual result must not move Elo")

		profile, err := userRepo.UserProfile(ctx, userID)
		require.NoError(t, err)
		assert.Empty(t, profile.Rankings, "a casual game must not create a ranking row")
	}
}

// signallingMatchRepo wraps the real repository and announces each completed ranked
// write, so the test waits on the write itself instead of polling the database.
// The embedded interface supplies every other method unchanged.
type signallingMatchRepo struct {
	db.MatchRepository
	finalizeSignal
}

func newSignallingMatchRepo(inner db.MatchRepository) *signallingMatchRepo {
	return &signallingMatchRepo{MatchRepository: inner, finalizeSignal: newFinalizeSignal()}
}

func (s *signallingMatchRepo) FinalizeRankedMatch(
	ctx context.Context, ref db.GameRef, orderedUserIDs []uuid.UUID, places []int,
) error {
	err := s.MatchRepository.FinalizeRankedMatch(ctx, ref, orderedUserIDs, places)
	s.fire()
	return err
}
