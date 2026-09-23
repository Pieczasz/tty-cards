# Onboarding

Day one. What the product is, where everything lives, how to run it, and what to
do first.

This document does **not** restate the contracts or the invariants - those are
[`architecture.md`](architecture.md), which is canonical. It does not restate the
reading order either; that is [`reading-guide.md`](reading-guide.md). Commands
and environment variables live in the repository [`README.md`](../README.md).

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
| Factory + single registration point | `internal/catalog.All` -> `game.Registry` |
| Functional options | `lobby.Option` (`WithCardGame`, `WithMaxPlayers`, `WithPrivate`, `WithRanked`), `game.EngineOption` (`WithTurnTimeout`) |
| Facade | `game.BoundEngine` via `game.Bind(engine, playerID)` - nil-safe throughout |
| Embedded base type | `gameview.Session` in every game view's model; `shed.State` in the crazy eights and uno `Extra` |
| Repository | `db.Authenticator` / `db.Profiles` / `db.Leaderboard` (their union is `db.UserRepository`) and `db.MatchRepository`, declared in `internal/db`, implemented in `internal/repository` - each consumer takes the smallest one it calls |
| Middleware chain | `wish.WithMiddleware` in `internal/ssh/server.go`; `withCORS`/`withRateLimit` in `internal/httpapi` |
| Generation counter (fencing token) | the engine's `clock.seq`, `SessionTracker` generations |
| Snapshot / DTO | `game.StateSnapshot`, `game.PlayerSnapshot`, `lobby.BrowseEntry` - built under one lock, rendered lock-free |
| Double-checked locking with TTL | `repository.BestPlayers` (5 min) |
| Dirty-flag cache invalidation | `Manager.cacheDirty atomic.Bool` + a 2 s TTL - atomic specifically to avoid inverting lock order |
| Optional interface (capability probe) | `game.PlayerLeaveHandler`, `TurnTimeoutHandler`, `TurnDurationHandler`, `StandingScorer`, `router.Closer` |
| Sentinel errors | `broadcaster.ErrClosed`/`ErrAtCapacity`, `db.ErrUsernameTaken`…, `ssh.ErrNoPublicKey`… |

Idiomatic Go habits worth matching: `context.Context` first on every repository
method; `%w` wrapping with lowercase messages (`wrapcheck` is on); non-blocking
channel sends so a slow SSH client never stalls the engine; consumer-defined
interfaces (`httpapi.SessionCounter` exists so `httpapi` need not import `ssh`);
atomics only where a lock would invert an order; `defer` ordering used as a
correctness tool in `cmd/server/main.go`; `goleak` `TestMain` in 26 packages.

---

## 2. The tree

