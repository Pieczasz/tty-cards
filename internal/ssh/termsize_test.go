package ssh

import (
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Pieczasz/tty-cards/internal/db"
	"github.com/Pieczasz/tty-cards/internal/testutil"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// Each tier's threshold is the session count its cells fit the budget at: 250 at
// 250x80, 416 at 200x60, 625 at 160x50. Past one the tier drops; it comes back only
// at 90% of it, so a count hovering at a threshold does not resize every big session
// back and forth.
func TestNextTier(t *testing.T) {
	t.Parallel()

	const top, big, mid, floor = 3, 2, 1, 0
	tests := []struct {
		name     string
		cur, n   int
		wantTier int
	}{
		{"an empty server grants the top tier", floor, 0, top},
		{"one session gets the top tier", floor, 1, top},
		{"the top tier holds at its threshold", top, 250, top},
		{"one past the threshold steps down", top, 251, big},
		{"coming back needs 10 percent headroom", big, 250, big},
		{"at 90 percent of the threshold it steps back up", big, 225, top},
		{"200x60 holds at its threshold", big, 416, big},
		{"one past it steps down to 160x50", big, 417, mid},
		{"160x50 does not return to 200x60 short of 90 percent", mid, 400, mid},
		{"and does at it", mid, 375, big},
		{"160x50 holds at its threshold", mid, 625, mid},
		{"one past it is the floor", mid, 626, floor},
		{"a rush drops several tiers at once", top, 700, floor},
		{"an emptied server climbs several at once", floor, 10, top},
		{"the floor is granted however many connect", floor, 100_000, floor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.wantTier, nextTier(tt.cur, tt.n, cellBudget))
		})
	}
}

// Tiers nest - each is at least as wide and as tall as the one below - which is what
// lets a session's held tier bound everything its renderer has ever allocated. And no
// tier edge may sit just below a layout breakpoint: the full table needs 80x40.
func TestSizeTiers_NestAndClearTheLayoutBreakpoints(t *testing.T) {
	t.Parallel()
	for i := 1; i < len(sizeTiers); i++ {
		assert.GreaterOrEqual(t, sizeTiers[i].w, sizeTiers[i-1].w)
		assert.GreaterOrEqual(t, sizeTiers[i].h, sizeTiers[i-1].h)
	}
	assert.GreaterOrEqual(t, sizeTiers[0].w, 80, "the floor is narrower than the full table layout")
	assert.GreaterOrEqual(t, sizeTiers[0].h, 40, "the floor is shorter than the full table layout")
}

func TestSizeBudget_Grant(t *testing.T) {
	t.Parallel()

	floorExtra := func(tier int) int { return sizeTiers[tier].cells() - sizeTiers[0].cells() }
	tests := []struct {
		name      string
		tier      int
		extra     int // room for cells above the floor
		held      int
		want      termSize
		wantSize  termSize
		wantHeld  int
		wantSpent int
	}{
		{
			name: "a small terminal is untouched and spends nothing",
			tier: 3, extra: 1 << 30,
			want: termSize{80, 24}, wantSize: termSize{80, 24},
		},
		{
			name: "an absurd request is capped at the top tier",
			tier: 3, extra: 1 << 30,
			want: termSize{4_000_000, 3_000_000}, wantSize: termSize{250, 80},
			wantHeld: 3, wantSpent: floorExtra(3),
		},
		{
			name: "only the dimension out of range moves",
			tier: 3, extra: 1 << 30,
			want: termSize{4_000_000, 40}, wantSize: termSize{250, 40},
			wantHeld: 3, wantSpent: floorExtra(3),
		},
		{
			name: "the server's tier caps a big terminal",
			tier: 1, extra: 1 << 30,
			want: termSize{300, 100}, wantSize: termSize{160, 50},
			wantHeld: 1, wantSpent: floorExtra(1),
		},
		{
			name: "a request just past the floor is charged the tier it needs",
			tier: 3, extra: 1 << 30,
			want: termSize{130, 45}, wantSize: termSize{130, 45},
			wantHeld: 1, wantSpent: floorExtra(1),
		},
		{
			name: "no room above the floor still grants the floor",
			tier: 3, extra: 0,
			want: termSize{300, 100}, wantSize: termSize{120, 40},
		},
		{
			name: "short of room for the top tier, the largest that fits",
			tier: 3, extra: floorExtra(2),
			want: termSize{300, 100}, wantSize: termSize{200, 60},
			wantHeld: 2, wantSpent: floorExtra(2),
		},
		{
			name: "what a session already holds costs nothing again",
			tier: 3, extra: 0, held: 2,
			want: termSize{300, 100}, wantSize: termSize{200, 60}, wantHeld: 2,
		},
		{
			name: "a lowered tier shrinks a session but it keeps what it holds",
			tier: 0, extra: 1 << 30, held: 3,
			want: termSize{300, 100}, wantSize: termSize{120, 40}, wantHeld: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b := &sizeBudget{extra: tt.extra}
			b.tier.Store(int32(tt.tier))

			size, held := b.grant(tt.held, tt.want)
			assert.Equal(t, tt.wantSize, size)
			assert.Equal(t, tt.wantHeld, held)
			assert.Equal(t, int64(tt.wantSpent), b.extraUsed.Load())
		})
	}
}

