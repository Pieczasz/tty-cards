package uno

import (
	"image/color"
	"strings"

	"github.com/Pieczasz/tty-cards/internal/deck"
	logic "github.com/Pieczasz/tty-cards/internal/game/uno"
	"github.com/Pieczasz/tty-cards/internal/tui/components"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/views/gameview"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

func (m *model) View() tea.View {
	colorRow := m.renderHandColorRow(gameview.HandWidth(m.Global.Width), gameview.HandRows(m.Global.Height))
	return m.RenderTable(m.renderCenterTable(), colorRow)
}

func (m *model) renderCenterTable() string {
	discard := components.RenderCard(m.Global.Palette(), m.Base.TopDiscard, false)
	return styles.JoinVertical(lg.Center,
		gameview.RenderPiles(m.Global.Palette(), m.Base.DeckSize, discard),
		m.renderCurrentColorIndicator(),
		m.renderDirectionIndicator(),
	)
}

// renderCurrentColorIndicator names the colour to follow the way the picker does, so
// the colour a player picked reads back the same on the table.
func (m *model) renderCurrentColorIndicator() string {
	c, ok := colorPicker.Lookup(m.currentColor)
	if !ok {
		return ""
	}
	return m.Global.Palette().Muted.Render("Color: ") +
		lg.NewStyle().Bold(true).Foreground(c.Tone(m.Global.Palette())).Render(c.Label)
}

func (m *model) renderDirectionIndicator() string {
	dir := "⟳ Clockwise"
	if m.direction < 0 {
		dir = "⟲ Counterclockwise"
	}
	return m.Global.Palette().Dim.Render(dir)
}

// renderHandColorRow paints a Uno color glyph above each card so four colors stay
// distinct without changing shared suitStyle (which only has red/dark). The glyphs
// sit at card-slot spacing, so the row exists only when the hand below it is a fan:
// the strip the hand falls back to on a short or narrow terminal has no card columns
// to sit over, and a row drawn anyway lines up with nothing. Height decides that as
// much as width - 24-row terminals are the common case, not an edge one.
func (m *model) renderHandColorRow(maxWidth, maxRows int) string {
	hand := m.Base.Hand
	n := len(hand)
	if !gameview.FansHand(n, maxWidth, maxRows) {
		return ""
	}
	tuck := components.FanTuck(n, maxWidth)
	selected := components.Selection(m.HandCursor())
	parts := make([]string, 0, n)
	for i, c := range hand {
		w := components.CardSlotWidth(i, n, tuck, selected)
		glyph, fg := colorGlyph(m.Global.Palette(), c)
		cell := lg.NewStyle().Foreground(fg).Width(w).Align(lg.Center).Render(glyph)
		parts = append(parts, cell)
	}
	return strings.Join(parts, "")
}

func colorGlyph(t *styles.Theme, c deck.Card) (string, color.Color) {
	if logic.IsWild(c) {
		return "★", t.TextMuted
	}
	if choice, ok := colorPicker.Lookup(c.Suit); ok {
		return "●", choice.Tone(t)
	}
	return "·", t.TextMuted
}
