package lobby

import (
	"fmt"
	"log/slog"
	"slices"
	"strconv"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/lobby"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/views"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

// lobbyMsg carries the feed that delivered it: the router rebuilds this view on every
// visit, and a listener in flight from the last one would otherwise re-arm itself here.
type lobbyMsg struct {
	lobby.Event
	src <-chan lobby.Event
}

type model struct {
	global       router.GlobalContext
	currentLobby *lobby.Lobby
	lobbyChan    <-chan lobby.Event

	// leaving is set once this view has asked the router to take the player
	// somewhere else, so it asks once rather than on every message that arrives
	// afterwards - a resize alone would otherwise re-issue the navigation.
	leaving bool

	cursor           int
	isPrivate        bool
	isRanked         bool
	maxPlayers       int
	showLeaveConfirm bool
	actionErr        error
}

func listenToLobbyBroadcaster(ch <-chan lobby.Event) tea.Cmd {
	return views.ListenOn(ch, func(ev lobby.Event) tea.Msg { return lobbyMsg{Event: ev, src: ch} })
}

// New returns a new lobby model. We pass the current active lobby through Context.
func New(global router.GlobalContext, activeLobby *lobby.Lobby) tea.Model {
	playerID := views.SessionPlayerID(global)
	var ch <-chan lobby.Event
	var subErr error
	if activeLobby != nil {
		ch, subErr = activeLobby.Subscribe(playerID)
		if subErr != nil {
			// Without the feed this screen would never see another player join or the
			// game start, so the player is told instead of being left staring at a
			// roster that silently stops updating.
			slog.Error("lobby view could not subscribe to events", "error", subErr, "player_id", playerID)
			subErr = fmt.Errorf("live updates unavailable, rejoin the lobby: %w", subErr)
		}
	}
	isPrivate := true
	isRanked := false
	maxPlayers := defaultMaxPlayers
	if activeLobby != nil {
		isPrivate = activeLobby.IsPrivate()
		isRanked = activeLobby.IsRanked()
		maxPlayers = activeLobby.MaxPlayers()
	}
	return &model{
		global:       global,
		currentLobby: activeLobby,
		lobbyChan:    ch,
		cursor:       0,
		isPrivate:    isPrivate,
		isRanked:     isRanked,
		maxPlayers:   maxPlayers,
		actionErr:    subErr,
	}
}

func (m *model) Init() tea.Cmd {
	// A reconnecting player's lobby may already be mid-game: route straight back to
	// the table if their seat survived the disconnect grace. A seat the engine took
	// (idle removal) stays here and sees the roster instead.
	if m.currentLobby != nil {
		if engine := m.currentLobby.ActiveGame(); engine != nil && m.seatedIn(engine) {
			if mod, ok := m.global.GameRegistry.Module(m.currentLobby.GameName()); ok {
				m.unsubscribe()
				return func() tea.Msg {
					return router.ChangeViewMsg{ViewName: router.GameRoute(mod.Slug), Context: engine}
				}
			}
		}
	}
	return listenToLobbyBroadcaster(m.lobbyChan)
}

// seatedIn is a seat at a live table. A finished engine still lists its seats, and the
// lobby reopens on its own goroutine, so ActiveGame can hand one back for a moment:
// routing there shows a game-over screen whose esc lands straight back here.
func (m *model) seatedIn(engine *game.Engine) bool {
	if engine.IsFinished() {
		return false
	}
	me := views.SessionPlayerID(m.global)
	return slices.ContainsFunc(engine.Snapshot().Players, func(p game.PlayerSnapshot) bool {
		return p.ID == me
	})
}

// rating comes from the ratings the player was seated with, which are the snapshot
// taken when they logged in - not a live read. A rating that changes mid-session shows
// up the next time they connect.
func (m *model) rating(p *game.Player) uint32 {
	return lobby.Rating(p, m.currentLobby.GameName())
}

func (m *model) unsubscribe() {
	if m.currentLobby != nil && m.lobbyChan != nil {
		m.currentLobby.Unsubscribe(views.SessionPlayerID(m.global), m.lobbyChan)
		m.lobbyChan = nil
	}
}

func (m *model) selfPlayer() *game.Player {
	return views.SessionPlayer(m.global)
}

// maxCursor is the last row the cursor can sit on: the settings, then one row per guest.
func (m *model) maxCursor() int {
	return cursorMode + len(m.currentLobby.Guests())
}

const (
	cursorGame = iota
	cursorMaxPlayers
	cursorVisibility
	cursorMode
	cursorFirstGuest
)

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.currentLobby == nil {
		// Separate statement on purpose: m is returned by value and goHome mutates it
		// through the pointer receiver; the order of those two in one return is unspecified.
		cmd := m.goHome()
		return m, cmd
	}
	if handled, cmd := views.HandleCommonMsg(msg, &m.global); handled {
		return m, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case lobbyMsg:
		if msg.src != m.lobbyChan {
			return m, nil
		}
		return m.handleLobbyEvent(msg.Event)
	}
	return m, nil
}

