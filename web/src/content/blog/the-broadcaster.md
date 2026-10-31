---
title: "Broadcaster"
description: "How tty.cards fans game events out to every connected player: one generic in-memory broadcaster, buffered channels, and latest-wins dropping."
date: 2026-08-07
draft: false
---

When the game needed a way to tell every player at a table that something had
happened, a library felt like the wrong shape. What the problem actually wanted was a
fan-out with a per-subscriber buffer and an explicit drop policy, which is small enough
to own outright.

## What it actually is

`internal/broadcaster/broadcaster.go` is about 140 lines and generic over the message
type:

```go
// internal/broadcaster/broadcaster.go
type Broadcaster[T any] struct {
	mu             sync.Mutex
	subscribers    map[<-chan T]*subscriber[T]
	nextID         int
	closed         bool
	maxSubscribers int
	dropped        atomic.Int64
}
```

`Subscribe()` hands back a `<-chan T` with a 256-slot buffer. `Broadcast(msg)` walks
the map and sends to each one. `Unsubscribe(ch)` finds it, deletes it, closes it. The
lobby uses it for `lobby.Event`, the game engine uses it for `game.Event`. Same type,
two instantiations.

That is the whole thing. No topics, no routing keys, no ack.

## What happens when a reader falls behind

The interesting decision is what happens when a subscriber is not draining fast
enough. The obvious options are block or drop, and blocking is out: `Broadcast` runs
while the engine holds its mutex, so one slow reader would stall the entire table.

So it drops. But the direction matters:

```go
// internal/broadcaster/broadcaster.go
select {
case sub.ch <- msg:
default:
	// Buffer full: drop the oldest and enqueue the newest (latest-wins)
	// so slow subscribers don't get stuck on stale state.
	select {
	case <-sub.ch:
	default:
	}
	sub.ch <- msg
}
```

Dropping the *newest* message is the easy version and it is wrong here. A card game
view only ever wants the current state of the table. If a client stalls for two
seconds, the thing it needs on waking is the newest snapshot, not the one from two
seconds ago followed by a queue it has to chew through. So the full-buffer path
discards the head and pushes the tail. Latest-wins.

It is the same trade a shallow receive buffer makes: fresh data over complete data.

## Subscribe returns an error, and that was a bug fix

The first version returned a closed channel when the broadcaster was full or shut
down. That reads fine until you look at it from the caller's side:

```go
ch := b.Subscribe()
for ev := range ch { ... }   // exits immediately. why?
```

A closed channel and a finished game are indistinguishable from inside a `range`. A
view handed one would sit there having silently received nothing, with no way to say
whether the game ended or it never got attached. So now:

```go
// internal/broadcaster/broadcaster.go
var (
	ErrClosed     = errors.New("broadcaster is closed")
	ErrAtCapacity = errors.New("broadcaster is at subscriber capacity")
)
```

The caller has to look. The lobby view puts the error on screen
(`internal/tui/views/lobbyview/lobby.go`), which is how you find out that the capacity is
too low instead of wondering why the roster stopped updating.

## Capacity is `2*len(players) + 8`

Engines size their broadcaster at two feeds per seat plus eight
(`internal/game/engine.go`). One feed is the live seat; the second covers a reconnect
that still overlaps the old session's teardown. The eight is for non-player
subscribers - the lobby's watcher goroutine that waits for the game to end so it can
write the ranked result - plus a little spare.

Get that number wrong and a real player is the one who gets refused, which is the worst
possible thing to run out of.

## What it does not do

One process, one node. There is no cross-machine story here at all: subscribers are
map entries in memory, and if you wanted two servers you would replace this with real
pub/sub and nothing else about it would survive. The package comment says so out loud
rather than pretending it is more than it is.

For a game where a session is a few minutes and everyone at a table is attached to the
same process anyway, that is a fine place to stop.
