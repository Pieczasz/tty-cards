package lobby

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/views"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

const (
	createCursorGame = iota
	createCursorPlayers
	createCursorVisibility
	createCursorMode
	createCursorSubmit
)

type createModel struct {
	global      router.GlobalContext
	err         error
	cursor      int
	isPrivate   bool
	isRanked    bool
	maxPlayers  int
	gameOptions []string
	gameIndex   int
}

// NewCreate builds the lobby form. The games on offer come from the registry and
// nowhere else: a hardcoded fallback here would be a second place a game is
// declared, and it would offer a game the registry cannot build.
func NewCreate(global router.GlobalContext) tea.Model {
	gameOptions := global.GameRegistry.GameNames()
	return &createModel{
		global:      global,
		cursor:      0,
		isPrivate:   true,
		isRanked:    false, // casual default - matches lobby.setupDefaultOptions
		maxPlayers:  4,
		gameOptions: gameOptions,
		gameIndex:   0,
	}
}

func (m *createModel) Init() tea.Cmd {
	return nil
}

func (m *createModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := views.HandleCommonMsg(msg, &m.global); handled {
		return m, cmd
	}

	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	return m.handleKey(key)
}

func (m *createModel) handleKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if cmd, ok := views.NavigateOn(key.String()); ok {
		return m, cmd
	}

	switch key.String() {
	case "up", "k":
		m.cursor = components.StepCursor(m.cursor, -1, createCursorSubmit)
	case "down", "j":
		m.cursor = components.StepCursor(m.cursor, +1, createCursorSubmit)
	case "left", "h":
		m.adjustSetting(-1)
	case "right", "l":
		m.adjustSetting(+1)
	case "enter":
		if m.cursor == createCursorSubmit {
			return m.createLobby()
		}
	}
	return m, nil
}

// adjustSetting moves the highlighted setting by delta. The two booleans toggle in
// either direction, matching how the row renders as "< value >".
func (m *createModel) adjustSetting(delta int) {
	switch m.cursor {
	case createCursorGame:
		if next := m.gameIndex + delta; next >= 0 && next < len(m.gameOptions) {
			m.gameIndex = next
			m.clampMaxPlayers()
		}
	case createCursorVisibility:
		m.isPrivate = !m.isPrivate
	case createCursorMode:
		m.isRanked = !m.isRanked
	case createCursorPlayers:
		if minP, maxP := m.gamePlayerBounds(); m.maxPlayers+delta >= minP && m.maxPlayers+delta <= maxP {
			m.maxPlayers += delta
		}
	case createCursorSubmit:
		// The submitted row has no left/right adjustment.
	}
}

// errNoGames is what the form says when the registry is empty. It is a broken
// deployment rather than a player mistake, but it still has to read as something.
var errNoGames = errors.New("no games are available right now")

// selectedGame is the highlighted game, or empty when there are none to highlight.
func (m *createModel) selectedGame() string {
	if m.gameIndex < 0 || m.gameIndex >= len(m.gameOptions) {
		return ""
	}
	return m.gameOptions[m.gameIndex]
}

func (m *createModel) createLobby() (tea.Model, tea.Cmd) {
	name := m.selectedGame()
	if name == "" {
		m.err = errNoGames
		return m, nil
	}

	l, err := m.global.LobbyManager.CreateLobby(views.SessionPlayer(m.global),
		lobby.WithCardGame(name),
		lobby.WithMaxPlayers(m.maxPlayers),
		lobby.WithPrivate(m.isPrivate),
		lobby.WithRanked(m.isRanked),
	)
	if err != nil {
		m.err = err
		return m, nil
	}
	return m, func() tea.Msg { return router.ChangeViewMsg{ViewName: router.RouteLobby, Context: l} }
}

// gamePlayerBounds is the selected game's seat range, or 2..8 for one the registry
// cannot build.
func (m *createModel) gamePlayerBounds() (minP, maxP int) {
	rules, err := m.global.GameRegistry.Create(m.selectedGame())
	if err != nil {
		return 2, 8
	}
	return rules.MinPlayers(), rules.MaxPlayers()
}

func (m *createModel) clampMaxPlayers() {
	minP, maxP := m.gamePlayerBounds()
	// minP is applied last so it wins if a game's bounds ever cross.
	m.maxPlayers = max(min(m.maxPlayers, maxP), minP)
}

func (m *createModel) View() tea.View {
	renderOption := func(idx int, label, value string) string {
		cursor := "  "
		if m.cursor == idx {
			cursor = "> "
			label = m.global.Theme.PlayerItemSelected.Render(label)
			value = m.global.Theme.PlayerItemSelected.Render(value)
		}
		return fmt.Sprintf("%s%s: < %s >", cursor, label, value)
	}

	gameName := m.selectedGame()
	if gameName == "" {
		gameName = "none available"
	}
	gameStr := renderOption(createCursorGame, "Game", gameName)
	playersStr := renderOption(createCursorPlayers, "Max Players", strconv.Itoa(m.maxPlayers))

	vis := fmt.Sprintf("%-7s", "Public")
	if m.isPrivate {
		vis = fmt.Sprintf("%-7s", "Private")
	}
	visStr := renderOption(createCursorVisibility, "Visibility", vis)

	mode := fmt.Sprintf("%-7s", "Casual")
	if m.isRanked {
		mode = fmt.Sprintf("%-7s", "Ranked")
	}
	modeStr := renderOption(createCursorMode, "Mode", mode)

	submitCursor := "  "
	submitText := "[ Create Lobby ]"
	if m.cursor == createCursorSubmit {
		submitCursor = "> "
		submitText = m.global.Theme.SuccessText.Render(submitText)
	}
	submitStr := fmt.Sprintf("%s%s", submitCursor, submitText)

	form := lg.JoinVertical(lg.Left,
		gameStr,
		playersStr,
		visStr,
		modeStr,
		"",
		submitStr,
	)

	content := form
	if m.err != nil {
		content += "\n\n" + m.global.Theme.ErrorText.Render(fmt.Sprintf("Error: %v", m.err))
	}

	actions := []string{"enter - Confirm"}
	return tea.NewView(views.RenderScreen(m.global, "Create New Lobby", actions,
		func(int) string { return content }))
}
