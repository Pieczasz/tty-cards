package hearts

import (
	"fmt"

	"github.com/Pieczasz/terminal-card/internal/game"
	logic "github.com/Pieczasz/terminal-card/internal/game/hearts"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/views/gameview"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

const (
	keyHintsPlay = "<-/h: left | ->/l: right | enter: play | esc: leave"
	//nolint:gosec // G101: "Pass" is the card pass, not a credential
	keyHintsPass = "<-/h: left | ->/l: right | space: toggle | enter: pass 3 | esc: leave"
	keyHintsOver = "enter: next hand | esc: leave match"
)

func (m *model) View() tea.View {
	if screen, ok := m.LeaveConfirmScreen(); ok {
		return tea.NewView(screen)
	}
	if m.handComplete || m.matchComplete || m.phase == logic.PhaseHandOver {
		return tea.NewView(m.renderHandOver())
	}
	if m.Base.Phase != game.Playing {
		return tea.NewView(gameview.RenderWaitingScreen(m.Global, m.Base.Phase, m.Base.WinnerName))
	}

	// Seat art is the first thing to go: it costs seven rows per seat, and a name
	// with a hand count says everything a player reads off somebody else's seat.
	minimalSeats := gameview.IsCompact(m.Global.Width, m.Global.Height)

	return tea.NewView(gameview.RenderBands(m.Global,
		m.renderTopOpponent(minimalSeats), m.renderPlayerSection(), m.keyHints(),
		func(height int) string { return m.renderMiddleLayer(height, minimalSeats) }))
}

func (m *model) keyHints() string {
	if m.phase == logic.PhasePassing {
		return keyHintsPass
	}
	return keyHintsPlay
}

func (m *model) renderMiddleLayer(height int, minimalSeats bool) string {
	leftOpponent := m.renderSideOpponent(seatLeft, minimalSeats, height)
	rightOpponent := m.renderSideOpponent(seatRight, minimalSeats, height)
	centerStack := lg.JoinVertical(lg.Center,
		m.renderTrickArea(height),
		m.renderHeartsBrokenIndicator(),
		m.renderPassDirection(),
	)

	return gameview.RenderTableRow(m.Global.Width, height,
		leftOpponent, lg.NewStyle().MarginTop(1).Render(centerStack), rightOpponent)
}

// heartsSeats is the table the art layout is drawn for: one opponent on each of the
// three edges around the hero.
const heartsSeats = 4

// The three edges an opponent is drawn on, in clockwise order from the hero: the seat
// that plays next sits on the left.

const (
	seatLeft = iota
	seatTop
	seatRight
)

// opponentAt is the opponent on edge rel: seatLeft, seatTop or seatRight.
//
// It only answers for a full table with the hero at it: Opponents, which runs clockwise
// from the hero's left, is then exactly the three edges. Hearts deals four, but a seat
// stays empty for as long as it takes the engine to end the match after somebody
// leaves, and that is a frame the view still has to render; a session with no seat
// sees every seat as an opponent and has no edge of its own to draw them around.
func (m *model) opponentAt(rel int) (game.PlayerSnapshot, bool) {
	opponents := m.Base.Opponents
	if len(m.Base.Seats) != heartsSeats || len(opponents) != heartsSeats-1 || rel < 0 || rel >= len(opponents) {
		return game.PlayerSnapshot{}, false
	}
	return opponents[rel], true
}

func (m *model) renderTopOpponent(minimal bool) string {
	o, ok := m.opponentAt(seatTop)
	if !ok {
		// Off the art layout: name every opponent on one line rather than leave the
		// edge blank, which would hide players who are still holding cards.
		return m.renderSeatSummary()
	}
	isTurn := m.Base.CurrentPlayerID == o.ID
	if minimal {
		return gameview.RenderOpponentMinimal(m.Global.Theme, o, isTurn)
	}
	return gameview.RenderOpponent(m.Global.Theme, o, isTurn, gameview.OrientationTop,
		m.Base.TurnRemaining, m.Global.Width)
}

// renderSeatSummary is the degraded layout for a table that is not four-handed: names
// and hand counts, laid out along the top edge.
func (m *model) renderSeatSummary() string {
	if len(m.Base.Opponents) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m.Base.Opponents))
	for _, o := range m.Base.Opponents {
		parts = append(parts, gameview.RenderOpponentMinimal(
			m.Global.Theme, o, m.Base.CurrentPlayerID == o.ID), "  ")
	}
	return lg.JoinHorizontal(lg.Center, parts...)
}

