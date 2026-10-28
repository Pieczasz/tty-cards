package ssh

import tea "charm.land/bubbletea/v2"

// The largest terminal a session is drawn at, whatever its client asks for. bubbletea's
// renderer holds two cell buffers (~230 B a cell) for a session's whole life and
// compares the whole frame on every tick, so cells are what a session costs in memory
// and CPU. MAX_CONNECTIONS at 120x40 is what decision #60 measured, and 120x40 clears
// every layout breakpoint (the full table needs 80x40).
//
// ponytail: a fixed cap, not a size scaled to how full the server is (the tiers this
// replaced bought quiet servers a bigger screen for ~300 lines of budget bookkeeping).
// Raise the constants together with a cmd/loadtest run if big terminals matter.
const (
	maxTermWidth  = 120
	maxTermHeight = 40
)

// capSize clamps a terminal to the cap.
func capSize(w, h int) (int, int) {
	return min(w, maxTermWidth), min(h, maxTermHeight)
}

// capWindowSize caps a resize. The renderer's start size never passes through the
// filter, which is why sessionProgram caps that one itself.
func capWindowSize(msg tea.Msg) tea.Msg {
	if m, ok := msg.(tea.WindowSizeMsg); ok {
		w, h := capSize(m.Width, m.Height)
		return tea.WindowSizeMsg{Width: w, Height: h}
	}
	return msg
}
