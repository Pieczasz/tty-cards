package catalog

import (
	"slices"
	"testing"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/testutil"
	"github.com/Pieczasz/tty-cards/internal/tui/router"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type bogusAction struct{}

func (bogusAction) Name() string { return "bogus" }

// A dashboard that groups by game_type must see one series per game. The lobby and
// the view record different metrics for the same table, and they used to spell the
// game three ways between them. Both now label it with the catalog slug.
//
//nolint:paralleltest // reads the process-wide meter provider TestMain installed
func TestGames_LobbyAndViewShareOneGameTypeLabel(t *testing.T) {
	for _, entry := range Games {
		t.Run(entry.Slug, func(t *testing.T) {
			m := lobby.NewManager(t.Context(), nil, NewRegistry())
			players := testutil.Players(entry.MinPlayers)
			l, err := m.CreateLobby(players[0], lobby.WithCardGame(entry.Slug), lobby.WithMaxPlayers(len(players)))
			require.NoError(t, err)
			t.Cleanup(func() { m.RemoveLobby(l.Code()) })
			for _, p := range players[1:] {
				_, err := m.JoinLobbyByCode(t.Context(), l.Code(), p)
				require.NoError(t, err)
			}
			for _, p := range players {
				require.NoError(t, m.ToggleReady(p))
			}
			seat := l.Seat(players[0].ID)
			require.NotNil(t, seat, "the game did not start")

			global := router.GlobalContext{Player: lobby.NewPlayer(&db.User{ID: players[0].UserID, Username: players[0].Name})}
			view := entry.View(global, seat, entry.Slug)
			t.Cleanup(view.(router.Closer).Close)
			submitter, ok := view.(interface{ Submit(game.Action) error })
			require.True(t, ok, "every game view embeds gameview.Session")
			require.Error(t, submitter.Submit(bogusAction{}), "the rules have to refuse a move they do not know")
		})
	}

	var rm metricdata.ResourceMetrics
	require.NoError(t, testMetrics.Collect(t.Context(), &rm))
	want := make([]string, 0, len(Games))
	for _, e := range Games {
		want = append(want, e.Slug)
	}
	slices.Sort(want)
	require.Equal(t, want, gameTypes(rm, "ttycards.games.started"), "the lobby's label")
	require.Equal(t, want, gameTypes(rm, "ttycards.game.action.rejected"), "the view's label")
}

// gameTypes is the sorted set of game_type values recorded on the named counter.
func gameTypes(rm metricdata.ResourceMetrics, name string) []string {
	var out []string
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			sum, ok := mt.Data.(metricdata.Sum[int64])
			if mt.Name != name || !ok {
				continue
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value(attribute.Key("game_type")); ok && !slices.Contains(out, v.AsString()) {
					out = append(out, v.AsString())
				}
			}
		}
	}
	slices.Sort(out)
	return out
}
