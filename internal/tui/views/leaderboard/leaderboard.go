package leaderboard

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Pieczasz/terminal-card/internal/catalog"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/views"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
)

const (
	// maxRowsPerPage is the most ranks one page ever shows. rowsPerPage shrinks it to
	// what the terminal can actually hold - drawing twenty rows into an 80x24 screen
	// overflowed it by fourteen lines, and TooSmall reported the terminal as fine.
	maxRowsPerPage = 20
	minRowsPerPage = 3
	// fullChrome is what renderRankings spends on non-row lines: a leading blank, the
	// heading and its bottom margin, the table's titles and rule, and the pager.
	// compactChrome drops the blank and the margin, which is the difference between
	// fitting and not at the declared 64x20 minimum.
	fullChrome    = 6
	compactChrome = 4

	// screenTitle has to be the same string RenderScreen is given: the header it
	// builds from it is part of the height budget rowsPerPage reads.
	screenTitle = "Leaderboard"
	// maxLeaderboardPlayers is the hardest cap on how far pagination can go.
	maxLeaderboardPlayers = 200

	minPlayerWidth = 10
	maxPlayerWidth = 16

	colRank = 5
	colGame = 15
	colElo  = 5
)

// filterAll is the empty BestPlayers slug: every ranking across every game.
const filterAll = "All"

type boardFilter struct {
	label string
	slug  string
}

type model struct {
	global      router.GlobalContext
	rankings    []db.Ranking
	err         error
	filters     []boardFilter
	filterIndex int
	page        int
	loading     bool
	// exhausted means the last fetch returned everything the repository has (or
	// we already hold maxLeaderboardPlayers), so paging further must not re-query.
	exhausted bool
}

func New(global router.GlobalContext) tea.Model {
	filters := make([]boardFilter, 0, 1+len(catalog.All))
	filters = append(filters, boardFilter{label: filterAll})
	for _, e := range catalog.All {
		filters = append(filters, boardFilter{label: e.Name, slug: e.Slug})
	}
	return model{global: global, filters: filters}
}

// loadedMsg carries the filter it was fetched for as well as the page. Pressing the
// filter key twice quickly issues two queries, and the slower one can land last: without
// the identity its rows would be painted under the newer filter's heading.
type loadedMsg struct {
	rankings []db.Ranking
	err      error
	gameSlug string
	wantPage int
	// limit is what was asked for: judging the answer against anything else is how a
	// filter change that asked for a screenful read five rows as the end of the feed.
	limit int
}

func (m model) gameFilter() string {
	if m.filterIndex == 0 {
		return ""
	}
	return m.filters[m.filterIndex].slug
}

func (m model) filterLabel() string {
	return m.filters[m.filterIndex].label
}

// rowsPerPage is how many ranks fit between the header and the footer right now.
// Paging and rendering both read it, so a page always holds exactly what is drawn.
func (m model) rowsPerPage() int {
	rows, _ := m.pageLayout()
	return rows
}

// pageLayout splits the content budget between the rows and the chrome around them.
// The optional spacing goes first: at 64x20 the full chrome costs as many lines as
// the whole content area, so keeping it would leave no room for a single rank.
func (m model) pageLayout() (rows int, compact bool) {
	budget := views.ScreenContentHeight(m.global, screenTitle, m.actions())
	chrome := fullChrome
	if budget-fullChrome < minRowsPerPage {
		chrome, compact = compactChrome, true
	}
	return min(max(budget-chrome, 1), maxRowsPerPage), compact
}

func (m model) pageCount() int {
	rows := m.rowsPerPage()
	if len(m.rankings) == 0 {
		return 1
	}
	return (len(m.rankings) + rows - 1) / rows
}

func (m model) needsFetch(page int) int {
	// A short last page still covers that page index; only ask for more when the
	// cursor would land past what we already hold.
	rows := m.rowsPerPage()
	if page*rows < len(m.rankings) || m.exhausted {
		return 0
	}
	// Never fetch fewer than a full page's worth: on a short terminal that would walk
	// the repository three rows at a time.
	need := min(max((page+1)*rows, maxRowsPerPage), maxLeaderboardPlayers)
	if need <= len(m.rankings) {
		return 0
	}
	return need
}

func (m model) load(limit int, wantPage int) tea.Cmd {
	gameSlug := m.gameFilter()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.global.RequestContext(), 5*time.Second)
		defer cancel()
		rankings, err := m.global.UserRepository.BestPlayers(ctx, limit, gameSlug)
		return loadedMsg{rankings: rankings, err: err, gameSlug: gameSlug, wantPage: wantPage, limit: limit}
	}
}

func (m model) Init() tea.Cmd {
	// The first WindowSizeMsg has not arrived yet, so ask for a full page: it covers
	// any terminal, and rowsPerPage decides how much of it is drawn.
	return m.load(maxRowsPerPage, 0)
}

func (m model) cycleFilter(delta int) (tea.Model, tea.Cmd) {
	m.filterIndex = components.CycleIndex(m.filterIndex, delta, len(m.filters))
	m.rankings = nil
	m.err = nil
	m.page = 0
	m.exhausted = false
	m.loading = true
	return m, m.load(maxRowsPerPage, 0)
}

