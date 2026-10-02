package gameview

import (
	"strings"
	"testing"

	"github.com/Pieczasz/terminal-card/internal/tui/tuitest"

	lg "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchOverTitle(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "MATCH COMPLETE - alice wins", MatchOverTitle("alice"))
	assert.Equal(t, "MATCH COMPLETE", MatchOverTitle(""), "a match with no winner names nobody")
}

// Hearts, gin and poker all end a hand through this screen, so its order is the order
// every one of them reads in: headlines, the game's own block, the standings, the hint.
func TestRenderHandOver_DrawsEveryPartInOrder(t *testing.T) {
	t.Parallel()
	h := HandOver{
		Title:    "HAND 3 COMPLETE",
		Subtitle: "bob took it",
		Body:     "moon shot",
		Rows:     []string{"alice  12", "bob     4"},
		Hint:     "enter -> next hand",
	}

	out := tuitest.StripANSI(RenderHandOver(testContext(80, 24), h))

	at := -1
	for _, part := range []string{h.Title, h.Subtitle, h.Body, h.Rows[0], h.Rows[1], h.Hint} {
		i := strings.Index(out, part)
		require.GreaterOrEqualf(t, i, 0, "%q is missing from the hand-over screen", part)
		assert.Greaterf(t, i, at, "%q is out of order", part)
		at = i
	}
}

// The standings are a column: a shorter row centred on its own would no longer line up
// its score with the one above it.
func TestRenderHandOver_RowsShareALeftEdge(t *testing.T) {
	t.Parallel()
	rows := []string{"alice      112", "bo 4"}

	lines := strings.Split(tuitest.StripANSI(RenderHandOver(testContext(80, 24), HandOver{Title: "T", Rows: rows})), "\n")

	var starts []int
	for _, line := range lines {
		for _, row := range rows {
			if i := strings.Index(line, row); i >= 0 {
				starts = append(starts, i)
			}
		}
	}
	require.Len(t, starts, len(rows))
	assert.Equal(t, starts[0], starts[1], "every standings row starts in the same column")
}

// Optional parts leave no gap: a missing subtitle or body is not a blank line between
// the title and the rows.
func TestRenderHandOver_OmitsEmptyOptionalParts(t *testing.T) {
	t.Parallel()
	g := testContext(80, 24)
	bare := HandOver{Title: "T", Rows: []string{"row"}, Hint: "hint"}
	withBody := bare
	withBody.Subtitle, withBody.Body = "sub", "body"

	assert.Equal(t, lg.Height(strings.TrimSpace(RenderHandOver(g, bare)))+3,
		lg.Height(strings.TrimSpace(RenderHandOver(g, withBody))),
		"a subtitle is one line and a body is itself plus a spacer")
}

// The screen is the whole terminal, so however small it gets the frame cannot grow
// past it and push the renderer into scrolling.
func TestRenderHandOver_FitsTheTerminal(t *testing.T) {
	t.Parallel()
	h := HandOver{
		Title: "MATCH COMPLETE - somebody wins", Subtitle: "sub", Body: "a body line",
		Rows: []string{"alice 1", "bob 2", "carol 3", "dave 4"}, Hint: LobbyHint,
	}
	for _, size := range []struct{ w, h int }{{20, 6}, {40, 12}, {80, 24}, {120, 50}} {
		out := RenderHandOver(testContext(size.w, size.h), h)
		assert.LessOrEqualf(t, lg.Width(out), size.w, "%dx%d", size.w, size.h)
		assert.LessOrEqualf(t, lg.Height(out), size.h, "%dx%d", size.w, size.h)
	}
}
