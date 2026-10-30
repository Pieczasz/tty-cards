package ginrummy

import (
	"fmt"
	"strings"

	"github.com/Pieczasz/tty-cards/internal/deck"
	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/ginrummy"
	"github.com/Pieczasz/tty-cards/internal/tui/components"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/views/gameview"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

func (m *model) View() tea.View {
	if screen, ok := m.LeaveConfirmScreen(); ok {
		return tea.NewView(screen)
	}
	if m.phase == logic.PhaseHandOver || m.matchComplete || m.Base.Phase == game.Finished {
		return tea.NewView(m.renderHandOver())
	}
	if m.Base.Phase != game.Playing {
		return tea.NewView(gameview.RenderWaitingScreen(m.Global, m.Base.Phase, m.Base.WinnerName))
	}

	// Seat art is the first thing to go: it costs seven rows, and a name with a hand
	// count says everything a player reads off the other seat.
	minimalSeat := gameview.IsCompact(m.Global.Width, m.Global.Height)

	// Gin rummy seats one opponent, which the shared top row places and budgets the
	// same way crazy eights and uno do.
	top := gameview.RenderOpponentTop(m.Global.Palette(), m.Base, m.Global.Width, minimalSeat)

	return tea.NewView(gameview.RenderBands(m.Global,
		top, m.renderPlayerSection(), m.keyHints(), m.renderMiddleLayer))
}

func (m *model) keyHints() string {
	switch m.phase {
	case logic.PhaseAwaitingDraw:
		return "s: draw stock | t: take discard | esc: leave"
	case logic.PhaseAwaitingDiscard:
		return "<-/h: left | ->/l: right | enter: discard | k: knock | esc: leave"
	default:
		return "esc: leave"
	}
}

func (m *model) renderMiddleLayer(height int) string {
	discard := components.RenderCard(m.Global.Palette(), m.Base.TopDiscard, false)
	center := styles.JoinVertical(lg.Center,
		gameview.RenderPiles(m.Global.Palette(), m.Base.DeckSize, discard), m.renderScoreLine())

	return styles.Place(m.Global.Width, height, lg.Center, lg.Center, center)
}

func (m *model) renderScoreLine() string {
	parts := make([]string, 0, len(m.Base.Seats))
	for _, seat := range m.Base.Seats {
		parts = append(parts, fmt.Sprintf("%s %d", seat.Name, m.cumulativeScores[seat.ID]))
	}
	return m.Global.Palette().Dim.Render(fmt.Sprintf("hand %d · %s", m.handNumber, strings.Join(parts, "  ")))
}

func (m *model) renderPlayerSection() string {
	statusView := gameview.RenderStatus(m.Global.Palette(), m.Base.CurrentPlayerName, m.Base.MyTurn, m.Base.TurnRemaining)
	handView := gameview.RenderHand(m.Global.Palette(), m.Base.Hand, m.Selected, nil,
		gameview.HandWidth(m.Global.Width), gameview.HandRows(m.Global.Height))

	return gameview.RenderHeroBand(m.Global.Palette(), m.ActionErr, statusView, handView)
}

func (m *model) renderHandOver() string {
	h := gameview.HandOver{
		Title:    fmt.Sprintf("HAND %d COMPLETE", m.handNumber),
		Subtitle: m.renderOutcome(),
		Body:     m.renderHandResult(),
		Header:   fmt.Sprintf("%-12s  %9s", "player", "total"),
		Hint:     m.handOverHint(),
	}
	h.ApplyMatchEnd(m.Base.EndReason, m.matchComplete, m.Base.Phase, m.Base.WinnerName, m.leavers)

	for _, seat := range m.Base.Seats {
		h.Rows = append(h.Rows, m.Global.Palette().Muted.Render(fmt.Sprintf("%-12s  %9d",
			styles.PadTruncate(seat.Name, 12), m.cumulativeScores[seat.ID])))
	}
	return gameview.RenderHandOver(m.Global, h)
}

// handOverHint offers enter only to the seat that deals. Everyone else is waiting.
func (m *model) handOverHint() string {
	if m.Base.MyTurn {
		return "enter: deal next hand | esc: leave"
	}
	return "waiting for the next hand | esc: leave"
}

// renderOutcome is how the hand ended and who scored. It is the hand-over's subtitle
// rather than part of the card art below it, so it survives a terminal too small for
// the art.
func (m *model) renderOutcome() string {
	r := m.lastHandResult
	if r == nil {
		return ""
	}

	var banner string
	switch r.Outcome {
	case logic.OutcomeWall:
		return m.Global.Palette().WarningText.Render("WALL - stock exhausted, no score")
	case logic.OutcomeGin:
		banner = m.Global.Palette().SuccessText.Render("GIN!")
	case logic.OutcomeUndercut:
		banner = m.Global.Palette().WarningText.Render("UNDERCUT")
	case logic.OutcomeKnock, logic.OutcomeUnknown:
		banner = m.Global.Palette().Accented.Render("KNOCK")
	}

	winnerName := m.Base.SeatNames()[r.Winner]
	return banner + "  " + m.Global.Palette().SuccessText.Render(fmt.Sprintf("+%d -> %s", r.ScoreDelta, winnerName))
}

// renderHandResult is the cards behind the outcome: the knocker's melds, what the
// opponent was left holding and what they laid off. A wall has none.
func (m *model) renderHandResult() string {
	r := m.lastHandResult
	if r == nil || r.Outcome == logic.OutcomeWall {
		return ""
	}

	knockerMelds := m.renderMeldGroups("knocker melds", r.KnockerMelds)
	oppDead := m.renderCardRow("opponent deadwood", r.OpponentDeadwood, false)
	laidOff := ""
	if len(r.LaidOffCards) > 0 {
		laidOff = m.renderCardRow("laid off", r.LaidOffCards, true)
	}

	return styles.JoinVertical(lg.Center, knockerMelds, oppDead, laidOff)
}

// renderCard draws a result card as a face, or in four columns on a terminal that
// cannot hold three rows of nine-row faces next to the standings.
func (m *model) renderCard(card deck.Card, highlight bool) string {
	if gameview.IsCompact(m.Global.Width, m.Global.Height) {
		return components.RenderMiniCard(m.Global.Palette(), card)
	}
	return components.RenderCard(m.Global.Palette(), card, highlight)
}

func (m *model) renderMeldGroups(label string, melds [][]deck.Card) string {
	if len(melds) == 0 {
		return m.Global.Palette().Dim.Render(label + ": -")
	}
	groups := make([]string, 0, len(melds))
	for _, meld := range melds {
		kind := "SET"
		if len(meld) >= 3 && meld[0].Rank != meld[1].Rank {
			kind = "RUN"
		}
		cards := make([]string, 0, len(meld))
		for _, card := range meld {
			cards = append(cards, m.renderCard(card, false))
		}
		box := styles.JoinHorizontal(lg.Top, cards...)
		groups = append(groups, m.Global.Palette().MeldBox.Render(
			styles.JoinVertical(lg.Left, m.Global.Palette().Dim.Render(kind), box)))
	}
	return styles.JoinVertical(lg.Left,
		m.Global.Palette().Muted.Render(label),
		styles.JoinHorizontal(lg.Top, groups...),
	)
}

func (m *model) renderCardRow(label string, cards []deck.Card, highlight bool) string {
	if len(cards) == 0 {
		return m.Global.Palette().Dim.Render(label + ": -")
	}
	parts := make([]string, 0, len(cards))
	for _, card := range cards {
		parts = append(parts, m.renderCard(card, highlight))
	}
	return styles.JoinVertical(lg.Left,
		m.Global.Palette().Muted.Render(label),
		styles.JoinHorizontal(lg.Top, parts...),
	)
}
