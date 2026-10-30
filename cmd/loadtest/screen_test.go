package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/game"
	logic "github.com/Pieczasz/tty-cards/internal/game/crazyeight"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func screenText(s *screen) string {
	lines, _, _ := s.snapshot()
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// The sequences the renderer emits, one at a time, against a terminal small enough
// to read the whole result.
func TestScreen_Sequences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		writes []string
		want   string
	}{
		{name: "text, CR and LF", writes: []string{"ab\r\ncd"}, want: "ab\ncd"},
		{name: "LF keeps the column", writes: []string{"ab\ncd"}, want: "ab\n  cd"},
		{name: "cursor position overwrites", writes: []string{"hello", "\x1b[1;2HE"}, want: "hEllo"},
		{name: "relative moves", writes: []string{"\x1b[2;3Hx\x1b[Ay\x1b[2Bz\x1b[5Dw"}, want: "   y\n  x\nw   z"},
		{name: "absolute column and row", writes: []string{"\x1b[3dA\x1b[5GB"}, want: "\n\nA   B"},
		{name: "erase line right, left and whole", writes: []string{
			"abcdef\r\nabcdef\r\nabcdef", "\x1b[1;3H\x1b[K", "\x1b[2;3H\x1b[1K", "\x1b[3;3H\x1b[2K",
		}, want: "ab\n   def"},
		{name: "erase display below", writes: []string{"aaa\r\nbbb\r\nccc", "\x1b[2;2H\x1b[J"}, want: "aaa\nb"},
		{name: "erase whole display", writes: []string{"aaa\r\nbbb", "\x1b[2J"}, want: ""},
		{name: "erase characters", writes: []string{"abcdef", "\x1b[1;2H\x1b[3X"}, want: "a   ef"},
		{name: "insert characters", writes: []string{"abcdef", "\x1b[1;2H\x1b[2@XY"}, want: "aXYbcdef"},
		{name: "delete characters", writes: []string{"abcdef", "\x1b[1;2H\x1b[2P"}, want: "adef"},
		{name: "insert and delete lines", writes: []string{"a\r\nb\r\nc", "\x1b[2;1H\x1b[L", "\x1b[1;1H\x1b[M"}, want: "\nb\nc"},
		{name: "scroll up and down inside a region", writes: []string{
			"a\r\nb\r\nc\r\nd", "\x1b[2;3r\x1b[S", "\x1b[r",
		}, want: "a\nc\n\nd"},
		{name: "reverse index at the top scrolls down", writes: []string{"a\r\nb", "\x1b[H\x1bM"}, want: "\na\nb"},
		{name: "line feed at the bottom scrolls", writes: []string{"a\r\nb\r\nc\r\nd\r\ne"}, want: "b\nc\nd\ne"},
		{name: "repeat the last glyph", writes: []string{"x\x1b[3b"}, want: "xxxx"},
		{name: "wrap after the last column", writes: []string{"0123456789ab"}, want: "0123456789\nab"},
		{name: "no wrap with autowrap off", writes: []string{"\x1b[?7l0123456789ab"}, want: "012345678b"},
		{name: "wide glyph takes two cells", writes: []string{"世x"}, want: "世x"},
		{name: "a text-presentation selector takes no cell", writes: []string{"♥︎x"}, want: "♥︎x"},
		{name: "colour, modes and strings draw nothing", writes: []string{
			"\x1b[?1049h\x1b[31;1ma\x1b[0m\x1b]11;?\x07\x1b[?25lb",
		}, want: "ab"},
		{name: "a sequence split across writes", writes: []string{"\x1b[", "2;", "3Hx"}, want: "\n  x"},
		{name: "a rune split across writes", writes: []string{"\xe2\x99", "\xa0"}, want: "♠"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newScreen(10, 4)
			for _, w := range tt.writes {
				_, _ = s.Write([]byte(w))
			}
			assert.Equal(t, tt.want, screenText(s))
		})
	}
}

