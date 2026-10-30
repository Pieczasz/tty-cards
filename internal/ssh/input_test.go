package ssh

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// inputChunk is one Read's worth of a scripted client: wait is how long the read
// blocks on the network before data arrives.
type inputChunk struct {
	data string
	wait time.Duration
}

// scriptedInput replays chunks against a fake clock, so an hour of typing runs in
// microseconds and the quiet gaps are exact.
type scriptedInput struct {
	chunks []inputChunk
	clock  time.Time
	reads  int
}

func (in *scriptedInput) Read(p []byte) (int, error) {
	if in.reads >= len(in.chunks) {
		return 0, io.EOF
	}
	c := in.chunks[in.reads]
	in.reads++
	in.clock = in.clock.Add(c.wait)
	return copy(p, c.data), nil
}

func guardOver(in *scriptedInput) (*inputGuard, *int) {
	trips := 0
	g := newInputGuard(in, func() { trips++ })
	g.now = func() time.Time { return in.clock }
	return g, &trips
}

// drainGuard reads until the guard or the script stops, returning what got through.
func drainGuard(g *inputGuard) (string, error) {
	var got bytes.Buffer
	buf := make([]byte, 4096)
	for {
		n, err := g.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			return got.String(), err
		}
	}
}

func repeatChunks(n int, c inputChunk) []inputChunk {
	chunks := make([]inputChunk, n)
	for i := range chunks {
		chunks[i] = c
	}
	return chunks
}

func TestInputGuard(t *testing.T) {
	t.Parallel()

	// Two hours of play: bursts of fast typing, arrow keys held down for a minute on
	// key repeat, and the pauses a person takes to think.
	var human []inputChunk
	for range 120 {
		for _, key := range []string{"a", "\x1b[A", "\x1b[99;5u", "\r"} {
			human = append(human, inputChunk{data: key, wait: 120 * time.Millisecond})
		}
		human = append(human, repeatChunks(1800, inputChunk{data: "\x1b[1;5D", wait: 33 * time.Millisecond})...)
		human = append(human, inputChunk{data: "q", wait: 3 * time.Second})
	}

	tests := []struct {
		name      string
		chunks    []inputChunk
		pasting   bool
		wantTrip  bool
		maxPassed int
	}{
		{name: "a human is never cut off", chunks: human},
		{
			name:   "a lobby code pasted in one go",
			chunks: []inputChunk{{data: "\x1b[200~ABCD1234\x1b[201~", wait: time.Second}},
		},
		{
			name:     "a flood is cut off after the burst",
			chunks:   repeatChunks(2560, inputChunk{data: strings.Repeat("x", 4096)}),
			wantTrip: true, maxPassed: inputBurstBytes + 4096,
		},
		{
			// Under the rate budget, but never pausing: bubbletea holds all of it
			// behind the unparseable first byte.
			name: "a drip that never pauses is cut off at the hold limit",
			chunks: append([]inputChunk{{data: "\xff"}},
				repeatChunks(10_000, inputChunk{data: strings.Repeat("1", 64), wait: 100 * time.Millisecond})...),
			wantTrip: true, maxPassed: maxHeldInput,
		},
		{
			name:   "a drip that pauses is flushed each time and runs forever",
			chunks: repeatChunks(1000, inputChunk{data: strings.Repeat("1", 1024), wait: 2 * time.Second}),
		},
		{
			name:     "an unterminated paste is cut off at the hold limit, pauses or not",
			chunks:   repeatChunks(1000, inputChunk{data: strings.Repeat("1", 1024), wait: 2 * time.Second}),
			pasting:  true,
			wantTrip: true, maxPassed: maxHeldInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := &scriptedInput{chunks: tt.chunks, clock: time.Unix(0, 0)}
			g, trips := guardOver(in)
			g.pasting.Store(tt.pasting)

			got, err := drainGuard(g)

			if !tt.wantTrip {
				require.ErrorIs(t, err, io.EOF)
				assert.Zero(t, *trips)
				var want strings.Builder
				for _, c := range tt.chunks {
					want.WriteString(c.data)
				}
				assert.Equal(t, want.String(), got, "the guard changed the input it let through")
				return
			}
			require.ErrorIs(t, err, errInputOverBudget)
			assert.Equal(t, 1, *trips, "the notice is sent once")
			assert.LessOrEqual(t, len(got), tt.maxPassed)

			reads := in.reads
			_, err = g.Read(make([]byte, 16))
			require.ErrorIs(t, err, errInputOverBudget)
			assert.Equal(t, reads, in.reads, "a closed session kept reading from the client")
		})
	}
}

// The guard learns about pastes from bubbletea's own messages rather than a second
// parser of its own, and passes every message on to the session's filter.
func TestInputGuard_FilterTracksThePaste(t *testing.T) {
	t.Parallel()
	g := newInputGuard(nil, nil)

	assert.Equal(t, tea.PasteStartMsg{}, g.filter(nil, tea.PasteStartMsg{}))
	assert.True(t, g.pasting.Load())
	assert.Equal(t, tea.PasteMsg{Content: "ABCD1234"}, g.filter(nil, tea.PasteMsg{Content: "ABCD1234"}))
	assert.False(t, g.pasting.Load())

	g.filter(nil, tea.PasteStartMsg{})
	g.filter(nil, tea.PasteEndMsg{})
	assert.False(t, g.pasting.Load())

	assert.Equal(t, tea.ResumeMsg{}, g.filter(nil, tea.SuspendMsg{}), "the session's own filter no longer runs")
}

