package components

import (
	"image/color"

	"github.com/Pieczasz/terminal-card/internal/tui/styles"

	lg "charm.land/lipgloss/v2"
)

// gridCols is the picker's shape. Both games that open one have four choices, and
// two rows of two read as a palette where a single row reads as a menu.
const gridCols = 2

// GridPicker is the modal grid Crazy Eights and Uno both open over the table to ask
// for a suit or a colour. Labels are in grid order, reading left to right then down,
// and the cursor indexes the same slice - so the cell under the cursor and the value
// it stands for cannot drift apart the way two parallel tables do.
type GridPicker struct {
	Title  string
	Labels []string
	// Colors is one foreground per cell, or nil to leave the cells in the theme's
	// muted text until the cursor picks one out.
	Colors []color.Color
	Cursor int
}

// GridStep moves a grid cursor by dx cells along its row and dy cells down, staying
// inside the grid: a step off any edge leaves the cursor where it was.
func GridStep(cursor, n, dx, dy int) int {
	if cursor < 0 || cursor >= n {
		return cursor
	}
	col := cursor%gridCols + dx
	row := cursor/gridCols + dy
	if col < 0 || col >= gridCols || row < 0 {
		return cursor
	}
	if next := row*gridCols + col; next < n {
		return next
	}
	return cursor
}

// Render draws the picker: a title, then the cells in rows of gridCols, framed.
func (p GridPicker) Render(t styles.Theme) string {
	if len(p.Labels) == 0 {
		return ""
	}

	// One width for every cell keeps the grid a rectangle whatever the label
	// lengths. lipgloss counts border and padding inside Width, so the widest label
	// needs four extra columns - without them "♦ Diamonds" wraps under its own glyph.
	width := widestLabel(p.Labels) + 4

	cells := make([]string, 0, len(p.Labels))
	for i, label := range p.Labels {
		style := t.PickerCell.Width(width).Align(lg.Center)
		if i < len(p.Colors) {
			style = style.Foreground(p.Colors[i])
		} else {
			style = style.Foreground(t.TextMuted)
		}
		if i == p.Cursor {
			style = style.BorderForeground(t.Selection).Bold(true)
			if len(p.Colors) == 0 {
				style = style.Foreground(t.Selection)
			}
		} else {
			style = style.BorderForeground(t.BorderMuted)
		}
		cells = append(cells, style.Render(label))
	}

	rows := make([]string, 0, (len(cells)+gridCols-1)/gridCols)
	for start := 0; start < len(cells); start += gridCols {
		rows = append(rows, lg.JoinHorizontal(lg.Center, cells[start:min(start+gridCols, len(cells))]...))
	}

	return t.PickerBox.Render(lg.JoinVertical(lg.Center,
		lg.NewStyle().Bold(true).Foreground(t.Selection).Render(p.Title),
		"",
		lg.JoinVertical(lg.Center, rows...),
	))
}

// widestLabel is the display width of the longest label, for sizing a column of them.
func widestLabel(labels []string) int {
	widest := 0
	for _, l := range labels {
		widest = max(widest, lg.Width(l))
	}
	return widest
}
