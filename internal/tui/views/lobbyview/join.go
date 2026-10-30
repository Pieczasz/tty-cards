package lobby

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/tui/components"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/views"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

// browseRefresh is how often the list re-reads the manager. It matches the
// manager's public-lobby cache window, so however many players are browsing at
// once, the tables are scanned once per window and no more.
const browseRefresh = 2 * time.Second

const (
	// maxVisibleRows is the most of the list ever on screen at once. visibleRows
	// shrinks it to what the terminal can hold - a fixed ten plus its chrome did not
	// fit an 80x24 terminal, let alone the declared 64x20 minimum.
	maxVisibleRows = 10
	// listChrome is what the screen spends around the rows: the filter line, a blank,
	// the table's titles and rule, the pager, a blank, the code prompt and its input.
	// compactListChrome drops the two blanks.
	listChrome        = 8
	compactListChrome = 6
)

// listLayout splits a content budget between the rows and the chrome around them,
// dropping the optional spacing first when even one row would not otherwise fit.
func listLayout(contentHeight int) (rows int, compact bool) {
	chrome := listChrome
	if contentHeight-listChrome < 1 {
		chrome, compact = compactListChrome, true
	}
	return min(max(contentHeight-chrome, 1), maxVisibleRows), compact
}

// refreshMsg names the screen that armed it. The router builds a new join screen on
// every visit, and a tick in flight from the last one would otherwise re-arm itself
// here and run a second chain.
type refreshMsg struct{ owner *joinModel }

func (m *joinModel) refreshTick() tea.Cmd {
	return tea.Tick(browseRefresh, func(time.Time) tea.Msg { return refreshMsg{owner: m} })
}

type joinModel struct {
	global      router.GlobalContext
	textInput   textinput.Model
	err         error
	cursor      int
	writingCode bool

	// entries is the rendered list, refreshed on a timer rather than derived per
	// frame: a lobby getter takes the lobby's lock, and a table of them would take
	// one per column per row on every keystroke.
	entries []lobby.BrowseEntry
	filter  lobby.BrowseFilter
	// games is the set of games with a table right now, for cycling the filter.
	games []game.Module

	// lastKey/lastView memoize the rendered frame. The browse tick re-renders this
	// screen every 2s for every player parked on it, and a frame costs ~6.7k
	// allocations - so identical state returns the previous string instead.
	lastKey  string
	lastView string
}

// NewJoin is the join screen: the public lobby browser and the join-by-code prompt.
func NewJoin(global router.GlobalContext) tea.Model {
	ti := textinput.New()
	ti.Placeholder = "8-character code"
	ti.CharLimit = 8
	ti.SetWidth(20)

	m := &joinModel{
		global:    global,
		textInput: ti,
		// Full tables are hidden by default: the reason to open this screen is to
		// find a seat, and a table with none is not one. Limit is the hard cap on
		// how many matching tables we keep; only a screenful shows at once.
		filter: lobby.BrowseFilter{OnlyWithRoom: true, Limit: lobby.MaxBrowseLimit},
	}
	m.refresh()
	return m
}

func (m *joinModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.refreshTick())
}

// refresh re-reads the list and keeps the cursor on a real row. Tables appear and
// disappear underneath it as other players start games, so the cursor is clamped
// every time rather than only when the list shrinks to empty.
func (m *joinModel) refresh() {
	m.entries = m.global.LobbyManager.BrowseLobbies(m.global.Player, m.filter)
	m.games = m.global.LobbyManager.GamesOnOffer()
	m.cursor = components.StepCursor(m.cursor, 0, len(m.entries)-1)
}