// The zero budget is the floor for everyone, which is what a sessionRegistry{} in a
// test gets.
func TestSizeBudget_ZeroValueGrantsTheFloor(t *testing.T) {
	t.Parallel()
	var b sizeBudget
	b.join()
	size, held := b.grant(0, termSize{300, 100})
	assert.Equal(t, sizeTiers[0], size)
	assert.Zero(t, held)
}

// Leaving gives back the cells above the floor the session held, so the next big
// terminal can have them.
func TestSizeBudget_LeaveReturnsWhatTheSessionHeld(t *testing.T) {
	t.Parallel()
	b := &sizeBudget{cells: cellBudget, extra: 1 << 30}
	b.join()
	_, held := b.grant(0, termSize{300, 100})
	require.Equal(t, 3, held)

	b.leave(held)
	assert.Zero(t, b.extraUsed.Load())
}

func TestSessionSize_Filter(t *testing.T) {
	t.Parallel()

	t.Run("bubbletea's first resize echoes the grant and is not a request", func(t *testing.T) {
		t.Parallel()
		b := &sizeBudget{cells: cellBudget, extra: 1 << 30}
		sz := &sessionSize{}
		start, _ := sz.start(b, termSize{300, 100})
		require.Equal(t, termSize{250, 80}, start)

		b.tier.Store(1) // the server fills up before the echo arrives
		got := sz.filter(tea.WindowSizeMsg{Width: start.w, Height: start.h})
		assert.Equal(t, tea.WindowSizeMsg{Width: 160, Height: 50}, got)
		assert.Equal(t, termSize{300, 100}, *sz.wanted.Load(), "the echo replaced the client's request")
	})

	t.Run("a resize that beats the echo is not undone by it", func(t *testing.T) {
		t.Parallel()
		b := &sizeBudget{cells: cellBudget, extra: 1 << 30}
		sz := &sessionSize{}
		start, _ := sz.start(b, termSize{300, 100})

		sz.filter(tea.WindowSizeMsg{Width: 100, Height: 30}) // the client shrank at once
		got := sz.filter(tea.WindowSizeMsg{Width: start.w, Height: start.h})
		assert.Equal(t, tea.WindowSizeMsg{Width: 100, Height: 30}, got,
			"the echo was taken for a request and the session drew past the client's window")
	})

	t.Run("a resize is a request, clamped", func(t *testing.T) {
		t.Parallel()
		b := &sizeBudget{cells: cellBudget, extra: 1 << 30}
		sz := &sessionSize{}
		sz.start(b, termSize{80, 24})
		sz.filter(tea.WindowSizeMsg{Width: 80, Height: 24}) // the echo

		got := sz.filter(tea.WindowSizeMsg{Width: 4_000_000, Height: 3_000_000})
		assert.Equal(t, tea.WindowSizeMsg{Width: 250, Height: 80}, got)
		assert.Equal(t, termSize{250, 80}, *sz.granted.Load())
	})

	t.Run("a retier to the same size is dropped", func(t *testing.T) {
		t.Parallel()
		b := &sizeBudget{cells: cellBudget, extra: 1 << 30}
		sz := &sessionSize{}
		sz.start(b, termSize{300, 100})
		assert.Nil(t, sz.filter(retierMsg{}), "an unchanged size would repaint the screen for nothing")
		assert.False(t, sz.stale())
	})

	t.Run("a retier after the tier drops shrinks the session", func(t *testing.T) {
		t.Parallel()
		b := &sizeBudget{cells: cellBudget, extra: 1 << 30}
		sz := &sessionSize{}
		sz.start(b, termSize{300, 100})

		b.tier.Store(0)
		require.True(t, sz.stale())
		assert.Equal(t, tea.WindowSizeMsg{Width: 120, Height: 40}, sz.filter(retierMsg{}))
		assert.False(t, sz.stale())
	})

	// ssh -tt with stdin not a terminal asks for 0x0. Granted as asked, the TUI read
	// it as "no size yet": nothing was drawn, yet every key still acted.
	t.Run("a pty with no size starts at the default", func(t *testing.T) {
		t.Parallel()
		sz := &sessionSize{}
		start, _ := sz.start(&sizeBudget{}, termSize{0, 0})
		assert.Equal(t, termSize{80, 24}, start)
		assert.Equal(t, tea.WindowSizeMsg{Width: 80, Height: 24},
			sz.filter(tea.WindowSizeMsg{Width: 80, Height: 24}), "the echo of the default")
	})

	t.Run("a resize with no size keeps the one the session has", func(t *testing.T) {
		t.Parallel()
		sz := &sessionSize{}
		sz.start(&sizeBudget{}, termSize{100, 30})
		sz.filter(tea.WindowSizeMsg{Width: 100, Height: 30}) // the echo

		for _, unknown := range []tea.WindowSizeMsg{{}, {Width: 100}, {Height: 30}} {
			assert.Nil(t, sz.filter(unknown), "%v blanked the screen", unknown)
		}
		assert.Equal(t, termSize{100, 30}, *sz.granted.Load())
	})

	t.Run("anything else passes through", func(t *testing.T) {
		t.Parallel()
		sz := &sessionSize{}
		sz.start(&sizeBudget{}, termSize{80, 24})
		assert.Equal(t, tea.KeyPressMsg{Code: 'a'}, sz.filter(tea.KeyPressMsg{Code: 'a'}))
	})
}