// recordingModel keeps the messages a player's input produces. It never quits: the
// program ends when its input does, which is also the only ending that lets
// bubbletea's decoder hand over every event it still holds.
type recordingModel struct{ got *[]tea.Msg }

func (m recordingModel) Init() tea.Cmd { return nil }

func (m recordingModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case tea.KeyPressMsg, tea.PasteMsg:
		*m.got = append(*m.got, msg)
	}
	return m, nil
}

func (m recordingModel) View() tea.View { return tea.NewView("") }

var errInputEnded = errors.New("client hung up")

// runGuarded runs a real bubbletea program over the guard, as a session does, feeds
// it input and then hangs up. It returns what the model saw and how the program ended.
func runGuarded(t *testing.T, input func(w io.Writer)) ([]tea.Msg, error) {
	t.Helper()
	pr, pw := io.Pipe()
	var got []tea.Msg
	g := newInputGuard(pr, nil)
	p := tea.NewProgram(recordingModel{got: &got},
		tea.WithInput(g), tea.WithOutput(io.Discard), tea.WithFilter(g.filter), tea.WithWindowSize(80, 24))
	go func() {
		input(pw)
		_ = pw.CloseWithError(errInputEnded)
	}()

	done := make(chan error, 1)
	go func() {
		_, err := p.Run()
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		p.Kill()
		<-done
		t.Fatal("the program never ended")
	}
	// Unblocks a writer the guard stopped reading for.
	_ = pr.CloseWithError(io.ErrClosedPipe)
	return got, err
}

func TestSessionInput_ThroughBubbletea(t *testing.T) {
	t.Parallel()

	t.Run("a pasted lobby code still arrives as a paste", func(t *testing.T) {
		t.Parallel()
		got, err := runGuarded(t, func(w io.Writer) {
			_, _ = io.WriteString(w, "\x1b[200~ABCD1234\x1b[201~")
		})
		require.ErrorIs(t, err, errInputEnded)
		require.Len(t, got, 1)
		assert.Equal(t, tea.PasteMsg{Content: "ABCD1234"}, got[0])
	})

	t.Run("typing arrives key by key", func(t *testing.T) {
		t.Parallel()
		got, err := runGuarded(t, func(w io.Writer) {
			for _, key := range []string{"a", "b", "\r"} {
				_, _ = io.WriteString(w, key)
				time.Sleep(20 * time.Millisecond)
			}
		})
		require.ErrorIs(t, err, errInputEnded)
		require.Len(t, got, 3)
		assert.Equal(t, "a", got[0].(tea.KeyPressMsg).String())
		assert.Equal(t, "b", got[1].(tea.KeyPressMsg).String())
		assert.Equal(t, "enter", got[2].(tea.KeyPressMsg).String())
	})

	t.Run("a 10 MiB paste with no end ends the program", func(t *testing.T) {
		t.Parallel()
		var written atomic.Int64
		_, err := runGuarded(t, func(w io.Writer) {
			_, _ = io.WriteString(w, "\x1b[200~")
			chunk := strings.Repeat("A", 4096)
			for range (10 << 20) / len(chunk) {
				n, err := io.WriteString(w, chunk)
				written.Add(int64(n))
				if err != nil {
					return
				}
			}
		})
		require.ErrorIs(t, err, errInputOverBudget)
		assert.LessOrEqual(t, written.Load(), int64(maxHeldInput+8192),
			"the program kept reading a paste it could only buffer")
	})
}

// The whole path over a real ssh connection: the flood has to close the session and
// tell the player why, not leave a frozen screen.
func TestNewServer_UnterminatedPasteClosesTheSession(t *testing.T) {
	t.Parallel()
	addr, _ := startInProcessServer(t, &db.User{ID: testutil.UID(40), Username: "paster"})
	client := dialInProcess(t, addr, "paster")

	s, err := client.NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	var stderr bytes.Buffer
	s.Stderr = &stderr
	stdin, err := s.StdinPipe()
	require.NoError(t, err)
	require.NoError(t, s.RequestPty("xterm", 24, 80, gossh.TerminalModes{}))
	require.NoError(t, s.Shell())

	go func() {
		_, _ = io.WriteString(stdin, "\x1b[200~")
		chunk := strings.Repeat("A", 32<<10)
		for range (10 << 20) / len(chunk) {
			if _, err := io.WriteString(stdin, chunk); err != nil {
				return
			}
		}
	}()

	ended := make(chan struct{})
	go func() {
		_ = s.Wait()
		close(ended)
	}()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the session is still open under a 10 MiB unterminated paste")
	}
	assert.Contains(t, stderr.String(), "Too much input too fast", "the player was not told why")
}