func (m *joinModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if tick, ok := msg.(refreshMsg); ok {
		if tick.owner != m {
			return m, nil
		}
		m.refresh()
		next := m.refreshTick()
		return m, next
	}

	// The shared handler claims resizes, the theme switch and ctrl+c, and nothing
	// else, so it is safe to run while a code is being typed: quitting has to work
	// from a focused text field too.
	if handled, cmd := views.HandleCommonMsg(msg, &m.global); handled {
		return m, cmd
	}

	key, isKey := msg.(tea.KeyPressMsg)
	if !isKey {
		if m.writingCode {
			var cmd tea.Cmd
			m.textInput, cmd = m.textInput.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	if m.writingCode {
		return m.handleCodeEntry(key)
	}
	return m.handleBrowsing(key)
}

func (m *joinModel) handleCodeEntry(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.writingCode = false
		m.textInput.Blur()
		return m, nil
	case "enter":
		return m.joinByCode(strings.ToUpper(m.textInput.Value()))
	default:
		var cmd tea.Cmd
		m.textInput, cmd = m.textInput.Update(key)
		m.textInput.SetValue(strings.ToUpper(m.textInput.Value()))
		return m, cmd
	}
}

func (m *joinModel) handleBrowsing(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if cmd, ok := views.NavigateOn(key.String()); ok {
		return m, cmd
	}

	switch key.String() {
	case "up", "k":
		m.cursor = components.StepCursor(m.cursor, -1, len(m.entries)-1)
	case "down", "j":
		m.cursor = components.StepCursor(m.cursor, +1, len(m.entries)-1)
	case "g":
		m.cycleGame()
	case "m":
		m.cycleMode()
	case "o":
		m.filter.OnlyWithRoom = !m.filter.OnlyWithRoom
		m.applyFilter()
	case "c":
		m.writingCode = true
		m.textInput.Focus()
		return m, textinput.Blink
	// "space", not " ": KeyPressMsg.String() normalises the spacebar to the name, so
	// the literal never matched and space on the browse list did nothing at all.
	case "enter", "space":
		return m.joinSelected()
	}
	return m, nil
}

// applyFilter re-reads immediately so a filter keypress is visible now rather than
// on the next tick, and puts the cursor back at the top of a list that just changed
// out from under it.
func (m *joinModel) applyFilter() {
	m.cursor = 0
	m.refresh()
}

// cycleGame steps through "any" and each game that currently has a table. A filter
// pinned to a game nobody is playing would show an empty list with no way to tell
// why, so a game that disappears drops the filter back to any.
func (m *joinModel) cycleGame() {
	if len(m.games) == 0 {
		m.filter.Slug = ""
		m.applyFilter()
		return
	}
	current := m.gameIndex(m.filter.Slug)
	if current+1 >= len(m.games) {
		m.filter.Slug = ""
	} else {
		m.filter.Slug = m.games[current+1].Slug
	}
	m.applyFilter()
}

func (m *joinModel) cycleMode() {
	switch m.filter.Mode {
	case lobby.BrowseAny:
		m.filter.Mode = lobby.BrowseRanked
	case lobby.BrowseRanked:
		m.filter.Mode = lobby.BrowseCasual
	case lobby.BrowseCasual:
		m.filter.Mode = lobby.BrowseAny
	}
	m.applyFilter()
}

func (m *joinModel) joinSelected() (tea.Model, tea.Cmd) {
	if m.cursor >= len(m.entries) {
		return m, nil
	}
	return m.joinByCode(m.entries[m.cursor].Code)
}

func (m *joinModel) joinByCode(code string) (tea.Model, tea.Cmd) {
	if code == "" {
		return m, nil
	}
	joined, err := m.global.LobbyManager.JoinLobbyByCode(m.global.RequestContext(), code, m.global.Player)
	if err != nil {
		m.err = views.PlayerError(m.global.RequestContext(), err)
		// The table may have filled or started while the list was on screen, so show
		// the player what is actually joinable now instead of a stale row.
		m.refresh()
		return m, nil
	}
	return m, router.NavigateToLobby(joined)
}

// modeLabel names the current mode filter for the status line.
func (m *joinModel) modeLabel() string {
	switch m.filter.Mode {
	case lobby.BrowseRanked:
		return "ranked"
	case lobby.BrowseCasual:
		return "casual"
	case lobby.BrowseAny:
	}
	return "any"
}

// gameIndex is where slug sits in games, or -1.
func (m *joinModel) gameIndex(slug string) int {
	return slices.IndexFunc(m.games, func(g game.Module) bool { return g.Slug == slug })
}

func (m *joinModel) filterLine() string {
	label := "any"
	if i := m.gameIndex(m.filter.Slug); i >= 0 {
		label = m.games[i].Name
	} else if m.filter.Slug != "" {
		label = m.filter.Slug
	}
	seats := "all tables"
	if m.filter.OnlyWithRoom {
		seats = "with seats"
	}
	return m.global.Palette().Muted.Render(fmt.Sprintf("game: %s   mode: %s   showing: %s", label, m.modeLabel(), seats))
}

// Column widths are fixed so a filter keypress cannot resize the list under the
// cursor. Codes stay off this list: JoinLobbyByCode never checks IsPrivate.
const (
	colGame   = 16
	colSeats  = 7
	colMode   = 6
	colRating = 4
)

// browseTable is the list's shape. Lead is the cursor gutter, which every row carries
// so the marker does not push the columns sideways on the selected one.
var browseTable = components.Table{
	Cols: []components.Column{
		{Title: "Game", Width: colGame},
		{Title: "Seats", Width: colSeats},
		{Title: "Mode", Width: colMode},
		{Title: "Elo", Width: colRating},
	},
	Lead: " ",
}

func (m *joinModel) renderRow(entry lobby.BrowseEntry, selected bool) string {
	theme := m.global.Palette()

	mode := theme.Muted.Render(styles.PadTruncate("casual", colMode))
	if entry.Ranked {
		mode = theme.Accented.Render(styles.PadTruncate("ranked", colMode))
	}

	marker := " "
	if selected {
		marker = theme.PlayerItemSelected.Render(">")
	}
	return marker + browseTable.Row(
		styles.PadTruncate(entry.GameName, colGame),
		styles.PadTruncate(fmt.Sprintf("%d/%d", entry.Players, entry.MaxPlayers), colSeats),
		mode,
		styles.PadTruncate(strconv.FormatUint(uint64(entry.AvgElo), 10), colRating),
	)
}

// visibleWindow is the slice of rows on screen, scrolled to keep the cursor in view.
func (m *joinModel) visibleWindow(rows int) (start, end int) {
	start = 0
	if m.cursor >= rows {
		start = m.cursor - rows + 1
	}
	return start, min(start+rows, len(m.entries))
}

func (m *joinModel) renderList(rows int) string {
	if len(m.entries) == 0 {
		return m.global.Palette().Muted.Render("No tables match right now - press g, m or o to widen the filters.")
	}

	start, end := m.visibleWindow(rows)
	cells := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		cells = append(cells, m.renderRow(m.entries[i], !m.writingCode && m.cursor == i))
	}
	pager := m.global.Palette().Dim.Render(
		fmt.Sprintf(" %d-%d of %d  ↑/↓ scroll", start+1, end, len(m.entries)))
	tbl := browseTable
	tbl.PadTo = rows
	return tbl.Render(m.global.Palette(), cells) + "\n" + pager
}

