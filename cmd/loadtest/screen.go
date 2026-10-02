package main

import (
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// screen is the smallest terminal a bot can read the game off. The server's renderer
// sends only what changed since the last frame - cursor moves, erases, inserts and
// scrolls - so stripping the escapes from the stream gives fragments, not a frame.
// It replays the subset of VT100/ECMA-48 the Bubble Tea renderer emits for an
// xterm-256color PTY and ignores colour, modes and strings.
type screen struct {
	mu      sync.Mutex
	w, h    int
	cells   [][]string // "" is the right half of a wide cell
	x, y    int
	wrap    bool // a glyph landed in the last column; the next one wraps first
	autoWrp bool
	insert  bool
	top     int
	bot     int
	saveX   int
	saveY   int
	last    string // the last glyph printed, for REP
	pending []byte // an escape or UTF-8 rune split across two writes
	parser  *ansi.Parser

	version uint64
	changed chan struct{} // closed and replaced on every write
}

func newScreen(w, h int) *screen {
	s := &screen{w: w, h: h, autoWrp: true, bot: h - 1, parser: ansi.NewParser(), changed: make(chan struct{})}
	s.parser.SetParamsSize(32)
	s.cells = make([][]string, h)
	for i := range s.cells {
		s.cells[i] = blankRow(w)
	}
	return s
}

func blankRow(w int) []string {
	row := make([]string, w)
	for i := range row {
		row[i] = " "
	}
	return row
}

// Write never fails: a sequence it does not know is skipped, like a terminal does.
func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := slices.Concat(s.pending, p)
	var state byte
	for len(b) > 0 {
		if b[0] >= 0xC0 && !utf8.FullRune(b) {
			break
		}
		seq, width, n, next := ansi.DecodeSequence(b, state, s.parser)
		if n == len(b) && next != ansi.NormalState {
			break // the rest of this sequence is in the next write
		}
		s.apply(seq, width)
		b = b[n:]
	}
	s.pending = slices.Clone(b)
	s.version++
	close(s.changed)
	s.changed = make(chan struct{})
	return len(p), nil
}

// snapshot is the screen as lines of text, its version, and a channel closed on the
// next write.
func (s *screen) snapshot() (lines []string, version uint64, changed <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines = make([]string, s.h)
	for i, row := range s.cells {
		lines[i] = strings.Join(row, "")
	}
	return lines, s.version, s.changed
}

func (s *screen) apply(seq []byte, width int) {
	switch {
	case width > 0:
		s.print(string(seq), width)
	case len(seq) == 1:
		s.control(seq[0])
	case len(seq) > 1 && seq[0] == ansi.ESC && seq[1] == '[':
		s.csi(ansi.Cmd(s.parser.Command()))
	case len(seq) > 1 && seq[0] == ansi.ESC:
		s.esc(ansi.Cmd(s.parser.Command()).Final())
	}
	// Anything else - OSC, DCS, APC, a lone variation selector - draws nothing.
}

func (s *screen) print(g string, width int) {
	if s.wrap && s.autoWrp {
		s.x = 0
		s.lineFeed()
	}
	s.wrap = false
	if s.x+width > s.w {
		return
	}
	row := s.cells[s.y]
	if s.insert {
		copy(row[s.x+width:], row[s.x:])
	}
	row[s.x] = g
	for i := 1; i < width; i++ {
		row[s.x+i] = ""
	}
	s.last = g
	s.x += width
	if s.x >= s.w {
		s.x = s.w - 1
		s.wrap = true
	}
}

func (s *screen) control(c byte) {
	switch c {
	case '\r':
		s.moveTo(0, s.y)
	case '\n', '\v', '\f':
		s.wrap = false
		s.lineFeed()
	case '\b':
		s.moveTo(s.x-1, s.y)
	case '\t':
		s.moveTo((s.x/8+1)*8, s.y)
	}
}

func (s *screen) esc(final byte) {
	switch final {
	case 'M': // reverse index
		if s.y == s.top {
			s.scroll(s.top, s.bot, -1)
		} else {
			s.moveTo(s.x, s.y-1)
		}
	case 'D':
		s.lineFeed()
	case 'E':
		s.moveTo(0, s.y)
		s.lineFeed()
	case '7':
		s.saveX, s.saveY = s.x, s.y
	case '8':
		s.moveTo(s.saveX, s.saveY)
	}
}

func (s *screen) lineFeed() {
	if s.y == s.bot {
		s.scroll(s.top, s.bot, 1)
		return
	}
	s.y = min(s.y+1, s.h-1)
}

// param is the i-th CSI parameter; def stands in for a missing one and for zero where
// the sequence treats zero as one.
func (s *screen) param(i, def int) int {
	v, _ := s.parser.Param(i, def)
	if v == 0 && def == 1 {
		return 1
	}
	return v
}

func (s *screen) csi(cmd ansi.Cmd) {
	if cmd.Prefix() == '?' {
		s.privateMode(cmd.Final())
		return
	}
	if s.cursorCSI(cmd.Final()) {
		return
	}
	s.editCSI(cmd.Final())
}

