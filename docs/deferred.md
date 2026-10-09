# Deferred design work

The 2026-10 design review (SOLID, idiomatic Go, 100 Go Mistakes) was acted on in the
`fix/review-2026-09` series. This file holds what it deliberately did **not** do:
either the cost outweighed the gain today, or the change only pays once something
else exists. Each entry says what triggers it, so it is picked up then rather than
forgotten or done early.

The list lives here, not as markers in the code, because `godox` fails the lint on a
`TODO`/`FIXME` comment. When you act on an entry, delete it in the same commit; when a
reason here stops being true, fix the entry.

## Leave alone until the trigger arrives

| What | Trigger | Why not now |
|---|---|---|
| Split the domain types from the GORM models (`db.User`, `db.Ranking`, `db.Match` carry `gorm.Model` and are rendered by the TUI and the stats API) | A second store, or a TUI bug from a missed `Preload` | Every consumer reads plain fields; a parallel set of read models would be copied by hand with nothing to swap in. `db` stays the shared contract (decisions #26, #50). |
| Move presence and the session lifecycle out of `internal/ssh` (`SessionTracker`, displacement, the resume generation, `ssh.Deps` forwarding to `tui.Deps`) | A second transport (a web terminal) | It would be a `lobby.Manager` `Attach`/`Detach(user, gen)` and an `ssh`-owned session-factory interface built in `cmd/server`. With one transport the indirection buys nothing. A cheap half-step when it starts: `tui.ReleaseSeat` mirroring `tui.ResumeSeat`, so `ssh` stops importing `lobby`. |
| A template-method framework for game views (Session owns `Update`/`View` and calls a per-game interface) | A sixth game whose view is mostly hearts/gin plumbing | Poker doesn't fit the frame (its own breakpoint, no hints line), and `ShedTable` already covers the two games where the variation is small. Rule-of-three helpers are listed below instead. |
| Split `internal/game` into an SPI package (`Rules`, `State`, `Action`) and the engine | Rules packages start needing engine internals, or a second engine | The semgrep rule `rules-package-reaches-the-engine` gives the same guarantee in a few lines, and a split would need an exported setter for `State`'s logger. |
| `Engine[S]` generics, or a typed `Extra` adapter | A sixth game | The lobby, router and catalog hold engines of different games, so `Engine[S]` ripples everywhere. Each rules package reads `Extra` through one panicking `extraOf`, which is the cheap version. |

## Considered in the series and rejected or postponed

- **Move `State.Phase` onto the engine, unexport `State.LeftPlayers`.** No rules set writes `Phase`, about 40 fixtures set it, and only the poker view reads it. Poker's pots and standings and `RankByScore` legitimately read `LeftPlayers`.
- **Merge the two leave hooks** (`OnPlayerLeave` / `AfterPlayerRemoved`). `LeaveOutcome` makes it easy later. Do it when a fifth user of the hook needs state carried between the two calls (uno stashes `leaverWasOnTurn` today).
- **A generic per-viewer projection** (`Rules.ProjectFor(viewer) any`). Poker is the only game with hidden table state on screen, and it has `poker.SeatsFor`. Hearts' `PendingPasses` is hidden but no view reads it.
- **A manager-wide ratings overlay, or reloading the user from `Profiles` on entering the lobby.** The reload can run before the finalize commits, and the overlay would be a cache that's never evicted. Ratings are instead copied onto the seated player after a ranked write. **Remaining gaps:** a player seated nowhere when the write commits keeps the old rating in their session until they reconnect. Two finalizes for the same user and game that commit out of order (a second match finishing inside the first one's 15s write window) can put the older rating back on the seat; the database stays correct. Carry a per-user version, or skip a refresh whose ranking row is older, if it is ever seen.
- **Removing `Lobby.Seat`'s engine-roster recheck.** The idle hook runs after the engine lock is released, so the window it guards is still reachable.
- **Drop the public-lobby cache.** Measured and kept (decision #62). If a cached refresh (about 70µs at 1000 tables) ever shows in a profile, the next step is a per-lobby entry that the lobby's own writes keep current, not removing the cache.
- **`lobby.BeginShutdown` ending live tables itself.** The session WaitGroup plus the watcher gate cover the shutdown race. The "server is restarting" notice is mostly unseen, because `Close` has already closed the connection.
- **Finalize asks `Rules.Standings` on a table that ended with a rules error** (via `Engine.Standings`) to write it unrated (decision #11). The places may come from a half-applied state. Revisit if a rules error ever lands mid-scoring in production.
- **`RankByScore` orders leavers by score, last to leave first on ties.** Nothing in the five current games can observe the difference, but a game whose leavers keep a meaningful score should check it.

## Lower-priority review items

Small, local, and safe to pick up whenever their file is open anyway.

- **TUI**
  - `router.GlobalContext` is about 12.9 KB (12.8 KB of it `styles.Theme`) and mixes three lifetimes: session deps, router-owned screen state and derived identity. Split it into a validated `Session` and a router-owned `Screen`, which would also remove the nil branches production code carries for partly built test contexts.
  - `styles.Theme` is a pure function of one bool. Two immutable package-level themes would make the per-theme caches correct by construction. The five copy-pasted `sync.Map` caches rely on convention for their size bound (`RenderFigureASCII` is exported and takes any string).
  - View plumbing repeated across hearts, gin rummy and the shedding table: `Session.HandleCursorKey`, a `Session.Overlay` for the forfeit, hand-over and waiting prelude, a shared hero-hand band, and one catalog-wide test that mid-game esc shows the forfeit prompt and a following non-`y` key neither leaves nor plays.
  - `gameview.RenderHand` picks single-select or multi-select by `staged == nil` versus empty; make the mode explicit.
  - Poker's raise prompt is an unnamed modal sub-state; extract a `raisePrompt` type like `ChoicePicker`. `RenderBands` could skip an empty hints line, and the hero rotation could be generic, so poker stops copying both.
  - `gameview.Session.Bound` is still exported: hearts' view tests build `model{Bound: ...}` without a subscription.
  - The create form starts at 4 seats before clamping to the selected game.
  - `tuitest` has no command driver or context fixture, so each test package writes its own.
- **Engine and rules**
  - Test seams in the production API: `Engine.CurrentPlayerID`, `Engine.SubscriberCount`, `Engine.WithState` and `Lobby.ActiveGame` are reached only from tests (the semgrep fence keeps them out of the TUI).
  - The engine's own `EventGameStarted` reaches no subscriber, because the lobby subscribes after `Start` returns.
  - Poker's `PhaseShowdown` really means "hand over" (`ReachedShowdown` tells the two apart). Rename it.
  - `deck.AllRanks` is an exported mutable slice; make it a function or an array.
  - Crazy eights' "eight is wild" is spelled in the rules, the view and the test descriptor. Export `crazyeight.IsWild` like uno's.
- **Persistence**
  - `UpdateUserActivity(ctx, *User, *PublicSSHKey)` takes loaded objects back. It once rewrote every ranking row of a player on login; take IDs instead.
  - `MatchRepository` takes parallel slices (`orderedUserIDs`, `places`, `leavers`) where `rating.Seat{UserID, Place}` already exists.
  - The leaderboard cache (TTL, singleflight, invalidation on erasure) lives inside the GORM repository, testable only with Docker and a sleep. Extract an unexported cache type with an injected clock and fetch function.
  - `LoadUserByFingerprint` reports "absent" as `nil, nil, nil`, while `UserProfile` and `DeleteAccount` use `db.ErrUserNotFound`.
  - `SetupTestDB` starts one Postgres container per test. One container per package with a template database would cut CI time, and would ease the container-startup timeouts the full `make cover` run hits on a loaded laptop.
- **Transport and config**
  - `ssh/server.go` holds eight concerns while its tests are already split by concern. Split it by file, not into subpackages.
  - The `pprof` build tag starts its server from `init()` with no stop and before logging is installed.
  - `ratelimit.NetKey`/`WideKey` accept any string, so failing closed is each caller's job. Take `netip.Addr`.
  - `config.Validate` returns the first range error, and runs only after parsing succeeds, so "reports every bad variable at once" is only partly true.
  - The stats API's leaderboard maximum (200) matches the repository's cache size only by a comment.
- **Cross-cutting**
  - `elo` logs its "can't happen" conditions through bare `slog` instead of returning them.
  - `ActionRejected` is counted in the game view while every other game metric comes from engine events in the lobby.
  - Package-name collisions are aliased five different ways (`logic`, `*rules`/`*view`, `internallobby`, `lobbyview`, `crazyview`); pick one convention.
  - `internal/systemtest` is app-level (catalog, lobby and engine with a fake repository), and the real through-the-TUI end-to-end test is `cmd/loadtest/bot_test.go`. Rename or document the tiers.
