package ssh

import (
	"sync"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
)

// termSize is a terminal in cells.
type termSize struct{ w, h int }

func (s termSize) cells() int { return s.w * s.h }

// sizeTiers are the terminal sizes the server grants, smallest first. Each nests in
// the next, so the largest tier a session was ever granted bounds every buffer its
// renderer has allocated. The floor is granted to everyone, however full the server:
// MAX_CONNECTIONS at 120x40 is what decision #60 measured. It clears every layout
// breakpoint (the full table needs 80x40), so the tiers above only add room.
var sizeTiers = [...]termSize{{120, 40}, {160, 50}, {200, 60}, {250, 80}}

// bubbletea's renderer holds two cell buffers, ~230 B a cell, for a session's whole
// life, and compares the whole frame on every tick, so terminal cells are what a
// session costs in both memory and CPU. The server therefore shares them out.
const (
	// cellBudget is how many cells the live sessions may be drawing at once: a tier is
	// active while sessions x its cells fit. Decision #60: 850 browsing at 120x40 plus
	// 400 playing at 80x24 is 4.85M cells, and held on 3 cores; 1500 sessions at
	// similar sizes (~6.0M) saturated them. So 5M: 250 sessions at 250x80, 416 at
	// 200x60, 625 at 160x50, and past that everyone gets the floor.
	cellBudget = 5_000_000
	// extraCellBudget bounds the cells held above the floor. It is separate because
	// bubbletea never gives renderer memory back: a smaller WindowSizeMsg reslices
	// its buffers (ultraviolet Buffer.Resize), so a session keeps the largest tier it
	// was ever granted until it ends, and a tier step-down saves CPU but not memory.
	// Held memory is at most sessions x the floor (the pre-tier ceiling, 1250 x 4800 =
	// 6M cells) plus this: 1.5M cells is ~330 MiB, or 98 sessions at 250x80.
	extraCellBudget = 1_500_000
)

// nextTier is the largest tier whose cells for n sessions fit budget. Climbing past
// cur needs a tenth of the budget spare, so a count hovering at a threshold does not
// resize every big session back and forth.
func nextTier(cur, n, budget int) int {
	for t := len(sizeTiers) - 1; t > 0; t-- {
		limit := budget
		if t > cur {
			limit -= budget / 10
		}
		if n*sizeTiers[t].cells() <= limit {
			return t
		}
	}
	return 0
}

// tierFor is the smallest tier that fits want, or the top one.
func tierFor(want termSize) int {
	for t, s := range sizeTiers {
		if want.w <= s.w && want.h <= s.h {
			return t
		}
	}
	return len(sizeTiers) - 1
}

// sizeBudget is the server's share of terminal cells. The zero value grants only the
// floor. tier is read on every resize and written only under mu, on a session's
// start and end.
type sizeBudget struct {
	cells, extra int
	extraUsed    atomic.Int64
	tier         atomic.Int32

	mu   sync.Mutex
	live int
}

func newSessionRegistry(cells, extra int) *sessionRegistry {
	reg := &sessionRegistry{sizes: sizeBudget{cells: cells, extra: extra}}
	// An empty server is at the top, not climbing to it.
	reg.sizes.tier.Store(int32(len(sizeTiers) - 1))
	return reg
}

// join and leave count a session and report whether that moved the tier.
func (b *sizeBudget) join() bool { return b.recount(1) }

func (b *sizeBudget) leave(held int) bool {
	b.extraUsed.Add(-int64(sizeTiers[held].cells() - sizeTiers[0].cells()))
	return b.recount(-1)
}

func (b *sizeBudget) recount(delta int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.live += delta
	cur := int(b.tier.Load())
	next := nextTier(cur, b.live, b.cells)
	b.tier.Store(int32(next)) //nolint:gosec // a tier index, at most len(sizeTiers)
	return next != cur
}

// grant is the size a session asking for want is told, and the tier it then holds.
// The server's tier caps it; growing past what the session already holds takes cells
// from the extra budget, falling back tier by tier to what it holds when there is
// not room.
func (b *sizeBudget) grant(held int, want termSize) (termSize, int) {
	t := min(tierFor(want), int(b.tier.Load()))
	for t > held && !b.takeExtra(sizeTiers[t].cells()-sizeTiers[held].cells()) {
		t--
	}
	limit := sizeTiers[t]
	return termSize{min(want.w, limit.w), min(want.h, limit.h)}, max(held, t)
}

func (b *sizeBudget) takeExtra(n int) bool {
	if b.extraUsed.Add(int64(n)) > int64(b.extra) {
		b.extraUsed.Add(-int64(n))
		return false
	}
	return true
}

// retierMsg asks a session's event loop to grant its size again after the tier
// moved. It carries no size: the loop reads its own latest request, so a resize that
// lands while the nudge is in flight is never overwritten by a stale one.
type retierMsg struct{}

// sessionSize is one session's side of the budget. held and echo belong to the
// goroutine running the program (its event loop is the filter); wanted and granted
// are also read by the retier sweep.
type sessionSize struct {
	budget  *sizeBudget
	wanted  atomic.Pointer[termSize]
	granted atomic.Pointer[termSize]
	held    int
	// echo is the size the program started at until bubbletea's first WindowSizeMsg,
	// which repeats it rather than anything the client asked for.
	echo *termSize
}

// start joins the budget and returns the size the program starts at, and whether
// joining moved the tier.
func (sz *sessionSize) start(b *sizeBudget, want termSize) (termSize, bool) {
	sz.budget = b
	moved := b.join()
	sz.wanted.Store(&want)
	size := sz.regrant()
	sz.echo = &size
	return size, moved
}

func (sz *sessionSize) regrant() termSize {
	size, held := sz.budget.grant(sz.held, *sz.wanted.Load())
	sz.held = held
	sz.granted.Store(&size)
	return size
}

// stale reports whether the tier now grants this session a different size.
func (sz *sessionSize) stale() bool {
	want := *sz.wanted.Load()
	limit := sizeTiers[min(tierFor(want), int(sz.budget.tier.Load()))]
	return termSize{min(want.w, limit.w), min(want.h, limit.h)} != *sz.granted.Load()
}

// filter turns a resize into the granted size. The renderer never passes its start
// size through here, which is why start grants that one.
func (sz *sessionSize) filter(msg tea.Msg) tea.Msg {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		got := termSize{msg.Width, msg.Height}
		if sz.echo != nil && got == *sz.echo {
			sz.echo = nil
		} else {
			sz.wanted.Store(&got)
		}
		size := sz.regrant()
		return tea.WindowSizeMsg{Width: size.w, Height: size.h}
	case retierMsg:
		before := *sz.granted.Load()
		if size := sz.regrant(); size != before {
			return tea.WindowSizeMsg{Width: size.w, Height: size.h}
		}
		return nil
	}
	return msg
}