func (m model) goPage(delta int) (tea.Model, tea.Cmd) {
	next := m.page + delta
	if next < 0 {
		return m, nil
	}
	if need := m.needsFetch(next); need > 0 {
		m.loading = true
		return m, m.load(need, next)
	}
	if next >= m.pageCount() {
		return m, nil
	}
	m.page = next
	return m, nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := views.HandleCommonMsg(msg, &m.global); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case loadedMsg:
		if msg.gameSlug != m.gameFilter() {
			// A response for a filter the player has already cycled past.
			return m, nil
		}
		m.loading = false
		m.rankings = msg.rankings
		m.err = msg.err
		if msg.err != nil {
			slog.Error("database error while fetching leaderboard", "error", msg.err)
			return m, nil
		}
		// Fewer rows than the request (or the hard cap) means there is nothing left to page into.
		m.exhausted = len(msg.rankings) < msg.limit || len(msg.rankings) >= maxLeaderboardPlayers
		m.page = min(msg.wantPage, max(m.pageCount()-1, 0))
	case tea.KeyPressMsg:
		switch msg.String() {
		case "g":
			return m.cycleFilter(1)
		case "right", "l":
			return m.cycleFilter(1)
		case "left", "h":
			return m.cycleFilter(-1)
		case "down", "j", "pgdown":
			return m.goPage(1)
		case "up", "k", "pgup":
			return m.goPage(-1)
		}
		if cmd, ok := views.NavigateOn(msg.String()); ok {
			return m, cmd
		}
	}
	return m, nil
}

func (m model) actions() []string {
	return []string{"g/←/-> - Filter: " + m.filterLabel(), "↑/↓ - Page"}
}

func (m model) View() tea.View {
	return tea.NewView(views.RenderScreen(m.global, screenTitle, m.actions(), func(int) string {
		switch {
		case m.err != nil:
			return m.renderError()
		case m.rankings == nil || m.loading && len(m.rankings) == 0:
			return m.renderLoading()
		case len(m.rankings) == 0:
			return m.renderEmpty()
		default:
			return m.renderRankings(styles.InnerWidth(m.global.Width))
		}
	}))
}

func (m model) renderError() string {
	return lg.JoinVertical(lg.Center, "Unable to load leaderboard. Please try again.")
}

func (m model) renderLoading() string {
	return lg.JoinVertical(lg.Center, "Loading leaderboard.")
}

func (m model) renderEmpty() string {
	if m.filterIndex == 0 {
		return lg.JoinVertical(lg.Center, "No players have ranked yet.")
	}
	return lg.JoinVertical(lg.Center, fmt.Sprintf("No rankings for %s yet.", m.filterLabel()))
}

// table is the fixed-cell board. The player column is the only one that flexes, and
// only with the terminal: the rest stay put so paging cannot shift the columns.
func (m model) table(contentWidth, rows int) components.Table {
	playerWidth := min(max(contentWidth-(colRank+colGame+colElo+9), minPlayerWidth), maxPlayerWidth)
	return components.Table{
		Cols: []components.Column{
			{Title: "Rank", Width: colRank},
			{Title: "Player", Width: playerWidth},
			{Title: "Game", Width: colGame},
			{Title: "Elo", Width: colElo},
		},
		PadTo: rows,
	}
}

func (m model) renderRankings(contentWidth int) string {
	rows, compact := m.pageLayout()
	tbl := m.table(contentWidth, rows)
	start := m.page * rows
	end := min(start+rows, len(m.rankings))

	cells := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		cells = append(cells, m.renderPlayerRow(tbl, i, m.rankings[i]))
	}

	table := tbl.Render(m.global.Theme, cells)
	headingStyle := m.global.Theme.Title.Bold(true)
	if !compact {
		headingStyle = headingStyle.MarginBottom(1)
	}
	heading := headingStyle.Render("Filter: " + m.filterLabel())
	pager := m.global.Theme.Dim.Render(fmt.Sprintf("page %d/%d  ranks %d-%d of %d",
		m.page+1, m.pageCount(), start+1, end, len(m.rankings)))
	if m.loading {
		pager = m.global.Theme.Dim.Render("loading…")
	}
	if compact {
		return lg.JoinVertical(lg.Center, heading, table, pager)
	}
	return lg.JoinVertical(lg.Center, "", heading, table, pager)
}

// renderPlayerRow lays the cells out itself rather than through Table.Cells: the
// viewer's own row is highlighted, and a styled cell cannot be padded by rune count
// afterwards without counting the escape sequence as text.
func (m model) renderPlayerRow(tbl components.Table, index int, r db.Ranking) string {
	playerWidth := tbl.Cols[1].Width
	userStr := styles.PadTruncate(r.User.Username, playerWidth)
	if m.global.User != nil && r.User.ID == m.global.User.ID {
		userStr = m.global.Theme.PlayerItemSelected.Bold(true).Render(userStr)
	}
	return fmt.Sprintf("%-*d | %s | %-*s | %d",
		colRank, index+1, userStr, colGame, styles.PadTruncate(r.Game.Name, colGame), r.Elo)
}