func (m *model) renderSideOpponent(rel int, minimal bool, height int) string {
	o, ok := m.opponentAt(rel)
	if !ok {
		return ""
	}
	isTurn := m.Base.CurrentPlayerID == o.ID
	orient := gameview.OrientationLeft
	if rel == seatRight {
		orient = gameview.OrientationRight
	}
	if minimal {
		return gameview.RenderOpponentMinimal(m.Global.Theme, o, isTurn)
	}
	// Two of the rows go to the seat's name and hand count.
	return gameview.RenderOpponent(m.Global.Theme, o, isTurn, orient, m.Base.TurnRemaining, height-2)
}

func (m *model) opponentID(rel int) string {
	o, ok := m.opponentAt(rel)
	if !ok {
		return ""
	}
	return o.ID
}

// trickArtRows is what the trick costs drawn with card faces: three rows of them, a
// framed card each, plus the two indicator lines under the cross.
const trickArtRows = 3*(components.FaceHeight+3) + 2

// renderTrickArea draws the trick as a cross, the hero's card nearest them.
//
// Faces only when the middle band can hold three rows of them, mini cards otherwise.
// The four cards of a trick are the one thing in Hearts a player cannot play without
// seeing - which card led, whether hearts are in - so a trick that does not fit has to
// shrink rather than be cut off at the band's edge.
func (m *model) renderTrickArea(height int) string {
	mini := height > 0 && height < trickArtRows

	hero := m.renderTrickSlot(m.Bound.PlayerID(), mini)
	left := m.renderTrickSlot(m.opponentID(seatLeft), mini)
	top := m.renderTrickSlot(m.opponentID(seatTop), mini)
	right := m.renderTrickSlot(m.opponentID(seatRight), mini)
	return lg.JoinVertical(lg.Center,
		top,
		lg.JoinHorizontal(lg.Center, left, "  ", right),
		hero,
	)
}

func (m *model) renderTrickSlot(playerID string, mini bool) string {
	card, ok := m.trickCards[playerID]
	if playerID == "" || !ok {
		if mini {
			return components.MiniCardSlot(m.Global.Theme)
		}
		return m.Global.Theme.Dim.Render(" · ")
	}
	if mini {
		return components.RenderMiniCard(m.Global.Theme, card)
	}
	return components.RenderCard(m.Global.Theme, card, false)
}

func (m *model) renderHeartsBrokenIndicator() string {
	if m.heartsBroken {
		return lg.NewStyle().Foreground(m.Global.Theme.Warning).Render("♥ Hearts: broken")
	}
	return m.Global.Theme.Muted.Render("♥ Hearts: not yet broken")
}

func (m *model) renderPassDirection() string {
	if m.phase != logic.PhasePassing {
		return ""
	}
	// PassDirection.String is the one label table for the enum; a hold hand never
	// reaches the passing stage, so its "hold" label cannot show here anyway.
	return m.Global.Theme.Dim.Render("Pass: " + m.passDirection.String())
}

func (m *model) renderPlayerSection() string {
	statusView := gameview.RenderStatus(m.Global.Theme, m.Base.CurrentPlayerName, m.Base.MyTurn, m.Base.TurnRemaining)
	handWidth := gameview.HandWidth(m.Global.Width)
	handRows := gameview.HandRows(m.Global.Height)
	var handView string
	if m.phase == logic.PhasePassing {
		handView = gameview.RenderHandMulti(m.Global.Theme, m.Base.Hand, m.passIndices(),
			m.Selected, handWidth, handRows)
	} else {
		handView = gameview.RenderHand(m.Global.Theme, m.Base.Hand, m.Selected, false, handWidth, handRows)
	}

	return gameview.RenderHeroBand(m.Global.Theme, m.ActionErr, statusView, handView)
}

func (m *model) renderHandOver() string {
	h := gameview.HandOver{Title: fmt.Sprintf("HAND %d COMPLETE", m.handNumber), Hint: keyHintsOver}
	if m.matchComplete || m.Base.Phase == game.Finished {
		h.Title, h.Hint = gameview.MatchOverTitle(m.Base.WinnerName), gameview.LobbyHint
	}

	for _, seat := range m.Base.Seats {
		h.Rows = append(h.Rows, m.Global.Theme.Muted.Render(fmt.Sprintf("%-12s  hand %3d  total %3d",
			styles.PadTruncate(seat.Name, 12), m.handPoints[seat.ID], m.cumulativeScores[seat.ID])))
	}
	return gameview.RenderHandOver(m.Global, h)
}