func (s *screen) privateMode(final byte) {
	on := final == 'h'
	if !on && final != 'l' {
		return
	}
	for _, p := range s.parser.Params() {
		switch p.Param(0) {
		case 7:
			s.autoWrp = on
		case 1049, 1047, 47:
			s.eraseRows(0, s.h)
		}
	}
}

// cursorCSI handles the sequences that only move the cursor.
func (s *screen) cursorCSI(final byte) bool {
	n := s.param(0, 1)
	switch final {
	case 'A':
		s.moveTo(s.x, s.y-n)
	case 'B', 'e':
		s.moveTo(s.x, s.y+n)
	case 'C', 'a':
		s.moveTo(s.x+n, s.y)
	case 'D':
		s.moveTo(s.x-n, s.y)
	case 'E':
		s.moveTo(0, s.y+n)
	case 'F':
		s.moveTo(0, s.y-n)
	case 'G', '`':
		s.moveTo(n-1, s.y)
	case 'd':
		s.moveTo(s.x, n-1)
	case 'H', 'f':
		s.moveTo(s.param(1, 1)-1, n-1)
	case 'I':
		s.moveTo((s.x/8+n)*8, s.y)
	case 'Z':
		s.moveTo(max((s.x-1)/8-n+1, 0)*8, s.y)
	case 's':
		s.saveX, s.saveY = s.x, s.y
	case 'u':
		s.moveTo(s.saveX, s.saveY)
	default:
		return false
	}
	return true
}

// editCSI handles the sequences that change cells.
func (s *screen) editCSI(final byte) {
	n := s.param(0, 1)
	switch final {
	case 'J':
		s.eraseDisplay(s.param(0, 0))
	case 'K':
		s.eraseLine(s.param(0, 0))
	case 'X', '@', 'P', 'b':
		s.charCSI(final, n)
	case 'L', 'M', 'S', 'T':
		s.lineCSI(final, n)
	case 'r':
		s.top, s.bot = s.param(0, 1)-1, min(s.param(1, s.h), s.h)-1
		s.moveTo(0, 0)
	case 'h', 'l':
		if s.param(0, 0) == 4 {
			s.insert = final == 'h'
		}
	}
}

// charCSI erases, inserts, deletes or repeats n cells on the cursor's row.
func (s *screen) charCSI(final byte, n int) {
	row := s.cells[s.y]
	n = min(n, s.w-s.x)
	switch final {
	case 'X':
		fill(row[s.x : s.x+n])
	case '@':
		copy(row[s.x+n:], row[s.x:])
		fill(row[s.x : s.x+n])
	case 'P':
		copy(row[s.x:], row[s.x+n:])
		fill(row[s.w-n:])
	case 'b':
		for range n {
			s.print(s.last, max(ansi.StringWidth(s.last), 1))
		}
	}
}

// lineCSI inserts or deletes n lines at the cursor, or scrolls the region by n.
func (s *screen) lineCSI(final byte, n int) {
	inRegion := s.y >= s.top && s.y <= s.bot
	switch {
	case final == 'L' && inRegion:
		s.scroll(s.y, s.bot, -n)
	case final == 'M' && inRegion:
		s.scroll(s.y, s.bot, n)
	case final == 'S':
		s.scroll(s.top, s.bot, n)
	case final == 'T':
		s.scroll(s.top, s.bot, -n)
	}
}

func (s *screen) moveTo(x, y int) {
	s.wrap = false
	s.x = max(min(x, s.w-1), 0)
	s.y = max(min(y, s.h-1), 0)
}

func (s *screen) eraseDisplay(mode int) {
	switch mode {
	case 0:
		s.eraseLine(0)
		s.eraseRows(s.y+1, s.h)
	case 1:
		s.eraseLine(1)
		s.eraseRows(0, s.y)
	case 2, 3:
		s.eraseRows(0, s.h)
	}
}

func (s *screen) eraseLine(mode int) {
	row := s.cells[s.y]
	switch mode {
	case 0:
		fill(row[s.x:])
	case 1:
		fill(row[:s.x+1])
	case 2:
		fill(row)
	}
}

func (s *screen) eraseRows(from, to int) {
	for y := max(from, 0); y < min(to, s.h); y++ {
		fill(s.cells[y])
	}
}

// scroll moves rows top..bot up by n (down when n is negative), blanking what is
// uncovered.
func (s *screen) scroll(top, bot, n int) {
	if top < 0 || bot >= s.h || top > bot || n == 0 {
		return
	}
	span := bot - top + 1
	for range min(max(n, -n), span) {
		if n > 0 {
			first := s.cells[top]
			copy(s.cells[top:bot], s.cells[top+1:bot+1])
			fill(first)
			s.cells[bot] = first
		} else {
			last := s.cells[bot]
			copy(s.cells[top+1:bot+1], s.cells[top:bot])
			fill(last)
			s.cells[top] = last
		}
	}
}

func fill(cells []string) {
	for i := range cells {
		cells[i] = " "
	}
}
