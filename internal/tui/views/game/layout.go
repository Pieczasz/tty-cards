package game

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Pieczasz/terminal-card/internal/deck"
	"github.com/Pieczasz/terminal-card/internal/game"
	"github.com/Pieczasz/terminal-card/internal/tui/components"
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"
	"github.com/Pieczasz/terminal-card/internal/tui/views"

	tea "charm.land/bubbletea/v2"
	lg "charm.land/lipgloss/v2"
)

// fanFits reports whether maxRows leaves room for a fanned hand and its index row.
// Zero rows means unbounded.
func fanFits(maxRows int) bool {
	return maxRows <= 0 || maxRows >= components.FanRows+1
}

// FansHand lets a caller decorating the fan's card slots (uno's colour row) ask the same
// question the renderer does, instead of painting slot-spaced glyphs over a strip.
func FansHand(handSize, maxWidth, maxRows int) bool {
	return handSize > 0 && components.FanTuck(handSize, maxWidth) != 0 && fanFits(maxRows)
}

// RenderHand draws the hero's hand into at most maxWidth columns, with an index row
// under it. A hand that fits no fan falls back to the rank-and-suit strip, which has no
// index row to line up.
//
// maxRows bounds the fan only. The strip wraps to however many rows the hand needs: a
// forty-card Uno hand on a short terminal costs the table a row or two, which the frame
// absorbs by clamping, and that is the better trade - cards cut out of a player's own
// hand are cards they cannot play.
func RenderHand(
	t styles.Theme,
	hand []deck.Card,
	selectedIdx int,
	disableSelection bool,
	maxWidth, maxRows int,
) string {
	if len(hand) == 0 {
		return ""
	}
	selected := selectedIdx
	if disableSelection {
		selected = -1
	}

	tuck := components.FanTuck(len(hand), maxWidth)
	if tuck == 0 || !fanFits(maxRows) {
		return components.RenderStrip(t, hand, nil, selected, maxWidth)
	}

	fan := components.RenderFan(t, hand, selected, tuck)

	// The index row sits under the fan, one number centred in each card's visible slot,
	// so a player can see which key picks which card.
	var labels strings.Builder
	for i := range hand {
		slot := components.CardSlotWidth(i, len(hand), selected, tuck)
		label := " "
		if i < 10 {
			style := t.Dim
			if i == selected {
				style = t.PlayerItemSelected.Bold(true)
			}
			label = style.Render(strconv.Itoa(i))
		}
		labels.WriteString(styles.PadCenter(slot, label))
	}

	return lg.JoinVertical(lg.Left, fan, labels.String())
}

// RenderHandMulti is RenderHand for multi-select (Hearts pass phase). selected marks
// which cards are currently staged for the pass; cursor highlights the focused index.
func RenderHandMulti(
	t styles.Theme,
	hand []deck.Card,
	selected map[int]struct{},
	cursor, maxWidth, maxRows int,
) string {
	if len(hand) == 0 {
		return ""
	}
	tuck := components.FanTuck(len(hand), maxWidth)
	if tuck == 0 || !fanFits(maxRows) {
		return components.RenderStrip(t, hand, selected, cursor, maxWidth)
	}
	fan := components.RenderFanMulti(t, hand, selected, tuck)

	var labels strings.Builder
	for i := range hand {
		slot := components.CardSlotWidthMulti(i, len(hand), tuck, selected)
		label := " "
		if i < 10 {
			style := t.Dim
			if _, ok := selected[i]; ok {
				style = t.PlayerItemSelected.Bold(true)
			} else if i == cursor {
				style = t.PlayerItemSelected
			}
			label = style.Render(strconv.Itoa(i))
		}
		labels.WriteString(styles.PadCenter(slot, label))
	}

	return lg.JoinVertical(lg.Left, fan, labels.String())
}

type Orientation int

const (
	OrientationTop Orientation = iota
	OrientationLeft
	OrientationRight
)

// topCardsFrame is the columns a top-edge stack costs beyond its card count: the
// left edge, the seven columns of back on the last card, and its right edge.
const topCardsFrame = 8

// renderTopCards draws count face-down cards along the top edge. Past maxWidth the stack
// is cut short rather than run off screen; the seat's hand count beside the art is what a
// player actually reads. Zero or less means unbounded.
func renderTopCards(t styles.Theme, count, maxWidth int) string {
	if maxWidth > 0 {
		count = min(count, maxWidth-topCardsFrame)
	}
	if count <= 0 {
		return ""
	}

	botLine := lg.NewStyle().Foreground(t.CardFace).Render("╰" + strings.Repeat("┴", count-1) + "───────╯")

	edge := lg.NewStyle().Foreground(t.CardFace).Render("│" + strings.Repeat("│", count-1))
	body := lg.NewStyle().Foreground(t.CardBack).Render("░░░░░░░")
	rightEdge := lg.NewStyle().Foreground(t.CardFace).Render("│")

	midLine := edge + body + rightEdge

	var sb strings.Builder
	sb.Grow(len(midLine)*4 + len(botLine) + 5)

	sb.WriteString(midLine)
	sb.WriteByte('\n')
	sb.WriteString(midLine)
	sb.WriteByte('\n')
	sb.WriteString(midLine)
	sb.WriteByte('\n')
	sb.WriteString(midLine)
	sb.WriteByte('\n')
	sb.WriteString(botLine)

	return sb.String()
}

