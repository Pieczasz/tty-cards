---
title: "Game state"
description: "Live game state in tty.cards lives in memory in a single Go process. What that means for the locking contract and for restarts."
date: 2026-08-05
draft: false
---

All live game state is a struct in memory in one process. No Redis, no actor per
table, no serialisation anywhere between a keypress and the rules that answer it.

## The locking contract

`Engine` holds one mutex for the whole of `Start`, `SubmitAction` and
`RemovePlayer` (`internal/game/engine.go`). `State` itself has no lock - it is
only ever reached while that mutex is held:

```go
// internal/game/engine.go
func (e *Engine) SubmitAction(playerID string, action Action) (err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// ...
	return e.applyActionLocked(current, action)
}
```

`Rules` methods are always called with it held. That gives implementations a useful
guarantee: a `Rules` can treat `*State` as if it owned it. Mutate the slice, reassign
the map, no copying, no retry loop, no version check.

The cost is one rule: **a `Rules` implementation must never call back into `Engine`.**
That deadlocks immediately. It is the entire concurrency contract and it fits in a
sentence, which is the trade I wanted. The same rule is written on
`WithState` and `BoundEngine.Frame`.

## Turn resolution

The cursor is settled in one place, `settleTurnLocked`, in this order
(`internal/game/engine.go`):

1. `State.OverrideNextTurn` if the rules set one
2. otherwise leave `State.CurrentTurn` where an advance already put it
3. clamp into the live seat range and arm the turn clock

Poker needs (1) constantly - after a betting round closes, the next actor is not the
next seat, it is first-to-act on the new street. So `ApplyAction` calls
`State.OverrideTurn` / `SetTurn` (`internal/game/turn.go`) and the engine picks it up.
Crazy Eights almost never sets it and just advances.

`ApplyAction` is the whole move. An error from it finishes the game
(`EndReasonRulesError`), because a rules set that cannot reach a consistent state
should not keep dealing. That is why anything checkable up front belongs in
`ValidateAction` instead (`internal/game/rules.go`).

## Disconnects took the longest

Not the rules. Not the TUI. A player closing a laptop mid-hand is the normal case, and
every layer has to agree on what just happened.

There is an optional interface for it (`internal/game/rules.go`):

```go
// internal/game/rules.go
type PlayerLeaveHandler interface {
	OnPlayerLeave(state *State, playerID string)
	AfterPlayerRemoved(state *State, removedIndex int) LeaveOutcome
}
```

Two hooks and not one, because poker needs both sides of the removal
(`internal/game/poker/leave.go`). `OnPlayerLeave` folds them while the seat indices
are still the old ones. `AfterPlayerRemoved` runs once the slice has shifted, and
that is where the button and blinds get reindexed and the next actor is picked from
the *new* seat numbering. Doing either in the other's slot points a marker at the
wrong player. The return value tells the engine whether the leave settled, kept a
lone all-in hand open, or interrupted the match.

The failure that actually cost me an afternoon was quieter than any of that. A view
holding a broadcaster subscription has to release it (`internal/tui/router/router.go`):

```go
// internal/tui/router/router.go
type Closer interface{ Close() }
```

Skip it and the listener goroutine parks on a channel forever and keeps a subscriber
slot until the engine itself closes. Nothing fails. No assertion notices. You find out
weeks later from a graph. Packages that open subscriptions (and a lot of their
neighbours) now run `goleak.VerifyTestMain`, which is the only reason I would trust it.

## What a restart costs

Every table in flight, gone. Ranked results are written to Postgres when a hand
finishes, so nothing durable is lost, but a hand in progress is not recoverable.

For sessions measured in minutes that is fine. For sessions measured in hours it would
not be, and the fix would not be a small one.
