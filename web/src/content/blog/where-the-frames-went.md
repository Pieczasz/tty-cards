---
title: "Where the frames went"
description: "Profiling the tty.cards render path: 4,151 allocations to draw a poker table, what pprof said about them, and the one optimisation that got thrown away."
date: 2026-08-08
draft: false
---

Nobody complained about speed. The question was how many players fit on one VPS, and that
needs a number for what a drawn frame costs.

A frame is one `View()` call - the string a terminal gets after a keypress. Benchmarked
against a real model with a real engine behind it:

```
BenchmarkPokerView_Render/seats=6      1,036 µs/op   4,151 allocs/op
BenchmarkCrazyEightView_Render/seats=4 1,298 µs/op   4,310 allocs/op
```

The microseconds are fine. Four thousand allocations per keypress is the number to look at.

## Profiling

```sh
go test -run='^$' -bench=BenchmarkPokerView_Render -memprofile=mem.out ./internal/tui/views/gameview/poker/
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

Replacement is the same function without those, padding served from one preallocated run:

```go
var padCache = strings.Repeat(" ", 512)

func spaces(n int) string {
	if n <= len(padCache) {
		return padCache[:n]
	}
	return strings.Repeat(" ", n)
}
```

In isolation: 82,255 ns -> 4,199 ns, 1,239 allocs -> 2.

It is held to lipgloss's output by a `rapid` property test over random content, widths,
heights and all nine positions - which caught the first version being wrong. lipgloss
special-cases `Left` and `Right` in the opposite direction to its own centre formula, so
unifying them mirrored the layout.

## Cards are pure functions

A rendered card depends on rank, suit, selected, and dark palette. A few hundred strings
for the life of the process.

```go
type cardKey struct {
	rank     deck.Rank
	suit     deck.Suit
	selected bool
	dark     bool
}

var cardCache sync.Map // cardKey -> string
```

The palette collapses to one bool because every colour in a `Theme` is a function of
`Dark` - a test fails if anyone adds one that isn't. Same for the columns of an overlapping
fan, which mattered more: every row of every card was re-emitting its own colour escape
sequence each frame.

Poker 4,151 -> 930. Crazy Eights 4,310 -> 563.

## The unbenchmarked views were worse

The menus had no benchmarks, on the assumption they were trivial:

| view | allocs/frame |
| --- | --- |
| Home | 17,588 |
| Profile | 12,411 |
| Leaderboard | 12,003 |
| Poker, 6 seats *(optimised)* | 930 |

`go-figure` draws the banner at the top of every screen and re-parses the whole figlet font
on every call - 85% of a menu frame. It memoises like a card does; every menu drops to
~6,100.

## The cache that was a vulnerability

```go
// home.go
banner := styles.RenderFigureASCII(welcomeUser, innerWidth)
```

The home screen banners the player's own username, and anyone with an SSH key can register
one. So that cache was keyed on attacker-supplied data, one multi-line string per key,
unbounded. Now capped at 512:

```go
banner := renderFigureASCII(text, maxWidth)
if figureCached.Load() < maxFigureKeys {
	if _, loaded := figureCache.LoadOrStore(key, banner); !loaded {
		figureCached.Add(1)
	}
}
return banner
```

Past the cap banners still render, they just stop being remembered. The fixed screen titles
are in by then.

Later the cap went too. The shared cache now holds fixed titles only - every key is a string
in the source - and a player's name is rendered outside it and memoised on that player's own
session, so there is no shared key left for anyone to mint.

A second cache went entirely. Caching the wrapped header and footer was worth ~17%, but its
keys were whole rendered banners, username included, and nothing forced headers to stay
static. 17% on a keypress-driven render is not worth leaving that for the next person.

## One optimisation was fake

Walking lines with a callback instead of `strings.Split` went from two allocations to one.
At frame level it was neutral to slightly worse: the closure escapes to the heap and it
scans the string twice. Reverted.

Which is why the benchmarks call `View()` and not the helpers.

## Allocations are the ceiling

Allocation rate decides whether more cores become more players. On the parallel benchmark,
`GOGC=100` turned 2->6 cores into 1.4×; GC was the ceiling, not CPU. `GOGC=400` turned the
same jump into 2.2×. Compose sets that, with `GOMEMLIMIT` as the backstop.

| | before | after |
| --- | --- | --- |
| Poker frame, 6 seats | 1,036 µs / 4,151 allocs | **726 µs / 930 allocs** |
| Crazy Eights frame | 1,298 µs / 4,310 allocs | **804 µs / 563 allocs** |
| Home screen | 1,353 µs / 17,588 allocs | **794 µs / 6,165 allocs** |
| Parallel render @ 6 cores | 1,917 renders/s | **4,683 renders/s** |

The ~6,000 left in a menu frame were lipgloss composing the 120×40 border and wrapping text,
and this post used to say there was no shortcut there. There was one.

## Update, October 2026: the border

lipgloss word-wraps a whole frame before it draws the border around it, and by then the frame
has already been sized to fit, so the wrap never changes anything. Under 1,000 load-tested
sessions that wrap was 38% of every byte the server allocated. The border, padding and
centring are now built directly whenever the wrap would leave the text as it is - every line
fits, none leaves a colour open, none holds a control character - and lipgloss gets
everything else. The same kind of `rapid` test holds it to lipgloss's output.

| 120×40 menu frame layout | before | after |
| --- | --- | --- |
| time | 1,026 µs | 223 µs |
| allocations | 6,281 | 60 |
| bytes | 222 KB | 41 KB |

The joins went the same way earlier: lipgloss's grew an unsized builder over a whole-screen
frame, and zeroing that memory alone was 27% of CPU at 1,000 sessions. Byte-identical
replacements cut total server CPU there by a third.
