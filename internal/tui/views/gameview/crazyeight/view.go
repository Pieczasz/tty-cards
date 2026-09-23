package crazyeight

import (
	"slices"

	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/views/gameview"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

func (m *model) View() tea.View {
	if screen, ok := m.LeaveConfirmScreen(); ok {
		return tea.NewView(screen)
	}
	if m.Base.Phase != game.Playing {
		return tea.NewView(gameview.RenderWaitingScreen(m.Global, m.Base.Phase, m.Base.WinnerName))
	}

	// Seat art is the first thing to go: it costs seven rows per seat, and a name
	// with a hand count says everything a player reads off somebody else's seat.
	minimalSeats := gameview.IsCompact(m.Global.Width, m.Global.Height)
	top := gameview.RenderOpponentTop(m.Global.Theme, m.Base, m.Global.Width, minimalSeats)

	return tea.NewView(gameview.RenderBands(m.Global, top, m.renderPlayerSection(), gameview.HandKeyHints,
		func(height int) string { return m.renderMiddleLayer(height, minimalSeats) }))
}

func (m *model) renderMiddleLayer(height int, minimalSeats bool) string {
	centerStack := m.suit.Render(m.Global.Theme)
	if !m.suit.Open {
		centerStack = m.renderCenterTable()
	}

	left, right := gameview.RenderOpponentSides(m.Global.Theme, m.Base, height, minimalSeats)

	return gameview.RenderTableRow(m.Global.Width, height,
		left, lg.NewStyle().MarginTop(1).Render(centerStack), right)
}

func (m *model) renderCenterTable() string {
	discardView := components.RenderCard(m.Global.Theme, m.Base.TopDiscard, false)
	return lg.JoinVertical(lg.Center, discardView, m.renderCurrentSuitIndicator())
}

// renderCurrentSuitIndicator names the suit to follow the way the picker does, so the
// suit a player picked reads back the same on the table.
func (m *model) renderCurrentSuitIndicator() string {
	i := slices.IndexFunc(suitPicker.Choices, func(c gameview.Choice) bool { return c.Suit == m.currentSuit })
	if i < 0 {
		return ""
	}
	return m.Global.Theme.Muted.Render("Current Suit: ") +
		lg.NewStyle().Bold(true).Foreground(m.Global.Theme.Text).Render(suitPicker.Choices[i].Label)
}

func (m *model) renderPlayerSection() string {
	statusView := gameview.RenderStatus(m.Global.Theme, m.Base.CurrentPlayerName, m.Base.MyTurn, m.Base.TurnRemaining)
	handView := gameview.RenderHand(m.Global.Theme, m.Base.Hand, m.Selected, m.suit.Open,
		gameview.HandWidth(m.Global.Width), gameview.HandRows(m.Global.Height))

	return gameview.RenderHeroBand(m.Global.Theme, m.ActionErr, statusView, handView)
}