// screen is a session's stdout, read while the session writes it.
type screen struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (s *screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// visible is what the session drew, without its escape sequences.
func (s *screen) visible() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(ansi.Strip(s.buf.String()))
}

// The whole path for a 0x0 pty-req, as `ssh -tt host < /dev/null` sends: the
// session has to draw a screen rather than act on keys nobody can see the result of.
func TestSession_APtyWithNoSizeIsDrawn(t *testing.T) {
	t.Parallel()
	addr, _ := startInProcessServer(t, &db.User{ID: testutil.UID(97), Username: "sizeless"})
	s, err := dialInProcess(t, addr, "sizeless").NewSession()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	out := &screen{}
	s.Stdout, s.Stderr = out, io.Discard
	require.NoError(t, s.RequestPty("xterm", 0, 0, gossh.TerminalModes{}))
	require.NoError(t, s.Shell())

	assert.Eventually(t, func() bool { return out.visible() != "" },
		3*time.Second, 20*time.Millisecond, "a 0x0 terminal got a blank screen")
}

// grantedSizes is the size every live session was last told, by what it asked for.
func grantedSizes(reg *sessionRegistry) map[termSize]termSize {
	out := map[termSize]termSize{}
	reg.states.Range(func(_, v any) bool {
		st, _ := v.(*sessionState)
		if w, g := st.size.wanted.Load(), st.size.granted.Load(); w != nil && g != nil {
			out[*w] = *g
		}
		return true
	})
	return out
}

func openSession(t *testing.T, addr string, w, h int) (*gossh.Client, *countingDiscard) {
	t.Helper()
	client := dialInProcess(t, addr, "bench")
	s, err := client.NewSession()
	require.NoError(t, err)
	out := &countingDiscard{}
	s.Stdout = out
	s.Stderr = io.Discard
	require.NoError(t, s.RequestPty("xterm-256color", h, w, gossh.TerminalModes{}))
	require.NoError(t, s.Shell())
	return client, out
}

