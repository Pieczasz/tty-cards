package poker

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	gameview "github.com/Pieczasz/terminal-card/internal/tui/views/game"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

func (m *Model) View() tea.View {
	if m.handComplete || m.matchComplete {
		return tea.NewView(styles.Clamp(m.Global.Width, m.Global.Height, m.renderHandOver()))
	}
	if m.Base.Phase != game.Playing {
		return tea.NewView(gameview.RenderWaitingScreen(m.Global, m.Base.Phase, m.winnerName))
	}

	compact := m.compact()
	zones := m.seatZones()

	top := m.renderTopRow(zones.Top, compact)
	bot := m.renderHero(compact)
	midH := max(m.Global.Height-lg.Height(top)-lg.Height(bot), 0)
	mid := m.renderMiddle(midH, zones.Left, zones.Right, compact)

	// Clamped, not padded: nine seats of card art is wider than any terminal, and
	// PadCenter hands overwide rows straight through for the terminal to wrap.
	return tea.NewView(styles.Clamp(m.Global.Width, m.Global.Height, lg.JoinVertical(lg.Left,
		styles.PadCenter(m.Global.Width, top),
		styles.Clamp(m.Global.Width, midH, mid),
		styles.PadCenter(m.Global.Width, bot),
	)))
}

// artTableHeight is the height poker needs before it can draw card art. It is taller
// than the shared breakpoint because poker spends a whole card on the hero's own seat
// on top of the hand controls: the bottom band alone costs what a whole table does
// elsewhere, and the five community cards sit above it.
const artTableHeight = 48

func (m *Model) compact() bool {
	return m.Global.Height < artTableHeight || gameview.IsCompact(m.Global.Width, m.Global.Height)
}

// seatNameWidth is the name column on the results screen. Usernames run to 16
// characters, so it has to elide rather than let a long name shove the chip
// columns out of line.
const seatNameWidth = 12

func (m *Model) renderHandOver() string {
	compact := m.compact()
	board := m.renderBoard(compact)

	title := m.Global.Theme.Accented.Render(fmt.Sprintf("HAND %d/%d COMPLETE", m.handNumber, m.handsTotal))
	winner := m.Global.Theme.Accented.Render(m.winnerName + " wins the hand")
	if m.matchComplete {
		title = m.Global.Theme.Accented.Render("MATCH COMPLETE")
		winner = m.Global.Theme.Accented.Render(m.winnerName + " wins")
	}

	seatLines := make([]string, 0, len(m.seats))
	for _, s := range m.seats {
		// Chip glyphs carry their own colour, so they are joined in rather than
		// rendered through m.Global.Theme.Muted.
		line := m.Global.Theme.Muted.Render(fmt.Sprintf("%s %6d  ", styles.PadTruncate(s.Name, seatNameWidth), s.Chips)) +
			renderChipStack(m.Global.Theme, s.Chips)
		if s.Folded {
			line += m.Global.Theme.Muted.Render("  folded")
		}
		if len(s.Hole) == 2 {
			line += "  " + renderMiniCard(m.Global.Theme, s.Hole[0]) + renderMiniCard(m.Global.Theme, s.Hole[1])
		}
		seatLines = append(seatLines, line)
	}

	content := lg.JoinVertical(lg.Center,
		title, winner, "", board, "",
		lg.JoinVertical(lg.Left, seatLines...),
		"", m.Global.Theme.Dim.Render(m.handOverHint()),
	)
	return styles.Place(m.Global.Width, m.Global.Height, lg.Center, lg.Center, content)
}

// handOverHint spells out that esc leaves the whole match. The screen looks like
// the end of a game, but with hands still to play esc forfeits the stack the
// player just spent them building.
func (m *Model) handOverHint() string {
	if m.matchComplete {
		return "esc / enter -> lobby"
	}
	leave := "esc: leave the match, forfeiting your chips"

	var next string
	switch {
	case m.heroBusted():
		next = "out of chips - watching until the match ends"
	case m.canDeal():
		next = fmt.Sprintf("enter: deal hand %d", m.handNumber+1)
	case m.Base.CurrentPlayer != "":
		next = "waiting for " + m.Base.CurrentPlayer + " to deal hand " + strconv.Itoa(m.handNumber+1)
	default:
		next = "waiting for the next hand"
	}
	return next + "   |   " + leave
}

// seatZones places the opponents around the table, starting from the seat on the
// hero's left so the order on screen is the order the action moves in.
func (m *Model) seatZones() gameview.TableZones[Seat] {
	heroIdx := -1
	for i, s := range m.seats {
		if s.IsHero {
			heroIdx = i
			break
		}
	}

	opps := make([]Seat, 0, len(m.seats))
	if heroIdx < 0 {
		opps = append(opps, m.seats...)
	} else {
		n := len(m.seats)
		for i := 1; i < n; i++ {
			opps = append(opps, m.seats[(heroIdx+i)%n])
		}
	}
	return gameview.SplitZones(opps)
}

