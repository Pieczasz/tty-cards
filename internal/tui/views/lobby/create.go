package lobby

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/tui/components"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/views"

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
	gameOptions []game.Module
	gameIndex   int
}

// NewCreate builds the lobby form. The games on offer come from global.Games and
// nowhere else: a hardcoded fallback here would be a second place a game is
// declared, and it would offer a game the lobby manager refuses.
func NewCreate(global router.GlobalContext) tea.Model {
	return &createModel{
		global: global,
		cursor: 0,
		// Public: a newcomer has nobody to hand a code to, and an unlisted table is one
		// nobody can find. Private is one toggle away, and the row says which it is.
		isPrivate:   false,
		isRanked:    false, // casual, the lobby's own default
		maxPlayers:  defaultMaxPlayers,
		gameOptions: global.Games,
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
		// Enter on a setting changes it, like right, but wraps round: a key that does
		// nothing on four of the five rows reads as a broken form.
		m.cycleSetting()
	}
	return m, nil
}

// cycleSetting steps the highlighted setting forward and wraps from its last value to
// its first.
func (m *createModel) cycleSetting() {
	switch m.cursor {
	case createCursorGame:
		if n := len(m.gameOptions); n > 0 {
			m.gameIndex = (m.gameIndex + 1) % n
			m.clampMaxPlayers()
		}
	case createCursorPlayers:
		minP, maxP := m.playerBounds()
		m.maxPlayers++
		if m.maxPlayers > maxP {
			m.maxPlayers = minP
		}
	default:
		m.adjustSetting(+1)
	}
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
		minP, maxP := m.playerBounds()
		if next := m.maxPlayers + delta; next >= minP && next <= maxP {
			m.maxPlayers = next
		}
	case createCursorSubmit:
		// The submitted row has no left/right adjustment.
	}
}

// errNoGames is what the form says when no game is on offer. It is a broken
// deployment rather than a player mistake, but it still has to read as something.
var errNoGames = errors.New("no games are available right now")

// selectedGame is the highlighted game, or the zero Module when there are none to
// highlight.
func (m *createModel) selectedGame() game.Module {
	if m.gameIndex < 0 || m.gameIndex >= len(m.gameOptions) {
		return game.Module{}
	}
	return m.gameOptions[m.gameIndex]
}

func (m *createModel) createLobby() (tea.Model, tea.Cmd) {
	slug := m.selectedGame().Slug
	if slug == "" {
		m.err = errNoGames
		return m, nil
	}

	l, err := m.global.LobbyManager.CreateLobby(views.SessionPlayer(m.global),
		lobby.WithCardGame(slug),
		lobby.WithMaxPlayers(m.maxPlayers),
		lobby.WithPrivate(m.isPrivate),
		lobby.WithRanked(m.isRanked),
	)
	if err != nil {
		m.err = views.PlayerError(m.global.RequestContext(), err)
		return m, nil
	}
	return m, router.NavigateToLobby(l)
}

// defaultMaxPlayers is the seat count the form starts at, the same default the lobby
// clamps to its game when WithMaxPlayers is not given.
const defaultMaxPlayers = 4

// The seat range with no game to select, so an empty form still shows a sane row.
const (
	fallbackMinPlayers = 2
	fallbackMaxPlayers = 6
)

// playerBounds is the selected game's seat range, or the fallback range when there is
// no game to select.
func (m *createModel) playerBounds() (minP, maxP int) {
	mod := m.selectedGame()
	if mod.Slug == "" {
		return fallbackMinPlayers, fallbackMaxPlayers
	}
	return mod.MinPlayers, mod.MaxPlayers
}

func (m *createModel) clampMaxPlayers() {
	minP, maxP := m.playerBounds()
	// minP is applied last so it wins if a game's bounds ever cross.
	m.maxPlayers = max(min(m.maxPlayers, maxP), minP)
}

// renderOption is one "label: < value >" settings row, marked when the cursor is on it.
// A row nobody can change is drawn with no chevrons: "< value >" promises a key that
// works.
func renderOption(t styles.Theme, selected, adjustable bool, label, value string) string {
	cursor := "  "
	if selected {
		cursor = "> "
		label = t.PlayerItemSelected.Render(label)
		value = t.PlayerItemSelected.Render(value)
	}
	if !adjustable {
		return fmt.Sprintf("%s%s: %s", cursor, label, value)
	}
	return fmt.Sprintf("%s%s: < %s >", cursor, label, value)
}

// Each option's labels are padded to one width, so toggling a row cannot shift the
// "<" and ">" around it. "Ranked" and "Casual" already are.
func visibilityLabel(private bool) string {
	if private {
		return "Private"
	}
	return "Public "
}

func modeLabel(ranked bool) string {
	if ranked {
		return "Ranked"
	}
	return "Casual"
}

func (m *createModel) View() tea.View {
	t := m.global.Theme
	gameName := m.selectedGame().Name
	if gameName == "" {
		gameName = "none available"
	}
	gameStr := renderOption(t, m.cursor == createCursorGame, len(m.gameOptions) > 1, "Game", gameName)
	playersStr := renderOption(t, m.cursor == createCursorPlayers, true, "Max Players", strconv.Itoa(m.maxPlayers))
	visStr := renderOption(t, m.cursor == createCursorVisibility, true, "Visibility", visibilityLabel(m.isPrivate))
	modeStr := renderOption(t, m.cursor == createCursorMode, true, "Mode", modeLabel(m.isRanked))

	submitCursor := "  "
	submitText := "[ Create Lobby ]"
	if m.cursor == createCursorSubmit {
		submitCursor = "> "
		submitText = m.global.Theme.SuccessText.Render(submitText)
	}
	submitStr := fmt.Sprintf("%s%s", submitCursor, submitText)

	form := styles.JoinVertical(lg.Left,
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

	actions := []string{"up/down - Move", "left/right - Change", "enter - Change / Create"}
	return tea.NewView(views.RenderScreen(m.global, "Create New Lobby", actions,
		func(int) string { return content }))
}
