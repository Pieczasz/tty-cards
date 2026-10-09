// Package lobby holds the three lobby screens: creating a table, finding one to join,
// and the waiting room that hands its players over to the game view.
package lobby

import (
	"fmt"
	"log/slog"
	"slices"
	"strconv"

	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/lobby"
	"github.com/Pieczasz/tty-cards/internal/observability"
	"github.com/Pieczasz/tty-cards/internal/tui/components"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/views"

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

// New is the waiting room for activeLobby, subscribed to its feed. A nil lobby is a
// view that goes straight home.
func New(global router.GlobalContext, activeLobby *lobby.Lobby) tea.Model {
	m := &model{global: global, currentLobby: activeLobby}
	if activeLobby == nil {
		return m
	}
	m.syncSettings()
	playerID := views.SessionPlayerID(global)
	ch, err := activeLobby.Subscribe(playerID)
	if err != nil {
		// Without the feed this screen would never see another player join or the
		// game start, so the player is told instead of being left staring at a
		// roster that silently stops updating.
		ctx := global.RequestContext()
		slog.ErrorContext(ctx, "lobby view could not subscribe to events", "error", err, "player_id", playerID)
		observability.SubscribeFailure(ctx, "lobby_view")
		m.actionErr = fmt.Errorf("live updates unavailable, rejoin the lobby: %w", err)
	}
	m.lobbyChan = ch
	return m
}

// syncSettings copies the lobby's settings onto the form.
func (m *model) syncSettings() {
	m.isPrivate = m.currentLobby.IsPrivate()
	m.isRanked = m.currentLobby.IsRanked()
	m.maxPlayers = m.currentLobby.MaxPlayers()
}

func (m *model) Init() tea.Cmd {
	// A reconnecting player's lobby may already be mid-game: route straight back to
	// the table if their seat survived the disconnect grace. A seat the engine took
	// (idle removal) stays here and sees the roster instead.
	if m.currentLobby != nil {
		if table := m.currentLobby.Seat(views.SessionPlayerID(m.global)); table != nil {
			m.unsubscribe()
			return router.NavigateToGame(m.currentLobby.Slug(), table)
		}
	}
	return listenToLobbyBroadcaster(m.lobbyChan)
}

// rating comes from the ratings the player was seated with, which are the snapshot
// taken when they logged in - not a live read. A rating that changes mid-session shows
// up the next time they connect.
func (m *model) rating(p *game.Player) uint32 {
	return lobby.Rating(p, m.currentLobby.Slug())
}

func (m *model) unsubscribe() {
	if m.currentLobby != nil && m.lobbyChan != nil {
		m.currentLobby.Unsubscribe(views.SessionPlayerID(m.global), m.lobbyChan)
		m.lobbyChan = nil
	}
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

	self := views.SessionPlayer(m.global)
	switch msg.String() {
	case "esc", "x", "q":
		m.showLeaveConfirm = true
	case "r":
		// Debug, not Error: nearly every refusal here is the player's own (no seat, too
		// few players) and is already on their screen, so a held key would flood the log.
		m.actionErr = m.global.LobbyManager.ToggleReady(self)
		if m.actionErr != nil {
			slog.DebugContext(m.global.RequestContext(), "ready refused", "error", m.actionErr)
		}
	default:
		if m.currentLobby.IsLeader(self) {
			m.handleLeaderKey(self, msg.String())
		}
	}
	return m, nil
}

// handleLeaderKey is the form and the kick key, which only the leader may drive.
func (m *model) handleLeaderKey(self *game.Player, key string) {
	switch key {
	case "up", "k":
		m.cursor = components.StepCursor(m.cursor, -1, m.maxCursor())
	case "down", "j":
		m.cursor = components.StepCursor(m.cursor, +1, m.maxCursor())
	case "left", "h":
		m.adjustSetting(self, -1)
	case "right", "l":
		m.adjustSetting(self, +1)
	case "enter":
		guests := m.currentLobby.Guests()
		if i := m.cursor - cursorFirstGuest; i >= 0 && i < len(guests) {
			m.actionErr = m.global.LobbyManager.Kick(self, guests[i])
		}
	}
}

// goHome navigates to the home screen once. Without the guard a view with no lobby
// returns a ChangeViewMsg for every message it is handed, resize messages included,
// and the router rebuilds the home view on each one.
func (m *model) goHome() tea.Cmd {
	if m.leaving {
		return nil
	}
	m.leaving = true
	return router.Navigate(router.RouteHome)
}

func (m *model) handleLeaveConfirm(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y":
		m.global.LobbyManager.LeaveLobby(views.SessionPlayer(m.global))
		m.unsubscribe()
		cmd := m.goHome()
		return m, cmd
	case "n", "N", "esc":
		m.showLeaveConfirm = false
	}
	return m, nil
}

