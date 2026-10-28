# Architecture

The one design document. It says what the system is, how a session travels through it, and
what each layer may do; every section names the file to open. Reasons for the non-obvious
choices are in [`decisions.md`](decisions.md) (cited as `#N`); where code already explains
itself, this points at the comment instead of repeating it.

---

## 1. What this is

An SSH server that serves a terminal UI. You `ssh tty.cards`, the server allocates a PTY and a
Bubble Tea program renders the whole interface as ANSI text over the SSH channel: home, a
lobby browser, and card games against other people connected to the same process. There is no
web client, no game HTTP API and no WebSocket in the game path. Identity is the SSH public-key
fingerprint (#16); results persist to Postgres with Elo.

Five games: Crazy Eights, Uno, Hearts, Gin Rummy and No-Limit Texas Hold'em.

**One process holds the table.** Lobbies, engines and hands live only in RAM. Postgres holds
what must outlive the process: users, keys, games, rankings, match history. No Redis, no broker,
no shared cache. A read-only JSON API (`internal/httpapi`) feeds the marketing site in `web/`,
a separate Astro toolchain that is not part of the Go module.

**Not tick-driven.** There is no game loop. The engine advances only when a player submits an
action or a turn timer fires. Periodic work is per-session UI scheduling only: the turn
countdown (1s, 100ms under 6s), the lobby-browser refresh (2s), the router's idle watchdog
(quits after 5 min idle, never at a live table) and the engine's 30s turn clock.

---

## 2. Topology

```
        ssh client (port 22)           browser (ports 80, 443)
                 |                                |
                 v                                v
   +----------------------------------------------------------+
   |  nginx           internal/config/nginx.conf               |
   |  stream{}  limit_conn per client network, PROXY protocol  |
   |  http{}    :443 /api/ -> backend:6970, else redirect      |
   +----------------------------------------------------------+
                 |  PROXY header (real client IP)
                 v
   +-----------------------------------------+
   |  backend :6969 (Go)      cmd/server      |
   |   Wish SSH + Bubble Tea                  |
   |   lobby.Manager (in memory)              |
   |   game.Engine per match                  |
   |   stats API :6970       internal/httpapi |
   +-----------------------------------------+
          |                        |
          v                        v
   +---------------+   +---------------------------+
   | Postgres 18   |   | Alloy -> Loki/Tempo/Prom  |
   | users, keys,  |   |        -> Grafana         |
   | games, ranks, |   +---------------------------+
   | matches       |
   +---------------+
```

**Published ports are 22, 80 and 443, all on nginx** (IPv4 and IPv6). 80 is only the ACME
challenge, `/healthz` and a redirect; 443 serves the stats API with the certificate the
`certbot` service keeps current. Grafana is the one exception, on `127.0.0.1:3000`. Everything
else (Postgres, Alloy, Loki, Tempo, Prometheus, Alertmanager, the backend's 6969/6970) is
`expose`-only. Publishing 6969 lets a client forge the PROXY header; publishing 6970 lets it
forge `X-Forwarded-For` (#19). A bare local `ssh` sends no PROXY header, so local development
needs `PROXY_PROTOCOL=false` (`scripts/dev-session.sh`).

**Three compose networks.** `edge` holds nginx and the backend only, has IPv6 enabled (so an
IPv6 player reaches nginx with their own address instead of the bridge gateway's) and fixed
subnets `172.29.69.0/24` and `fd6b:1e37:9a52:6969::/64`, which the backend's
`PROXY_TRUSTED_CIDRS` names. `default` carries everything else. `docker-api` is `internal: true`
and holds only Alloy and `docker-socket-proxy`, which answers Alloy alone and only the GETs it
makes. A section-level allow would also serve any container's files, and even container
inspect carries every container's environment.

### Defence in depth

| Layer | Limit | Where |
|---|---|---|
| nginx stream | 8 conns per client network, 64 per IPv6 /48 | `nginx.conf` |
| nginx http | 16 per network, 2 r/s + burst on `/api/`, 429 on refusal | `nginx.conf` |
| PROXY header | honored only from `PROXY_TRUSTED_CIDRS` | `proxyListener`, `cmd/server/main.go` |
| listener | `2 x MAX_CONNECTIONS`, a silent backstop for handshake floods | `netutil.LimitListener` |
| session slots | `MAX_CONNECTIONS`, refused with a message | `ssh.SessionTracker` |
| auth | `RATE_LIMIT_CONNECTIONS` per `RATE_LIMIT_WINDOW`, per network | `ssh.rateLimitAuth` |
| registration | new accounts per `REGISTRATION_WINDOW`: per /64, per /48, server-wide; refunded when the registration fails | `ssh.allowRegistration` |
| database round trips at login | 5s each | `ssh.authTimeout` |
| session input | 1 KiB/s after a 16 KiB burst; 32 KiB held; over either the session closes | `ssh.inputGuard` |
| terminal size | capped at 120x40 (#60) | `internal/ssh/termsize.go` |
| channels | 2 session channels per connection; 32 `env` requests / 8 KiB | `ssh.limitSessionChannels` |
| account | one live session per account; a second displaces and closes the first (#14) | `SessionTracker.Connect` |
| lobby join | 10/s per player | `lobby.Manager.joinLimiter` |
| stats API | `API_REQUESTS_PER_MINUTE` per network | `httpapi.withRateLimit` |
| idle | TUI 5 min (not at a live table), SSH 30 min, handshake 20s | `router`, `wish` |

Every network limit keys on `ratelimit.NetKey`, which collapses IPv6 to its /64 (one customer is
handed 2^64 addresses); `WideKey` is the /48 (IPv4 stays per address) for budgets an attacker
can afford to spread. Both are total functions; the fail-closed decision is `ssh.clientHost`,
which refuses an unkeyable address rather than giving every such peer one shared bucket. nginx
repeats the /64 rule in a `map` (duplicated across `stream` and `http`); `NetKey` is the
authority and `nginx.conf` says so.

---

## 3. A session, end to end

```
SSH connection -> wish middleware -> Bubble Tea Router -> view -> game.BoundEngine -> game.Engine -> Rules
```

**Middleware** in `wish.WithMiddleware` runs last-first, so `sessionLifecycle` is listed last to
be outermost. It admits the handler (`admit`, counted so shutdown can wait for teardowns),
records state in a per-server `sessionRegistry` (never `s.Context()`, which is per
*connection* and shared by every channel), and runs the deferred teardown: size, model close,
release of the seat and slot, panic recovery, span end. `recoverSession` must stay a *direct*
defer (a `recover()` one call deeper returns nil); what the player sees comes from
`reportingModel`, which wraps `Init/Update/View` because nothing panics *out of* bubbletea.

**Login.** `sessionModel` resolves the fingerprint (`LoadOrRegisterUser`, each round trip bounded
by `authTimeout`). A newcomer first passes the registration gate, which checks for a free session
slot *before* the account exists, so a full server never registers someone it cannot seat; if
the registration then fails (a taken name, a racing key) the budget it spent is refunded. The TUI
model is built before the slot is claimed, so a constructor panic strands nothing. Once
`Tracker.Connect` hands over the slot, the session state records user, generation and model
immediately, then `tui.ResumeSeat` cancels any disconnect grace holding the player's seat.

**Teardown** gives up the seat and the slot as one step: `ReleaseWith(user, gen, fn)` runs
`DisconnectPlayer` under the account's lock for the live generation only, and frees the slot in a
defer so a panic in `fn` cannot strand it (#45). A displaced session (stale generation) touches
neither.

**Views.** `router.Router` owns screens, typed `router.Route`s (`GameRoute(slug)`) and
`GlobalContext`. A game view embeds `gameview.Session`, which owns what is the same in all five
games: subscribing to the seat the lobby bound, the `Update` loop, the last rejected move, the
hand cursor, the forfeit prompt (esc at a live table asks first), the shared frame and the
between-hands screen. A new game implements its own rules rendering and nothing else. Any view
that subscribes implements `router.Closer`; the router calls it on navigation and ssh on
disconnect, or a listener goroutine and a subscriber slot leak.

Anything a view keeps after `Frame`/`Sync` must be copied, not aliased; the hand-based views
copy in one pure `readTable`. Every TUI feed message carries the channel that delivered it, and a
view drops one stamped with any other.

---

## 4. Contracts

### Engine and rules (`internal/game`)

`Engine.mu` is the only lock and covers the clock fields and `State` (#1). `Rules` methods
(and `WithState`/`Frame` callbacks) run with it held, receive only `*State`, and must not call
back into the engine. Per-game state lives in `State.Extra`, read through one panicking
`extraOf(state)` per rules package; hand-based games name where a hand is with a `Phase`.

The turn cursor moves through two functions: after an accepted action `advanceTurnLocked` takes
`State.OverrideNextTurn`, else the next seat; `settleTurnLocked` (start and leave paths) takes the
override, else stays. Both clamp and arm the clock. `ApplyAction` is the whole move, the rules'
own after-step included; an error from it ends the table (`EndReasonRulesError`), so anything
checkable up front belongs in `ValidateAction`. Every `ApplyAction` ends in a `default:` arm
returning `ErrUnknownAction`. Every end goes through `endGameLocked(winner, reason)`; the
`EndReason` (win / rules error / forfeit / abandoned / interrupted) is recorded on the engine
because an end on a closed engine broadcasts nothing and the lobby still has to finalize it.

**Turn clock** (#4). Settling the cursor arms a 30s per-turn timer. Three consecutive missed
turns remove the seat and report it through `WithOnIdleRemoved`, a lossless hook that runs on the
timer goroutine after the lock is released (the lobby roster follows it; the feed's
`EventPlayerIdle` is only a metric). `TimeoutAction` is part of `Rules` and must return a move
`ValidateAction` accepts, or the turn re-arms and costs another miss. `TurnDurationHandler`
stretches a turn (hearts' pass phase, between-hands deals). `WithTurnTimeout(0)` is the off switch.

**Panics** (#37). Every place rules code runs under the lock recovers and ends the table as a rules
error: `SubmitAction`, `RemovePlayer`, `Start` (which refuses instead, the table never started),
`Standings` and the turn timer, and the idle hook after it. Standings are never asked of a state a
hook panicked on.

**Leaving mid-hand.** Rules implement optional `PlayerLeaveHandler`: `OnPlayerLeave` before
removal, `AfterPlayerRemoved` after seat indices shift, returning `LeaveSettled` (the engine decides),
`LeaveKeepsHandOpen` (poker's all-in leaver still contests the pot) or `LeaveInterruptsMatch` (hearts).

**Events.** `broadcaster.Broadcaster[T]` is latest-wins and `Subscribe` returns an error (#3).
The feed is reached only through `Engine.Subscribe/Unsubscribe`; nothing outside the engine can
broadcast on it or close it.

**Views reach the engine through `BoundEngine`** (#5): a session-scoped handle that submits only as
its player and whose `Frame` returns only that player's hand. `Lobby.Seat(playerID)` and
`Event.Seat(playerID)` make it; `internal/tui` never names `game.Engine` (a semgrep rule fails it).

### Lobby (`internal/lobby`)

`Manager` owns every lobby, which lobby each player sits at (`playerLobby`, updated in the same
`m.mu` hold as the roster), the game registry and the shutdown gate. A `Lobby` is a leaf: it takes
only `l.mu` and has no pointer to its manager; `Manager` is the only type that ever holds two
locks. Order: tracker account -> `m.mu` -> `l.mu` -> `Engine.mu` (#45).

| File | Role |
|---|---|
| `manager.go` | create / join / leave / kick, `ToggleReady`, `RemoveLobby`, `recoverGoroutine` |
| `lobby.go` | roster, settings, ready, start, events |
| `watch.go` | `watchLocked` (subscribe at start) and `watchGame`, one per game, which reopens the table and persists |
| `finalize.go` | the rating gate (`unratedReason`), the game gate and watcher count, `BeginShutdown`, `BeginTeardown`, `WaitForFinalizers` |
| `disconnect.go` | the 90s mid-game grace (`graceTimers`), `ResumePlayer`, `releaseHeldSeats` (#14) |
| `browse.go` | the public list, its cache (#62), Elo-distance sort |

Every goroutine the lobby starts (the watcher, the grace timer, the idle-seat hook) recovers its
own panics: nothing above them does, and one table's bug must not take every player's session down.

**Finalize.** The snapshot a finished match is written from (game, ranked flag, start time) is taken
when the game *starts*, not when it ends, since by then the lobby may have been reconfigured.
Reopen first, then persist: a 15s write must not pin the table in-game. `NewManager` requires a
`MatchRepository` (a manager that silently drops results is a wiring bug and panics). A rated
result becomes casual history in three cases (#11); an interrupted one rates only its leavers (#39).
Ratings a write returns are reseated on the players at whatever table they sit at now
(`refreshRatings`); a seated `*game.Player` and its `Ratings` are never written in place.

**Shutdown.** `BeginShutdown` closes a gate on new games and gives up held seats, but players may
keep playing: results in this window are rated. `BeginTeardown` (called just before the server
hangs up on remaining sessions) is when results stop being trustworthy and are recorded
unrated. `WaitForFinalizers` waits for every game's watcher (counted from game start) or times out.
The sequence and its 76s budget are in `cmd/server/main.go` and #29.

### Persistence and ratings

`internal/db` holds the GORM models **and** the repository interfaces (the contract);
`internal/repository` holds the implementations; everything else depends on the interfaces.
Consumers ask for the smallest one (`Authenticator`, `Profiles`, `Leaderboard`, `MatchRepository`).
`internal/rating` is the ladder policy as a pure function, `rating.Rate(Table)`: provisional
threshold, the 24h pair cap, loss-only interrupted scope, erased seats (#10). The repository keeps
the locks, reads and writes. Advisory locks are per seat, sorted by folded key, and taken by
finalize and erasure alike. Finalize retries a Postgres conflict abort and a failure that provably
never reached the server, with backoff; a connection lost mid-transaction is not retried because it
may have committed.

Schema changes are SQL migrations in `internal/db/migrations/` (up and down).
`testutil.SetupTestDB` applies the same files, so the tested schema is the deployed one. A game's
persisted identity is its slug (#7); account ids are UUIDv7 (#8); erasure anonymises the user row (#22).

### Catalog

`catalog.Games` is the one place a game is declared (#6): `Name`, `Slug`, seat range, rules
`Factory` and the view constructor. `cmd/server` builds the lobby registry from it and hands the
same entries to the TUI. Every catalog game runs `gametest.RunRules` and
`gametest.SoakAmongRandomMoves` from `catalog/rules_test.go`.

### Poker chips

Chips are conserved: `checkChipConservation` is the tripwire, not the enforcement (the betting
code keeps the arithmetic right). Fold-out dead money goes to the winner (D-2); short all-ins that
add up to a full raise reopen the betting (D-3); the odd chip of a split pot goes to the
lowest-sorted player id.

---

## 5. Packages

| Package | Owns | Must not |
|---|---|---|
| `cmd/server` | composition root, drain | game rules |
| `internal/ssh` | transport, auth, session generations, size cap | match writes |
| `internal/tui` (+ `views/gameview`) | presentation, one subpackage per game | `MatchRepository`, `game.Engine` |
| `internal/lobby` | tables, grace, finalize orchestration | card rules |
| `internal/game` (+ one package per game, `shed`, `gametest`) | engine and rules | db, tui, routes, lobby |
| `internal/db` | models, repository **interfaces**, auth sentinels | GORM queries |
| `internal/repository` | GORM implementations | SSH / TUI |
| `internal/rating`, `internal/elo` | ladder policy and maths | I/O |
| `internal/catalog` | the game list | runtime state |
| `internal/httpapi` | read-only stats and leaderboard | writes, auth |
| `internal/deck`, `broadcaster`, `ratelimit` | shared helpers | domain knowledge |

Two layer rules are lint rules (`depguard` in `.golangci.yml`), not conventions: only `cmd/server`
may import `internal/repository`, and `internal/game/**` may import only the standard library,
`deck`, `broadcaster`, `internal/game/...` and `uuid` (tests and `gametest` exempt), so a new
import there is a decision. `internal/rating` and `internal/elo` are held to stdlib, `elo` and
`uuid` the same way. Seat identity inside `internal/game` is the scalars on `game.Player`, never a
`*db.User`; `lobby.NewPlayer` is the only place a user becomes a player.

Shared helpers so games do not copy each other: `internal/deck` owns hand removal and the three
rank questions that must not be swapped (`RankValue` ace high, `RunOrder` ace low, `PipValue`
courts 10; each answers 0 outside Ace..King); `game.RankByScore`, `game.SetTurn`/`OverrideTurn`,
`game.SeatAt`/`NextSeat`, `game.ValidateNextHand`; the shedding games share `internal/game/shed`.

---

## 6. Stats API, observability and retention

`internal/httpapi` serves `/v1/stats` (players online, hands in play, tables open),
`/v1/leaderboard?limit=N` and `/healthz`, on `API_PORT` (6970), reached only through nginx's
`/api/`. It is deliberately narrow (no writes, no auth, no per-user data beyond what the TUI
leaderboard shows), which is what makes it safe unauthenticated. `httpapi.NewServer` refuses a
missing dependency rather than serve zeros. `API_TRUST_PROXY` (default false; compose opts in)
makes the limiter read `X-Forwarded-For`, and with `PROXY_TRUSTED_CIDRS` only from those networks.
It has a no-op tracer and an empty propagator, so no visitor chooses a trace id.

The resource is `service.name=tty-cards-server` in every environment, told apart by
`deployment.environment.name`; there is no `service.instance.id`. Every metric is `ttycards.*`
and its attribute keys are an allow-list (`internal/observability/metrics_test.go`). The app pushes
OTLP to Alloy; Prometheus scrapes only the pipeline. Logs name their table
(`slog.With("lobby", code, "game", slug)`), and rules log through `State.Logger()`. Connect and
disconnect log the client's /64, not its address. Retention: Loki 14d, Tempo 48h, Prometheus 30d,
Alertmanager state 5d. The per-field personal-data inventory is [`data-inventory.md`](data-inventory.md).

---

## 7. Invariants that bite

1. Never publish `:6969` or `:6970`.
2. `sessionLifecycle` is outermost; a recover is a direct defer.
3. A displaced session must not leave the lobby or free the tracker slot; `Connect` closes the
   displaced connection outside the account's lock; seat and slot are released as one step.
4. A mid-game drop calls `DisconnectPlayer`, not `LeaveLobby`; the hold is released on hand end and
   on shutdown, not only by its timer.
5. Finalize uses the snapshot taken at game start; the three unrated ends write history without Elo.
6. Soft-deleted rankings are revived before the seed, or finalize aborts.
7. Lock order: tracker account -> manager -> lobby -> engine. `State` has no lock.
8. Any view that subscribes implements `router.Closer`.
9. A catalog entry is rules and view; the slug is persisted, so changing one is a data migration.
10. An accept-loop or API failure still runs `drainServer`.
11. Every screen fits 64x20; measure with `AvailableContentHeight`, never assume rows.
12. `TimeoutAction` returns a move the same package's `ValidateAction` accepts.
13. A rules panic ends the table, not the process; recovers are direct defers and none asks the rules
    for standings.
14. The same seat with the same turn length keeps its deadline (10s floor); a miss is charged once
    per seat-turn.
15. `MAX_CONNECTIONS` x the 120x40 cap has to fit `GOMEMLIMIT`, and the connection count has to fit
    the cores: raise one only with the others, after `cmd/loadtest`.

---

## 8. Where to start reading

Bottom-up: `internal/deck` -> `internal/game` (`rules.go`, `state.go`, `engine.go`, `turnclock.go`,
`bound.go`) and one rules package (`crazyeight` is the smallest) -> `internal/lobby` (`manager.go`,
`lobby.go`, `finalize.go`) -> `internal/ssh` (`server.go`, `tracker.go`) -> `internal/tui` (`app.go`,
`router`, `views/gameview/session.go`) -> `internal/db` and `internal/repository/match.go`. Each
package's tests are the best statement of its contract; `internal/lobby/finalize_test.go`,
`internal/game/engine_timeout_test.go` and `internal/catalog/render_test.go` teach the most.
`code-review-graph` (see `CLAUDE.md`) answers call-graph questions; a channel receive or a
`time.AfterFunc` is not a call edge, so an empty result across the feed or a timer means a dynamic
hop, not dead code (`make deadcode` is authoritative).
