package crazyeight

import (
	"github.com/Pieczasz/tty-cards/internal/tui/components"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/views/gameview"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

func (m *model) View() tea.View {
	return m.RenderTable(m.renderCenterTable(), "")
}

func (m *model) renderCenterTable() string {
	discard := components.RenderCard(m.Global.Palette(), m.Base.TopDiscard, false)
	return styles.JoinVertical(lg.Center, gameview.RenderPiles(m.Global.Palette(), m.Base.DeckSize, discard),
		m.renderCurrentSuitIndicator())
}

// renderCurrentSuitIndicator names the suit to follow the way the picker does, so the
// suit a player picked reads back the same on the table.
func (m *model) renderCurrentSuitIndicator() string {
	c, ok := suitPicker.Lookup(m.currentSuit)
	if !ok {
		return ""
	}
	return m.Global.Palette().Muted.Render("Current Suit: ") +
		lg.NewStyle().Bold(true).Foreground(m.Global.Palette().Text).Render(c.Label)
}