// A reader of the screen waits on changed, so every write has to wake it.
func TestScreen_WriteSignalsReaders(t *testing.T) {
	t.Parallel()
	s := newScreen(10, 2)
	_, v0, changed := s.snapshot()

	_, _ = s.Write([]byte("x"))

	select {
	case <-changed:
	default:
		t.Fatal("a write did not close the channel handed out before it")
	}
	_, v1, _ := s.snapshot()
	assert.Greater(t, v1, v0)
}

// frameModel shows whatever frame it was last sent, the way the router shows its view.
type frameModel struct{ frame string }

type frameMsg string

func (m *frameModel) Init() tea.Cmd { return nil }

func (m *frameModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if f, ok := msg.(frameMsg); ok {
		m.frame = string(f)
	}
	return m, nil
}

func (m *frameModel) View() tea.View {
	v := tea.NewView(m.frame)
	v.AltScreen = true
	return v
}

// onlcr is the newline translation the server's PTY applies on the way out. Without
// a TTY on its input the renderer counts on it, sending a bare LF to mean CR LF.
type onlcr struct{ w io.Writer }

func (o onlcr) Write(p []byte) (int, error) {
	_, err := o.w.Write(bytes.ReplaceAll(p, []byte("\n"), []byte("\r\n")))
	return len(p), err
}

// startProgram runs model the way the ssh server does, rendering into a screen: an
// xterm-256color terminal of 80x24.
func startProgram(t *testing.T, model tea.Model) (*tea.Program, *screen, io.Writer) {
	t.Helper()
	scr := newScreen(termWidth, termHeight)
	in, keys := io.Pipe()
	p := tea.NewProgram(model,
		tea.WithInput(in), tea.WithOutput(onlcr{scr}), tea.WithoutSignalHandler(),
		tea.WithEnvironment([]string{"TERM=xterm-256color"}), tea.WithWindowSize(termWidth, termHeight))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.Run()
	}()
	t.Cleanup(func() {
		p.Quit()
		_ = keys.Close()
		<-done
	})
	return p, scr, keys
}

func waitForText(t *testing.T, scr *screen, want string, msgAndArgs ...any) {
	t.Helper()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Equal(c, want, screenText(scr))
	}, 5*time.Second, 5*time.Millisecond, msgAndArgs...)
}

func plain(frame string) string {
	lines := strings.Split(ansi.Strip(frame), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// The emulator is only as good as its agreement with the real renderer, which sends
// diffs: cursor moves, erases, inserts and scrolls. Real game frames from a whole
// game, then the same frame shifted up and down to provoke the scroll optimisation,
// must each come out on the screen exactly as the view drew them.
func TestScreen_ReplaysTheRealRenderer(t *testing.T) {
	t.Parallel()
	frames := gameFrames(t, 30)
	shifted := strings.SplitN(frames[len(frames)-1], "\n", 4)
	frames = append(frames, shifted[3], strings.Repeat("\n", 3)+shifted[3], "", "short\nframe")

	p, scr, _ := startProgram(t, &frameModel{})
	for i, f := range frames {
		p.Send(frameMsg(f))
		waitForText(t, scr, plain(f), "frame %d never showed on the screen", i)
	}
}

// gameFrames is up to n frames of one seat's view of a Crazy Eights game, a move
// apart.
func gameFrames(t *testing.T, n int) []string {
	t.Helper()
	engine := game.NewEngine(&logic.Rules{}, testutil.NamedPlayers("alice", "bob", "carol", "dave"))
	require.NoError(t, engine.Start())
	t.Cleanup(engine.Close)

	frames := make([]string, 0, n)
	for range n {
		frames = append(frames, renderSeat(t, engine, testutil.SeatID(1)))
		if engine.IsFinished() {
			break
		}
		require.NoError(t, engine.SubmitAction(engine.CurrentPlayerID(), logic.ActionDrawCard{}))
	}
	return frames
}