// A budget of one top-tier terminal: a second session drops the tier to 160x50, and
// the big session already running has to be told, or it keeps rendering 250x80 for
// as long as it stays.
func TestSession_TierStepDownResizesTheBigSession(t *testing.T) {
	t.Parallel()
	reg := newSessionRegistry(sizeTiers[3].cells(), 1<<30)
	addr, tracker := startServerWithRegistry(t, &usersByKey{users: map[string]*db.User{}}, reg)
	big, small := termSize{300, 100}, termSize{80, 24}

	_, out := openSession(t, addr, big.w, big.h)
	require.Eventually(t, func() bool {
		return out.n.Load() > 0 && grantedSizes(reg)[big] == termSize{250, 80}
	}, 5*time.Second, 10*time.Millisecond, "one session on an empty server should get the top tier")

	second, _ := openSession(t, addr, small.w, small.h)
	require.Eventually(t, func() bool { return tracker.Count() == 2 }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return grantedSizes(reg)[big] == termSize{160, 50} },
		5*time.Second, 10*time.Millisecond, "the big session was never shrunk to the new tier")
	assert.Equal(t, small, grantedSizes(reg)[small])

	// Back to one session: 90% of the budget fits 200x60 but not 250x80.
	_ = second.Close()
	require.Eventually(t, func() bool { return grantedSizes(reg)[big] == termSize{200, 60} },
		5*time.Second, 10*time.Millisecond, "the big session was never grown back")
}

func liveHeap() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / (1 << 20)
}

// byteCounter is a program's output that only counts what was written to it.
type byteCounter struct{ n atomic.Int64 }

func (c *byteCounter) Write(b []byte) (int, error) {
	c.n.Add(int64(len(b)))
	return len(b), nil
}

// fillModel draws every cell it is given, as a full-screen view does.
type fillModel struct{ w, h int }

func (fillModel) Init() tea.Cmd { return nil }

func (m fillModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.w, m.h = size.Width, size.Height
	}
	return m, nil
}

func (m fillModel) View() tea.View {
	v := tea.NewView(strings.TrimSuffix(strings.Repeat(strings.Repeat("x", m.w)+"\n", m.h), "\n"))
	v.AltScreen = true
	return v
}

// rendererHeap runs a program at size, then shrinks it to 120x40 if shrink is set,
// and returns the heap it holds.
func rendererHeap(t *testing.T, size termSize, shrink bool) float64 {
	t.Helper()
	before := liveHeap()
	out := &byteCounter{}
	p := tea.NewProgram(fillModel{}, tea.WithInput(nil), tea.WithOutput(out),
		tea.WithWindowSize(size.w, size.h), tea.WithFPS(120), tea.WithoutSignals())
	done := make(chan struct{})
	go func() { defer close(done); _, _ = p.Run() }()
	// A frame writes at least one byte per cell, so the byte count says when one has
	// been drawn: a fixed sleep measured an undrawn program on a loaded runner.
	drawn := func(bytes int) {
		require.Eventually(t, func() bool { return out.n.Load() >= int64(bytes) },
			5*time.Second, 5*time.Millisecond, "the frame was never drawn")
	}
	drawn(size.w * size.h)
	if shrink {
		mark := int(out.n.Load())
		p.Send(tea.WindowSizeMsg{Width: 120, Height: 40})
		drawn(mark + 120*40)
	}
	held := liveHeap() - before
	p.Quit()
	<-done
	return held
}

// extraCellBudget exists because of this: bubbletea reslices its cell buffers on a
// shrink rather than reallocating them, so a session keeps the memory of the largest
// size it was ever granted. If this starts failing, bubbletea frees on shrink and the
// extra budget can go: a tier step-down alone would then bound memory too.
//
//nolint:paralleltest // measures the process heap
func TestRenderer_KeepsItsBuffersAfterAShrink(t *testing.T) {
	top, floor := sizeTiers[len(sizeTiers)-1], sizeTiers[0]
	atTop := rendererHeap(t, top, false)
	shrunk := rendererHeap(t, top, true)
	atFloor := rendererHeap(t, floor, false)
	t.Logf("250x80 %.2f MiB, shrunk to 120x40 %.2f MiB, started at 120x40 %.2f MiB", atTop, shrunk, atFloor)
	require.Greater(t, atTop-atFloor, 1.0, "the measurement cannot tell the two sizes apart")
	assert.Greater(t, shrunk, atFloor+(atTop-atFloor)/2, "a shrink gave the buffers back")
}

