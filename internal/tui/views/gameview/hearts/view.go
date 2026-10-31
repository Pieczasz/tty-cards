package hearts

import (
	"fmt"

	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/hearts"
	"github.com/Pieczasz/tty-cards/internal/tui/components"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"
	"github.com/Pieczasz/tty-cards/internal/tui/views/gameview"

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
	// Finished as well: a rules error ends the table mid-trick without MatchComplete,
	// and the scores still belong on screen.
	if m.phase == logic.PhaseHandOver || m.matchComplete || m.Base.Phase == game.Finished {
		return tea.NewView(m.renderHandOver())
	}
	if m.Base.Phase != game.Playing {
		return tea.NewView(gameview.RenderWaitingScreen(m.Global, m.Base.Phase, m.Base.WinnerName))
	}

	// Seat art is the first thing to go: it costs seven rows per seat, and a name
	// with a hand count says everything a player reads off somebody else's seat.
	minimalSeats := gameview.IsCompact(m.Global.Width, m.Global.Height)
	top := gameview.RenderOpponentTop(m.Global.Palette(), m.Base, m.Global.Width, minimalSeats)

	return tea.NewView(gameview.RenderBands(m.Global, top, m.renderPlayerSection(), m.keyHints(),
		func(height int) string { return m.renderMiddleLayer(height, minimalSeats) }))
}

// handOverHint offers enter only to the seat that deals. Everyone else is waiting,
// and an enter hint there is a key that does nothing.
func (m *model) handOverHint() string {
	if m.Base.MyTurn {
		return keyHintsOver
	}
	return "waiting for the next hand | esc: leave match"
}

func (m *model) keyHints() string {
	// Seats pass and play one at a time: off-turn, space and enter do nothing.
	if !m.Base.MyTurn {
		return gameview.WaitingKeyHints
	}
	if m.phase == logic.PhasePassing {
		return keyHintsPass
	}
	return keyHintsPlay
}

func (m *model) renderMiddleLayer(height int, minimalSeats bool) string {
	leftOpponent, rightOpponent := gameview.RenderOpponentSides(m.Global.Palette(), m.Base, height, minimalSeats)
	centerStack := styles.JoinVertical(lg.Center,
		m.renderTrickArea(height),
		m.renderHeartsBrokenIndicator(),
		m.renderPassDirection(),
	)

	return gameview.RenderTableRow(m.Global.Width, height,
		leftOpponent, styles.MarginTop(1, centerStack), rightOpponent)
}

// heartsSeats is the table the art layout is drawn for: one opponent on each of the
// three edges around the hero.
const heartsSeats = 4

// The three edges an opponent is drawn on, in clockwise order from the hero: the seat
// that plays next sits on the left. It is the order gameview.SplitZones seats three
// opponents in, so each trick card lands in front of the seat that played it.
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

	hero := m.renderTrickSlot(m.HeroID(), mini)
	left := m.renderTrickSlot(m.opponentID(seatLeft), mini)
	top := m.renderTrickSlot(m.opponentID(seatTop), mini)
	right := m.renderTrickSlot(m.opponentID(seatRight), mini)
	return styles.JoinVertical(lg.Center,
		top,
		styles.JoinHorizontal(lg.Center, left, "  ", right),
		hero,
	)
}

func (m *model) renderTrickSlot(playerID string, mini bool) string {
	card, ok := m.trickCards[playerID]
	if playerID == "" || !ok {
		if mini {
			return components.MiniCardSlot(m.Global.Palette())
		}
		return components.EmptyCardSlot(m.Global.Palette())
	}
	if mini {
		return components.RenderMiniCard(m.Global.Palette(), card)
	}
	return components.RenderCard(m.Global.Palette(), card, false)
}

func (m *model) renderHeartsBrokenIndicator() string {
	if m.heartsBroken {
		return m.Global.Palette().WarningText.Render("♥ Hearts: broken")
	}
	return m.Global.Palette().Muted.Render("♥ Hearts: not yet broken")
}

func (m *model) renderPassDirection() string {
	if m.phase != logic.PhasePassing {
		return ""
	}
	// PassDirection.String is the one label table for the enum; a hold hand never
	// reaches the passing stage, so its "hold" label cannot show here anyway.
	return m.Global.Palette().Dim.Render("Pass: " + m.passDirection.String())
}

func (m *model) renderPlayerSection() string {
	statusView := gameview.RenderStatus(m.Global.Palette(), m.Base.CurrentPlayerName, m.Base.MyTurn, m.Base.TurnRemaining)
	handWidth := gameview.HandWidth(m.Global.Width)
	handRows := gameview.HandRows(m.Global.Width, m.Global.Height)
	var staged map[int]struct{}
	if m.phase == logic.PhasePassing {
		staged = m.passIndices()
	}
	handView := gameview.RenderHand(m.Global.Palette(), m.Base.Hand, m.Selected, staged, handWidth, handRows)

	return gameview.RenderHeroBand(m.Global.Palette(), m.ActionErr, statusView, handView)
}

func (m *model) renderHandOver() string {
	h := gameview.HandOver{
		Title:  fmt.Sprintf("HAND %d COMPLETE", m.handNumber),
		Header: fmt.Sprintf("%-12s  %8s  %9s", "player", "hand", "total"),
		Hint:   m.handOverHint(),
	}
	h.ApplyMatchEnd(m.Base.EndReason, m.matchComplete, m.Base.Phase, m.Base.WinnerName, m.leavers)

	for _, seat := range m.Base.Seats {
		h.Rows = append(h.Rows, m.Global.Palette().Muted.Render(fmt.Sprintf("%-12s  %8d  %9d",
			styles.PadTruncate(seat.Name, 12), m.handPoints[seat.ID], m.cumulativeScores[seat.ID])))
	}
	return gameview.RenderHandOver(m.Global, h)
}
