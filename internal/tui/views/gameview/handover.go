package gameview

import (
	"strings"

	"github.com/Pieczasz/tty-cards/internal/game"
	"github.com/Pieczasz/tty-cards/internal/tui/router"
	"github.com/Pieczasz/tty-cards/internal/tui/styles"

	lg "charm.land/lipgloss/v2"
)

// HandOver is the between-hands screen: what just happened, where everyone stands and
// what to press next. Every multi-hand game renders its result through it, so the three
// cannot drift apart in layout.
type HandOver struct {
	Title string
	// Subtitle is an optional second headline, such as who took the hand.
	Subtitle string
	// Body is an optional game-specific block between the headlines and the rows.
	Body string
	// Header is an optional column heading over Rows, aligned with them.
	Header string
	// Rows are the standings, one pre-styled line per seat, drawn left-aligned.
	Rows []string
	Hint string
}

// LobbyHint is the hand-over hint once the match is over.
const LobbyHint = "esc / enter -> lobby"

// MatchOverTitle is the headline once the match is over, naming the winner when there
// is one.
func MatchOverTitle(winner string) string {
	if winner == "" {
		return "MATCH COMPLETE"
	}
	return "MATCH COMPLETE - " + winner + " wins"
}

// InterruptedTitle is the headline of a match a leave ended early for everyone. It
// names who left rather than a winner: the seats still at the table are tied on
// wherever the hand had got to, and a winner on those scores reads as a result.
func InterruptedTitle(leavers []string) string {
	if len(leavers) == 0 {
		return "MATCH ENDED EARLY"
	}
	return "MATCH ENDED EARLY - " + strings.Join(leavers, ", ") + " left the table"
}

// ApplyMatchEnd sets the headline once the match is over. An interrupted match
// wins over a winner: the seats that stayed are not a result. A hand still inside
// the match keeps the title and hint the caller set.
func (h *HandOver) ApplyMatchEnd(reason game.EndReason, matchOver bool, phase game.Phase, winner string, leavers []string) {
	switch {
	case reason == game.EndReasonInterrupted:
		h.Title, h.Hint = InterruptedTitle(leavers), LobbyHint
	case matchOver || phase == game.Finished:
		h.Title, h.Hint = MatchOverTitle(winner), LobbyHint
	}
}

// RenderHandOver draws h centred on the whole screen and clamped to it.
func RenderHandOver(g router.GlobalContext, h HandOver) string {
	t := g.Palette()
	parts := []string{t.Accented.Bold(true).Render(h.Title)}
	if h.Subtitle != "" {
		parts = append(parts, t.Accented.Render(h.Subtitle))
	}
	parts = append(parts, "")
	if h.Body != "" {
		parts = append(parts, h.Body, "")
	}
	rows := h.Rows
	if h.Header != "" {
		rows = append([]string{t.SectionHeading.Render(h.Header)}, rows...)
	}
	parts = append(parts, styles.JoinVertical(lg.Left, rows...), "", t.Dim.Render(h.Hint))

	// The menus' box, so a result reads as a screen of its own rather than a few lines
	// floating in an empty terminal.
	content := t.Box.Render(styles.JoinVertical(lg.Center, parts...))
	return styles.Clamp(g.Width, g.Height, styles.Place(g.Width, g.Height, lg.Center, lg.Center, content))
}
