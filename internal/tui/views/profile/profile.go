// Package profile is the player's own screen: ratings, match history and account
// deletion.
package profile

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/views"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"

	"github.com/Pieczasz/terminal-card/internal/db"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	"uuid"
)

const (
	historyFetchLimit = 50
	filterAllGames    = "All"
	filterAllResults  = "All"
	filterWins        = "Wins"
	filterLosses      = "Losses"

	// Fixed cells so cycling filters or empty history cannot resize the layout.
	colGame   = 12 // longest catalog name today: "Crazy Eights"
	colElo    = 4
	colPlace  = 10 // "1st place"
	colResult = 14 // "Elo change: +99" / "casual game"
	// tableGap is the space between the two tables when they sit side by side, and
	// what the fit check has to account for when deciding whether they can.
	tableGap = 4
	// twoTableMinHeight is what a stacked pair costs at its smallest: the two label
	// lines, two spacers, two 2-line table headers, a row each, and the gap between
	// them. Below it one table has to go.
	twoTableMinHeight = 11

	// deleteConfirmWord is typed out in full on purpose: erasure is irreversible, and
	// a single keystroke is one too few between a mistyped filter key and an account.
	deleteConfirmWord = "DELETE"
	// A cap so a held key cannot grow the string without bound; four characters of
	// slack past the word leave room to see a typo before backspacing it.
	deleteTypedMax = len(deleteConfirmWord) + 4
)

// deletePhase is the account-erasure state machine. The confirmation is modal: while
// it is open every key belongs to it, or typing DELETE would cycle the filters on the
// way past.
type deletePhase int

const (
	deleteIdle deletePhase = iota
	deleteConfirming
	// deleteRunning is the round trip itself. It swallows every key but ctrl+c: backing
	// out now would let the player navigate away and keep playing on an account that is
	// already being erased, and a second enter would issue the delete twice.
	deleteRunning
	deleteDone
)

type model struct {
	global      router.GlobalContext
	userProfile *db.User
	history     []db.MatchParticipant
	err         error
	historyErr  error

	gameFilters   []string
	gameFilterIdx int
	resultFilters []string
	resultIdx     int

	phase deletePhase
	typed string
	// notice replaces the filter line rather than adding one, so a refusal cannot
	// make the screen a row taller than the terminal it was measured against.
	notice string
}

// New builds the profile screen with one game filter per registered game.
func New(global router.GlobalContext) tea.Model {
	gameFilters := []string{filterAllGames}
	if global.GameRegistry != nil {
		gameFilters = append(gameFilters, global.GameRegistry.GameNames()...)
	}
	return &model{
		global:        global,
		gameFilters:   gameFilters,
		resultFilters: []string{filterAllResults, filterWins, filterLosses},
	}
}

// profileLoadedMsg keeps the two failures apart. They are two queries, and a
// player whose match history could not be read still has a profile worth showing.
type profileLoadedMsg struct {
	user       *db.User
	history    []db.MatchParticipant
	err        error
	historyErr error
}

func loadProfile(ctx context.Context, profiles db.Profiles, userID uuid.UUID) tea.Cmd {
	return func() tea.Msg {
		reqCtx, cancel := context.WithTimeout(ctx, views.RequestTimeout)
		defer cancel()
		user, err := profiles.UserProfile(reqCtx, userID)
		if err != nil {
			return profileLoadedMsg{err: err}
		}
		history, historyErr := profiles.UserMatchHistory(reqCtx, userID, historyFetchLimit)
		return profileLoadedMsg{user: user, history: history, historyErr: historyErr}
	}
}

func (m *model) Init() tea.Cmd {
	if m.global.User == nil {
		return nil
	}
	return loadProfile(m.global.RequestContext(), m.global.Profiles, m.global.User.ID)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := views.HandleCommonMsg(msg, &m.global); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case accountDeletedMsg:
		return m.accountDeleted(msg)
	case profileLoadedMsg:
		m.userProfile = msg.user
		m.history = msg.history
		m.err = msg.err
		m.historyErr = msg.historyErr
		if msg.err != nil {
			slog.Error("database error while loading user profile", "error", msg.err)
		}
		if msg.historyErr != nil {
			slog.Error("database error while loading match history", "error", msg.historyErr)
		}
	case tea.KeyPressMsg:
		switch m.phase {
		case deleteConfirming:
			return m.confirmKey(msg)
		case deleteRunning, deleteDone:
			return m, nil
		case deleteIdle:
		}
		m.notice = ""
		switch msg.String() {
		case "x":
			return m.openConfirm()
		case "g":
			m.gameFilterIdx = components.CycleIndex(m.gameFilterIdx, 1, len(m.gameFilters))
			return m, nil
		case "r":
			m.resultIdx = components.CycleIndex(m.resultIdx, 1, len(m.resultFilters))
			return m, nil
		}
		if cmd, ok := views.NavigateOn(msg.String()); ok {
			return m, cmd
		}
	}
	return m, nil
}

func (m *model) View() tea.View {
	actions := []string{"g - Game", "r - Result", "x - Delete account"}
	return tea.NewView(views.RenderScreen(m.global, "User Profile", actions, m.renderContent))
}

