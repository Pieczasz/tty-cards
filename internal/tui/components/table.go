package components

import (
	"strings"

	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	lg "charm.land/lipgloss/v2"
)

// Column is one fixed-width cell of a Table. Fixed is the point: cycling a filter or
// paging onto a shorter page must not resize the layout under the cursor.
type Column struct {
	Title string
	Width int
}

// colSep is what sits between two cells, header and data rows alike.
const colSep = " | "

// Table is the header, the rule under it and the fixed height that the leaderboard,
// the profile and the join list all draw around their rows.
//
// The rows themselves stay with the caller. Each of the three screens styles
// individual cells - the viewer's own rank, a ranked table's mode - and a component
// that could express all three would be longer than the three call sites together.
// Cells is here for the rows that are plain text.
type Table struct {
	Cols []Column
	// Lead is printed before every row, header included: the join list's cursor gutter.
	Lead string
	// PadTo keeps this many data rows on screen, so a short page does not make the
	// rest of the layout jump.
	PadTo int
}

// Width is the printable width of one row, Lead included.
func (tb Table) Width() int {
	width := lg.Width(tb.Lead)
	for i, c := range tb.Cols {
		if i > 0 {
			width += len(colSep)
		}
		width += c.Width
	}
	return width
}

// Cells lays plain values out in the columns, one per column, padded and elided to fit.
func (tb Table) Cells(values ...string) string {
	cells := make([]string, 0, len(tb.Cols))
	for i, c := range tb.Cols {
		value := ""
		if i < len(values) {
			value = values[i]
		}
		cells = append(cells, styles.PadTruncate(value, c.Width))
	}
	return tb.Lead + strings.Join(cells, colSep)
}

// Header is the column titles and the rule under them, two lines.
func (tb Table) Header(t styles.Theme) string {
	titles := make([]string, 0, len(tb.Cols))
	for _, c := range tb.Cols {
		titles = append(titles, styles.PadTruncate(c.Title, c.Width))
	}
	head := t.SectionHeading.Render(tb.Lead + strings.Join(titles, colSep))
	return head + "\n" + t.Dim.Render(tb.Lead+strings.Repeat("-", tb.Width()-lg.Width(tb.Lead)))
}

// Render is the header, the rows, and blank rows up to PadTo.
func (tb Table) Render(t styles.Theme, rows []string) string {
	out := make([]string, 0, len(rows)+tb.PadTo+2)
	out = append(out, tb.Header(t))
	out = append(out, rows...)
	blank := strings.Repeat(" ", tb.Width())
	for range tb.PadTo - len(rows) {
		out = append(out, blank)
	}
	return strings.Join(out, "\n")
}