```
terminal-card/
├── cmd/
│   ├── server/
│   │   ├── main.go             process entry: logging -> config -> OTel -> DB -> repos
│   │   │                       -> lobby -> registry -> SSH server -> stats API -> serve
│   │   └── Dockerfile          3 stages, final image FROM scratch, USER nonroot
│   └── loadtest/               SSH concurrency harness (prints numbers, asserts none)
├── internal/
│   ├── broadcaster/
│   │   └── broadcaster.go      Broadcaster[T], latest-wins, ErrClosed/ErrAtCapacity
│   ├── catalog/
│   │   └── catalog.go          `All` - the ONLY place a game is declared; NewRegistry
│   ├── config/
│   │   ├── config.go           env loading, Validate(), DSN(), String() (redacts pw)
│   │   ├── nginx.conf          stream{} SSH proxy w/ PROXY protocol + http{} site+API
│   │   ├── alloy/config.alloy  Grafana Alloy pipeline (OTLP in, LGTM out)
│   │   ├── loki/loki.yaml      14-day retention + compactor
│   │   ├── tempo/tempo.yaml    48-hour block retention
│   │   ├── prometheus/         prometheus.yml, alerts.yml
│   │   └── grafana/            provisioning (datasources, dashboards) + 4 dashboards
│   ├── db/                     GORM models AND the repository interfaces
│   │   ├── repository.go       Authenticator, Profiles, Leaderboard (UserRepository
│   │   │                       is their union), MatchRepository  ← the contract
│   │   ├── users.go            User, PublicKey, Ranking, ValidateUsername
│   │   ├── games.go            Game, GameRef (slug + display name)
│   │   ├── matches.go          Match, MatchParticipant
│   │   ├── uuid_sql.go         the `stduuid` GORM serializer for stdlib uuid.UUID
│   │   ├── errors.go           the auth sentinels internal/ssh matches on
│   │   ├── migrations.go       //go:embed migrations/*.sql
│   │   └── migrations/         000001_init … 000007_ranked_matches_window (up + down each)
│   ├── deck/                   card.go, deck.go (Pile, Standard) - shared card mechanics
│   ├── elo/elo.go              multiplayer Elo over adjacent pairs, bounded transfers
│   ├── game/                   PURE rules/engine. no db, no tui, no routes
│   │   ├── engine.go           Engine: one mutex, Subscribe, RemovePlayer, Standings
│   │   ├── turnclock.go        per-turn timer, clock.seq fencing, idle removal
│   │   ├── turn.go             SetTurn/OverrideTurn, SeatAt, NextSeat, ValidateNextHand
│   │   ├── state.go            State - no lock of its own; Engine.mu covers it;
│   │   │                       StateSnapshot, PlayerSnapshot
│   │   ├── event.go            Event, EventType, EndReason (each with String)
│   │   ├── player.go           seat scalars (UserID, Name, Ratings, Cards)
│   │   ├── rules.go            Action, Rules + the four optional handler interfaces
│   │   ├── bound.go            BoundEngine - the per-player façade
│   │   ├── registry.go         immutable name -> Module lookup, NewRegistry(mods...)
│   │   ├── shed/               what crazy eights and uno share (play check, draw, standings)
│   │   ├── gametest/           the shared shedding-game suite, RunShed
│   │   ├── crazyeight/         rules.go, state.go
│   │   ├── uno/                rules.go, state.go, deck.go
│   │   ├── hearts/             rules.go, state.go, trick.go
│   │   ├── ginrummy/           rules.go, state.go, melds.go, layoffs.go
│   │   └── poker/              rules.go, hand.go, betting.go, leave.go, streets.go,
│   │                           evaluator.go, state.go
│   ├── httpapi/httpapi.go      read-only JSON: /v1/stats, /v1/leaderboard, /healthz
│   ├── lobby/
│   │   ├── manager.go          lobby registry, CreateLobby, codes, join limiter, leave, kick
│   │   ├── errors.go           the sentinels (ErrLobbyFull, ErrNotLeader, …)
│   │   ├── lobby.go            one table: roster, settings, ready, start
│   │   ├── watch.go            the engine watcher: register, reopen, persist
│   │   ├── finalize.go         persist a finished match; the rating gate; the
│   │   │                       finalizer registry, BeginShutdown, WaitForFinalizers
│   │   ├── disconnect.go       the 90s mid-game grace state machine, ResumePlayer
│   │   ├── browse.go           BrowseEntry/BrowseFilter/BrowseLobbies + the cache
│   │   └── player.go           db.User -> game.Player, the only such place
│   ├── observability/
│   │   ├── otel.go             Setup: logs + traces + metrics over OTLP gRPC
│   │   └── metrics.go          counters + histograms; metrics_test pins the attrs
│   ├── ratelimit/
│   │   ├── limiter.go          SlidingWindow (New); a full table evicts, it does not refuse
│   │   └── netkey.go           NetKey: IPv6 -> /64, unmaps v4-in-v6
│   ├── repository/             the GORM implementations (only cmd/server imports it)
│   │   ├── connect.go          Connect(dsn, Pool): pool, slow-query threshold
│   │   ├── user.go             register, profile, leaderboard cache, DeleteAccount
│   │   └── match.go            FinalizeRankedMatch: advisory locks, SELECT … FOR UPDATE
│   ├── ssh/
│   │   ├── server.go           NewServer(Deps), PTY clamp, channel and env caps,
│   │   │                       sessionRegistry, sessionLifecycle, recoverSession,
│   │   │                       reportingModel
│   │   ├── tracker.go          SessionTracker: generations, ReleaseWith
│   │   └── auth.go             fingerprint auth, LoadOrRegisterUser
│   ├── systemtest/             cross-package tests through public APIs only; only
│   │                           persistence_test.go is behind //go:build integration
│   ├── testutil/               db.go (testcontainers + the production migrations),
│   │                           players.go (Players, NamedPlayers), uid.go
│   └── tui/
│       ├── app.go              New(Deps): builds GlobalContext, registers every route
│       ├── router/router.go    Router, typed Route, Navigate, GlobalContext, Closer, idle tick
│       ├── styles/
│       │   ├── theme.go        THE only file allowed to name a colour
│       │   ├── common.go       layoutHeights / AvailableContentHeight /
│       │   │                   RenderMainLayout / TitleHeightBudget - the fit budget
│       │   └── pad.go          PadTruncate and friends
│       ├── components/         card.go, fan.go, table.go, picker.go, cursor.go
│       ├── tuitest/            the shared test helpers: Key, StripANSI, FitSizes
│       └── views/
│           ├── common.go       HandleCommonMsg, NavigateOn, Footer, RenderScreen
│           ├── gameview/       session.go (the shared view baseline), layout.go,
│           │   │               state.go (BaseState), frame.go, zones.go,
│           │   │               choice.go (ChoicePicker), handover.go (RenderHandOver)
│           │   └── poker/ crazyeight/ uno/ hearts/ ginrummy/  - one MUV triple each
│           └── home/  lobby/  leaderboard/  profile/
├── docs/                       ← you are here
│   ├── README.md               the index and the recommended path
│   ├── architecture.md         the canonical design document
│   ├── decisions.md            one record per non-obvious choice
│   ├── reading-guide.md        the ordered, bottom-up code tour
│   ├── onboarding.md           this file
│   ├── CONTRIBUTING.md         adding a game, test and PR norms
│   ├── SECURITY.md             disclosure policy and deployment hardening
│   ├── data-inventory.md       per-field personal-data inventory
│   ├── privacy.md / terms.md   mirrors of the published policies
│   └── changelog.md            what changed, for players and operators
├── web/                        Astro static site (separate toolchain, pnpm)
├── scripts/                    backup.sh (pg_dump + zstd), dev-session.sh (tmux x3)
├── .github/workflows/          test.yml (test · integration · lint · vulncheck ·
│                               deadcode · image · compose), web.yml
├── compose.yaml                migrate, backend, db, proxy, alloy, loki, tempo,
│                               prometheus, grafana
├── Makefile                    ci = fmt fix lint test build
├── .golangci.yml               the enabled linters, the size gates, depguard
├── README.md                   orientation, commands, configuration
├── CLAUDE.md / AGENTS.md       terse briefs for coding agents
└── LICENSE                     MIT
```