func (m *model) renderContent(contentHeight int) string {
	switch m.phase {
	case deleteConfirming:
		return m.renderConfirm()
	case deleteRunning:
		return "Deleting your account..."
	case deleteDone:
		return "Your account has been deleted. Goodbye."
	case deleteIdle:
	}
	if m.err != nil {
		return "Unable to load profile. Please try again."
	}
	if m.userProfile == nil {
		return "Loading profile..."
	}

	stacked, rankItems, histItems := tableBudget(styles.InnerWidth(m.global.Width), contentHeight)

	userInfo := "Profile for: " + m.userProfile.Username
	filters := m.global.Theme.Muted.Render(fmt.Sprintf("Game: %s  Result: %s",
		styles.PadTruncate(m.gameFilters[m.gameFilterIdx], colGame),
		styles.PadTruncate(m.resultFilters[m.resultIdx], len(filterLosses)),
	))
	if m.notice != "" {
		filters = m.global.Theme.ErrorText.Render(m.notice)
	}

	// At the declared 64x20 minimum the title and footer leave six lines, fewer than
	// two stacked tables need at their smallest. The rankings summary gives way to
	// the match history, which is what a player opens this screen for.
	if stacked && contentHeight < twoTableMinHeight {
		items := max(contentHeight-4, 1) // the two labels and the 2-line header
		return lg.JoinVertical(lg.Left, userInfo, filters,
			lg.JoinVertical(lg.Left, m.historyRows(items)...))
	}

	rankingsStyle := lg.NewStyle().Align(lg.Left).Width(rankingsTable.Width())
	if !stacked {
		rankingsStyle = rankingsStyle.MarginRight(tableGap)
	}
	rankingsCol := rankingsStyle.Render(lg.JoinVertical(lg.Left, m.rankingRows(rankItems)...))
	historyCol := lg.NewStyle().Align(lg.Left).Width(historyTable.Width()).
		Render(lg.JoinVertical(lg.Left, m.historyRows(histItems)...))

	tables := lg.JoinHorizontal(lg.Top, rankingsCol, historyCol)
	if stacked {
		tables = lg.JoinVertical(lg.Left, rankingsCol, "", historyCol)
	}

	return lg.JoinVertical(lg.Left, userInfo, "", filters, "", tables)
}

// tableBudget decides whether the two tables sit side by side or stacked, and how many
// rows each may show in contentHeight.
func tableBudget(innerWidth, contentHeight int) (stacked bool, rankItems, histItems int) {
	// userInfo, spacer, filter, spacer, and the table header - which is two lines,
	// its titles and the rule under them.
	const extraVerticalLines = 6
	maxItems := max(contentHeight-extraVerticalLines, 1)

	// The two tables are fixed-width, so below a certain terminal they do not fit
	// beside each other and lipgloss word-wraps the columns into confetti rather
	// than shrinking them. Stacking is what renderForm does for the same reason.
	stacked = rankingsTable.Width()+tableGap+historyTable.Width() > innerWidth
	if !stacked {
		return false, maxItems, maxItems
	}
	// Both tables now spend height instead of sharing it: two headers and the
	// spacer between them come out of the same budget.
	rankItems = max((maxItems-3)/2, 1)
	return true, rankItems, max(maxItems-3-rankItems, 1)
}

// The two tables' cells are fixed so cycling a filter cannot resize the layout.
var (
	rankingsTable = components.Table{Cols: []components.Column{
		{Title: "Game", Width: colGame},
		{Title: "Elo", Width: colElo},
	}}
	historyTable = components.Table{Cols: []components.Column{
		{Title: "Game", Width: colGame},
		{Title: "Place", Width: colPlace},
		{Title: "Result", Width: colResult},
	}}
)

// limitRows splits n items into what fits and whether to say so. The "... and more"
// line comes *out* of the budget rather than being appended past it, or a truncated
// table is one line taller than the space it was given.
func limitRows(n, maxItems int) (show int, more bool) {
	if n <= maxItems {
		return n, false
	}
	return max(maxItems-1, 0), true
}

func (m *model) rankingRows(maxItems int) []string {
	rows := []string{rankingsTable.Header(m.global.Theme)}
	if len(m.userProfile.Rankings) == 0 {
		return append(rows, styles.PadTruncate("No games yet.", rankingsTable.Width()))
	}
	show, more := limitRows(len(m.userProfile.Rankings), maxItems)
	for _, r := range m.userProfile.Rankings[:show] {
		rows = append(rows, rankingsTable.Cells(r.Game.Name, strconv.FormatUint(uint64(r.Elo), 10)))
	}
	if more {
		rows = append(rows, "... and more")
	}
	return rows
}

func (m *model) filteredHistory() []db.MatchParticipant {
	out := make([]db.MatchParticipant, 0, len(m.history))
	wantGame := m.gameFilters[m.gameFilterIdx]
	wantResult := m.resultFilters[m.resultIdx]
	for _, h := range m.history {
		if wantGame != filterAllGames && h.Match.Game.Name != wantGame {
			continue
		}
		won := h.Placement == 1
		switch wantResult {
		case filterWins:
			if !won {
				continue
			}
		case filterLosses:
			if won {
				continue
			}
		}
		out = append(out, h)
	}
	return out
}

