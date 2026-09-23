package ginrummy

import (
	"fmt"
	"strings"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/ginrummy"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

func (m *model) View() tea.View {
	if screen, ok := m.LeaveConfirmScreen(); ok {
		return tea.NewView(screen)
	}
	if m.handComplete || m.matchComplete || m.phase == logic.PhaseHandOver {
		return tea.NewView(m.renderHandOver())
	}
	if m.Base.Phase != game.Playing {
		return tea.NewView(gameview.RenderWaitingScreen(m.Global, m.Base.Phase, m.Base.Winner))
	}

	// Seat art is the first thing to go: it costs seven rows, and a name with a hand
	// count says everything a player reads off the other seat.
	minimalSeat := gameview.IsCompact(m.Global.Width, m.Global.Height)

	// Gin rummy seats one opponent, which the shared top row places and budgets the
	// same way crazy eights and uno do.
	top := gameview.RenderOpponentTop(m.Global.Theme, m.Base, m.Global.Width, minimalSeat)

	return tea.NewView(gameview.RenderBands(m.Global,
		top, m.renderPlayerSection(), m.keyHints(), m.renderMiddleLayer))
}

func (m *model) keyHints() string {
	switch m.phase {
	case logic.PhaseAwaitingDraw:
		return "s: draw stock | t: take discard | esc: leave"
	case logic.PhaseAwaitingDiscard:
		return "<-/h left | ->/l right | enter: discard | k: knock | esc: leave"
	default:
		return "esc: leave"
	}
}

func (m *model) renderMiddleLayer(height int) string {
	discardView := components.RenderCard(m.Global.Theme, m.Base.TopDiscard, false)
	stockLabel := m.Global.Theme.Muted.Render(fmt.Sprintf("stock %d", m.Base.DeckSize))
	scores := m.renderScoreLine()
	center := lg.JoinVertical(lg.Center, discardView, stockLabel, scores)

	return styles.Place(m.Global.Width, height, lg.Center, lg.Center, center)
}

func (m *model) renderScoreLine() string {
	parts := make([]string, 0, len(m.Base.Seats))
	for _, seat := range m.Base.Seats {
		parts = append(parts, fmt.Sprintf("%s %d", seat.Username, m.cumulativeScores[seat.ID]))
	}
	return m.Global.Theme.Dim.Render(fmt.Sprintf("hand %d · %s", m.handNumber, strings.Join(parts, "  ")))
}

func (m *model) renderPlayerSection() string {
	statusView := gameview.RenderStatus(m.Global.Theme, m.Base.CurrentPlayer, m.Base.MyTurn, m.Base.TurnRemaining)
	handView := gameview.RenderHand(m.Global.Theme, m.Base.Hand, m.Selected, false,
		gameview.HandWidth(m.Global.Width), gameview.HandRows(m.Global.Height))

	return gameview.RenderHeroBand(m.Global.Theme, m.ActionErr, statusView, handView)
}

func (m *model) renderHandOver() string {
	h := gameview.HandOver{
		Title: fmt.Sprintf("HAND %d COMPLETE", m.handNumber),
		Body:  m.renderHandResult(),
		Hint:  "enter: deal next hand | esc: leave",
	}
	if m.matchComplete || m.Base.Phase == game.Finished {
		h.Title, h.Hint = gameview.MatchOverTitle(m.Base.Winner), gameview.LobbyHint
	}

	for _, seat := range m.Base.Seats {
		h.Rows = append(h.Rows, m.Global.Theme.Muted.Render(fmt.Sprintf("%-12s  total %3d",
			styles.PadTruncate(seat.Username, 12), m.cumulativeScores[seat.ID])))
	}
	return gameview.RenderHandOver(m.Global, h)
}

func (m *model) renderHandResult() string {
	r := m.lastHandResult
	if r == nil {
		return ""
	}

	var banner string
	switch r.Outcome {
	case logic.OutcomeWall:
		return lg.NewStyle().Foreground(m.Global.Theme.Warning).Render("WALL - stock exhausted, no score")
	case logic.OutcomeGin:
		banner = m.Global.Theme.SuccessText.Render("GIN!")
	case logic.OutcomeUndercut:
		banner = lg.NewStyle().Foreground(m.Global.Theme.Warning).Render("UNDERCUT")
	case logic.OutcomeKnock, logic.OutcomeUnknown:
		banner = m.Global.Theme.Accented.Render("KNOCK")
	}

	winnerName := m.Base.SeatNames()[r.Winner]
	delta := m.Global.Theme.SuccessText.Render(fmt.Sprintf("+%d -> %s", r.ScoreDelta, winnerName))

	knockerMelds := m.renderMeldGroups("knocker melds", r.KnockerMelds, false)
	oppDead := m.renderCardRow("opponent deadwood", r.OpponentDeadwood, false)
	laidOff := ""
	if len(r.LaidOffCards) > 0 {
		laidOff = m.renderCardRow("laid off", r.LaidOffCards, true)
	}

	return lg.JoinVertical(lg.Center, banner, delta, "", knockerMelds, oppDead, laidOff)
}

func (m *model) renderMeldGroups(label string, melds [][]deck.Card, laidOff bool) string {
	if len(melds) == 0 {
		return m.Global.Theme.Dim.Render(label + ": -")
	}
	groups := make([]string, 0, len(melds))
	for _, meld := range melds {
		kind := "SET"
		if len(meld) >= 3 && meld[0].Rank != meld[1].Rank {
			kind = "RUN"
		}
		cards := make([]string, 0, len(meld))
		for _, card := range meld {
			cards = append(cards, components.RenderCard(m.Global.Theme, card, laidOff))
		}
		box := lg.JoinHorizontal(lg.Top, cards...)
		groups = append(groups, m.Global.Theme.MeldBox.Render(
			lg.JoinVertical(lg.Left, m.Global.Theme.Dim.Render(kind), box)))
	}
	return lg.JoinVertical(lg.Left,
		m.Global.Theme.Muted.Render(label),
		lg.JoinHorizontal(lg.Top, groups...),
	)
}

func (m *model) renderCardRow(label string, cards []deck.Card, highlight bool) string {
	if len(cards) == 0 {
		return m.Global.Theme.Dim.Render(label + ": -")
	}
	parts := make([]string, 0, len(cards))
	for _, card := range cards {
		parts = append(parts, components.RenderCard(m.Global.Theme, card, highlight))
	}
	return lg.JoinVertical(lg.Left,
		m.Global.Theme.Muted.Render(label),
		lg.JoinHorizontal(lg.Top, parts...),
	)
}