// sideCardsFrame is the rows a side stack costs beyond its card count: the four rows
// of exposed back on the last card, plus the two edges.
const sideCardsFrame = 5

// renderLeftCards draws count face-down cards down the left edge. Unbounded stacks were
// the one place a table outgrew the terminal: an Uno hand runs past twenty cards.
func renderLeftCards(t styles.Theme, count, maxRows int) string {
	count = capSideCards(count, maxRows)
	if count <= 0 {
		return ""
	}

	topEdge := lg.NewStyle().Foreground(t.CardFace).Render("─────╮")
	midEdge := lg.NewStyle().Foreground(t.CardFace).Render("─────┤")
	botEdge := lg.NewStyle().Foreground(t.CardFace).Render("─────╯")
	cardBody := lg.NewStyle().Foreground(t.CardBack).Render("░░░░░") + lg.NewStyle().Foreground(t.CardFace).Render("│")

	return buildVerticalCardsString(count, topEdge, midEdge, botEdge, cardBody)
}

func renderRightCards(t styles.Theme, count, maxRows int) string {
	count = capSideCards(count, maxRows)
	if count <= 0 {
		return ""
	}

	topEdge := lg.NewStyle().Foreground(t.CardFace).Render("╭─────")
	midEdge := lg.NewStyle().Foreground(t.CardFace).Render("├─────")
	botEdge := lg.NewStyle().Foreground(t.CardFace).Render("╰─────")
	cardBody := lg.NewStyle().Foreground(t.CardFace).Render("│") + lg.NewStyle().Foreground(t.CardBack).Render("░░░░░")

	return buildVerticalCardsString(count, topEdge, midEdge, botEdge, cardBody)
}

func capSideCards(count, maxRows int) int {
	if maxRows > 0 {
		return min(count, maxRows-sideCardsFrame)
	}
	return count
}

func buildVerticalCardsString(count int, topEdge, midEdge, botEdge, cardBody string) string {
	var sb strings.Builder
	sb.Grow((count + 3) * 20)

	sb.WriteString(topEdge)
	sb.WriteByte('\n')
	for range count - 1 {
		sb.WriteString(midEdge)
		sb.WriteByte('\n')
	}
	sb.WriteString(cardBody)
	sb.WriteByte('\n')
	sb.WriteString(cardBody)
	sb.WriteByte('\n')
	sb.WriteString(cardBody)
	sb.WriteByte('\n')
	sb.WriteString(cardBody)
	sb.WriteByte('\n')
	sb.WriteString(botEdge)

	return sb.String()
}

// RenderOpponent draws one opponent's seat. budget is how much room the card art may
// take: columns across for a seat on the top edge, rows down for one stacked at a
// side. Zero means unbounded.
func RenderOpponent(
	t styles.Theme,
	o game.PlayerSnapshot,
	isCurrentTurn bool,
	orientation Orientation,
	remaining time.Duration,
	budget int,
) string {
	nameStyle := t.SectionHeading
	if isCurrentTurn {
		nameStyle = t.TurnName
	}
	nameView := nameStyle.Render(o.Username)
	cardsCountView := t.Muted.Render(fmt.Sprintf("[%d cards]", o.HandSize))

	infoView := lg.JoinVertical(lg.Center, nameView, cardsCountView)

	var cardsView string
	switch orientation {
	case OrientationTop:
		cardsView = renderTopCards(t, o.HandSize, budget)
	case OrientationLeft:
		cardsView = renderLeftCards(t, o.HandSize, budget)
	case OrientationRight:
		cardsView = renderRightCards(t, o.HandSize, budget)
	}

	var block string
	switch orientation {
	case OrientationTop:
		block = lg.JoinVertical(lg.Center, cardsView, infoView)
	case OrientationLeft:
		block = lg.JoinVertical(lg.Left, infoView, cardsView)
	default:
		block = lg.JoinVertical(lg.Right, infoView, cardsView)
	}

	if !isCurrentTurn {
		return block
	}
	return AttachTurnClock(block, RenderTurnClock(t, remaining, false), orientation)
}

func RenderOpponentMinimal(t styles.Theme, o game.PlayerSnapshot, isCurrentTurn bool) string {
	nameStyle := t.SectionHeading
	if isCurrentTurn {
		nameStyle = t.TurnName
	}
	nameView := nameStyle.Render(o.Username)
	cardsCountView := t.Muted.Render(fmt.Sprintf("[%d cards]", o.HandSize))

	return lg.JoinHorizontal(lg.Center, nameView, " ", cardsCountView)
}