func (m *model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.showLeaveConfirm {
		return m.handleLeaveConfirm(msg.String())
	}

	self := m.selfPlayer()
	isLeader := m.currentLobby.IsLeader(self)

	// Leaving via a global shortcut has to release the lobby subscription first.
	if route, ok := views.GlobalRoute(msg.String()); ok {
		m.unsubscribe()
		return m, func() tea.Msg { return router.ChangeViewMsg{ViewName: route} }
	}

	switch msg.String() {
	case "esc", "x", "q":
		m.showLeaveConfirm = true
		return m, nil
	case "r":
		if err := m.currentLobby.ToggleReady(self, m.global.GameRegistry); err != nil {
			m.actionErr = err
			slog.Error("failed to toggle ready or start game engine", "error", err)
		} else {
			m.actionErr = nil
		}
		return m, nil
	case "up", "k":
		if isLeader {
			m.cursor = components.StepCursor(m.cursor, -1, m.maxCursor())
		}
	case "down", "j":
		if isLeader {
			m.cursor = components.StepCursor(m.cursor, +1, m.maxCursor())
		}
	case "left", "h":
		if isLeader {
			m.adjustSetting(self, -1)
		}
	case "right", "l":
		if isLeader {
			m.adjustSetting(self, +1)
		}
	case "enter":
		if isLeader && m.cursor >= cursorFirstGuest {
			guestIdx := m.cursor - cursorFirstGuest
			guests := m.currentLobby.Guests()
			if guestIdx < len(guests) {
				if err := m.global.LobbyManager.Kick(self, guests[guestIdx]); err != nil {
					slog.Error("failed to kick player", "error", err)
				}
			}
		}
	}
	return m, nil
}

// goHome navigates to the home screen once. Without the guard a view with no lobby
// returns a ChangeViewMsg for every message it is handed, resize messages included,
// and the router rebuilds the home view on each one.
func (m *model) goHome() tea.Cmd {
	if m.leaving {
		return nil
	}
	m.leaving = true
	return func() tea.Msg { return router.ChangeViewMsg{ViewName: router.RouteHome} }
}

func (m *model) handleLeaveConfirm(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y":
		m.global.LobbyManager.LeaveLobby(m.selfPlayer())
		m.unsubscribe()
		// Separate statement on purpose: m is returned by value and goHome mutates it
		// through the pointer receiver; the order of those two in one return is unspecified.
		cmd := m.goHome()
		return m, cmd
	case "n", "N", "esc":
		m.showLeaveConfirm = false
	}
	return m, nil
}

func (m *model) adjustSetting(self *game.Player, delta int) {
	switch m.cursor {
	case cursorMaxPlayers:
		rulesMin, rulesMax := gamePlayerBounds(m.global.GameRegistry, m.currentLobby.GameName())
		next := m.maxPlayers + delta
		if delta < 0 && (next < rulesMin || next < m.currentLobby.CurrentPlayers()) {
			return
		}
		if delta > 0 && next > rulesMax {
			return
		}
		m.maxPlayers = next
		if err := m.currentLobby.SetMaxPlayers(self, m.maxPlayers, rulesMin, rulesMax); err != nil {
			m.maxPlayers -= delta
			slog.Error("failed to set max players", "error", err)
		}
	case cursorVisibility:
		m.isPrivate = !m.isPrivate
		if err := m.currentLobby.SetPrivate(self, m.isPrivate); err != nil {
			m.isPrivate = !m.isPrivate
			slog.Error("failed to set privacy", "error", err)
		}
	case cursorMode:
		m.isRanked = !m.isRanked
		if err := m.currentLobby.SetRanked(self, m.isRanked); err != nil {
			m.isRanked = !m.isRanked
			slog.Error("failed to set ranked mode", "error", err)
		}
	default:
		// cursorGame and the guest rows: the game is fixed once a lobby exists and a
		// guest row has no left/right adjustment, so both rows ignore it.
	}
}