func (m *Model) renderTopRow(seats []Seat, compact bool) string {
	if len(seats) == 0 {
		return ""
	}
	parts := make([]string, 0, len(seats))
	for _, s := range seats {
		parts = append(parts, m.renderSeat(s, compact, gameview.OrientationTop))
	}
	row := lg.JoinHorizontal(lg.Bottom, parts...)
	if !compact {
		return lg.NewStyle().MarginTop(1).Render(row)
	}
	return row
}

func (m *Model) renderMiddle(height int, left, right []Seat, compact bool) string {
	leftView := m.renderSideStack(left, compact, gameview.OrientationLeft)
	rightView := m.renderSideStack(right, compact, gameview.OrientationRight)

	return gameview.RenderTableRow(m.Global.Width, height, leftView, m.renderCenter(compact), rightView)
}

func (m *Model) renderSideStack(seats []Seat, compact bool, orientation gameview.Orientation) string {
	if len(seats) == 0 {
		return ""
	}
	parts := make([]string, 0, len(seats))
	for _, s := range seats {
		parts = append(parts, m.renderSeat(s, compact, orientation))
	}
	return lg.JoinVertical(lg.Center, parts...)
}

func (m *Model) renderCenter(compact bool) string {
	board := m.renderBoard(compact)
	potLine := m.Global.Theme.Accented.Render(fmt.Sprintf("POT %d", m.pot))
	if m.sidePots > 1 {
		potLine += m.Global.Theme.Muted.Render(fmt.Sprintf("  (%d pots)", m.sidePots))
	}
	street := m.Global.Theme.Accented.Render(fmt.Sprintf("%s | hand %d/%d", m.street, m.handNumber, m.handsTotal))
	betLine := m.Global.Theme.Muted.Render(fmt.Sprintf("bet %d | to call %d", m.currentBet, m.toCall))

	return lg.JoinVertical(lg.Center, board, "", potLine, renderChipStack(m.Global.Theme, m.pot), street, betLine)
}

func (m *Model) renderBoard(compact bool) string {
	slots := make([]string, 5)
	for i := range 5 {
		switch {
		case i < len(m.board) && compact:
			slots[i] = renderMiniCard(m.Global.Theme, m.board[i])
		case i < len(m.board):
			slots[i] = components.RenderCard(m.Global.Theme, m.board[i], false)
		case compact:
			slots[i] = components.MiniCardSlot(m.Global.Theme)
		default:
			slots[i] = renderEmptySlot(m.Global.Theme)
		}
	}
	return lg.JoinHorizontal(lg.Bottom, slots...)
}

func renderEmptySlot(t styles.Theme) string {
	// Theme.CardFrame is the same chrome components.RenderCard wears, which is what
	// keeps the board from jumping by a column when a card lands in a slot. The old
	// Place(7,5)+Padding box was a leftover from a smaller face.
	blank := strings.Repeat(" ", components.FaceWidth)
	inner := blank + strings.Repeat("\n"+blank, components.FaceHeight-1)
	return t.CardFrame.BorderForeground(t.BorderMuted).MarginTop(1).Render(inner)
}

// renderFacedownCard is a single hole-card back that matches RenderCard's footprint,
// so a seat that later turns its cards up does not change size.
func renderFacedownCard(t styles.Theme) string {
	lines := make([]string, components.FaceHeight)
	fill := lg.NewStyle().Foreground(t.CardBack).Render(strings.Repeat("░", components.FaceWidth))
	for i := range lines {
		lines[i] = fill
	}
	return t.CardFrame.MarginTop(1).Render(strings.Join(lines, "\n"))
}

// The mini card lives in components: Hearts falls back to the same four columns when
// its trick will not fit, and two tables disagreeing about how a card reads at mini
// size is how a player learns the wrong shorthand.
func renderMiniCard(t styles.Theme, c deck.Card) string {
	return components.RenderMiniCard(t, c)
}

func renderHoleBack(t styles.Theme) string {
	return components.MiniCardBack(t)
}