**Two boundaries worth restating**, because they are the ones people breach:

- **Nothing outside `cmd/server` imports `internal/repository`.** Everything
  depends on the interfaces in `internal/db`, and the auth sentinels live there
  too, so `internal/ssh` does not need the implementation package for them.
- **`internal/game` imports no db, no tui, no lobby, no routes.** Seat identity is
  the scalars on `game.Player`, never a `*db.User`.

Both are lint rules; the second is an allow-list (stdlib, `deck`, `broadcaster`,
`internal/game/...`, `uuid`), so a new import there has to be argued for. See [`architecture.md`](architecture.md) §8 for the full
table of what each package owns.

---

## 3. Running it locally

Three ways, in increasing order of realism.

### 3.1 Bare `go run` against a local Postgres - the fastest loop

Prerequisites: Go 1.27.1, PostgreSQL **18** (the `uuidv7()` default in migration
`000001` needs it).

```bash
cp .env.example .env
export DB_DSN='postgres://postgres:PASSWORD@localhost:5432/terminal_card?sslmode=disable'
make install-tools     # golang-migrate
make migrate-up
make build
PROXY_PROTOCOL=false ./bin/server
```

```bash
ssh -p 6969 yourname@localhost
```

`PROXY_PROTOCOL=false` is **required** for a bare `ssh` client. The default
assumes nginx in front speaking PROXY protocol, and `proxyproto` defaults to
REQUIRE: without the header every connection is refused.

### 3.2 `./scripts/dev-session.sh` - three clients at once