func (m *model) handleLobbyEvent(msg lobby.Event) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case lobby.EventLobbyClosed:
		m.unsubscribe()
		// Separate statement on purpose: m is returned by value and goHome mutates it
		// through the pointer receiver; the order of those two in one return is unspecified.
		cmd := m.goHome()
		return m, cmd
	case lobby.EventGameStarted:
		engine := msg.Engine
		if engine == nil {
			slog.Error("GAME_STARTED carried no game engine")
			return m, listenToLobbyBroadcaster(m.lobbyChan)
		}
		mod, ok := m.global.GameRegistry.Module(m.currentLobby.GameName())
		if !ok {
			// Keep the listener armed: an unregistered game is no reason to eject
			// the player, and returning a nil command would leave this view deaf to
			// every later lobby event while still holding its subscriber slot.
			slog.Error("game not registered, cannot route to its view", "game", m.currentLobby.GameName())
			return m, listenToLobbyBroadcaster(m.lobbyChan)
		}
		m.unsubscribe()
		return m, func() tea.Msg {
			return router.ChangeViewMsg{ViewName: router.GameRoute(mod.Slug), Context: engine}
		}
	case lobby.EventSettingsUpdated, lobby.EventPlayersUpdated:
		self := m.selfPlayer()
		if !m.currentLobby.Leader().Equal(self) {
			found := false
			for _, g := range m.currentLobby.Guests() {
				if g.Equal(self) {
					found = true
					break
				}
			}
			if !found {
				m.unsubscribe()
				// Separate statement on purpose: m is returned by value and goHome mutates it
				// through the pointer receiver; the order of those two in one return is unspecified.
				cmd := m.goHome()
				return m, cmd
			}
		}
		m.isPrivate = m.currentLobby.IsPrivate()
		m.isRanked = m.currentLobby.IsRanked()
		m.maxPlayers = m.currentLobby.MaxPlayers()
		m.cursor = components.StepCursor(m.cursor, 0, m.maxCursor())
	}
	return m, listenToLobbyBroadcaster(m.lobbyChan)
}

func (m *model) View() tea.View {
	if m.currentLobby == nil {
		return tea.NewView("No active lobby.")
	}

	actions := []string{"x - Leave Lobby", "r - Ready"}
	return tea.NewView(views.RenderScreen(m.global, "Lobby", actions, func(height int) string {
		if m.showLeaveConfirm {
			redYes := m.global.Theme.ErrorText.Bold(true).Render("Yes")
			return fmt.Sprintf("Are you sure you want to leave the lobby?\n\n[y] %s   [n] No", redYes)
		}

		innerWidth := styles.InnerWidth(m.global.Width)
		isLeader := m.currentLobby.IsLeader(m.selfPlayer())
		if m.actionErr == nil {
			return m.renderForm(isLeader, innerWidth, height)
		}
		// Wrapped before it is measured: on a narrow terminal the message is two rows,
		// and a budget that assumed one puts the form back over the frame.
		errText := m.global.Theme.ErrorText.Width(innerWidth).Render(m.actionErr.Error())
		form := m.renderForm(isLeader, innerWidth, height-lg.Height(errText)-1)
		return lg.JoinVertical(lg.Center, form, "", errText)
	}))
}

// settingsRows is the fixed height of renderSettings: the heading, the lobby code and
// the four option rows. The roster is the only part of this screen that grows, so it
// is the part that gives when the terminal cannot hold both.
const settingsRows = 6

// stackedGap is the blank rows between the stacked columns. It is the first thing the
// roster takes back on a terminal too short for both.
const stackedGap = 2

