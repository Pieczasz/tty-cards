package leaderboard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/catalog"
	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
	"github.com/Pieczasz/terminal-card/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rankings(n int) []db.Ranking {
	out := make([]db.Ranking, 0, n)
	for i := range n {
		out = append(out, db.Ranking{
			UserID: testutil.UID(byte(i + 1)),
			Elo:    uint32(2000 - i),
			User:   db.User{ID: testutil.UID(byte(i + 1)), Username: fmt.Sprintf("player%02d", i+1)},
			Game:   db.Game{Name: "Poker"},
		})
	}
	return out
}

func board(t *testing.T, n int) model {
	t.Helper()
	return model{
		global:   router.GlobalContext{Theme: styles.NewTheme(true), Width: 100, Height: 40},
		rankings: rankings(n),
		filters:  []boardFilter{{label: filterAll}, {label: "Poker", slug: "poker"}, {label: "Uno", slug: "uno"}},
	}
}

// boardRows is the page size board() produces, so tests page by what it draws
// instead of assuming the cap.
func boardRows(t *testing.T) int {
	t.Helper()
	return board(t, 0).rowsPerPage()
}

func TestCycleFilter_AdvancesAndClearsRows(t *testing.T) {
	t.Parallel()
	m := board(t, boardRows(t))
	m.filterIndex = 0

	next, cmd := m.cycleFilter(1)
	nm := next.(model)
	assert.Equal(t, 1, nm.filterIndex)
	assert.Equal(t, "Poker", nm.filters[nm.filterIndex].label)
	assert.Nil(t, nm.rankings, "stale rows must not linger under a new filter")
	assert.Equal(t, 0, nm.page)
	assert.NotNil(t, cmd, "a filter change reloads from the repository")
	assert.Equal(t, "poker", nm.gameFilter())
}

func TestGoPage_StaysInsideLoadedPages(t *testing.T) {
	t.Parallel()
	m := board(t, boardRows(t)*2+3) // 3 pages, last short

	next, cmd := m.goPage(1)
	require.Nil(t, cmd, "page 2 is already loaded")
	nm := next.(model)
	assert.Equal(t, 1, nm.page)

	next, cmd = nm.goPage(1)
	require.Nil(t, cmd)
	nm = next.(model)
	assert.Equal(t, 2, nm.page)

	next, cmd = nm.goPage(1)
	assert.NotNil(t, cmd, "one more page past a short tail still probes the repository")
	assert.Equal(t, 2, next.(model).page, "page only advances after the fetch lands")
}

func TestGoPage_FetchesWhenTheNextPageIsMissing(t *testing.T) {
	t.Parallel()
	rows := boardRows(t)
	m := board(t, rows) // only page 1 loaded

	next, cmd := m.goPage(1)
	nm := next.(model)
	assert.True(t, nm.loading)
	assert.NotNil(t, cmd, "moving past the loaded window must request more rows")
	assert.Equal(t, rows*2, nm.needsFetch(1))
}

func TestGoPage_DoesNotRefetchAShortLastPage(t *testing.T) {
	t.Parallel()
	m := board(t, boardRows(t)*2+3) // pages 0-2 already held; last page short
	m.page = 1

	next, cmd := m.goPage(1)
	require.Nil(t, cmd, "short last page is already on hand")
	nm := next.(model)
	assert.Equal(t, 2, nm.page)

	_, cmd = nm.goPage(1)
	assert.NotNil(t, cmd, "past the held window still asks once in case more exist")
}

func TestGoPage_StopsWhenExhausted(t *testing.T) {
	t.Parallel()
	m := board(t, boardRows(t))
	m.exhausted = true

	next, cmd := m.goPage(1)
	require.Nil(t, cmd, "an exhausted feed must not re-query")
	assert.Equal(t, 0, next.(model).page)
}