The fastest way to test anything multiplayer. It opens three tmux-attached SSH
clients with **distinct keys** and `IdentitiesOnly=yes`, so ssh-agent does not log
all three in as the same user. `TC_PORT=6969` points them past the nginx proxy at
a server started as in 3.1.

### 3.3 Docker Compose - the whole stack

```bash
cp .env.example .env
# set DB_PASSWORD - it has no usable default and production refuses to boot without one
docker compose up -d --build
ssh -p 22 yourname@localhost
```

Migrations run automatically (the `migrate` service) before the backend starts.
SSH host keys persist in the `ssh-keys` volume - keep it across redeploys or every
client sees a host-key change, which is indistinguishable from an attack.

Exactly two ports reach the world: **22** and **80**, both on nginx. Grafana is on
`127.0.0.1:3000`; reach it with `ssh -L 3000:127.0.0.1:3000 <host>`. Every
service carries an explicit `mem_limit` and they add up to 5632 MiB, sized for a
12 GB / 6-core VPS - the arithmetic is in the header comment of `compose.yaml`.

### 3.4 Tests

```bash
make test-short        # unit tests, no Docker
make test              # go test -race ./...
make test-integration  # -tags=integration, needs Docker (testcontainers)
make lint              # golangci-lint
make ci                # fmt, fix, lint, test, build - run before you push
```

`testutil.SetupTestDB` starts a `postgres:18-alpine` testcontainer and applies the
**production** migration files up, down and up again, so every down migration is
exercised and the tested schema cannot drift from the deployed one. It skips when
Docker is absent.

---

## 4. Your first day

In order. Each one teaches something the next assumes.

1. **Play a full hand of Crazy Eights against yourself** with
   `./scripts/dev-session.sh`. Then find your `matches` row and your
   `match_participants` rows in Postgres.
2. **Read [`architecture.md`](architecture.md) §1-§4.** Do not try to hold it all;
   the point is to know which words are load-bearing.
3. **Work through [`reading-guide.md`](reading-guide.md) steps 1-3.** That is
   `internal/deck`, `internal/broadcaster`, the engine core, and one whole game.
   About 1,500 lines of Go, and the densest value in the repo.
4. **Break something on purpose.** Change `MaxMissedTurns` to 1, rebuild, sit out
   a turn, and watch the seat go. Put it back.
5. **Drop a client mid-hand and reconnect inside 90 seconds.** Confirm you resume
   rather than forfeit, and find the `session dropped mid-game, holding the seat`
   log line.
6. **Add a sixth game's catalog entry** reusing an existing rules package with a
   new slug and name. Confirm the route appears and that you touched nothing in
   `internal/game`. Then revert it.
7. **Read [`decisions.md`](decisions.md) end to end**, quickly. You will not
   remember it; you will remember that it exists, which is the point.

Then pick an issue. [`CONTRIBUTING.md`](CONTRIBUTING.md) has the test conventions
and the one rule that matters most: **every fix ships a test that fails on the old
code** - one you have actually watched fail, not one that passes afterwards.

---

## 5. Premises that do not hold

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
| **Alloy scraping the Go process** | Inverted: the app **pushes** OTLP to Alloy. Alloy scrapes only the host, via `prometheus.exporter.unix` |
| **Frame-render-time metrics** | Not instrumented. There are counters and four histograms (session duration, game duration, lobby time-to-start), but nothing times a render |
| **Tracing across game events** | Only `ssh.session` and ten `db.*` spans; the stats API is deliberately untraced. `game`, `lobby` and `tui` are untraced |
| **`tea.Every` subscription loops** | Not used. Periodic work is self-rescheduling `tea.Tick`, which is what lets the countdown change rate mid-turn |
| **A WebSocket or HTTP game path to replace** | There never was one. `internal/httpapi` is a read-only stats feed for the marketing site |

The two seams that *would* be the extension points if the single-node assumption
ever broke: `broadcaster.Broadcaster[T]`, where a distributed implementation would
have to preserve latest-wins and the `ErrClosed`/`ErrAtCapacity` contract; and
`Manager.lobbies` / `Manager.playerLobby`, where the hard part is not the storage
but moving the lock-order guarantees across a network.
