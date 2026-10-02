package uno

import (
	"image/color"
	"strings"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/uno"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
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
	centerStack := m.color.Render(m.Global.Theme)
	if !m.color.Open {
		centerStack = m.renderCenterTable()
	}

	left, right := gameview.RenderOpponentSides(m.Global.Theme, m.Base, height, minimalSeats)

	return gameview.RenderTableRow(m.Global.Width, height,
		left, lg.NewStyle().MarginTop(1).Render(centerStack), right)
}

func (m *model) renderCenterTable() string {
	discardView := components.RenderCard(m.Global.Theme, m.Base.TopDiscard, false)
	return lg.JoinVertical(lg.Center,
		discardView,
		m.renderCurrentColorIndicator(),
		m.renderDirectionIndicator(),
	)
}

func (m *model) renderCurrentColorIndicator() string {
	label, fg := colorLabel(m.Global.Theme, m.currentColor)
	if label == "" {
		return ""
	}
	return m.Global.Theme.Muted.Render("Color: ") +
		lg.NewStyle().Bold(true).Foreground(fg).Render(label)
}

func (m *model) renderDirectionIndicator() string {
	dir := "⟳ Clockwise"
	if m.direction < 0 {
		dir = "⟲ Counterclockwise"
	}
	return m.Global.Theme.Dim.Render(dir)
}

func colorLabel(t styles.Theme, s deck.Suit) (string, color.Color) {
	switch s {
	case logic.ColorRed:
		return "Red", t.UnoRed
	case logic.ColorYellow:
		return "Yellow", t.UnoYellow
	case logic.ColorGreen:
		return "Green", t.UnoGreen
	case logic.ColorBlue:
		return "Blue", t.UnoBlue
	default:
		return "", t.TextMuted
	}
}

func (m *model) renderPlayerSection() string {
	statusView := gameview.RenderStatus(m.Global.Theme, m.Base.CurrentPlayerName, m.Base.MyTurn, m.Base.TurnRemaining)
	handWidth, handRows := gameview.HandWidth(m.Global.Width), gameview.HandRows(m.Global.Height)
	colorRow := m.renderHandColorRow(handWidth, handRows)
	cursor := m.Selected
	if m.color.Open {
		cursor = -1 // the picker has the keys, so no card is lifted
	}
	handView := gameview.RenderHand(m.Global.Theme, m.Base.Hand, cursor, nil, handWidth, handRows)

	return gameview.RenderHeroBand(m.Global.Theme, m.ActionErr, statusView, colorRow, handView)
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
	selected := components.Selection(m.Selected)
	if m.color.Open {
		selected = nil
	}
	parts := make([]string, 0, n)
	for i, c := range hand {
		w := components.CardSlotWidth(i, n, tuck, selected)
		glyph, fg := colorGlyph(m.Global.Theme, c)
		cell := lg.NewStyle().Foreground(fg).Width(w).Align(lg.Center).Render(glyph)
		parts = append(parts, cell)
	}
	return strings.Join(parts, "")
}

func colorGlyph(t styles.Theme, c deck.Card) (string, color.Color) {
	if c.Rank == logic.Wild || c.Rank == logic.WildDrawFour {
		return "★", t.TextMuted
	}
	switch c.Suit {
	case logic.ColorRed:
		return "●", t.UnoRed
	case logic.ColorYellow:
		return "●", t.UnoYellow
	case logic.ColorGreen:
		return "●", t.UnoGreen
	case logic.ColorBlue:
		return "●", t.UnoBlue
	default:
		return "·", t.TextMuted
	}
}