// A page shows exactly rowsPerPage ranks and no more: a window that drew fewer than
// it paged by would skip the difference on every page turn.
func TestRenderRankings_DrawsExactlyOnePage(t *testing.T) {
	t.Parallel()
	rows := boardRows(t)
	m := board(t, rows+5)
	m.page = 0

	out := stripANSI(m.renderRankings(80))
	assert.Contains(t, out, "player01")
	assert.Contains(t, out, fmt.Sprintf("player%02d", rows))
	assert.NotContains(t, out, fmt.Sprintf("player%02d", rows+1),
		"page 1 must not spill into page 2")
	assert.Contains(t, out, "page 1/")

	m.page = 1
	out = stripANSI(m.renderRankings(80))
	assert.Contains(t, out, fmt.Sprintf("player%02d", rows+1))
	assert.NotContains(t, out, "player01")
	assert.Contains(t, out, "page 2/")
}

func TestNeedsFetch_CapsAtMax(t *testing.T) {
	t.Parallel()
	m := board(t, maxLeaderboardPlayers)
	assert.Equal(t, 0, m.needsFetch(maxLeaderboardPlayers/maxRowsPerPage),
		"a full window does not ask the repository again")
}

func stripANSI(s string) string {
	var out strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape && (r == 'm' || r == 'K' || r == 'H'):
			inEscape = false
		case !inEscape:
			out.WriteRune(r)
		}
	}
	return out.String()
}

// The board is a full-screen view, so it has to fit the screen. It used to force
// twenty rows plus chrome at every size, which overran a stock 80x24 terminal - and
// TooSmall reports 80x24 as perfectly fine, so nothing anywhere caught it.
func TestView_FitsTheTerminalAtEverySupportedSize(t *testing.T) {
	t.Parallel()
	for _, size := range []struct {
		name string
		w, h int
	}{
		{"the declared minimum", styles.MinWidth, styles.MinHeight},
		{"a stock terminal", 80, 24},
		{"a tall terminal", 120, 50},
	} {
		t.Run(size.name, func(t *testing.T) {
			t.Parallel()
			m := model{
				global:   router.GlobalContext{Theme: styles.NewTheme(true), Width: size.w, Height: size.h},
				rankings: rankings(maxLeaderboardPlayers),
				filters:  []boardFilter{{label: filterAll}, {label: "Poker", slug: "poker"}},
			}

			out := m.View().Content

			assert.LessOrEqual(t, lg.Height(out), size.h, "the board is taller than the terminal")
			assert.LessOrEqual(t, lg.Width(out), size.w, "the board is wider than the terminal")
		})
	}
}

// Paging must move by exactly what is drawn: a page that steps twenty while showing
// six silently skips fourteen players.
func TestRowsPerPage_PagesByWhatItDraws(t *testing.T) {
	t.Parallel()
	short := model{
		global:   router.GlobalContext{Theme: styles.NewTheme(true), Width: 80, Height: 24},
		rankings: rankings(maxLeaderboardPlayers),
		filters:  []boardFilter{{label: filterAll}},
	}
	tall := short
	tall.global.Height = 50

	shortRows, tallRows := short.rowsPerPage(), tall.rowsPerPage()
	assert.Less(t, shortRows, maxRowsPerPage, "a stock terminal cannot hold a full page")
	assert.GreaterOrEqual(t, shortRows, minRowsPerPage)
	assert.Equal(t, maxRowsPerPage, tallRows, "a tall terminal still gets the full page")

	// Page 2 starts where page 1 stopped drawing, at both sizes.
	next, _ := short.goPage(1)
	assert.Equal(t, 1, next.(model).page)
	assert.Contains(t, next.(model).renderRankings(styles.InnerWidth(80)),
		fmt.Sprintf("ranks %d-%d", shortRows+1, shortRows*2))
}

// fakeUsers stands in for the repository. Only BestPlayers is reachable from this
// view; the embedded interface makes any other call a loud nil panic rather than a
// quietly passing test.
type fakeUsers struct {
	db.UserRepository
	best func(ctx context.Context, limit int, gameName string) ([]db.Ranking, error)
}

func (f fakeUsers) BestPlayers(ctx context.Context, gameName string, limit int) ([]db.Ranking, error) {
	return f.best(ctx, limit, gameName)
}