// renderForm lays the settings and player columns side by side, stacking them
// vertically when they would not fit innerWidth: lipgloss word-wraps the columns
// rather than shrinking them. maxHeight is the rows RenderScreen has to spare.
func (m *model) renderForm(isLeader bool, innerWidth, maxHeight int) string {
	settingsStack := m.renderSettings(isLeader)
	rows := m.renderPlayerList(isLeader)

	if lg.Width(settingsStack)+lg.Width(lg.JoinVertical(lg.Left, rows...))+4 > innerWidth {
		// Stacked: the settings take their rows first and the roster gets the rest.
		gap := stackedGap
		if maxHeight-settingsRows-gap < 2 {
			gap = 0
		}
		players := lg.JoinVertical(lg.Left, capRoster(rows, maxHeight-settingsRows-gap)...)
		settingsCol := lg.NewStyle().Align(lg.Left).Render(settingsStack)
		playersCol := lg.NewStyle().Align(lg.Left).MarginTop(gap).Render(players)
		return lg.JoinVertical(lg.Left, settingsCol, playersCol)
	}
	// Side by side, so the roster has the whole height to itself.
	players := lg.JoinVertical(lg.Left, capRoster(rows, maxHeight)...)
	settingsCol := lg.NewStyle().Align(lg.Left).MarginRight(6).Render(settingsStack)
	playersCol := lg.NewStyle().Align(lg.Left).Render(players)
	return lg.NewStyle().Align(lg.Center).Render(lg.JoinHorizontal(lg.Top, settingsCol, playersCol))
}

// capRoster trims the player list to the rows it was given and says how many seats it
// hid. Rendering past the frame instead hands the overflow to the terminal to wrap,
// which shifts every row above it - including the lobby code friends join by.
func capRoster(rows []string, maxRows int) []string {
	switch {
	case maxRows >= len(rows):
		return rows
	case maxRows <= 0:
		return nil
	}
	keep := maxRows - 1
	return append(rows[:keep:keep], fmt.Sprintf("  ... and %d more", len(rows)-keep))
}

func (m *model) renderSettings(isLeader bool) string {
	t := m.global.Theme
	on := func(row int) bool { return isLeader && m.cursor == row }
	return lg.JoinVertical(lg.Left,
		"  "+t.SectionHeading.Render("Settings"),
		"  Lobby Code: "+t.LobbyCode.Render(m.currentLobby.Code()),
		renderOption(t, on(cursorGame), "Game", m.currentLobby.GameName()),
		renderOption(t, on(cursorMaxPlayers), "Max Players", strconv.Itoa(m.maxPlayers)),
		renderOption(t, on(cursorVisibility), "Visibility", visibilityLabel(m.isPrivate)),
		renderOption(t, on(cursorMode), "Mode", modeLabel(m.isRanked)),
	)
}

// renderPlayerList returns the heading, the leader row, then one row per guest.
// Only the leader gets a cursor, since only they can kick.
func (m *model) renderPlayerList(isLeader bool) []string {
	guests := m.currentLobby.Guests()
	rows := make([]string, 0, 2+len(guests))
	rows = append(rows, "  "+m.global.Theme.SectionHeading.Render("Players"))

	leader := m.currentLobby.Leader()
	rows = append(rows, fmt.Sprintf("  %s %s (Elo: %d)%s",
		m.global.Theme.HostTag.Render("[Leader]"), leader.DisplayName(), m.rating(leader), m.readyMark(leader)))

	for i, g := range guests {
		cursor := "  "
		isSelected := isLeader && m.cursor == i+cursorFirstGuest
		if isSelected {
			cursor = "> "
		}
		row := fmt.Sprintf("%s%s %s (Elo: %d)%s",
			cursor, m.global.Theme.GuestTag.Render("[Guest] "), g.DisplayName(), m.rating(g), m.readyMark(g))
		if isSelected {
			row = m.global.Theme.PlayerItemSelected.Render(row)
		}
		rows = append(rows, row)
	}
	return rows
}

func (m *model) readyMark(p *game.Player) string {
	if !m.currentLobby.IsReady(p) {
		return ""
	}
	return m.global.Theme.SuccessText.Render(" - Ready")
}

// Close releases the lobby subscription when the router replaces this view or the
// session ends.
func (m *model) Close() {
	m.unsubscribe()
}