// adjustSetting changes the highlighted setting, and the form follows only once the
// lobby has accepted it.
func (m *model) adjustSetting(self *game.Player, delta int) {
	var err error
	switch m.cursor {
	case cursorMaxPlayers:
		rulesMin, rulesMax := gamePlayerBounds(m.global.GameRegistry, m.currentLobby.Slug())
		next := m.maxPlayers + delta
		if next < max(rulesMin, m.currentLobby.CurrentPlayers()) || next > rulesMax {
			return
		}
		if err = m.currentLobby.SetMaxPlayers(self, next); err == nil {
			m.maxPlayers = next
		}
	case cursorVisibility:
		if err = m.currentLobby.SetPrivate(self, !m.isPrivate); err == nil {
			m.isPrivate = !m.isPrivate
		}
	case cursorMode:
		if err = m.currentLobby.SetRanked(self, !m.isRanked); err == nil {
			m.isRanked = !m.isRanked
		}
	default:
		// cursorGame and the guest rows: the game is fixed once a lobby exists and a
		// guest row has no left/right adjustment, so both rows ignore it.
		return
	}
	m.actionErr = err
}

func (m *model) handleLobbyEvent(msg lobby.Event) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case lobby.EventLobbyClosed:
		m.unsubscribe()
		cmd := m.goHome()
		return m, cmd
	case lobby.EventGameStarted:
		table := msg.Seat(views.SessionPlayerID(m.global))
		if table == nil {
			slog.ErrorContext(m.global.RequestContext(), "GAME_STARTED carried no game engine")
			return m, listenToLobbyBroadcaster(m.lobbyChan)
		}
		m.unsubscribe()
		return m, router.NavigateToGame(m.currentLobby.Slug(), table)
	case lobby.EventSettingsUpdated, lobby.EventPlayersUpdated:
		self := views.SessionPlayer(m.global)
		if !m.currentLobby.Leader().Equal(self) && !slices.ContainsFunc(m.currentLobby.Guests(), self.Equal) {
			m.unsubscribe()
			cmd := m.goHome()
			return m, cmd
		}
		m.syncSettings()
		m.cursor = components.StepCursor(m.cursor, 0, m.maxCursor())
	}
	return m, listenToLobbyBroadcaster(m.lobbyChan)
}

func (m *model) View() tea.View {
	if m.currentLobby == nil {
		return tea.NewView("No active lobby.")
	}

	actions := []string{"x - Leave Lobby", "r - Ready"}
	return tea.NewView(views.RenderSeatedScreen(m.global, "Lobby", actions, func(height int) string {
		if m.showLeaveConfirm {
			redYes := m.global.Theme.ErrorText.Bold(true).Render("Yes")
			return fmt.Sprintf("Are you sure you want to leave the lobby?\n\n[y] %s   [n] No", redYes)
		}

		innerWidth := styles.InnerWidth(m.global.Width)
		isLeader := m.currentLobby.IsLeader(views.SessionPlayer(m.global))
		if m.actionErr == nil {
			return m.renderForm(isLeader, innerWidth, height)
		}
		// Wrapped before it is measured: on a narrow terminal the message is two rows,
		// and a budget that assumed one puts the form back over the frame.
		errText := m.global.Theme.ErrorText.Width(innerWidth).Render(m.actionErr.Error())
		form := m.renderForm(isLeader, innerWidth, height-lg.Height(errText)-1)
		return styles.JoinVertical(lg.Center, form, "", errText)
	}))
}

// settingsRows is the fixed height of renderSettings: the heading, the lobby code and
// the four option rows. The roster is the only part of this screen that grows, so it
// is the part that gives when the terminal cannot hold both.
const settingsRows = 6

// columnGap is the blank columns between the side-by-side settings and roster. The fit
// check and the layout both use it: checked narrower than laid out, a roster row that
// did not fit was set beside the settings anyway and lipgloss wrapped its tail.
const columnGap = 6

// stackedGap is the blank rows between the stacked columns. It is the first thing the
// roster takes back on a terminal too short for both.
const stackedGap = 2

// renderForm lays the settings and player columns side by side, stacking them
// vertically when they would not fit innerWidth: lipgloss word-wraps the columns
// rather than shrinking them. maxHeight is the rows RenderScreen has to spare.
func (m *model) renderForm(isLeader bool, innerWidth, maxHeight int) string {
	settingsStack := m.renderSettings(isLeader)
	rows := m.renderPlayerList(isLeader)

	if lg.Width(settingsStack)+columnGap+lg.Width(styles.JoinVertical(lg.Left, rows...)) > innerWidth {
		// Stacked: the settings take their rows first and the roster gets the rest.
		gap := stackedGap
		if maxHeight-settingsRows-gap < 2 {
			gap = 0
		}
		players := styles.JoinVertical(lg.Left, capRoster(rows, maxHeight-settingsRows-gap)...)
		settingsCol := lg.NewStyle().Align(lg.Left).Render(settingsStack)
		playersCol := lg.NewStyle().Align(lg.Left).MarginTop(gap).Render(players)
		return styles.JoinVertical(lg.Left, settingsCol, playersCol)
	}
	// Side by side, so the roster has the whole height to itself.
	players := styles.JoinVertical(lg.Left, capRoster(rows, maxHeight)...)
	settingsCol := lg.NewStyle().Align(lg.Left).MarginRight(columnGap).Render(settingsStack)
	playersCol := lg.NewStyle().Align(lg.Left).Render(players)
	return lg.NewStyle().Align(lg.Center).Render(styles.JoinHorizontal(lg.Top, settingsCol, playersCol))
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
	return styles.JoinVertical(lg.Left,
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
