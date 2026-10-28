package styles

import (
	"math"
	"strings"

	lg "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// JoinVertical is lipgloss.JoinVertical, byte for byte, without its cost. Profiled
// under 1000 sessions, lipgloss's joins were two thirds of the frame-building CPU:
// they grow an unsized builder over a whole-screen frame, measure every line's width
// twice, and allocate each run of padding. This measures once, sizes the output up
// front and pads from a shared run of spaces.
func JoinVertical(pos lg.Position, strs ...string) string {
	switch len(strs) {
	case 0:
		return ""
	case 1:
		return strs[0]
	}

	blocks := make([]block, len(strs))
	maxWidth, size := 0, 0
	for i, s := range strs {
		blocks[i] = splitBlock(s)
		maxWidth = max(maxWidth, blocks[i].width)
		size += blocks[i].bytes
	}
	lines := 0
	for _, b := range blocks {
		lines += len(b.lines)
	}

	var sb strings.Builder
	sb.Grow(size + lines*(maxWidth+1))
	for i, b := range blocks {
		for j, line := range b.lines {
			gap := maxWidth - b.widths[j]
			switch pos {
			case lg.Left:
				sb.WriteString(line)
				sb.WriteString(spaces(gap))
			case lg.Right:
				sb.WriteString(spaces(gap))
				sb.WriteString(line)
			default:
				if gap < 1 {
					sb.WriteString(line)
					break
				}
				right := gap - int(math.Round(float64(gap)*positionValue(pos)))
				sb.WriteString(spaces(gap - right))
				sb.WriteString(line)
				sb.WriteString(spaces(right))
			}
			if i != len(blocks)-1 || j != len(b.lines)-1 {
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

// JoinHorizontal is lipgloss.JoinHorizontal, byte for byte; see JoinVertical for why.
func JoinHorizontal(pos lg.Position, strs ...string) string {
	switch len(strs) {
	case 0:
		return ""
	case 1:
		return strs[0]
	}

	blocks := make([]block, len(strs))
	maxHeight, size, rowWidth := 0, 0, 0
	for i, s := range strs {
		blocks[i] = splitBlock(s)
		maxHeight = max(maxHeight, len(blocks[i].lines))
		size += blocks[i].bytes
		rowWidth += blocks[i].width
	}

	var sb strings.Builder
	sb.Grow(size + maxHeight*(rowWidth+1))
	for row := range maxHeight {
		for i := range blocks {
			line, width := blocks[i].lineAt(row, maxHeight, pos)
			sb.WriteString(line)
			sb.WriteString(spaces(blocks[i].width - width))
		}
		if row < maxHeight-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// block is one joined string split into lines, each measured once.
type block struct {
	lines  []string
	widths []int
	width  int // the widest line
	bytes  int
}

func splitBlock(s string) block {
	if strings.Contains(s, "\t") {
		s = strings.ReplaceAll(s, "\t", "    ")
	}
	if strings.Contains(s, "\r\n") {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	b := block{lines: strings.Split(s, "\n"), bytes: len(s)}
	b.widths = make([]int, len(b.lines))
	for i, line := range b.lines {
		b.widths[i] = ansi.StringWidth(line)
		b.width = max(b.width, b.widths[i])
	}
	return b
}

// lineAt is the block's row once it is padded to height rows the way
// lipgloss.JoinHorizontal pads it: blank rows below it at Top, above it at Bottom,
// and split between the two anywhere else.
func (b block) lineAt(row, height int, pos lg.Position) (string, int) {
	extra := height - len(b.lines)
	var above int
	switch pos {
	case lg.Top:
		above = 0
	case lg.Bottom:
		above = extra
	default:
		top := extra - int(math.Round(float64(extra)*positionValue(pos)))
		above = extra - top
	}
	i := row - above
	if i < 0 || i >= len(b.lines) {
		return "", 0
	}
	return b.lines[i], b.widths[i]
}

func positionValue(pos lg.Position) float64 {
	return math.Min(1, math.Max(0, float64(pos)))
}

// MarginTop is lg.NewStyle().MarginTop(n).Render(s), byte for byte: n blank rows over
// s, and every line padded to the widest. A table's middle band went through lipgloss
// for that one row of air on every frame, paying for its alignment of the whole block.
func MarginTop(n int, s string) string {
	b := splitBlock(s)
	var sb strings.Builder
	sb.Grow(b.bytes + (n+len(b.lines))*(b.width+1))
	for range max(n, 0) {
		sb.WriteString(spaces(b.width))
		sb.WriteByte('\n')
	}
	for i, line := range b.lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(line)
		sb.WriteString(spaces(b.width - b.widths[i]))
	}
	return sb.String()
}