func (m *model) historyRows(maxItems int) []string {
	rows := []string{historyTable.Header(m.global.Theme)}
	if m.historyErr != nil {
		return append(rows, m.global.Theme.ErrorText.Render("Unable to load match history."))
	}
	filtered := m.filteredHistory()
	if len(filtered) == 0 {
		return append(rows, styles.PadTruncate("No matches for this filter.", historyTable.Width()))
	}
	show, more := limitRows(len(filtered), maxItems)
	for _, h := range filtered[:show] {
		rows = append(rows, historyTable.Cells(
			h.Match.Game.Name, placementPlain(h.Placement), resultPlain(h)))
	}
	if more {
		rows = append(rows, "... and more")
	}
	return rows
}

func resultPlain(h db.MatchParticipant) string {
	if !h.Match.Ranked {
		return "casual game"
	}
	if h.EloDelta >= 0 {
		return fmt.Sprintf("Elo +%d", h.EloDelta)
	}
	return fmt.Sprintf("Elo %d", h.EloDelta)
}

func placementPlain(placement int) string {
	switch placement {
	case 1, 2, 3:
		return placementWords[placement-1]
	default:
		return fmt.Sprintf("%d place", placement)
	}
}

var placementWords = [3]string{"1st place", "2nd place", "3rd place"}

// accountDeletedMsg is the result of the erasure round trip; err nil means the row
// is already anonymised and the session has nothing left to authenticate.
type accountDeletedMsg struct{ err error }

func deleteAccount(ctx context.Context, profiles db.Profiles, userID uuid.UUID) tea.Cmd {
	return func() tea.Msg {
		reqCtx, cancel := context.WithTimeout(ctx, views.RequestTimeout)
		defer cancel()
		return accountDeletedMsg{err: profiles.DeleteAccount(reqCtx, userID)}
	}
}

func (m *model) openConfirm() (tea.Model, tea.Cmd) {
	if m.global.User == nil {
		return m, nil
	}
	// A seat is live state the lobby and engine hold under this player ID. Erasing the
	// account out from under it would rename a player mid-hand and forfeit the table
	// for everyone else, so leaving is the player's move to make first.
	if p := views.SessionPlayer(m.global); p != nil && m.global.LobbyManager != nil &&
		m.global.LobbyManager.FindLobbyByPlayer(p) != nil {
		m.notice = "Leave your table before deleting your account."
		return m, nil
	}
	m.phase = deleteConfirming
	m.typed = ""
	m.notice = ""
	return m, nil
}

func (m *model) confirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key := msg.String(); key {
	case "esc":
		m.phase, m.typed, m.notice = deleteIdle, "", ""
	case "enter":
		if m.typed != deleteConfirmWord {
			m.notice = "Type " + deleteConfirmWord + " exactly, then press enter."
			return m, nil
		}
		m.phase = deleteRunning
		return m, deleteAccount(m.global.RequestContext(), m.global.Profiles, m.global.User.ID)
	case "backspace":
		if runes := []rune(m.typed); len(runes) > 0 {
			m.typed = string(runes[:len(runes)-1])
		}
		m.notice = ""
	default:
		// Text is empty for every key that is not a character, so arrows and function
		// keys cannot end up in the confirmation string.
		if msg.Text != "" && len([]rune(m.typed)) < deleteTypedMax {
			m.typed += msg.Text
			m.notice = ""
		}
	}
	return m, nil
}

func (m *model) accountDeleted(msg accountDeletedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		slog.Error("database error while deleting account", "error", msg.err)
		m.phase = deleteConfirming
		m.typed = ""
		m.notice = "Could not delete the account. Please try again."
		return m, nil
	}
	m.phase = deleteDone
	// Quitting is what ends the SSH session through the usual release path, which is
	// also what frees the session slot - the view never touches the ssh layer itself.
	return m, tea.Quit
}

// The warning is deliberately not paged or shortened for a small terminal: it fits
// the declared 64x20 minimum as it is (TestView_FitsTheTerminal), and an erasure
// warning is the last screen worth trimming to save a row.
func (m *model) renderConfirm() string {
	anonymised := db.AnonymisedUsername(m.global.User.ID)
	prompt := "> " + styles.PadTruncate(m.typed, deleteTypedMax)
	lines := []string{
		m.global.Theme.ErrorText.Render("Delete your account permanently?"),
		"",
		"- SSH keys removed; a new login is a new account",
		"- ratings removed from every leaderboard",
		// InnerWidth at MinWidth is 54; the 40-char deleted_ name has to share that
		// line or the confirmation wraps taller than the 20-row minimum.
		"- kept as " + anonymised,
		"- this cannot be undone",
		"",
		"Type DELETE and press enter, esc to cancel",
		prompt,
	}
	if m.notice != "" {
		lines = append(lines, m.global.Theme.ErrorText.Render(m.notice))
	}
	return lg.JoinVertical(lg.Left, lines...)
}
