package gameview

import (
	"github.com/Pieczasz/terminal-card/internal/tui/router"
	"github.com/Pieczasz/terminal-card/internal/tui/styles"

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

// RenderHandOver draws h centred on the whole screen and clamped to it.
func RenderHandOver(g router.GlobalContext, h HandOver) string {
	t := g.Theme
	parts := []string{t.Accented.Render(h.Title)}
	if h.Subtitle != "" {
		parts = append(parts, t.Accented.Render(h.Subtitle))
	}
	parts = append(parts, "")
	if h.Body != "" {
		parts = append(parts, h.Body, "")
	}
	parts = append(parts, lg.JoinVertical(lg.Left, h.Rows...), "", t.Dim.Render(h.Hint))

	content := lg.JoinVertical(lg.Center, parts...)
	return styles.Clamp(g.Width, g.Height, styles.Place(g.Width, g.Height, lg.Center, lg.Center, content))
}
