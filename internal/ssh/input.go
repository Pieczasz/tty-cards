package ssh

import (
	"errors"
	"io"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
)

// A session's stdin is budgeted before bubbletea's decoder sees it. The fastest real
// input is key repeat on an arrow key: about 30 a second at up to 8 bytes each with
// the kitty disambiguation bubbletea asks for, so under 300 B/s. The app turns on no
// mouse or focus reporting, so nothing else writes to stdin. Past the budget the
// session is closed, which also bounds a keypress flood: every key is an Update and a
// full View, and some take a table's engine lock.
const (
	inputBytesPerSecond = 1 << 10
	inputBurstBytes     = 16 << 10
	// maxHeldInput bounds what the decoder can be holding. Whenever the head of its
	// buffer does not parse yet (a lone 0xff, an unterminated CSI) it keeps every
	// byte until input pauses, re-parsing all of it on each read; and it keeps a
	// bracketed paste until the paste ends, however long that takes. One session
	// pasting 226 MiB with no end marker took the server from 14 MiB to 1.74 GiB.
	maxHeldInput = 32 << 10
	// inputQuietGap is how long a read must wait for input before the decoder is
	// taken to have flushed: its own escape timeout is 50ms, the rest is margin.
	inputQuietGap = time.Second
)

var errInputOverBudget = errors.New("session input over budget")

const inputLimitNotice = "\r\nToo much input too fast; the session was closed.\r\n"

// inputGuard is the session's stdin as bubbletea reads it. It passes bytes through
// untouched: the limits are on how many, not which, so there is no second parser to
// drift out of step with bubbletea's.
//
// Read runs on bubbletea's input goroutine and filter on its event loop; the two
// share only atomics.
type inputGuard struct {
	r      io.Reader
	now    func() time.Time
	onTrip func()

	// pasting is what bubbletea's decoder reported: between a PasteStartMsg and the
	// paste itself it is buffering, and a pause in the input does not flush it.
	pasting atomic.Bool
	tripped atomic.Bool

	tokens float64
	last   time.Time
	held   int
}

func newInputGuard(r io.Reader, onTrip func()) *inputGuard {
	return &inputGuard{r: r, now: time.Now, onTrip: onTrip, tokens: inputBurstBytes}
}

func (g *inputGuard) Read(p []byte) (int, error) {
	if g.tripped.Load() {
		return 0, errInputOverBudget
	}
	start := g.now()
	n, err := g.r.Read(p)
	if n > 0 && !g.admit(n, g.now().Sub(start)) {
		g.tripped.Store(true)
		if g.onTrip != nil {
			g.onTrip()
		}
		return 0, errInputOverBudget
	}
	return n, err //nolint:wrapcheck // io.EOF has to reach bubbletea as itself
}

// admit spends n bytes from the rate budget and adds them to what the decoder may be
// holding, which starts over only after a read waited out a quiet gap.
func (g *inputGuard) admit(n int, waited time.Duration) bool {
	now := g.now()
	if !g.last.IsZero() {
		g.tokens = min(inputBurstBytes, g.tokens+now.Sub(g.last).Seconds()*inputBytesPerSecond)
	}
	g.last = now
	g.tokens -= float64(n)

	if waited >= inputQuietGap && !g.pasting.Load() {
		g.held = 0
	}
	g.held += n
	return g.tokens >= 0 && g.held <= maxHeldInput
}

// filter tracks the decoder's paste state from the messages it emits, then applies
// the session's own message rules.
func (g *inputGuard) filter(m tea.Model, msg tea.Msg) tea.Msg {
	switch msg.(type) {
	case tea.PasteStartMsg:
		g.pasting.Store(true)
	case tea.PasteMsg, tea.PasteEndMsg:
		g.pasting.Store(false)
	}
	return filterSessionMsg(m, msg)
}
