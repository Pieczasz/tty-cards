package catalog

import (
	"slices"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/Pieczasz/terminal-card/internal/tui/router"

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
func TestAll_LobbyAndViewShareOneGameTypeLabel(t *testing.T) {
	registry := NewRegistry()
	for _, entry := range All {
		t.Run(entry.Slug, func(t *testing.T) {
			m := lobby.NewManager(t.Context(), nil)
			players := testutil.Players(entry.Factory().MinPlayers())
			l, err := m.CreateLobby(players[0], lobby.WithCardGame(entry.Name), lobby.WithMaxPlayers(len(players)))
			require.NoError(t, err)
			t.Cleanup(func() { m.RemoveLobby(l.Code()) })
			for _, p := range players[1:] {
				_, err := m.JoinLobbyByCode(l.Code(), p)
				require.NoError(t, err)
			}
			for _, p := range players {
				require.NoError(t, l.ToggleReady(p, registry))
			}
			engine := l.ActiveGame()
			require.NotNil(t, engine, "the game did not start")

			global := router.GlobalContext{User: &db.User{ID: players[0].UserID, Username: players[0].Name}}
			view := entry.View(global, engine, entry.Slug)
			t.Cleanup(view.(router.Closer).Close)
			submitter, ok := view.(interface{ Submit(game.Action) error })
			require.True(t, ok, "every game view embeds gameview.Session")
			require.Error(t, submitter.Submit(bogusAction{}), "the rules have to refuse a move they do not know")
		})
	}

	var rm metricdata.ResourceMetrics
	require.NoError(t, testMetrics.Collect(t.Context(), &rm))
	want := make([]string, 0, len(All))
	for _, e := range All {
		want = append(want, e.Slug)
	}
	slices.Sort(want)
	require.Equal(t, want, gameTypes(rm, "terminalcard.games.started"), "the lobby's label")
	require.Equal(t, want, gameTypes(rm, "terminalcard.game.action.rejected"), "the view's label")
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
