# Onboarding

Day one. What the product is, and what to do first.

This document does **not** restate the contracts or the invariants - those are
[`architecture.md`](architecture.md). Commands and environment variables live in
the repository [`README.md`](../README.md).

---

## 1. What it is

A multiplayer card-game server whose **only** client is an SSH terminal. A player
runs `ssh tty.cards`, the server allocates a PTY, and a Bubble Tea program
renders the whole interface as ANSI text over the SSH channel. There is no web
client, no game HTTP API and no WebSocket layer.

No download, no browser, no account form: the first connection with your SSH key
claims your username. That one product decision is why identity is a key
fingerprint, why anyone can register, and why every anti-farm rule in the rating
system exists - minting an identity is free.

Five games ship: **Crazy Eights**, **No-Limit Texas Hold'em** (a 10-hand match),
**Uno**, **Hearts** and **Gin Rummy**.

An Astro static marketing site lives in `web/` and is served by the same nginx,
but it is not part of the Go module - it consumes the read-only JSON in
`internal/httpapi`.

### Three commitments explain most of the code

1. **One process holds the table.** All live state is in memory in a single
   process. No Redis, no message broker, no shared cache. Postgres stores only
   what must outlive the process: users, keys, games, Elo, match history.
2. **The engine knows nothing about the terminal.** `internal/game` imports no
   `internal/tui`, no route strings, no database. That is enforced by `depguard`
   in `.golangci.yml`, not by convention.
3. **Nothing may hold the table hostage.** Every seat has a 30-second clock,
   three consecutive misses loses it, and every lock has a documented order.

### Design patterns actually in use

Named here so you recognise them on sight, not as an inventory.