func (m *joinModel) View() tea.View {
	if key := m.renderKey(); key == m.lastKey && m.lastView != "" {
		return tea.NewView(m.lastView)
	}
	actions := []string{"c - Enter Code", "g - Game", "m - Mode", "o - Seats"}
	rendered := views.RenderScreen(m.global, "Join Game", actions, m.renderContent)
	m.lastKey, m.lastView = m.renderKey(), rendered
	return tea.NewView(rendered)
}

func (m *joinModel) renderContent(contentHeight int) string {
	codeInputStr := "Or press 'c' to enter a private lobby code:"
	if m.writingCode {
		codeInputStr = "Entering private lobby code (press ESC to cancel):"
	}

	// The error is wrapped and measured before the list takes its rows: appended after
	// a list that had spent the whole budget, it pushed the frame past the terminal.
	var errLine string
	if m.err != nil {
		errLine = m.global.Palette().ErrorText.Width(styles.InnerWidth(m.global.Width)).
			Render("Error: " + m.err.Error())
		contentHeight -= lg.Height(errLine)
	}

	rows, compact := listLayout(contentHeight)
	parts := []string{m.filterLine(), "", m.renderList(rows), "",
		m.global.Palette().Muted.Render(codeInputStr), m.textInput.View()}
	if compact {
		parts = []string{m.filterLine(), m.renderList(rows),
			m.global.Palette().Muted.Render(codeInputStr), m.textInput.View()}
	}
	if errLine != "" {
		parts = append(parts, errLine)
	}
	return styles.JoinVertical(lg.Left, parts...)
}

// renderKey is every input View reads, cheap enough to build per frame. The entry
// fingerprint covers what the list renders (code, players, game), so a lobby
// filling up or closing invalidates the memo even when the count does not change.
func (m *joinModel) renderKey() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%dx%d|%v|%d|%v|%+v|%v|%s",
		m.global.Width, m.global.Height, m.global.Palette().Dark,
		m.cursor, m.writingCode, m.filter, m.err, m.textInput.View())
	for _, e := range m.entries {
		fmt.Fprintf(&b, "|%s:%s:%d/%d:%v:%d:%d", e.Code, e.GameName, e.Players, e.MaxPlayers, e.Ranked, e.AvgElo, e.EloDelta)
	}
	return b.String()
}