// staleState is a session granted the top tier on a server now at the floor.
func staleState(reg *sessionRegistry, p *tea.Program) *sessionState {
	st := &sessionState{}
	st.size.budget = &reg.sizes
	top := sizeTiers[len(sizeTiers)-1]
	st.size.wanted.Store(&top)
	st.size.granted.Store(&top)
	st.program.Store(p)
	return st
}

// A session whose event loop is stuck - its peer stopped taking output - does not
// take a nudge. The sweep used to wait on each session in turn, so every session it
// reached after a stuck one kept a size the tier no longer grants, and every later
// sweep stopped at the same place.
func TestRetier_AStuckSessionDoesNotHoldUpTheOthers(t *testing.T) {
	t.Parallel()
	reg := newSessionRegistry(cellBudget, extraCellBudget)
	reg.sizes.tier.Store(0)
	for i := range 4 {
		// Never run, so a Send waits until the test ends.
		stuck := tea.NewProgram(fillModel{}, tea.WithContext(t.Context()), tea.WithInput(nil), tea.WithOutput(io.Discard))
		reg.states.Store(-1-i, staleState(reg, stuck))
	}
	var nudged atomic.Int32
	const live = 8
	for i := range live {
		p := tea.NewProgram(fillModel{}, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutSignals(),
			tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
				if _, ok := msg.(retierMsg); ok {
					nudged.Add(1)
					return nil
				}
				return msg
			}))
		done := make(chan struct{})
		go func() { defer close(done); _, _ = p.Run() }()
		t.Cleanup(func() { p.Quit(); <-done })
		reg.states.Store(i, staleState(reg, p))
	}

	reg.retier()

	assert.Eventually(t, func() bool { return nudged.Load() == live }, 2*time.Second, 10*time.Millisecond,
		"the sweep stalled behind a stuck session")
}

// An extra budget of one step to the top: the second big terminal is held at the
// floor for want of extra cells. When the first leaves without moving the tier, the
// cells it frees have to reach the one held back, not wait for it to resize.
func TestLeaveSizes_FreedExtraCellsReachASessionHeldBack(t *testing.T) {
	t.Parallel()
	reg := newSessionRegistry(cellBudget, sizeTiers[3].cells()-sizeTiers[0].cells())

	leaving := &sessionState{}
	got, _ := leaving.size.start(&reg.sizes, termSize{300, 100})
	require.Equal(t, termSize{250, 80}, got)
	leaving.program.Store(tea.NewProgram(fillModel{})) // never run: leaveSizes only asks that it exists
	session := &stubSession{}
	reg.store(session, leaving)

	held := &sessionState{}
	got, _ = held.size.start(&reg.sizes, termSize{260, 90})
	require.Equal(t, sizeTiers[0], got, "the second session should be held at the floor")
	// The start size comes back once as bubbletea's first resize, which regrants:
	// the leave has to come after it, or that regrant takes the freed cells itself.
	echoed := make(chan struct{})
	var once sync.Once
	p := tea.NewProgram(fillModel{}, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutSignals(),
		tea.WithWindowSize(got.w, got.h), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
			msg = held.size.filter(msg)
			if _, ok := msg.(tea.WindowSizeMsg); ok {
				once.Do(func() { close(echoed) })
			}
			return msg
		}))
	held.program.Store(p)
	done := make(chan struct{})
	go func() { defer close(done); _, _ = p.Run() }()
	t.Cleanup(func() { p.Quit(); <-done })
	reg.store(&stubSession{}, held)
	<-echoed
	require.Equal(t, sizeTiers[0], *held.size.granted.Load())

	reg.leaveSizes(session)

	assert.Eventually(t, func() bool { return *held.size.granted.Load() == termSize{250, 80} },
		2*time.Second, 10*time.Millisecond, "the cells the first session freed never reached the second")
}
