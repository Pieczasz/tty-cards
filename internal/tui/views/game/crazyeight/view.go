package crazyeight

import (
	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

const keyHints = gameview.HandKeyHints

// suitChoices is the picker: the label and the suit it stands for are one entry, so
// the cell under the cursor cannot mean a different suit from the one on screen.
var suitChoices = []struct {
	label string
	suit  deck.Suit
}{
	{"♠ Spades", deck.Spades},
	{"♥︎ Hearts", deck.Hearts},
	{"♦ Diamonds", deck.Diamonds},
	{"♣ Clubs", deck.Clubs},
}

func (m *Model) View() tea.View {
	if screen, ok := m.LeaveConfirmScreen(); ok {
		return tea.NewView(screen)
	}
	if m.Base.Phase != game.Playing {
		return tea.NewView(gameview.RenderWaitingScreen(m.Global, m.Base.Phase, m.Base.Winner))
	}

	// Seat art is the first thing to go: it costs seven rows per seat, and a name
	// with a hand count says everything a player reads off somebody else's seat.
	minimalSeats := gameview.IsCompact(m.Global.Width, m.Global.Height)
	top := gameview.RenderOpponentTop(m.Global.Theme, m.Base, m.Global.Width, minimalSeats)

	return tea.NewView(gameview.RenderBands(m.Global, top, m.renderPlayerSection(), keyHints,
		func(height int) string { return m.renderMiddleLayer(height, minimalSeats) }))
}

func (m *Model) renderMiddleLayer(height int, minimalSeats bool) string {
	var centerStack string
	if m.pickingSuit {
		centerStack = m.renderSuitPicker()
	} else {
		centerStack = m.renderCenterTable()
	}

	left, right := gameview.RenderOpponentSides(m.Global.Theme, m.Base, height, minimalSeats)

	return gameview.RenderTableRow(m.Global.Width, height,
		left, lg.NewStyle().MarginTop(1).Render(centerStack), right)
}

func (m *Model) renderCenterTable() string {
	discardView := components.RenderCard(m.Global.Theme, m.Base.TopDiscard, false)
	currentSuitView := m.renderCurrentSuitIndicator()
	return lg.JoinVertical(lg.Center, discardView, currentSuitView)
}

func (m *Model) renderCurrentSuitIndicator() string {
	suitStr := ""
	switch m.currentSuit {
	case deck.Spades:
		suitStr = "♠ Spades"
	case deck.Hearts:
		suitStr = "♥︎ Hearts"
	case deck.Diamonds:
		suitStr = "♦ Diamonds"
	case deck.Clubs:
		suitStr = "♣ Clubs"
	case deck.NoSuit:
		suitStr = ""
	}

	if suitStr == "" {
		return ""
	}

	return m.Global.Theme.Muted.Render("Current Suit: ") +
		lg.NewStyle().Bold(true).Foreground(m.Global.Theme.Text).Render(suitStr)
}

func (m *Model) renderPlayerSection() string {
	statusView := gameview.RenderStatus(m.Global.Theme, m.Base.CurrentPlayer, m.Base.MyTurn, m.Base.TurnRemaining)
	handView := gameview.RenderHand(m.Global.Theme, m.Base.Hand, m.Selected, m.pickingSuit,
		gameview.HandWidth(m.Global.Width), gameview.HandRows(m.Global.Height))

	return gameview.RenderHeroBand(m.Global.Theme, m.lastActionErr, statusView, handView)
}

func (m *Model) renderSuitPicker() string {
	if !m.pickingSuit {
		return ""
	}
	labels := make([]string, 0, len(suitChoices))
	for _, c := range suitChoices {
		labels = append(labels, c.label)
	}
	return components.GridPicker{
		Title:  "Pick a suit:",
		Labels: labels,
		Cursor: m.suitCursor,
	}.Render(m.Global.Theme)
}