// RenderHeroBand is the bottom band every table builds the same way: the turn status,
// whatever the game stacks under it, then the last action the engine rejected. Empty
// rows are dropped rather than joined as blank ones - the band's height comes straight
// off the middle band's, so a stray line costs a row of the table.
func RenderHeroBand(t styles.Theme, actionErr error, rows ...string) string {
	parts := make([]string, 0, len(rows)+1)
	for _, row := range rows {
		if row != "" {
			parts = append(parts, row)
		}
	}
	if actionErr != nil {
		parts = append(parts, t.ErrorText.Render(actionErr.Error()))
	}
	return lg.JoinVertical(lg.Center, parts...)
}

// RenderStatus names whose turn it is, with the countdown on the same line: a second row
// would grow botHeight and shove the discard pile every time the clock arms.
func RenderStatus(t styles.Theme, currentPlayer string, isMyTurn bool, remaining time.Duration) string {
	statusStyle := t.Dim.MarginTop(1).MarginBottom(1)
	statusStr := "Current turn: " + currentPlayer
	if isMyTurn {
		statusStyle = statusStyle.Foreground(t.Success).Bold(true)
		statusStr = "> YOUR TURN <"
		if clock := RenderTurnClock(t, remaining, true); clock != "" {
			statusStr += "  " + clock
		}
	}
	return statusStyle.Render(statusStr)
}

// Below this the countdown shows tenths: whole seconds hide up to a second of the time a
// player has, which late in a turn is the difference between acting and being folded.
const preciseClockThreshold = 6 * time.Second

// Tick rate while showing tenths, paid only for the last seconds of a turn: ten renders
// a second per client is not something to run for a whole turn over ssh.
const tenthTickInterval = 100 * time.Millisecond

// FormatTurnClock renders the turn countdown, or empty when no clock is running. precise
// asks for tenths below preciseClockThreshold, which only the player who has to act
// needs - it costs a tick ten times a second. Both forms round up, so the display never
// claims less time than the player has.
func FormatTurnClock(remaining time.Duration, precise bool) string {
	if remaining <= 0 {
		return ""
	}
	if precise && remaining < preciseClockThreshold {
		tenths := int(math.Ceil(remaining.Seconds() * 10))
		return fmt.Sprintf("%d.%d", tenths/10, tenths%10)
	}
	secs := int((remaining + time.Second - 1) / time.Second)
	return fmt.Sprintf("%d:%02d", secs/60, secs%60)
}

// RenderTurnClock turns urgent in the last seconds for whoever reads it, but counts in
// tenths only for the player those seconds belong to.
func RenderTurnClock(t styles.Theme, remaining time.Duration, precise bool) string {
	clock := FormatTurnClock(remaining, precise)
	if clock == "" {
		return ""
	}
	if remaining < preciseClockThreshold {
		return t.ErrorText.Bold(true).Render(clock)
	}
	return t.Muted.Render(clock)
}

// AttachTurnClock puts the countdown under a top or bottom seat and alongside a side one,
// where another row would push the stack apart. An empty clock changes nothing.
func AttachTurnClock(block, clock string, orientation Orientation) string {
	if clock == "" {
		return block
	}
	switch orientation {
	case OrientationLeft:
		return lg.JoinHorizontal(lg.Center, block, " ", clock)
	case OrientationRight:
		return lg.JoinHorizontal(lg.Center, clock, " ", block)
	case OrientationTop:
	}
	return lg.JoinVertical(lg.Center, block, clock)
}

// ClockTickMsg drives the turn countdown. Source is the feed of the session that armed
// it, for the same reason EventMsg carries one; a nil Source is a tick armed without a
// session (ClockTick), which any session accepts.
type ClockTickMsg struct{ Source <-chan game.Event }

// clockTickFrom ticks once a second, or ten times a second only for the player whose
// clock is running out. onTurn is what keeps a table cheap: a frame costs thousands of
// allocations, and every seat paying that rate for somebody else's digit multiplies the
// table's work for nothing.
func clockTickFrom(src <-chan game.Event, remaining time.Duration, onTurn bool) tea.Cmd {
	interval := time.Second
	if onTurn && remaining > 0 && remaining < preciseClockThreshold {
		interval = tenthTickInterval
	}
	return tea.Tick(interval, func(time.Time) tea.Msg { return ClockTickMsg{Source: src} })
}

func RenderWaitingScreen(g router.GlobalContext, phase game.Phase, winner string) string {
	content := "Waiting for game to start..."
	if phase == game.Finished {
		content = fmt.Sprintf("Game Over! Winner: %s\n\nPress Esc to go back.", winner)
	}
	return renderGameNotice(g, content)
}

// renderGameNotice is the full-screen frame a table shows in place of itself.
func renderGameNotice(g router.GlobalContext, content string) string {
	titleFig := styles.RenderFigureASCII("Active Game", styles.InnerWidth(g.Width), styles.TitleHeightBudget(g.Height))
	header := g.Theme.Title.Render(titleFig)
	footer := g.Theme.RenderActionFooter(styles.GlobalActions)
	return views.RenderCenteredLayout(g, header, content, footer)
}