| Pattern | Where |
|---|---|
| Elm architecture (Model/Update/View) | every `internal/tui/views/**` model |
| Fan-out broadcaster (observer) | `internal/broadcaster.Broadcaster[T]` - latest-wins, per-subscriber buffered channel |
| Strategy | `game.Rules`, implemented by all five rules packages; the engine calls rules, never the reverse |
| Factory + single registration point | `internal/catalog.Games` -> `game.Registry` |
| Functional options | `lobby.Option` (`WithCardGame`, `WithMaxPlayers`, `WithPrivate`, `WithRanked`), `game.EngineOption` (`WithLogger`, `WithOnIdleRemoved`, `WithTurnTimeout` - zero is the clock's off switch) |
| Facade | `game.BoundEngine` via `game.Bind(engine, playerID)` - nil-safe throughout |
| Embedded base type | `gameview.Session` in every game view's model; `shed.State` in the crazy eights and uno `Extra` |
| Repository | `db.Authenticator` / `db.Profiles` / `db.Leaderboard` (their union is `db.UserRepository`) and `db.MatchRepository`, declared in `internal/db`, implemented in `internal/repository` - each consumer takes the smallest one it calls |
| Middleware chain | `wish.WithMiddleware` in `internal/ssh/server.go`; `withCORS`/`withRateLimit` in `internal/httpapi` |
| Generation counter (fencing token) | the engine's `clock.generation`, `SessionTracker` generations |
| Snapshot / DTO | `game.StateSnapshot`, `game.PlayerSnapshot`, `lobby.BrowseEntry` - built under one lock, rendered lock-free |
| Double-checked locking with TTL | `repository.BestPlayers` (5 min) |
| Dirty-flag cache invalidation | `Manager.cacheDirty atomic.Bool` + a 2 s TTL - atomic and shared with every lobby, so a lobby sets it without the manager's lock |
| Optional interface (capability probe) | `game.PlayerLeaveHandler`, `TurnDurationHandler`, `router.Closer` |
| Sentinel errors | `broadcaster.ErrClosed`/`ErrAtCapacity`, `db.ErrUsernameTaken`… |

Idiomatic Go habits worth matching: `context.Context` first on every repository
method; `%w` wrapping with lowercase messages (`wrapcheck` is on); non-blocking
channel sends so a slow SSH client never stalls the engine; consumer-defined
interfaces (`httpapi.SessionCounter` exists so `httpapi` need not import `ssh`);
atomics only where a lock would invert an order; `defer` ordering used as a
correctness tool in `cmd/server/main.go`; `goleak` `TestMain` in 26 packages.

---

## 2. Your first day

In order. Each one teaches something the next assumes.

1. **Play a full hand of Crazy Eights against yourself** with
   `./scripts/dev-session.sh`. Then find your `matches` row and your
   `match_participants` rows in Postgres.
2. **Read [`architecture.md`](architecture.md).** Topology and invariants.
3. **Break something on purpose.** Change `MaxMissedTurns` to 1, rebuild, sit out
   a turn, and watch the seat go. Put it back.
4. **Drop a client mid-hand and reconnect inside 90 seconds.** Confirm you resume
   rather than forfeit, and find the `session dropped mid-game, holding the seat`
   log line.
5. **Add a sixth game's catalog entry** reusing an existing rules package with a
   new slug and name. Confirm the route appears and that you touched nothing in
   `internal/game`. Then revert it.
6. **Read [`decisions.md`](decisions.md).** You will not
   remember it; you will remember that it exists, which is the point.

Then pick an issue. [`CONTRIBUTING.md`](CONTRIBUTING.md) has the test conventions
and the one rule that matters most: **every fix ships a test that fails on the old
code** - one you have actually watched fail, not one that passes afterwards.

---

## 3. Premises that do not hold

Recorded so nobody goes looking for code that was never written. Several of these
are things a reader coming from a real-time game server would reasonably expect.

| Expected | Reality |
|---|---|
| **Tick rate / FPS, a logic loop decoupled from render** | No game loop and no frame loop exist. The engine advances on `SubmitAction` or a turn timer; the UI redraws on input, a broadcast or the countdown tick |
| **A worker pool** | None. No `errgroup`, no semaphore, no job queue. Concurrency is bounded by `netutil.LimitListener` and nginx `limit_conn ssh_addr 8` |
| **A ring buffer for dropped frames** | Latest-wins on a 256-deep buffered channel per subscriber. Same effect, different mechanism |
| **A Redis/Valkey scaling boundary, a storage abstraction to swap** | Absent by design. No cache interface, no pub/sub abstraction. The only mention in the repo is a design note in `broadcaster.go` naming Watermill-over-Redis as the upgrade path. `db.UserRepository` and `db.MatchRepository` *are* genuine swap points, but for Postgres, not for a cache |
| **A matchmaking queue** | None. Tables are found by browsing an Elo-proximity-ranked list capped at 20, or by 8-character code |
| **Collision detection / spatial logic** | Not applicable. Move legality is `Rules.ValidateAction` |
| **Mimir** | Not deployed. Metrics land in Prometheus' own TSDB via remote write |
| **Alloy scraping the Go process** | Inverted: the app **pushes** OTLP to Alloy, and nothing scrapes it. Alloy exports only the host (`prometheus.exporter.unix`); Prometheus scrapes the pipeline itself (Alloy, Loki, Tempo, Alertmanager) so a dead one reads `up == 0` |
| **Frame-render-time metrics** | Not instrumented. There are counters and four histograms (session duration, game duration, lobby time-to-start), but nothing times a render |
| **Tracing across game events** | Only `ssh.session` and ten `db.*` spans; the stats API is deliberately untraced. `game`, `lobby` and `tui` are untraced |
| **`tea.Every` subscription loops** | Not used. Periodic work is self-rescheduling `tea.Tick`, which is what lets the countdown change rate mid-turn |
| **A WebSocket or HTTP game path to replace** | There never was one. `internal/httpapi` is a read-only stats feed for the marketing site |

The two seams that *would* be the extension points if the single-node assumption
ever broke: `broadcaster.Broadcaster[T]`, where a distributed implementation would
have to preserve latest-wins and the `ErrClosed`/`ErrAtCapacity` contract; and
`Manager.lobbies` / `Manager.playerLobby`, where the hard part is not the storage
but moving the lock-order guarantees across a network.
