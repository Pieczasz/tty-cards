---
title: "Where the frames went"
description: "Profiling the tty.cards render path: thousands of allocations to draw a poker table, what pprof said about them, and the one optimisation that got thrown away."
date: 2026-08-08
draft: false
---

Nobody complained about speed. The question was how many players fit on one VPS, and that
needs a number for what a drawn frame costs.

A frame is one `View()` call - the string a terminal gets after a keypress. The first
pass, benchmarked against a real model with a real engine behind it, looked like this:

```
BenchmarkPokerView_Render/seats=6      1,036 µs/op   4,151 allocs/op
BenchmarkCrazyEightView_Render/seats=4 1,298 µs/op   4,310 allocs/op
```

The microseconds are fine. Four thousand allocations per keypress is the number to look at.

## Profiling

```sh
go test -run='^$' -bench=BenchmarkPokerView_Render -memprofile=mem.out \
  ./internal/tui/views/gameview/poker/
go tool pprof -sample_index=alloc_objects -top mem.out
```

`alloc_objects` counts allocations instead of weighing them, which reorders the list
completely - the top entry by bytes was a buffer allocated once, the top by count was a
function returning a one-space string a few thousand times. `-peek` on a suspicious leaf
prints its callers with their share, which separates "slow" from "called by everyone".

## Half the frame was whitespace

48.6% of a frame's allocations were lipgloss generating blank space. `PlaceHorizontal`
builds padding one rune at a time through a style renderer that supports a styled fill and
a custom rune - neither of which we use. Every framed screen calls it three times, and
every card and seat inside calls it again.

Replacement is the same function without those, padding served from one preallocated run
(`internal/tui/styles/pad.go`):

```go
// internal/tui/styles/pad.go
const padCacheWidth = 512

var padCache = strings.Repeat(" ", padCacheWidth)

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	if n <= padCacheWidth {
		return padCache[:n]
	}
	return strings.Repeat(" ", n)
}
```

In isolation: 82,255 ns -> 4,199 ns, 1,239 allocs -> 2.

It is held to lipgloss's output by a `rapid` property test over random content, widths,
heights and all nine positions (`internal/tui/styles/pad_test.go`) - which caught the
first version being wrong. lipgloss special-cases `Left` and `Right` in the opposite
direction to its own centre formula, so unifying them mirrored the layout.

## Cards are pure functions

A rendered card depends on rank, suit, selected, and dark palette. A few hundred strings
for the life of the process (`internal/tui/components/card.go`):

```go
// internal/tui/components/card.go
type cardKey struct {
	rank     deck.Rank
	suit     deck.Suit
	selected bool
	dark     bool
}

var cardCache sync.Map // cardKey -> string
```

The palette collapses to one bool because every colour in a `Theme` is a function of
`Dark` - `TestRenderCard_CacheKeyCoversTheWholePalette` fails if anyone adds one that
isn't. Same for the columns of an overlapping fan, which mattered more: every row of
every card was re-emitting its own colour escape sequence each frame.

That pass took poker from 4,151 allocs down toward a thousand, and Crazy Eights with it.

## The unbenchmarked views were worse

The menus had no benchmarks, on the assumption they were trivial. First measurements:

| view | allocs/frame |
| --- | --- |
| Home | 17,588 |
| Profile | 12,411 |
| Leaderboard | 12,003 |
| Poker, 6 seats *(mid-pass)* | ~930 |

`go-figure` draws the banner at the top of every screen and re-parsed the whole figlet
font on every call - most of a menu frame. Fixed titles now go through
`styles.RenderFigureASCII`, which memoises per `(text, font)`
(`internal/tui/styles/common.go`).

## The cache that was a vulnerability

The home screen used to banner the player's own username through that shared cache, and
anyone with an SSH key can register one. So the cache was keyed on attacker-supplied
data, one multi-line string per key, unbounded.

The shared cache is now only for fixed strings in the source. Player-chosen text uses
`RenderFigureUncached`, and the home view keeps a per-session memo
(`internal/tui/views/home/home.go`):

