package styles

import (
	"image/color"
	"strings"

	lg "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// renderBox is box.Width(width).Height(height).Render(s), byte for byte, for a box
// with a border and padding and no text style of its own - the theme's Box. Every
// full-screen frame went through lipgloss for its border, and lipgloss word-wraps the
// whole frame first: under 1000 sessions that was 38% of every byte the server
// allocated, for a frame RenderMainLayout has already sized to fit. When a line would
// wrap, or a style runs past the end of a line (lipgloss's wrap closes and reopens
// it), this hands the frame to lipgloss instead.
func renderBox(box lg.Style, width, height int, s string) string {
	box = box.Width(width).Height(height)
	top, right, bottom, left := box.GetPadding()
	inner := width - box.GetHorizontalBorderSize()
	rows := height - box.GetVerticalBorderSize()
	if inner-left-right <= 0 || !closedLines(s) {
		return box.Render(s)
	}
	b := splitBlock(s)
	if b.width > inner-left-right {
		return box.Render(s)
	}

	edge := box.GetBorderStyle()
	leftSide := borderPiece(firstRune(edge.Left), box.GetBorderLeftForeground())
	rightSide := borderPiece(firstRune(edge.Right), box.GetBorderRightForeground())
	blank := leftSide + spaces(inner) + rightSide

	total := max(top+len(b.lines)+bottom, rows)
	var sb strings.Builder
	sb.Grow(b.bytes + (total+2)*(inner+len(leftSide)+len(rightSide)+1) + 8*inner)
	sb.WriteString(borderPiece(horizontalEdge(edge.TopLeft, edge.Top, edge.TopRight, inner), box.GetBorderTopForeground()))
	for range top {
		sb.WriteByte('\n')
		sb.WriteString(blank)
	}
	for i, line := range b.lines {
		sb.WriteByte('\n')
		sb.WriteString(leftSide)
		sb.WriteString(spaces(left))
		sb.WriteString(line)
		sb.WriteString(spaces(inner - left - b.widths[i]))
		sb.WriteString(rightSide)
	}
	for range total - top - len(b.lines) {
		sb.WriteByte('\n')
		sb.WriteString(blank)
	}
	sb.WriteByte('\n')
	sb.WriteString(borderPiece(horizontalEdge(edge.BottomLeft, edge.Bottom, edge.BottomRight, inner), box.GetBorderBottomForeground()))
	return sb.String()
}

// closedLines reports whether s holds only text and SGR sequences - no hyperlink or
// other escape, no tab, carriage return or other control byte - and every line
// leaves no style open at its end: the frames lipgloss's wrap leaves as they are.
func closedLines(s string) bool {
	for line := range strings.SplitSeq(s, "\n") {
		if !closedLine(line) {
			return false
		}
	}
	return true
}

func closedLine(line string) bool {
	open := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c == ansi.ESC {
			params, n, ok := sgrAt(line[i:])
			if !ok {
				return false
			}
			open = params != "" && params != "0"
			i += n - 1
		} else if c < ' ' || c == ansi.DEL {
			return false
		}
	}
	return !open
}

// sgrAt reads the SGR sequence s starts with: its parameters and length, or false
// when s starts with any other escape.
func sgrAt(s string) (params string, n int, ok bool) {
	end := strings.IndexByte(s, 'm')
	if end < 2 || s[1] != '[' {
		return "", 0, false
	}
	params = s[2:end]
	if strings.Trim(params, "0123456789;:") != "" {
		return "", 0, false
	}
	return params, end + 1, true
}

func borderPiece(s string, fg color.Color) string {
	if _, none := fg.(lg.NoColor); none || fg == nil {
		return s
	}
	return ansi.Style{}.ForegroundColor(fg).Styled(s)
}

func firstRune(s string) string {
	if s == "" {
		return " "
	}
	for _, r := range s {
		return string(r)
	}
	return s
}

// horizontalEdge is a top or bottom border inner columns wide between its corners.
func horizontalEdge(leftCorner, middle, rightCorner string, inner int) string {
	return firstRune(leftCorner) + strings.Repeat(firstRune(middle), inner) + firstRune(rightCorner)
}