// A game cannot be played that is not in the catalog, so the filter list is derived
// from it: a game added to the catalog without a filter is unreachable on the board.
func TestNew_BuildsAFilterPerCatalogGame(t *testing.T) {
	t.Parallel()

	m, ok := New(router.GlobalContext{}).(model)
	require.True(t, ok)

	require.Len(t, m.filters, 1+len(catalog.All))
	assert.Equal(t, filterAll, m.filters[0].label, "the unfiltered view is the default")
	for i, e := range catalog.All {
		assert.Equal(t, e.Name, m.filters[i+1].label)
		assert.Equal(t, e.Slug, m.filters[i+1].slug)
	}
	assert.Empty(t, m.gameFilter(), "index 0 means every game, which is the empty slug")
}

// Init has to ask for a whole page: it runs before the first WindowSizeMsg, so
// rowsPerPage would compute from a zero-sized terminal.
func TestInit_AsksForAFullPageOfEveryGame(t *testing.T) {
	t.Parallel()

	var gotLimit int
	var gotGame string
	m := New(router.GlobalContext{UserRepository: fakeUsers{
		best: func(_ context.Context, limit int, gameName string) ([]db.Ranking, error) {
			gotLimit, gotGame = limit, gameName
			return rankings(3), nil
		},
	}})

	cmd := m.Init()
	require.NotNil(t, cmd)
	msg, ok := cmd().(loadedMsg)
	require.True(t, ok)

	assert.Equal(t, maxRowsPerPage, gotLimit)
	assert.Empty(t, gotGame, "the default filter is every game")
	assert.Equal(t, 0, msg.wantPage)
	assert.Len(t, msg.rankings, 3)
	assert.NoError(t, msg.err)
}

// Pressing the filter key twice issues two queries, and the first one can be the
// slower. Without the gameName on the response, the stale rows would be painted
// under the newer filter's heading - a Poker board labelled Uno.
func TestUpdate_DiscardsAResponseForAFilterAlreadyCycledPast(t *testing.T) {
	t.Parallel()

	m := board(t, 0)
	m.filterIndex = 0
	m.global.UserRepository = fakeUsers{
		best: func(context.Context, int, string) ([]db.Ranking, error) { return nil, nil },
	}

	press := func(m model) model {
		next, _ := m.Update(tea.KeyPressMsg{Code: 'g', Text: "g"})
		return next.(model)
	}
	m = press(m) // Poker
	require.Equal(t, "poker", m.gameFilter())
	m = press(m) // Uno, before Poker has answered
	require.Equal(t, "uno", m.gameFilter())

	uno := rankings(2)
	uno[0].User.Username = "uno_player"
	poker := rankings(5)
	poker[0].User.Username = "poker_player"

	// The current filter's answer lands first...
	next, _ := m.Update(loadedMsg{rankings: uno, gameSlug: "uno"})
	m = next.(model)
	require.Len(t, m.rankings, 2)

	// ...and the abandoned one arrives afterwards.
	next, cmd := m.Update(loadedMsg{rankings: poker, gameSlug: "poker"})
	m = next.(model)

	assert.Nil(t, cmd)
	require.Len(t, m.rankings, 2, "the stale filter's rows must not replace the current ones")
	assert.Equal(t, "uno_player", m.rankings[0].User.Username)
	assert.False(t, m.loading, "the live response already cleared the spinner")
}