```go
// internal/tui/views/home/home.go
title := styles.RenderFigureASCII("tty cards", width, styles.TitleHeightBudget(g.Height))
// ...
theme.Welcome.Render(styles.RenderFigureASCII("Welcome", width, each)),
theme.Accented.Render(m.nameBannerFor(name, width, each)),

// nameBannerFor renders outside the shared banner cache: a player chooses their
// name, and a cache keyed on it would let any account mint entries.
func (m *model) nameBannerFor(name string, width, height int) string {
	key := nameBannerKey{name: name, width: width, height: height}
	if key != m.nameKey || m.nameBanner == "" {
		m.nameKey, m.nameBanner = key, styles.RenderFigureUncached(name, width, height)
	}
	return m.nameBanner
}
```

A second cache went entirely. Caching the wrapped header and footer was worth ~17%, but
its keys were whole rendered banners, username included, and nothing forced headers to
stay static. 17% on a keypress-driven render is not worth leaving that for the next
person.

## One optimisation was fake

Walking lines with a callback instead of `strings.Split` went from two allocations to one.
At frame level it was neutral to slightly worse: the closure escapes to the heap and it
scans the string twice. Reverted.

Which is why the benchmarks call `View()` and not the helpers.

## The bug was not in our code

Frame allocs were only half the capacity question. The other half was what one SSH
session retained on the heap. Profiling that pointed past the app into
[charmbracelet/ultraviolet](https://github.com/charmbracelet/ultraviolet) - the
terminal layer under Bubble Tea v2 and Lip Gloss v2.

Every `NewTerminalReader` built its own key lookup table and kept it for the reader's
life. Readers never write to it, and with terminfo off the table depends only on the
legacy flags, so a wish/SSH server with one Bubble Tea program per connection was
keeping an identical ~137 KiB table per connected user:

| per `NewTerminalReader` | today | shared |
| --- | --- | --- |
| retained heap | 136.7 KiB | 0.9 KiB |
| build time | 135 µs | ~100 ns |
| allocated | 300 KB / 1,262 allocs | 384 B / 3 allocs |

At 1,000 sessions that was 137 MB - about 12% of the live heap, and the largest item
after the cell buffers. A 120×40 session dropped from 1.30 to 1.17 MiB with the table
shared. There is no downstream workaround: the table is unexported and built inside
the constructor.

Filed upstream, with a PR that caches one table per legacy-flag set (and deliberately
does not cache the terminfo path, because `TERM` is client-chosen on SSH):

- issue: [ultraviolet#197](https://github.com/charmbracelet/ultraviolet/issues/197)
- PR: [ultraviolet#199](https://github.com/charmbracelet/ultraviolet/pull/199)

The same pass found a correctness bug next to it: `Legacy`, `UseTerminfo` and
`SetLogger` set after `NewTerminalReader` were silently ignored, because the
constructor copies them into the scanner before the caller can set them. Bubble Tea
never sets those fields, so most apps never notice; direct `TerminalReader` users do.

- issue: [ultraviolet#196](https://github.com/charmbracelet/ultraviolet/issues/196)
- PR: [ultraviolet#198](https://github.com/charmbracelet/ultraviolet/pull/198)

Both were opened first against bubbletea
([#1824](https://github.com/charmbracelet/bubbletea/issues/1824),
[#1823](https://github.com/charmbracelet/bubbletea/issues/1823)) and refiled on
ultraviolet, which owns the reader. They are still open upstream as of writing.

## Allocations are the ceiling

Allocation rate decides whether more cores become more players. On the parallel
benchmark, `GOGC=100` turned more cores into a weak win; GC was the ceiling, not CPU.
`GOGC=400` turned the same jump into a real one - compose documents ~62% more frames a
second on 6 cores. `compose.yaml` sets `GOGC=400` with `GOMEMLIMIT=2560MiB` as the
backstop against that relaxed collector.

Re-measured on the same benches today:

| | first pass | now |
| --- | --- | --- |
| Poker frame, 6 seats | 1,036 µs / 4,151 allocs | **~374 µs / ~660 allocs** |
| Crazy Eights frame, 4 seats | 1,298 µs / 4,310 allocs | **~911 µs / ~618 allocs** |
| Home screen | 1,353 µs / 17,588 allocs | **~361 µs / ~168 allocs** |

What is left is mostly lipgloss composing the 120×40 border and wrapping text. No
shortcut there, and it only runs on keypress - a browsing player is cheap against a
table that redraws on every event.