func (m *Model) renderSeat(s Seat, compact bool, orientation gameview.Orientation) string {
	t := m.Global.Theme
	ns := lg.NewStyle().Bold(true).Foreground(t.Accent)
	if s.IsTurn {
		ns = t.TurnName
	} else if s.Folded {
		ns = t.Dim
	}

	badges := seatBadges(s)
	name := ns.Render(s.Name)
	if badges != "" {
		name = lg.JoinHorizontal(lg.Center, name, " ", m.Global.Theme.SuccessText.Render(badges))
	}

	stack := m.Global.Theme.Muted.Render(strconv.FormatUint(uint64(s.Chips), 10))
	if s.Bet > 0 {
		stack = lg.JoinHorizontal(lg.Center, stack, m.Global.Theme.Muted.Render(fmt.Sprintf(" | bet %d", s.Bet)))
	}
	if s.AllIn && !s.Folded {
		stack = lg.JoinHorizontal(lg.Center, stack, " ", m.Global.Theme.Accented.Render("ALL-IN"))
	}
	if s.Folded {
		stack = m.Global.Theme.Dim.Render("folded")
	}

	cards := m.renderSeatCards(s, compact)
	rows := []string{cards, name, stack}
	if !compact {
		// A short terminal needs the row for cards more than for decoration.
		rows = append(rows, renderChipStack(m.Global.Theme, s.Chips))
	}
	pad := lg.NewStyle().Padding(0, 1)
	block := pad.Render(lg.JoinVertical(lg.Center, rows...))

	// Only the seat on turn has a clock. The hero's countdown lives on the YOUR TURN
	// line instead: attaching it here would draw it twice and grow the bottom band.
	if !s.IsTurn || s.IsHero {
		return block
	}
	clock := gameview.RenderTurnClock(t, m.Base.TurnRemaining, false)
	return gameview.AttachTurnClock(block, clock, orientation)
}

func seatBadges(s Seat) string {
	var b []string
	if s.IsDealer {
		b = append(b, "D")
	}
	if s.IsSB {
		b = append(b, "SB")
	}
	if s.IsBB {
		b = append(b, "BB")
	}
	return strings.Join(b, "/")
}

func (m *Model) renderSeatCards(s Seat, compact bool) string {
	if len(s.Hole) == 2 {
		if compact {
			return lg.JoinHorizontal(lg.Center, renderMiniCard(m.Global.Theme, s.Hole[0]), renderMiniCard(m.Global.Theme, s.Hole[1]))
		}
		return lg.JoinHorizontal(lg.Bottom,
			components.RenderCard(m.Global.Theme, s.Hole[0], false),
			components.RenderCard(m.Global.Theme, s.Hole[1], false),
		)
	}
	// A busted seat is dealt nothing, so the count is what says so: a fixed pair of
	// backs would show a hand that was never dealt.
	if !compact {
		backs := make([]string, 0, s.HandSize)
		for range s.HandSize {
			backs = append(backs, renderFacedownCard(m.Global.Theme))
		}
		return lg.JoinHorizontal(lg.Bottom, backs...)
	}
	backs := make([]string, 0, s.HandSize)
	for range s.HandSize {
		backs = append(backs, renderHoleBack(m.Global.Theme))
	}
	return strings.Join(backs, "")
}

func (m *Model) renderHero(compact bool) string {
	hero := m.heroSeat()
	var seatBlock string
	if hero != nil {
		seatBlock = m.renderSeat(*hero, compact, gameview.OrientationTop)
	}

	status := gameview.RenderStatus(m.Global.Theme, m.Base.CurrentPlayer, m.Base.MyTurn, m.Base.TurnRemaining)
	actions := m.renderActionBar()

	block := gameview.RenderHeroBand(m.Global.Theme, m.lastActionErr, seatBlock, status, actions)
	if !compact {
		return lg.NewStyle().MarginBottom(1).Render(block)
	}
	return block
}

func (m *Model) renderActionBar() string {
	if m.raising {
		return m.renderRaisePrompt()
	}
	var opts []string
	if m.canFold() {
		opts = append(opts, "f fold")
	}
	if m.canCheck() {
		opts = append(opts, "c check")
	}
	if m.canCall() {
		opts = append(opts, fmt.Sprintf("c call %d", m.toCall))
	}
	if m.canRaise() {
		opts = append(opts, "r raise")
	}
	if m.canAllIn() {
		opts = append(opts, "a all-in")
	}
	if len(opts) == 0 {
		if m.handComplete || m.Base.Phase == game.Finished {
			return m.Global.Theme.Muted.Render("esc -> lobby")
		}
		return m.Global.Theme.Muted.Render("waiting…")
	}
	return m.Global.Theme.Accented.Render(strings.Join(opts, " | "))
}

// renderRaisePrompt shows the raise being built: the running total, the chips that
// can be pushed onto it, and how far it can still go.
func (m *Model) renderRaisePrompt() string {
	total := m.Global.Theme.Accented.Render(fmt.Sprintf("RAISE TO %d", m.raiseAmount))
	bounds := m.Global.Theme.Muted.Render(fmt.Sprintf("(min %d, max %d)", m.raiseMin, m.raiseMax))
	keys := m.Global.Theme.Dim.Render("[/] fine  |  enter confirm  |  esc cancel")
	return lg.JoinVertical(lg.Center,
		lg.JoinHorizontal(lg.Center, total, "  ", bounds),
		renderChipRack(m.Global.Theme),
		keys,
	)
}