func TestUpdate_Loaded(t *testing.T) {
	t.Parallel()

	t.Run("a short page means there is nothing left to fetch", func(t *testing.T) {
		t.Parallel()
		m := board(t, 0)
		m.loading = true

		next, _ := m.Update(loadedMsg{rankings: rankings(3), limit: maxRowsPerPage})
		nm := next.(model)

		assert.False(t, nm.loading)
		assert.True(t, nm.exhausted, "fewer rows than asked for is the end of the feed")
		assert.Equal(t, 0, nm.page)
	})

	t.Run("the hard cap also exhausts the feed", func(t *testing.T) {
		t.Parallel()
		m := board(t, 0)

		next, _ := m.Update(loadedMsg{rankings: rankings(maxLeaderboardPlayers)})

		assert.True(t, next.(model).exhausted, "pagination must stop at the cap")
	})

	// A page the response cannot fill would otherwise leave the cursor pointing past
	// the last row, and renderRankings would slice an empty window.
	t.Run("the cursor is clamped to what actually arrived", func(t *testing.T) {
		t.Parallel()
		m := board(t, 0)

		next, _ := m.Update(loadedMsg{rankings: rankings(2), wantPage: 5})

		assert.Equal(t, 0, next.(model).page)
	})

	// A failed query must not clear the board silently; the error line is the only
	// signal the player gets.
	t.Run("an error is kept for the error screen", func(t *testing.T) {
		t.Parallel()
		m := board(t, 0)
		m.loading = true

		next, cmd := m.Update(loadedMsg{err: errors.New("query failed")})
		nm := next.(model)

		assert.Nil(t, cmd)
		assert.False(t, nm.loading)
		require.Error(t, nm.err)
		assert.False(t, nm.exhausted, "a failure says nothing about how much data exists")
	})
}

func TestUpdate_Keys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		key        string
		wantFilter int
		wantPage   int
	}{
		{name: "g cycles forward", key: "g", wantFilter: 1},
		{name: "right cycles forward", key: "right", wantFilter: 1},
		{name: "l cycles forward", key: "l", wantFilter: 1},
		{name: "left wraps backwards", key: "left", wantFilter: 2},
		{name: "h wraps backwards", key: "h", wantFilter: 2},
		{name: "down pages forward", key: "down", wantPage: 1},
		{name: "j pages forward", key: "j", wantPage: 1},
		{name: "pgdown pages forward", key: "pgdown", wantPage: 1},
		{name: "up at the top stays put", key: "up", wantPage: 0},
		{name: "k at the top stays put", key: "k", wantPage: 0},
		{name: "pgup at the top stays put", key: "pgup", wantPage: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Two full pages held, so a forward page turn resolves without a fetch.
			m := board(t, boardRows(t)*2)

			next, _ := m.Update(keyPress(tt.key))
			nm := next.(model)

			assert.Equal(t, tt.wantFilter, nm.filterIndex)
			assert.Equal(t, tt.wantPage, nm.page)
		})
	}
}

// The navigation keys the footer advertises have to reach the shared handler; a
// board that swallowed them would strand the player on the leaderboard.
func TestUpdate_NavigationKeysStillNavigate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key  string
		want string
	}{
		{key: "f", want: router.RouteLobbyJoin},
		{key: "p", want: router.RouteProfile},
		{key: "n", want: router.RouteLobbyCreate},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			t.Parallel()
			m := board(t, 0)
			next, cmd := m.Update(keyPress(tt.key))

			assert.Equal(t, 0, next.(model).filterIndex)
			require.NotNil(t, cmd)
			msg, ok := cmd().(router.ChangeViewMsg)
			require.True(t, ok)
			assert.Equal(t, tt.want, msg.ViewName)
		})
	}
}

func TestUpdate_UnboundKeyDoesNothing(t *testing.T) {
	t.Parallel()
	m := board(t, boardRows(t))

	next, cmd := m.Update(keyPress("z"))

	assert.Nil(t, cmd)
	assert.Equal(t, m.page, next.(model).page)
	assert.Equal(t, m.filterIndex, next.(model).filterIndex)
}

func TestGoPage_CannotPageBeforeTheFirstPage(t *testing.T) {
	t.Parallel()
	m := board(t, boardRows(t)*2)

	next, cmd := m.goPage(-1)

	assert.Nil(t, cmd, "paging back from page 1 must not re-query")
	assert.Equal(t, 0, next.(model).page)
}

// The three empty-ish states are all a player ever sees when there is nothing to
// draw, so each has to say which one it is rather than showing a blank box.
func TestRenderStateMessages(t *testing.T) {
	t.Parallel()

	t.Run("error", func(t *testing.T) {
		t.Parallel()
		assert.Contains(t, board(t, 0).renderError(), "Unable to load leaderboard")
	})

	t.Run("loading", func(t *testing.T) {
		t.Parallel()
		assert.Contains(t, board(t, 0).renderLoading(), "Loading leaderboard")
	})

	t.Run("empty, unfiltered", func(t *testing.T) {
		t.Parallel()
		m := board(t, 0)
		m.filterIndex = 0
		assert.Contains(t, m.renderEmpty(), "No players have ranked yet.")
	})

	// Naming the filter is what tells a player the board is not broken, only empty
	// for the game they picked.
	t.Run("empty, filtered", func(t *testing.T) {
		t.Parallel()
		m := board(t, 0)
		m.filterIndex = 1
		assert.Contains(t, m.renderEmpty(), "No rankings for Poker yet.")
	})
}

// The board picks one of four bodies, and each has to fit the frame: a state that
// only fits while rows are present overflows the first time a query comes back empty.
func TestView_FitsTheTerminalInEveryContentState(t *testing.T) {
	t.Parallel()

	states := map[string]func(m model) model{
		"a full page of rows": func(m model) model {
			m.rankings = rankings(maxLeaderboardPlayers)
			return m
		},
		"loading": func(m model) model { m.loading = true; return m },
		"empty": func(m model) model {
			m.rankings = []db.Ranking{}
			m.filterIndex = 1
			return m
		},
		"an error": func(m model) model { m.err = errors.New("query failed"); return m },
	}

	for _, size := range []struct {
		name string
		w, h int
	}{
		{"the declared minimum", styles.MinWidth, styles.MinHeight},
		{"a stock terminal", 80, 24},
		{"a tall terminal", 120, 50},
	} {
		for stateName, apply := range states {
			t.Run(size.name+"/"+stateName, func(t *testing.T) {
				t.Parallel()
				m := apply(model{
					global:  router.GlobalContext{Theme: styles.NewTheme(true), Width: size.w, Height: size.h},
					filters: []boardFilter{{label: filterAll}, {label: "Poker", slug: "poker"}},
				})

				out := m.View().Content

				assert.LessOrEqual(t, lg.Height(out), size.h, "taller than the terminal")
				assert.LessOrEqual(t, lg.Width(out), size.w, "wider than the terminal")
			})
		}
	}
}

// The viewer's own row is highlighted so a player can find themselves on a board of
// two hundred; everyone else's is drawn plain.
func TestRenderPlayerRow_HighlightsTheViewer(t *testing.T) {
	t.Parallel()
	m := board(t, 3)
	m.global.User = &db.User{ID: testutil.UID(2)}
	tbl := m.table(80, 3)

	mine := m.renderPlayerRow(tbl, 1, m.rankings[1])
	theirs := m.renderPlayerRow(tbl, 0, m.rankings[0])

	assert.NotEqual(t, stripANSI(mine), mine, "the viewer's own row is styled")
	assert.Equal(t, stripANSI(theirs), theirs, "another player's row is plain")
	assert.Contains(t, stripANSI(mine), "player02")
}

// A filter change used to ask for a screenful and judge the answer against a full
// page: at 80x24 five rows came back, five is fewer than twenty, and a board of a
// hundred players was declared exhausted after its first page.
func TestCycleFilter_StillPagesAt80x24(t *testing.T) {
	t.Parallel()

	all := rankings(100)
	m := board(t, 0)
	m.global.Width, m.global.Height = 80, 24
	m.global.UserRepository = fakeUsers{
		best: func(_ context.Context, limit int, _ string) ([]db.Ranking, error) {
			return all[:min(limit, len(all))], nil
		},
	}
	require.Less(t, m.rowsPerPage(), maxRowsPerPage, "the bug needs a page shorter than the cap")

	next, cmd := m.cycleFilter(1)
	require.NotNil(t, cmd)
	next, _ = next.(model).Update(cmd())
	nm := next.(model)
	require.False(t, nm.exhausted, "a full answer is not the end of the feed")

	next, _ = nm.goPage(1)
	assert.Equal(t, 1, next.(model).page, "the second page has to be reachable")
}

func keyPress(key string) tea.KeyPressMsg {
	switch key {
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	default:
		return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
	}
}
