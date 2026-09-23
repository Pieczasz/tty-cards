# Architecture

The design document. Readable top to bottom by someone who has never seen the
code: what the system is, how a session travels through it, and what each layer
is allowed to do. Every section names the file or symbol to open.

Where the code already explains *why*, this document points at the comment rather
than paraphrasing it. The longer reasoning behind a particular choice lives in
[`decisions.md`](decisions.md), one record per decision. For a file-by-file walk
in dependency order, use [`reading-guide.md`](reading-guide.md).

---

## 1. What this is

An SSH server that serves a terminal UI. You `ssh tty.cards`, the server
allocates a PTY, and a Bubble Tea program renders the whole interface as ANSI
text over the SSH channel: home, a lobby browser, and card games against other
people connected to the same process. There is no web client, no game HTTP API
and no WebSocket layer anywhere in the game path.

Identity is the SSH public-key fingerprint. Results persist to Postgres with Elo.

Five games: **Crazy Eights**, **Uno**, **Hearts**, **Gin Rummy** and
**No-Limit Texas Hold'em**.

**One process holds the table.** Lobbies, engines and hands live only in RAM.
Postgres stores what must outlive the process: users, keys, games, rankings,
match history. There is no Redis, no message broker and no shared cache. That is
the single decision most of the rest of the design follows from - see
[`decisions.md` #3](decisions.md#3-latest-wins-broadcaster-and-subscribe-returns-an-error)
for the one place it is written down in code, `broadcaster.go`.

A read-only JSON API (`internal/httpapi`) feeds the marketing site in `web/`,
which is a separate Astro toolchain and not part of the Go module.

### Not tick-driven

There is **no game loop and no fixed tick rate**. The engine advances only when a
player submits an action or a turn timer fires. Nothing animates. What periodic
work exists is per-session UI scheduling:

| Timer | Interval | Where |
|---|---|---|
| Turn countdown (adaptive) | 1 s, or **100 ms** under 6 s remaining, on your own turn only | `internal/tui/views/game/layout.go` `clockTickFrom` |
| Lobby-browser refresh | 2 s | `internal/tui/views/lobby/join.go` |
| Router idle watchdog | 10 s poll, quits after 5 min idle (never at a live table) | `internal/tui/router/router.go` |
| Engine turn clock | 30 s one-shot, re-armed | `internal/game/turnclock.go` |

---

## 2. Topology

```
        ssh client (port 22)              browser (port 80)
                 |                                |
                 v                                v
   +----------------------------------------------------------+
   |  nginx           internal/config/nginx.conf               |
   |  stream{}  limit_conn 8 / $client_net, proxy_protocol on  |
   |  http{}    /api/ -> backend:6970, everything else -> www  |
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
   | users, keys   |   |        -> Grafana         |
   | games, ranks  |   +---------------------------+
   | matches       |
   +---------------+
```

**Published ports are 22 and 80, both on nginx,** which listens on IPv4 and IPv6
alike (`listen 22` and `listen [::]:22`, the same for 80). Grafana is the single
exception and binds `127.0.0.1:3000`. Everything else - Postgres, Alloy, Loki,
Tempo, Prometheus, and the backend's `6969`/`6970` - is `expose`-only on the
compose networks.

The comment above `proxy.ports` in `compose.yaml` states the reason: publishing
`6969` lets a client forge the PROXY header, and publishing `6970` lets it forge
`X-Forwarded-For`, which `API_TRUST_PROXY=true` tells the backend to believe.
A bare local `ssh` client sends no PROXY header, so local development needs
`PROXY_PROTOCOL=false` (see `scripts/dev-session.sh`).

**Three compose networks.** `edge` holds nginx and the backend only, with
`enable_ipv6: true` and fixed subnets `172.29.69.0/24` and
`fd6b:1e37:9a52:6969::/64`. IPv6 is on so an IPv6 client reaches nginx with its own
address; without it Docker's userland proxy re-originates the connection from the
bridge gateway and every IPv6 player shares one rate-limit bucket. The subnets are
fixed because the backend's `PROXY_TRUSTED_CIDRS` names them: a PROXY header is
honored only from those networks and any other peer is refused before its header
is read (`proxyListener`, `cmd/server/main.go`), and the stats API believes
`X-Forwarded-For` only from the same networks. `default` carries everything else.
`docker-api` is `internal: true` and holds only Alloy and `docker-socket-proxy`,
which answers GET only, for containers and networks, so Alloy discovers container
logs without the Docker socket itself.

nginx resolves `backend` through Docker's DNS (`resolver 127.0.0.11 valid=10s`,
`server backend:6969 resolve` in a `zone`d upstream), so a recreated backend with a
new address is picked up without restarting the proxy.

### Defence in depth

| Layer | Limit | Where |
|---|---|---|
| nginx (stream) | 8 concurrent conns / client network; 5 s connect timeout to the backend | `nginx.conf` `limit_conn ssh_addr 8`, `proxy_connect_timeout 5s` |
| nginx (http) | 16 concurrent conns / client network, 10 s header and body timeouts; 2 r/s + burst 10 on `/api/`; refusals are 429 | `nginx.conf` `limit_conn http_addr 16`, `limit_req_zone $client_net`, `limit_req_status 429` |
| PROXY header | honored only from `PROXY_TRUSTED_CIDRS` (compose: the `edge` subnets) | `proxyListener`, `cmd/server/main.go` |
| listener | `2 x MAX_CONNECTIONS` (2000) | `netutil.LimitListener`, `cmd/server/main.go` |
| account slots | `MAX_CONNECTIONS` (1000), with a message | `ssh.SessionTracker` |
| auth | `RATE_LIMIT_CONNECTIONS` / `RATE_LIMIT_WINDOW_MS` (5 / 1 s) per network | `ssh.rateLimitAuth` |
| **registration** | **`REGISTRATION_LIMIT` / `REGISTRATION_WINDOW` (5 / 1h) per network, new accounts only** | `ssh.allowRegistration` |
| stats API | `API_REQUESTS_PER_MINUTE` (120) per network | `httpapi.withRateLimit` |
| channels | 2 session channels / SSH connection, refused before `Accept`; 32 `env` requests / 8 KiB per channel | `ssh.limitSessionChannels`, `envCappedChannel` |
| account | one live slot / user; **a second `Connect` displaces and closes the first** | `ssh.SessionTracker.Connect` |
| lobby join | 10 / s / **player id** | `lobby.Manager.joinLimiter` |
| TUI idle | quit after 5 min idle, unless the view is a live table (`router.IdleExempt`) | `router.Router` |
| SSH idle | 30 min | `wish.WithIdleTimeout` |
| SSH handshake | 20 s | `ssh.handshakeTimeout` |

The two connection caps are deliberately different numbers.
`netutil.LimitListener` sits at **twice** `MAX_CONNECTIONS` and only backstops
handshake floods by silently refusing to accept; `SessionTracker` is the
player-visible capacity at `MAX_CONNECTIONS` and refuses with `ErrServerFull` and
a message.

Every network limit keys on `ratelimit.NetKey`, which collapses IPv6 to its /64 -
one customer is routinely handed 2^64 addresses, so keying on the full address is
keying on nothing. nginx does the same thing with a `$client_net` map, duplicated
across the `stream` and `http` contexts because the two cannot share one. Three
implementations of the same /64 rule therefore have to stay in sync; `NetKey` in
Go is the authoritative one and `nginx.conf` says so.

`NetKey` itself is total: an address it cannot parse comes back verbatim. The
**fail-closed** decision is one level up, in `ssh.netKeyFor`, which returns
`ok=false` for a nil address or a `SplitHostPort` error; `rateLimitAuth` and
`allowRegistration` both refuse on `!ok` rather than sharing one bucket for every
unkeyable peer.

---

## 3. The spine, end to end

### 3.1 Boot - `cmd/server/main.go`

Order matters so deferred teardown runs correctly (LIFO):

1. `installLogging()` - **first**, before config, so a config failure is a
   structured record rather than a default-handler line
2. `config.Load()` -> validated `*config.Config`
3. `observability.SetupOTel` (defer shutdown)
4. `db.Connect` (defer close)
5. Repositories -> `lobby.NewManager(ctx, matchRepo)`
6. `defer waitForFinalizers(lobbyManager)` - registered **after** the DB-close
   defer, so LIFO drains match writes before closing the handle they write
   through
7. `game.Registry` from `catalog.All`
8. `ssh.NewSessionTracker` -> `ssh.SetupServer` -> `serve(...)`. The one tracker is
   shared with the stats API; `SetupServer` refuses to start without it
   (`ErrNoTracker`), because a second one counted nobody online

`serve` stacks `tcp -> LimitListener -> proxyproto.Listener`, runs `Serve` in a
goroutine, and blocks on a signal, an accept error or an API error. Every exit
path calls `drainServer` (`BeginShutdown` + graceful SSH stop), so ranked matches
interrupted by a deploy are recorded **without** Elo.

Shutdown is sequential and budgeted: 30 s SSH `Shutdown` + 5 s stats API + 15 s
finalizers + a second 15 s finalizer window + 5 s OTel = 70 s worst case.
`compose.yaml` sets `stop_grace_period: 80s` against exactly that arithmetic, and
the comment on `waitForFinalizers` says the two numbers depend on each other.

### 3.2 Connection - `internal/ssh`

Wish middleware runs **last-first**, so the slice is in reverse execution order.
`sessionLifecycle` is listed last to be outermost:

```go
wish.WithMiddleware(
    bm.MiddlewareWithProgramHandler(sessionProgram(deps, reg, registerLimiter)),
    activeterm.Middleware(),
    sessionLifecycle(deps, reg),
)
```

`reg` is a `sessionRegistry` that `SetupServer` makes per server, so two servers in
one process (every test that starts one) cannot see each other's sessions.

**Channels are capped where they are opened.** `SetupServer` replaces the
`session` channel handler with `limitSessionChannels(ssh.DefaultSessionHandler)`: a
per-connection counter on the connection-scoped `ssh.Context` rejects a third
session channel with `ResourceShortage` **before** `Accept`. Counting in the
middleware bound nothing, because a channel that never asks for a shell never
reaches it yet holds its goroutine and buffers. The accepted channel is wrapped in
`envCappedChannel`, which answers `false` to `env` requests past 32 or 8 KiB; charm
ssh keeps every accepted one for the session's life.

Connect/disconnect logging is `sessionLifecycle`'s job rather than wish's own
logging middleware, which writes through the charm logger and so never reaches
the OTLP handler.

1. **Auth.** Any public key is accepted; identity is
   `cryptossh.FingerprintSHA256` -> `SHA256:<fingerprint>`. The rate limiter runs
   in the public-key callback, before a session exists.
2. **`LoadOrRegisterUser`.** First connection claims the SSH login name as the
   username. It takes an `allowRegister func() bool` and consults it **only** on
   the `user == nil` branch, so a returning player never spends the registration
   budget - and only after `db.ValidateUsername` has passed, so a typo does not
   spend it either.
3. **`tracker.Connect(userID, conn)` -> generation.** A second session for the
   same account **displaces** the first *and closes its connection*, outside the
   tracker lock (a wedged peer must not hold every other account's `Connect`
   behind it). Capacity -> `ErrServerFull`. The TUI model is built **before** the
   slot is claimed, so a panic there cannot strand a slot nothing releases.
4. **`tui.ResumeSeat(model)`** runs only once the slot is owned: a refused session
   (server full) must not cancel the grace timer holding this player's seat, and
   a displaced session has finished its teardown by then, so any timer it armed is
   there to cancel.
5. **Per-session state** (`user`, `model`, `gen`, span, trace context) goes in the
   `sessionRegistry`, never on `s.Context()`, which is per-**connection** and
   shared by every channel.
6. **Teardown** is `closeSessionModel` -> `releaseSession` -> `recoverSession` ->
   `finishSession`. `releaseSession` calls
   `tracker.ReleaseWith(userID, gen, DisconnectPlayer)`: the seat is given up and
   the slot freed as one step **under the tracker lock**, so a reconnect cannot
   take the slot and resume the seat in between and then have this session's
   `DisconnectPlayer` arm a grace timer on the seat it is playing. A displaced
   generation touches neither.

Auth sentinels (`db.ErrUsernameTaken`, …) live in `internal/db/errors.go`.
`internal/ssh` depends on the `db` contract package, not on `internal/repository`.

**A taken name is deliberately uninformative; an invalid one is not.**
`mapRegisterError` folds `db.ErrUsernameTaken` into `ErrNameUnavailable`; the
comment on `ErrNameUnavailable` says why (a distinguishable message turns the login
banner into a "does this account exist" oracle over every username). An invalid
name is a fixed rule, not a fact about other accounts, so `LoadOrRegisterUser`
returns `db.ErrInvalidUsername` wrapped with the validation reason, before the
registration budget is consulted. Taken is compared case-insensitively
(`lower(username)`, migration `000006`). The rate-limit refusal is its own string,
`ErrTooManyRegistrations`, and `db.ErrKeyAlreadyRegistered` passes through
verbatim because the key is the caller's own.

**A TUI panic reaches the player, in two layers.** `recoverSession` is a
**direct** `defer` in the outermost middleware - a `recover()` inside a function
called *by* a deferred function returns nil - and `wish.Fatalf`s the notice. But
nothing panics *out of* bubbletea, which owns the screen, so the primary path is
`reportingModel`: it wraps `Init`/`Update`/`View`, writes `panicNotice` to
**`s.Stderr()`**, and re-panics so bubbletea's own recover ends the program.
Both record the panic as a metric and leave the lobby cleanly.

`boundedPty` refuses a PTY wider than 2000 or taller than 600, and
`clampWindowSize` clamps later resizes.

### 3.3 TUI - `internal/tui`

`tui.Model` builds a `router.Router` with a `GlobalContext`:

- `User`, `UserRepository`
- `LobbyManager *lobby.Manager` - the whole manager. The old `lobby.SessionAPI`
  interface is **gone**: it had one implementation and one consumer, so it bought
  nothing and had to be edited every time a view needed a method
  ([`decisions.md` #25](decisions.md#25-lobbysessionapi-was-deleted))
- `GameRegistry`, `SessionCtx`, `Width`, `Height`, `Theme`

There is **no `MatchRepository` on the TUI**. Persistence is owned by
`lobby.Manager` after a game ends, and absence is the boundary.

Navigation is a message, not a call: a view returns a `router.ChangeViewMsg` and
the router performs the swap in `Goto`, closing the outgoing view if it
implements `router.Closer`.

Initial route: `tui.ResumeSeat` -> `Manager.ResumePlayer` -> a mid-game reconnect
lands back at the table (through the lobby view, which routes onward into a running
game); otherwise home. The ssh layer calls `ResumeSeat` separately from `tui.Model`,
once the session owns its tracker slot (§3.2).

Game routes come from `catalog.All` via `router.GameRoute(slug)` ->
`"game_<slug>"`. `internal/game` knows nothing about routes.

**A seated player cannot navigate away from their table.** `RouteHome`,
`RouteLobbyCreate`, `RouteLobbyJoin`, `RouteProfile` and `RouteLeaderboard` are all
registered through `seatedOr`, which calls `FindLobbyByPlayer` first and mounts the
lobby view instead (`internal/tui/app.go`). Navigating away unsubscribes but keeps
the seat, so without this the engine would auto-play the player's turns until the
idle timer took it.

**Leaving a live table asks first**
([`decisions.md` #44](decisions.md#44-leaving-a-live-game-asks-first)). While the
game is `Playing`, esc only arms a prompt - "Leave and forfeit this game?" - and
`y` then leaves; any other key disarms it and is swallowed rather than also playing
a card. Once the game is over, esc and enter go back to the lobby at once. It is
`gameview.Session.HandleLeaveKey` and `LeaveConfirmScreen`; the prompt takes the
whole screen, so no table's row budget changes.

**The idle quit spares a live table.** The router drops a session after 5 minutes
without input unless the active view implements `router.IdleExempt` and answers
true; `gameview.Session` answers `Phase == Playing`. A seat watching others act is
not idle, and the engine's own turn clock removes one that stopped playing; a
game-over screen is a menu like any other.

#### The layout budget - `internal/tui/styles/common.go`

Every screen must fit at 64x20 (`styles.MinWidth` x `styles.MinHeight`), 80x24
and 120x50. That holds because **one unexported function owns the arithmetic** and
both the renderer and the budget query call it:

- `layoutHeights(w, h, header, footer)` wraps the header and footer to
  `innerWidth` **before** measuring them, and returns
  `hContent = max(innerHeight - hHeader - hFooter, 0)`.
- `AvailableContentHeight(...)` is that, minus `opticalPadding` (2 - the blank
  lines the renderer appends).
- `Theme.RenderMainLayout(...)` calls the same `layoutHeights`, so the promise and
  the render cannot drift. The comment there names the two bugs this fixed:
  header and footer measured unwrapped in one place and wrapped in the other, and
  `opticalPadding` never being deducted.
- `TitleHeightBudget(h)` caps the figlet title at a fifth of the box interior. At
  20 rows that is 2 lines, which makes `RenderFigureASCII(text, maxW, maxH)` fall
  back to plain text and hand the rows back to content.

`BoxWidth`/`BoxHeight` clamp at both ends (`maxBoxWidth` 120, `maxBoxHeight` 40)
because `Global.Width` is 0 until the first `WindowSizeMsg`. `TooSmall` returns
**false** for a zero dimension: unknown is not small, and answering true there
would flash the resize prompt on every connection.

Views reach the budget through `views.RenderScreen` / `views.ScreenContentHeight`.
Lists size themselves from it rather than assuming rows - see
`leaderboard.pageLayout`.

`RenderFigureASCII` memoises by `(text, font)` in a `sync.Map`, with each banner's
measured size, and returns the largest cached font that fits both bounds - a
banner's size does not depend on the box, so keying on the box stored the same
banners once per terminal size. That is at most three entries per title and no cap,
which is safe only because every banner text is a fixed string in the source.
Nothing player-controlled may be banner text: the home screen used to figlet the
username, which let any account mint cache entries.

#### Theme resolution is per session

Two players can have opposite terminal backgrounds, so a shared palette would
leave one reading white on white. `router.New` defaults to dark, `Router.Init`
issues `tea.RequestBackgroundColor`, and both the router and the mounted view
rebuild the theme on `tea.BackgroundColorMsg`. `styles/theme.go` is the only file
permitted to name a colour, enforced by `TestNoRawColoursOutsideTheme`, and
`theme_test.go` asserts WCAG AA contrast against seven real terminal backgrounds.

#### Key names, not key literals

`tea.KeyPressMsg.String()` normalises the spacebar to `"space"`. Matching the
literal `" "` is a silent no-op, and it silently broke two screens: the Hearts
pass phase (playable only by waiting out the 45-second auto-pass) and the join
browser's select. Both now match `"space"`, with a comment saying so.

#### Account deletion - Profile

`x` on the Profile screen asks for confirmation; typing `DELETE` in full performs
it. Refused while the player is seated at a table. The view calls
`db.UserRepository.DeleteAccount(ctx, userID)` and then ends the session -
nothing can authenticate as that account afterwards. See §6.6.

### 3.4 Lobby - `internal/lobby`

A lobby is one table. Its state machine has three states (`lobby.go`,
`setStateLocked` is the only mutator, and it also flips the manager's browse-cache
dirty flag):

| From | To | Trigger |
|---|---|---|
| `Waiting` | `InGame` | `ToggleReady` with everyone ready -> `startGameLocked` |
| `InGame` | `Waiting` | `releaseFinishedGameLocked`, once the engine `IsFinished()` |
| any | `Closed` | `detachPlayerLocked`: the leader leaves and there are no guests |

`Closed` is terminal - `Subscribe`, `addGuest` and `Kick` all refuse.

- `Manager.New(leader, opts...)` generates an 8-character code from
  `[A-Z0-9]` using `crypto/rand`, retried up to 10 times against collisions.
  Defaults: `maxPlayers` 4, private, casual. `WithCardGame(name)` takes the
  **display name**, which is the `game.Registry` key; the persisted identity is
  the slug, resolved at game start (§6.1).
- `BrowseLobbies(player, BrowseFilter)` lists public `Waiting` tables, sorted by
  absolute Elo distance from the player (an unrated player is matched at 1500),
  ties broken by code so the list cannot reshuffle under a cursor. Default limit
  20, hard cap 200. Backed by a 2-second cache of lobby pointers; `browseEntry`
  re-checks each one under its own lock, because a miss that scanned just before a
  table went private or started stores it anyway.
- `JoinLobbyByCode` is rate limited per **player id**, 10 per second, because the
  code space is guessable-adjacent. It returns the `*Lobby` it joined, so the
  caller never looks the code up a second time - by when the table may be gone.
  `FuzzJoinLobbyByCode` fuzzes it.
- `Kick` is refused while `InGame`, and the comment says why: a leader who can
  kick mid-hand can farm Elo by dropping whoever is winning and letting the
  engine finish the match without them. It also clears the target's disconnect
  grace, which would otherwise later take the player out of whatever table they
  moved on to.
- **A ready is consent to the table as it was.** A settings change
  (`withLeaderSettings`: broadcasts `SETTINGS_UPDATED` then `PLAYERS_UPDATED`) and
  any roster removal - a guest leaving or kicked, or the leader leaving and a guest
  promoted - clear every ready flag. A join does not.
- `RemoveLobby` unmaps only the `playerLobby` entries still pointing at the removed
  lobby: `LeaveLobby` drops `m.mu` before calling it, so the player may already be
  seated at a newer table.

**Starting a game.** `startGameLocked` -> `registry.Create(name)` ->
`game.NewEngine(rules, players, rules.InitialDeck())` -> `engine.Start()` ->
`l.startedAt = time.Now()` -> `watchGameLocked(engine, db.GameRef{...})` ->
`setStateLocked(InGame)` -> broadcast `EventGameStarted` carrying the `*game.Engine`.
The leader is always seat 0.

**The finalize snapshot is taken when the game starts, not when it ends.**
`watchGameLocked` builds `finalizeRequest{lobbyCode, game, isRanked, startedAt}`
at subscribe time; its comment says why (by the time the game ends the lobby may
have reopened and been reconfigured, and the result would be written under the
new ranked flag, the new game and the next hand's start time). `startGameLocked`
sets `l.startedAt` immediately before, for the same reason.

**That watcher subscribes even with no match repository.** It is also the only
consumer of `EventPlayerIdle`, and skipping it would leave an idle-removed seat
on the roster while the engine no longer holds it - a table that can never reach
all-ready again. `finalizeFinishedGame` already no-ops without a repository.

**On `EventGameEnded` the lobby registers, reopens, then persists.**
`handleBroadcasterEvents` (`watch.go`) calls `requestFinalize`, which takes
`registerFinalizer()`, then `releaseFinishedGame()`, then
`finalizeFinishedGame(...)`: a 15-second write must not pin the table `InGame`
while the TUI is already back in the lobby ready-ing the next hand, and the
registration comes first because the reopen waits on `m.mu` in `releaseHeldSeats`,
and a shutdown that began inside that wait would otherwise refuse it and drop the
match. If the feed closes
without ever delivering `EventGameEnded` - the broadcaster is latest-wins and can
drop it - the loop falls through to `engine.IsFinished()` and does the same thing
with `EndReasonUnknown`.

**Mid-game disconnect.** `DisconnectPlayer` arms **`DisconnectGrace` (90 s)** via
`disconnectGrace` (`disconnect.go`: `pending` timers -> `expiring` claim ->
`LeaveLobby`). `ResumePlayer` cancels a pending leave, or returns the seat on a
takeover with no pending leave. Waiting-lobby seats and any seat during shutdown
still leave immediately. `expireLeave` moves `pending` -> `expiring` under the
manager lock so a reconnect cannot resume a seat about to vanish.

**The hold is released at two more points:**

- `releaseHeldSeats` (`disconnect.go`), called from `Lobby.releaseFinishedGame` - the hold only
  makes sense mid-hand. Once the table is `Waiting` again, a still-armed timer
  keeps the player out of every other table, and this one unable to reach
  all-ready, for up to 90 s. It claims the grace the way the timer would, so a
  racing `ResumePlayer` is refused rather than resuming a seat already gone.
- `BeginShutdown` (`finalize.go`) - those timers would fire long after the drain, leaving the
  lobby un-removed and its engine un-closed. The player is not coming back to a
  process that is exiting.

### 3.5 Play - `internal/game`

```
key -> view.Update -> BoundEngine.Submit(action)
                         |
                         v
                 Engine.SubmitAction (e.mu held)
                   ValidateAction -> ApplyAction -> AfterAction
                   CheckWinCondition / applyNextTurnLocked
                   broadcast Event*
```

Views sync with `Session.Sync(fn)` -> `BoundEngine.Frame(fn func(*State))`: one
lock hold for the snapshot, the player's own hand, the turn clock and the live
`*State`. `State.Extra` is unredacted - copy what you keep.

### 3.6 Finish - `Manager.finalizeFinishedGame`

The lobby watcher sees `EventGameEnded` -> `requestFinalize` (using the snapshot
taken at game start) -> **`Manager.finalizeFinishedGame`** (`finalize.go`):

1. `registerFinalizer` **first**, in `requestFinalize` before the table reopens -
   every statement between observing the end and that call is a window for
   shutdown to begin, and a refusal then drops a finished match with nothing for
   `WaitForFinalizers` to wait on. `finalizeFinishedGame` takes the result
2. `GameFinished` metric, then a 15-second timeout context
3. `StandingsWithPlaces` (ties share a place where the rules implement
   `StandingScorer`)
4. The rating gate (§6.4)
5. `recordFinishedMatch`: `RecordCasualMatch` for an unrated match,
   `FinalizeInterruptedMatch` for a ranked `EndReasonInterrupted`, otherwise
   `FinalizeRankedMatch`

Outcomes are counted as `ok` / `error` / `dropped`. A match is `dropped` - written
nowhere - when the finalizer registry is closed, the `GameRef` slug is empty, an
abandoned table produced no standings at all, or a standing carries a nil user id.

---

## 4. Contracts (the ones that bite)

### 4.1 Rules ↔ Engine

`Rules` is in `internal/game/rules.go`. The engine holds **exactly one** mutex
(`Engine.mu`, `engine.go`) for `Start`, `SubmitAction`, `RemovePlayer` and
`Frame`. `game.State` has **no** lock of its own; it is protected entirely by the
engine's.

- Rules must **never** call back into `Engine` (deadlock). This holds
  structurally: `Rules` methods receive only `*State`, which carries no engine
  handle. Do not add one.
- Rules may mutate `*State` freely under the engine lock.
- `ValidateAction` rejects cleanly; an error from `ApplyAction` or `AfterAction`
  finishes the game as `EndReasonRulesError` (state may be half-applied) ->
  **unrated** persistence. Anything checkable up front belongs in
  `ValidateAction`.
- Optional, probed by type assertion: `PlayerLeaveHandler`,
  `TurnTimeoutHandler`, `TurnDurationHandler`, `StandingScorer`.

Per-game state lives in `State.Extra` (`*crazyeight.State`, `*poker.State`,
`*uno.State`, `*hearts.State`, `*ginrummy.State`).

`game.AnyScoreAtLeast` is the shared match-target check for Hearts and Gin Rummy;
`internal/game/shed.go` holds what Crazy Eights and Uno share - among it
`ValidateShedPlay` (a card in play to match, and the played card in the hand of the
seat on turn, with the game's own match rule passed in) and `DrawWithReshuffle` (off
the stock, refilled from under the card in play; `false` is a forced pass).
`ReshuffleDiscardIntoStock` returns nothing: the error it used to return was
unreachable.

`NewEngine` seats **copies** of the players it is given, with `Cards` cleared, so
a finished engine's viewers and the next engine never share a hand. `Start` has no
rollback: a failed start leaves that engine unusable, and the lobby builds a new
engine per attempt. The first seat to act is `math/rand/v2` - it is public the
moment the table opens, so it is no secret worth `crypto/rand`.

### 4.2 Turn cursor and clock - `applyNextTurnLocked`

```go
switch {
case e.state.OverrideNextTurn != nil:
    e.state.CurrentTurn = *e.state.OverrideNextTurn
    e.state.OverrideNextTurn = nil
case advance:
    e.state.CurrentTurn++
}
e.clampTurnLocked()
e.armTurnTimerLocked()
```

`OverrideNextTurn` wins and is cleared; otherwise advance if asked; otherwise the
cursor stays where it is. `clampTurnLocked` then forces it into
`[0, len(Players))` with a modulo, because a leave handler can compute an index
against the pre-removal seat count.

The clock is `DefaultTurnTimeout` 30 s, `MaxMissedTurns` 3 (`turnclock.go`). **No
`TurnTimeoutHandler` means no clock at all**: there is nothing safe to play for an
absent player, so they get no clock rather than a silent removal.
`TurnDurationHandler` stretches a particular turn - hearts gives the pass phase
45 s and the between-hands prompt a minute; poker and gin rummy stretch their
between-hands deal the same way. Returning zero keeps the default and cannot
resurrect a clock `WithTurnTimeout` disabled.

What each game plays when a clock runs out: poker checks when free, calls when no
opponent can cover more than it already has out (everything past that comes back at
showdown, so folding would forfeit a bet already covered), folds otherwise, and
deals between hands (an absent dealer would otherwise freeze the table); crazy
eights and uno draw; hearts passes its three most dangerous cards (`Q♠`, then `A♠`
and `K♠`, then the highest hearts, then the highest of the rest), plays its first
legal card, and deals the next hand; gin rummy draws, knocks when a discard leaves
it gin, otherwise sheds its priciest deadwood, and deals. `TimeoutAction` must
return something `ValidateAction` accepts - gin rummy's `autoDiscard` skips the
card the upcard rule forbids for exactly this reason. A refused auto-play is a
rules bug: `submitTimedOutAction` re-arms the clock **on the same lock hold** that
refused it (after it is dropped, a player's own move may already have armed the
next seat's clock), as a fresh chargeable turn with the 10 s floor, so each refused
expiry still costs a miss and a rules set that always refuses loses the seat.

**A turn that carries on keeps its deadline**
([`decisions.md` #38](decisions.md#38-the-turn-clock-keeps-a-seats-deadline-while-its-turn-carries-on)).
`armTurnTimerLocked` compares the seat on turn and the turn length with the ones it
last armed for. The same seat with the same length is the same turn carrying on -
gin's draw then discard, a re-armed auto-play, somebody else leaving - and keeps
its running deadline, floored at `minTurnRemaining` (10 s, or the whole timeout if
that is shorter). A different seat or a different length is a fresh turn at full
length. A miss is charged **once per seat-turn** (`turnMissCharged`, reset only on
a fresh turn), not per expiry, so gin's draw-then-discard costs one miss, not two.
Uno's heads-up skip and reverse and a hearts trick winner leading again keep the
clock running rather than earning a fresh 30 s.

`turnSeq` fences stale timers: `stopTurnTimerLocked` increments it and
`armTurnTimerLocked` calls that first, so every cursor change invalidates timers
already in flight, and an auto-play carries the generation it was computed for.
Only **accepted** actions clear a player's miss count - a move the rules reject
does not, or spamming garbage would dodge removal forever.

`EventTurnTimedOut` is broadcast **inside** `resolveTurnTimeout`'s lock hold, on
the same hold that charged the miss. Outside it, a player whose action lands in
the gap gets the miss refunded while the "timed out" they disproved still ships.

`OverrideNextTurn` also keeps a last-seat-standing hand open (a poker heads-up
all-in leave still contests the pot) - `removePlayerLocked` checks for it before
declaring a forfeit.

`stopTurnTimerLocked` runs from `finishGameLocked`, the last-player-standing path
and `Close`. The engine's `closed` flag is what stops a concurrently-resolved
timeout re-arming a timer on a closed engine.

**A rules panic ends one table, not the process.** `onTurnTimeout` runs on a
`time.AfterFunc` goroutine and nothing above it recovers, so a panic in a rules
hook would take down every table for one game's defect. `Engine.recoverRulesPanic`
is a direct `defer` on that goroutine and re-takes `e.mu` (the locked helpers
release it in their own defers as the panic unwinds); `SubmitAction` has its own
direct deferred `recover`, which runs before its unlock. Both call
`endOnRulesPanicLocked`, which logs the panic with a stack and, on a live engine,
`finishAfterPanicLocked`: `Phase = Finished`, clock stopped,
`EventGameEnded{Reason: EndReasonRulesError}`. It deliberately does **not** call
`Rules.Standings` - the state a hook panicked on cannot be trusted to rank anyone,
and a second panic inside a recover would take the process down - and it does not
check for `Playing`, because a panic inside `finishGameLocked`'s own `Standings`
call has already set `Finished` without announcing it. On the player path the
panic comes back to the view as an error. `StandingsWithPlaces` recovers too and
returns `nil, nil`, so finalize has nothing to write. This table ends unrated, and
the rest keep playing.

### 4.3 BoundEngine

`Bind(engine, playerID)` is the default safe path: `Submit` acts as self,
`Frame` is one coherent read whose hand is yours, and `Subscribe`/`Unsubscribe`
join the feed without handing out the broadcaster (a view holding it could
`Broadcast` or `Close` the table's feed). It is **not** a capability boundary:
`Frame`'s callback hands the view the live, unredacted `*State`, and a poker table
reads every seat from it. There is no `Engine()` escape hatch; what the view shows
from that state is the view's stated job (`buildSeats`, which has its own test
file).

### 4.4 Session / view baseline

Every game view embeds `gameview.Session`
(`internal/tui/views/game/session.go`): binding, `NewSession`'s subscribe,
`HandleFrame` (the whole `Update` loop; it drops an `EventMsg` or `ClockTickMsg`
stamped with another session's feed), `ClockTick` (the countdown a view's `Init`
starts), the hand cursor (`MoveCursor`, `SelectDigit`, `SelectedCard`),
`IdleRemoved`, `ActionErr` (the last rejected move - `Submit` keeps it and the hero
band renders it), `HandleLeaveKey` / `LeaveConfirmScreen` (the forfeit prompt,
§3.3), `IdleExempt`, `Leave` and `Close` (which is what satisfies
`router.Closer`). The shared layout frame - `RenderBands`, the compact
breakpoints, the width-budgeted hand renderers - lives in
`internal/tui/views/game`. A new game implements its own rules rendering and
nothing else.

Read per-game state through the `extra` callback of `Session.Sync`; read seat
order, display names and stock size through `BaseState` (`Seats`, `SeatOrder()`,
`SeatNames()`, `DeckSize`) rather than re-deriving them from the live `*State`.
`PlayerSnapshot.Username` already falls back to the player id.

Anything a view keeps after releasing the engine lock must be **copied, not
aliased** (`maps.Clone`, `HandResult.Clone`).

### 4.5 Subscriptions

`broadcaster.Broadcaster[T]` is latest-wins: a full 256-deep buffer drops the
oldest and enqueues the newest. `Subscribe` returns `ErrAtCapacity` / `ErrClosed`
rather than a pre-closed channel - a closed channel is indistinguishable from a
finished game. Engines size the subscriber cap `len(players)+8` for the
ranked-finalize watcher and reconnect overlap; a lobby uses `10+8`, which is the
largest roster any game allows plus the same headroom, because `SetMaxPlayers`
can raise the seat cap long after the broadcaster exists.

Views surface a subscribe failure in their own error line; the lobby logs it
loudly, because there it means a match result will not be persisted. Any view
holding a subscription must implement `router.Closer` - the router closes the
active view on navigation and `ssh.closeSessionModel` closes the whole model on
disconnect.

**Every feed message carries the channel that delivered it** - `EventMsg.Source`
and `ClockTickMsg.Source` in the game views, `lobbyMsg.src` in the lobby view, and
the join browser's `refreshMsg.owner`. A listener or tick in flight when the router
replaces a view is handed to the next view; handling it there re-armed it beside
that view's own, so a message stamped with anything but the view's current feed is
consumed and dropped. Skipping `Close()` parks a listener goroutine and burns a subscriber
slot until the engine closes.

### 4.6 Lock order

`SessionTracker.mu` -> Manager (`m.mu`) -> lobby (`l.mu`) -> engine
(`Engine.mu`). Never invert. `State` has no lock, so there is no fifth level.

The tracker is first because `ReleaseWith` runs `Manager.DisconnectPlayer` under
it, so a session's teardown and a reconnect's resume are serialised
([`decisions.md` #45](decisions.md#45-the-session-tracker-lock-comes-before-the-managers)).
Nothing below it ever calls back into the tracker.

In Postgres the order is the per-seat advisory lock before any row lock: a ranked
finalize and an erasure both take `pg_advisory_xact_lock` for the seat first
(§6.5).

Three places in the code state the rule: the comment on `Manager.cacheDirty`, the
doc on `Manager.LeaveLobby`, and the doc on `Lobby.releaseFinishedGameLocked`.
The browse cache uses an `atomic.Bool` dirty flag **specifically** so a lobby can
mark it while holding its own lock without reaching for the manager's.
`Manager.Stats` and `getCachedPublicLobbies` both copy the lobby slice under
`m.mu` and release it before taking any `l.mu`.

### 4.7 Shared deck helpers - `internal/deck`

`RemoveOne`/`RemoveEach` never alias. Three rank questions that must not be
swapped: `RankValue` (Ace high, 14 - poker and hearts), `RunOrder` (Ace low,
courts distinct - gin rummy runs), `PipValue` (courts count 10 - deadwood). All
three answer **0** outside Ace..King, including the Joker: no deck here deals one,
and 0 loses loudly instead of quietly tying the ace. Standard ranks are 1-based so
a zero `deck.Card` is detectably empty; Uno's extra ranks sit at 20+.
`AllRanks` is what makes a `Rank`-keyed map testable for exhaustiveness. `IsSuit`
is the guard for "a card that lets the player name a suit" - an Eight, a Wild -
and refuses `NoSuit`, the zero value and client garbage alike.

`Pile.Shuffle()` **returns nothing**. It seeds a `math/rand/v2` ChaCha8 generator
once per call from `crypto/rand`, which since Go 1.24 cannot fail (it aborts the
process instead), so the error every caller used to plumb through was an
unreachable branch dressed as resilience. Several such guards were removed across
the engine and the rules packages in the same pass; each site carries a comment
saying why it was unreachable.

### 4.8 Events

`game.Event` is deliberately thin - `Type`, `PlayerID`, and `Reason` (which
qualifies `EventGameEnded` and is zero on everything else). The reasons are
`EndReasonWin`, `EndReasonRulesError`, `EndReasonForfeit` (last player standing),
`EndReasonAbandoned` (every seat left) and `EndReasonInterrupted` (one seat's leave
ended the match for everyone: hearts sets `State.Interrupted` in `OnPlayerLeave`,
and `removePlayerLocked` reports it in place of a win). It is a **cue to
re-read a snapshot**, never the state itself.

| `EventType` | Emitted by | Consumer behaviour |
|---|---|---|
| `EventGameStarted` | `Engine.Start` | views begin rendering the table |
| `EventActionApplied` | `submitActionLocked`, after `AfterAction` | re-sync |
| `EventTurnAdvanced` | `submitActionLocked`, `removePlayerLocked` | re-sync |
| `EventTurnTimedOut` | `resolveTurnTimeout` | re-sync; a safe move was played |
| `EventPlayerLeft` | `removePlayerLocked` | re-sync |
| `EventPlayerIdle` | `removeIfStillIdle` | **the named player's own view quits its program**, which ends the SSH session through the ordinary `releaseSession` path |
| `EventGameEnded` | `finishGameLocked`, `removePlayerLocked`, `finishAfterPanicLocked` | views show the result; the lobby's watcher persists it |
| `EventUnknown` | - | zero value, never sent |

`game.StateSnapshot` is the redaction boundary: hand *sizes*, never hand
contents. It carries both `CurrentPlayer` (a display name) and `CurrentPlayerID`,
because two players can share a name and whose turn it is must never be decided
from the former. A player's own cards come only from `BoundEngine.Frame`, which
clones the cards of the bound `playerID` and nobody else.

`lobby.Event` is `{Type string; Payload any}`; the payload is used by exactly one
type, `EventGameStarted`, which carries the `*game.Engine`.

---

## 5. Poker money invariants - `internal/game/poker`

The one place in the codebase where a bug is a *payout*, so it is defended in
layers. Read the comments on each of these; they state the failure mode.

| Concern | Symbol | File |
|---|---|---|
| Nobody wins chips nobody matched: the top contributor's excess over the second-highest goes back | `refundUncalled` | `streets.go` |
| A fold-out pays like a showdown: that one refund, then everything else - folders' dead money included - to the winner ([#40](decisions.md#40-fold-out-dead-money-goes-to-the-winner)) | `awardUncontested` | `streets.go` |
| Dead money from folded players rides with the last live layer | `buildSidePots` (`orphan`) | `streets.go` |
| A hand that cannot be played out unwinds | `refundContributions` | `streets.go` |
| An unplayable street unwinds on every path - a betting action or a leave | `settleOrUnwind`, reached through `resolveAfterChange` | `streets.go`, `betting.go` |
| A raise past what any opponent can call is refused, not staged | `largestCallableBet` / `validateRaiseTo` | `betting.go` |
| The one legal raise band; the view builds its prompt from it | `RaiseBounds` | `betting.go` |
| A player facing a sub-minimum all-in may only call or fold, unless the short all-ins since they acted add up to a full raise ([#41](decisions.md#41-short-all-ins-that-add-up-to-a-full-raise-reopen-the-betting)) | `checkBettingReopened` (`LastBetLevel`) | `betting.go` |
| A blind too short to post keeps the full bring-in | `beginHand` | `hand.go` |
| The tripwire: stacks + pool must equal the hand's starting total | `checkChipConservation` | `hand.go` |

`checkChipConservation` **logs**; it does not panic or refuse. By the time it
fires the hand is already closed out, so the value is the log line, not a
recovery. It also checks `MainPool != 0` separately, because `chipsInPlay` counts
the pool - a hand that ends without paying a pot out would otherwise balance, and
the next `resetForHand` would quietly zero the stranded chips.

The property test behind it is `TestChipsAreConservedAcrossRandomHands`
(`streets_test.go`), whose `handLedger` asserts "nobody loses chips nobody
matched" over rapid-generated hands.

One deliberate deviation from casino practice, named in the code: `splitEvenly`
gives the odd chip to the lowest-sorted player id rather than the first player
left of the button, because determinism is what a replayable table needs.

---

## 6. Persistence, Elo and the anti-farm rules

Interfaces in `internal/db`; GORM implementations in `internal/repository`.
Nothing outside `cmd/server` (the composition root) may import the implementation
package, and `depguard` enforces it. The sentinels callers compare against live in
`internal/db/errors.go`.

### 6.1 Game identity is the slug

`games.slug` is what a rating hangs off - `catalog.Entry.Slug`, the same value the
TUI derives routes from. `games.name` is a display column, refreshed whenever it
differs. Renaming a game in `internal/catalog` used to create a second `games` row
and orphan every ranking on the first (migration `000005_game_slug`, which also
drops the old unique index on `name`).

The two halves travel together as `db.GameRef{Slug, Name}`:

```
catalog.Entry{Name, Slug}
  -> game.Module{Name, Slug, Factory}     registry keyed by Name
  -> lobby options.cardGame (Name)
  -> at start: db.GameRef{Slug: mod.Slug, Name: l.options.cardGame}
  -> repository getOrCreateGame: read by slug; upsert ON CONFLICT (slug) only on a
     miss, a rename or a soft-deleted row
```

`getOrCreateGame` reads first (`Unscoped`, by slug) and returns the row when it is
live and its name matches, so the common finalize takes no row lock on `games`.
Otherwise it upserts with `DoUpdates`, not `DoNothing`: a soft-deleted game still
occupies the unique slug, and `DO NOTHING` would leave the returned `ID` zero
forever. It also takes the transaction handle rather than the pool, because going
back to the pool holds one connection while waiting for a second, and
`DB_MAX_OPEN_CONNS` concurrent finalizes would deadlock until timeout.

### 6.2 Account ids are UUIDv7

`users.id UUID PRIMARY KEY DEFAULT uuidv7()` - migration `000001_init.up.sql`,
whose second line says `-- uuidv7() is Postgres 18; time-ordered, not
gen_random_uuid().`

**Why not `BIGSERIAL`.** A sequential id is enumerable: it leaks how many accounts
exist, in what order they registered, and it makes a neighbour's id guessable.
The id is also what appears in logs as `player_id`.

**Why v7 and not v4.** A v7 UUID is time-ordered in its high bits, so inserts land
at the right-hand edge of the primary-key B-tree instead of scattering across it.
Random v4 keys fragment the index and dirty a new page per insert. v7 keeps a
sequence's index locality while keeping a random id's unguessability.

**Both sides can generate one.** Postgres fills the column by default, so any
writer gets a valid id; `User.BeforeCreate` in `internal/db/users.go` sets
`uuid.NewV7()` when the field is zero, so Go does not depend on the default being
present.

**The Go type is the stdlib one.** `import "uuid"` - the Go 1.27 standard library
package, not `github.com/google/uuid` (which appears only as an indirect test
dependency). Stdlib `uuid.UUID` is a `[16]byte` with no `database/sql` Scanner or
Valuer, so `internal/db/uuid_sql.go` registers a GORM serializer named `stduuid`
and every UUID field carries `gorm:"serializer:stduuid"`. Writes go out as the
canonical string; scans accept a string, a 16-byte slice or NULL. Raw query
parameters go through `uuid.String()` for the same reason - `repository.uuidStrings`
says so, because pgx will not encode the array type as a `uuid` on its own.

**Consequence for operators.** Postgres **18** is a hard floor. The rewritten
`000001` will checksum-fail against a volume created under an older major, so an
upgrade means recreating the volume or running `pg_upgrade`.

### 6.3 Migrations, and the schema that cannot drift

Schema changes are SQL files in `internal/db/migrations/`, up **and** down,
applied with golang-migrate. `internal/db/migrations.go` embeds them, and
`testutil.SetupTestDB` applies the same files **up -> down -> up**, seeding rows
before the down pass (`seedRoundTripData` - a down statement that breaks on real
data would otherwise pass on an empty schema), so every down migration is exercised
in CI and the tested schema cannot drift from the deployed one. Nothing calls
`AutoMigrate`.

| # | Adds |
|---|---|
| `000001_init` | the six tables; `users.id` UUIDv7; `users.username VARCHAR(40) NOT NULL UNIQUE` with the `username_valid` CHECK; `rankings.elo` CHECK `0..4000` |
| `000002_constraints_and_indexes` | four `SET NOT NULL`; the partial `idx_rankings_game_elo`, `idx_match_participants_user_match`, and the two FK indexes |
| `000003_provisional_rankings` | `rankings.matches_played BIGINT NOT NULL DEFAULT 0` |
| `000004_not_null_scalars` | pins `rankings.elo`, `public_keys.name`, `matches.game_id`, `public_keys.user_id` |
| `000005_game_slug` | `games.slug`, backfilled and made unique; drops `idx_games_name`. Its down is **lossy** ([#48](decisions.md#48-migration-000005s-down-is-lossy)) |
| `000006_username_ci` | `idx_users_username_lower`, a unique index on `lower(username)`; refuses to run, naming them, while usernames differing only by case exist |
| `000007_ranked_matches_window` | `idx_matches_ranked_created`, partial, on `matches (created_at) WHERE ranked AND deleted_at IS NULL`, for the 24-hour pair count |

Migration `000004` pinned the columns whose Go field is a plain scalar, because a
NULL scans into the zero value there and reads back as data rather than as a
missing value - an unrated player at 0 Elo, a match attached to game 0. It
backfills `rankings.elo` to 1500 and `public_keys.name` to `''` because those
have an honest resting value, and **deliberately does not invent a parent** for a
row with no owner. `TestSchemaNullabilityMatchesStructs` derives its list from the
GORM structs, so a new scalar field fails CI until it is pinned.

Those structs carry no `uniqueIndex`, `not null`, `default`, `check` or `type`
tags any more: nothing calls `AutoMigrate`, so those tags enforced nothing and one
had already drifted from the SQL. Only the tags GORM uses to build queries
survive.

### 6.4 What is rated, and what is only recorded

`persistFinishedMatch` and `recordFinishedMatch` (`internal/lobby/finalize.go`):

```go
rated := req.isRanked && !m.isShuttingDown() &&
    reason != game.EndReasonRulesError && reason != game.EndReasonAbandoned
```

Three ways a ranked match is written without Elo, and the comment gives one
reason for each: a deploy decided who was left holding cards, not play; a
half-applied rules error must not move the ladder; and an **abandoned** table -
every seat left - has standings that are reverse leave order, so rating it pays
the last to quit. `EndReasonForfeit` (last player standing) **is** rated, and so
is `EndReasonUnknown`. `unratedReason` logs which of the three it was, and
`endReasonLabel` gives the metric its label. An abandoned match with no standings
writes nothing at all.

An unrated ranked match goes through `RecordCasualMatch`, so it is history with
`matches.ranked = false` and - because the increment lives only in
`updateRankingsTx` - **no `matches_played` increment either**.

**An interrupted match is rated only against its leavers**
([`decisions.md` #39](decisions.md#39-an-interrupted-match-charges-only-its-leavers)).
A ranked `EndReasonInterrupted` goes through `FinalizeInterruptedMatch` with the
engine's `LeftPlayers` as the leavers. Elo is computed over the whole table, the
leavers already ranked last, but only the leavers' rows are written, and only
downwards (`min(new, old)`): seated players keep their rating and their
`matches_played`, and a seated player with no ranking row is not seeded one. The
leavers' `matches_played` goes up by one. The match is stored `ranked = true`, so
it counts towards the pair cap. A shutdown still overrides it to casual, like any
ranked match.

**Leavers rank strictly below seated players.** `standingsLocked` appends
`LeftPlayers` after the seats the rules placed (reverse leave order among
themselves), and `placesLocked` guards the tie branch with
`left[p.ID] == left[standings[i-1].ID]`: a leaver's `StandingScore` was measured
against a state they are no longer in, so tying it with a seated player would turn
a rage-quit into a rated draw. Two leavers with the same score do still share a
place, because splitting them mints Elo between two people who both quit.

### 6.5 Ranked finalize - one transaction

`FinalizeRankedMatch` -> `updateRankingsTx` (`internal/repository/match.go`):

1. **`lockPairing`** - an advisory lock **per seat**. Not per exact participant
   set: the cap below is per *pair*, and two different sets can share one. Ranking
   row locks are per `(user, game)`, so the same accounts finalizing Poker and
   Hearts at the same moment would otherwise both read an undamped count. The key
   is the UUID's two halves XORed into an `int64`; a collision only
   over-serializes and never mixes ratings. `seatLockKeys` sorts and deduplicates
   by that **folded key**, not by user id - once two seats can fold onto one key,
   user-id order put that key at a different point in each transaction, and two of
   them could deadlock. `lockKey` takes the **two-int4** form,
   `pg_advisory_xact_lock(hi, lo)`, which Postgres keeps apart from the single
   `bigint` form golang-migrate uses, so a seat key cannot collide with a
   migration's lock. Erasure takes the same per-seat lock (§6.6).
2. **`unerasedSeats`** - under those locks, drop the seats whose account was
   erased while the match ran (`Unscoped`, anonymised username), keeping their
   places aligned. Their participant row is still written, with a delta of 0, and
   they get no ranking.
3. **`seedRankingRows`** - revive soft-deleted ranking rows first (a
   `DO NOTHING` insert would leave them invisible to the default scope and abort
   the whole finalize), then upsert seeds at 1500. Only the rows about to be
   written are seeded (§6.4).
4. **`fetchRankings`** - `SELECT … FOR UPDATE`, **ordered by user id**. This is
   where the row locks are actually taken, because the seed's
   `ON CONFLICT DO NOTHING` locks nothing in the common case where the row already
   exists. Without the fixed order two overlapping finalizes lock in opposite
   orders and Postgres aborts one.
5. **The provisional rule and the pairing damp**, then `elo.Calculate`.
6. **Write** the match, the participants, the places and the deltas.

**Elo itself.** `internal/elo/elo.go` is pure: `DefaultRating` 1500, `MinRating`
100, `MaxRating` 4000, `KFactor` 32, expected score base 10 over 400. `Calculate`
takes a slice sorted first place to last and scores each player against their
**immediate neighbours only**. `capTransfer` trims each transfer so neither side
crosses the bounds, which keeps the clamping itself conservative. Ratings are
stored as `uint32` through `ToUint32`, and the database enforces the same range
with `CONSTRAINT elo_valid CHECK (elo >= 0 AND elo <= 4000)` - deliberately the
wider bound, so application policy can move the floor without a migration.

**Provisional is a per-pair rule, not a per-account flag.** `elo.Player` carries
`Provisional`, set by the repository from `MatchesPlayed < provisionalMatches`
(5; a missing ranking row counts as provisional). `unpaidAgainstProvisional`
clamps the **established** side's delta to at most zero on a mixed pair, while
letting losses through unchanged. Identity is a free SSH keypair, so a fresh 1500
is free to mint - but if an established player could not lose to one either,
seating an alt would freeze a rating in place and the anti-farm rule would become
a shield. The provisional side always moves, so it converges on real games. This
deliberately breaks Elo's zero-sum property for that pair; the comment says the
unfarmable ladder is worth more.

**The anti-farm cap is per pair, across every game.**
`repeatedPairCountLast24h` counts, over ranked non-deleted matches in the last 24
hours, the highest co-occurrence of any *pair* of players at this table - not the
exact participant set. It drives from the server's ranked matches inside the window
(`idx_matches_ranked_created`, migration `000007`) and joins out to these seats, so
it reads the last day's ranked traffic, not a veteran's whole history. `{A,B}`, `{A,B,C}` and `{A,B,D}` are three different sets,
so a cap on set repeats handed each its own budget and two accounts could farm
each other forever by rotating a third alt through. The window spans every game
for the same reason: switching game does not make it legitimate. At
`maxSamePairingPerDay` (3) the table is **damped** - the count is per pair but the
damp is table-wide, so nobody's `elo` is written, only `matches_played`, which is
what still lets a provisional account graduate.

**Casual.** `RecordCasualMatch` - history only, no Elo, game row created on first
sight.

**Leaderboard reads.** `BestPlayers(ctx, limit, gameSlug)` is a cache keyed by
game slug (empty = all games), 5-minute TTL, fetching `bestPlayersCacheSize` (200)
rows and slicing. A `limit` above 200 bypasses the cache entirely, which is also
why the HTTP endpoint caps there. Concurrent misses share one query through a
`singleflight.Group` keyed by slug, fetch size and cache generation; the query runs
on a detached context with its own 10-second timeout
(`bestPlayersQueryTimeout`), so one caller giving up does not fail the others.
Every erasure bumps the generation, and a read stores its rows only if the
generation it started under is still current - otherwise a read that fetched
before the erasure committed re-caches the erased name for the whole TTL.

### 6.6 Erasure - `DeleteAccount`

`db.UserRepository.DeleteAccount(ctx, userID)`, implemented by `eraseUserLocked`
(`internal/repository/user.go`), one transaction:

- It takes the **same per-seat advisory lock** a ranked finalize holds
  (`lockSeat`), so a finalize either committed before the erasure or waits for it
  and then skips the anonymised seat - otherwise it would seed a ranking and put
  the erased account back on the leaderboard.

- `public_keys` and `rankings` are **hard**-deleted (`Unscoped`). A soft-deleted
  key row would keep its unique fingerprint and lock the returning player out of
  registering again; a soft-deleted ranking still holds the `(user_id, game_id)`
  primary key. With the keys gone, nothing can authenticate as that account.
- The `users` row **survives, anonymised**: `username` becomes
  `db.AnonymisedUsername(userID)` (`deleted_` plus 32 hex digits of the UUID -
  exactly 40 characters, which has to satisfy `varchar(40)` and the CHECK that
  allows either a chosen name of at most 16 characters or that exact `deleted_`
  form), and `last_seen_at` becomes NULL. `ValidateUsername` refuses the
  `deleted_` prefix so nobody can squat it. The row is updated **by column**, not
  `Save`d from a loaded struct, because the save hooks would walk the
  associations the transaction just deleted and write them back, and `Unscoped`,
  so an operator soft-delete does not make a still-named account look unknown.
- `match_participants` is untouched. Those rows belong to the *other* players at
  those tables, and their history has to keep resolving to a name.
- The leaderboard cache is cleared wholesale afterwards and its generation bumped
  (§6.5) - a five-minute TTL is five minutes of an erased name on screen.

The TUI path is Profile -> `x` -> type `DELETE`, refused while seated, session ends
after.

---

## 7. Catalog - single registration point

`internal/catalog/catalog.go` `All` is the only place a game is declared. Each
entry carries the rules factory **and** the TUI view constructor. `cmd/server`
builds the registry; `internal/tui/app.go` registers the routes. A missing field
or duplicate slug fails `catalog_test.go`. Copying an entry and changing only the
rules still compiles, so keep the pair in lockstep by hand.

| Field | Consumer |
|---|---|
| `Module.Name` | registry key, lobby option, `db.Game.Name` (display) |
| `Slug` | TUI route `game_<slug>`, **and `games.slug`, the persisted identity** |

---

## 8. Package responsibilities

| Package | Owns | Must not |
|---|---|---|
| `cmd/server` | composition root, drain | game rules |
| `internal/ssh` | transport, auth, session generations | match writes |
| `internal/tui` | presentation | `MatchRepository` |
| `internal/lobby` | tables, grace, finalize orchestration | card rules |
| `internal/game` | engine + rules | db, tui, routes, lobby |
| `internal/db` | models + repository **interfaces** + auth sentinels | GORM queries |
| `internal/repository` | GORM implementations | SSH / TUI |
| `internal/catalog` | the game list | runtime state |
| `internal/httpapi` | read-only stats/leaderboard | writes / auth |
| `internal/deck` | shared card helpers | game-specific rules |
| `internal/elo` | rating maths | I/O of any kind |
| `internal/broadcaster` | fan-out | domain knowledge |

The two rules that matter are lint rules, not conventions -
`.golangci.yml` `depguard`: nothing but `cmd/server` and `internal/repository`
may import `internal/repository`, and `internal/game/**` may import neither
`internal/db`, `internal/tui` nor `internal/lobby`.

Seat identity inside `internal/game` is the scalars on `game.Player` (`UserID`,
`Name`, `Ratings`), never a `*db.User`. `lobby.NewPlayer` is the only place a
`db.User` becomes a `game.Player`, which is what lets `internal/game` stay free of
`internal/db`.

Lobby file split:

| File | Role |
|---|---|
| `manager.go` | maps, new / join / leave / kick, `RemoveLobby`, `Stats` |
| `lobby.go` | roster, settings, ready, start |
| `watch.go` | the engine watcher -> `requestFinalize` (register, reopen, persist) |
| `finalize.go` | persist finished matches, the rating gate, the finalizer registry, `BeginShutdown`, `WaitForFinalizers` |
| `disconnect.go` | the mid-game grace state machine, `ResumePlayer`, `releaseHeldSeats` |
| `browse.go` | public list and its cache, Elo-distance sort, `GameNames` |
| `player.go` | `db.User` -> `game.Player` |

---

## 9. Stats API - `internal/httpapi`

| Route | Returns |
|---|---|
| `GET /v1/stats` | `{"players_online":N,"hands_in_play":N,"tables_open":N}` |
| `GET /v1/leaderboard?limit=N` | a JSON **array** of `{rank, username, game, elo}`; `limit` default 5, silently capped at 200, non-numeric or `<1` is a 400 |
| `GET /healthz` | `{"status":"ok"}`, or 503 `{"error":"unhealthy"}`. Pings the database, so nginx returns **404** for `/api/healthz`: it exists for the backend container's own loopback healthcheck, and any caller could otherwise spend a database round-trip per request |
| `OPTIONS /` | 204, as a route rather than a short-circuit, so a preflight is spent against the same rate budget as everything else |
| anything else | 404 `{"error":"not found"}` |

Mounted by `cmd/server/main.go` on `API_PORT` (6970) and reached only through
nginx's `/api/` location. Successful reads carry
`Cache-Control: public, max-age=15`; errors and `/healthz` carry `no-store`,
because a cached health answer is a lie about a later moment.

Deliberately narrow: no writes, no auth, no per-user data, nothing the TUI
leaderboard does not already show any visitor. That is what makes it safe
unauthenticated. Live counts come from `ssh.SessionTracker.Count` and
`lobby.Manager.Stats`, and `SetupServer` requires the tracker the two share
(`ErrNoTracker`). `Deps.AllowOrigin` and `Deps.RequestsPerMinute` are required too:
`config.Load` owns the defaults and validates them, so a second copy in the package
could only drift from it.

`API_TRUST_PROXY` makes the limiter read the **leftmost** `X-Forwarded-For` entry,
falling back to the socket address if it will not parse - a blank or malformed
entry would otherwise key every such caller into one shared bucket. It **defaults
to false** and compose opts in explicitly: a directly exposed listener that trusts
the header can be evaded by forging it, so the unsafe direction has to be chosen.
nginx sets the header from `$remote_addr`, not `$proxy_add_x_forwarded_for`, so a
client cannot prepend its own value. With `PROXY_TRUSTED_CIDRS` set
(`Deps.TrustedProxyNetworks`) the header is believed only from a peer inside those
networks and anyone else is keyed on its socket address: `6970` is reachable from
every container on the backend's networks, not just nginx.

**The stats API makes no spans.** `otelhttp` runs with a noop tracer provider:
every website visitor polls this API, and a span per request put each visitor's
address and User-Agent into Tempo. The request metrics, which carry neither, are
what an operator reads. `otelhttp.WithServerName("stats-api:80")` pins
`server.address` and, through the explicit port, `server.port`; without it otelhttp
labels every request metric from the client's own `Host` header - an unbounded
name and up to 65535 port series. `internal/httpapi/telemetry_test.go` asserts
both: no spans, and only allow-listed `http.server.*` attributes.

---

## 10. Observability and retention

The app **pushes** OTLP to Alloy; Alloy scrapes only the host. Nothing pulls the
Go process, and there is no `/metrics` and no pprof endpoint. There is no
Prometheus client library in `go.mod`.

| Signal | Store | Retention | Set in |
|---|---|---|---|
| Logs | Loki | **14 days** | `internal/config/loki/loki.yaml` `retention_period: 336h` + compactor `retention_enabled` |
| Traces | Tempo | **48 hours** | `internal/config/tempo/tempo.yaml` `block_retention: 48h` |
| Metrics | Prometheus | **30 days** (8 GB cap) | `compose.yaml` `--storage.tsdb.retention.time=30d` |
| Container stdout | Docker json-file | 3 x 10 MB | `compose.yaml` `x-logging` |

Loki's `retention_period` alone only affects queries; the compactor block is what
actually deletes chunks, and the file says so.

Tracing is honest about its own extent: one `ssh.session` span per session and ten
`db.*` spans. The stats API makes none (§9). `game`, `lobby` and `tui` are
untraced, so a trace shows a session and the database work under it,
not the game events between them. Prometheus alerts live in
`internal/config/prometheus/alerts.yml`; `MatchResultLost` and
`BackendMetricsAbsent` are the two that page.

Two decisions worth knowing before you add an attribute:

- **The session span carries no client address.** It has `client_version` and the
  terminal size at start, and `user` at the end. The comment in `startSession`
  says why: the span already carries the username, and joining the two is exactly
  the record a trace store should not hold for 48 hours.
- **Connect and disconnect log `client_net`, the /64, not the IP.** The full
  `remote_addr` survives only on WARN and ERROR paths, where abuse investigation
  needs it. nginx's access logs carry no address: the `stream` block is
  `access_log off` and the `http` block uses a `log_format privacy` with no
  `$remote_addr` and no User-Agent. Its error log goes to stderr at `error` and
  above; the `limit_*` rejections are logged at `warn`, under that threshold, and
  Alloy rewrites the `client:` field on the proxy's remaining lines to
  `redacted`.

`internal/observability/metrics_test.go` collects every instrument and fails if
any attribute key falls outside a fixed allow-list, so "metrics carry no personal
data" is enforced rather than asserted.

Alloy also drops the backend's stderr JSON (`stage.drop` on the OTLP duplicate),
so structured records are stored once rather than twice, and drops its own and
Loki's container logs, because shipping Loki's "received push" lines back into
Loki makes every push generate the next one.

Per-field detail, for whoever has to answer a data question:
[`data-inventory.md`](data-inventory.md).

---

## 11. Security model

Identity, limits and the deployment shape are all one argument, so they are worth
reading together. The disclosure policy and the self-hosting checklist are in
[`SECURITY.md`](SECURITY.md).

**Identity is an SSH key fingerprint.** Any public key is accepted; the first
connection with a new key claims a username. There is no password, no email, no
reset and no second factor. The consequences are permanent and stated as such: a
new key is a new account and therefore a **rating reset**; losing the key loses
the account; and because minting an identity is free, every anti-farm rule in §6.5
exists.

**Registration is open on purpose, but rate limited.** A separate limiter caps
new accounts at `REGISTRATION_LIMIT` per `REGISTRATION_WINDOW` (5 per hour) per
client network, consulted only on the first-sight branch, after the name has
passed validation. For a private instance, put it behind a firewall, a VPN or an
allowlist.

**One live session per account, and the second wins.** A half-open TCP session
used to lock a player out of their own seat for the whole 90-second grace window,
so `Connect` now displaces and closes the old connection. Only the owning
generation may release the slot or the seat.

**PROXY protocol, and `:6969` is never published.** `proxyproto` defaults to
REQUIRE: every connection must open with a PROXY header, which is right behind
nginx and is exactly why the port must stay internal - any peer's header would
otherwise be honoured, and every per-network limit becomes forgeable.
`PROXY_TRUSTED_CIDRS` narrows it further: with it set, a connection from outside
those networks is refused before its header is read, so another container on the
network cannot forge one either
([`decisions.md` #47](decisions.md#47-proxy-and-x-forwarded-for-are-believed-only-from-the-proxys-networks)).
The same applies to `:6970` and `API_TRUST_PROXY=true`, whose header the same list
bounds.

**Grafana has no login at all** - anonymous Admin, login form off. That is safe
only because the port is published on loopback and CI asserts it stays there:
reaching it means an SSH session on the host, which already owns the database, the
volumes and every secret in the compose file. A password there would protect
nothing and add one more credential to rotate and forget. Widening the binding
without adding authentication is a vulnerability, and the compose comment says so.

**TLS is not terminated here yet.** The apex cannot sit behind Cloudflare,
because a proxied hostname resolves to Cloudflare's IPs and `ssh tty.cards` would
follow it there; so the apex is grey-clouded and needs a publicly trusted
certificate of its own. The `:443` block in `nginx.conf` is written and commented
out. Until it is enabled the website's live-stats panel, served over https, cannot
call the plaintext origin and says "server unreachable".

**Container hardening.** The backend image is `FROM scratch` with a `nonroot`
(uid 65532) passwd entry and no shell; the container runs `read_only: true`,
`cap_drop: [ALL]`, `no-new-privileges:true`. It probes its own health by
re-executing the server binary with `-healthcheck`, because there is no wget in
the image. Every compose image is pinned by digest. nginx keeps only the four
capabilities its master process needs (`NET_BIND_SERVICE`, `CHOWN`, `SETUID`,
`SETGID`). Alloy reads container logs through `docker-socket-proxy` on an internal
network rather than mounting the Docker socket, because `:ro` on a socket restricts
nothing and container inspect includes every container's environment.

One acknowledged debt, named in `compose.yaml`: the `migrate` service takes the
database password in its argv, so it is visible in `ps` on the host and in
`docker inspect`. The image is `FROM scratch` and cannot assemble a DSN from a
secret itself. The two real fixes are `PGPASSWORD` or running migrations from the
host, and the comment says neither has been done because neither can be verified
without bringing the stack up.

---

## 12. Invariants that bite

1. Never publish `:6969` (PROXY trust) or `:6970` (`API_TRUST_PROXY`).
2. `sessionLifecycle` outermost; recover is a **direct** defer; the bubbletea
   path is `reportingModel` -> `s.Stderr()`.
3. A displaced session must not `LeaveLobby` or free the tracker slot. `Connect`
   closes the displaced connection **outside** the tracker lock. Teardown gives up
   the seat and the slot as one step under it (`ReleaseWith`), and `ResumeSeat`
   runs only once the slot is owned.
4. A mid-game drop calls `DisconnectPlayer`, not `LeaveLobby`. The hold is
   released on hand end and on shutdown, not only by its timer.
5. Finalize lives on the Manager and uses the snapshot taken at game **start**.
   Rules error, abandoned and shutdown -> history without Elo. Interrupted -> only
   the leavers' losses are written.
6. Soft-deleted rankings must be revived before the seed, or finalize aborts.
7. Advisory locks are per seat, sorted by folded key, two-int4, and taken by
   finalize and erasure alike; the 24-hour damp count is per pair, across games.
8. Any view that subscribes implements `router.Closer`; the router and
   `releaseSession` call it.
9. Anything kept after `Frame`/`Sync` must be copied, not aliased.
10. Lock order is tracker -> manager -> lobby -> engine. `State` has no lock.
11. A catalog entry is rules **and** view; the slug is persisted, so changing one
    is a data migration.
12. An accept-loop or API failure still runs `drainServer`.
13. Every screen fits 64x20; measure with `AvailableContentHeight`, never assume
    rows.
14. Chips are conserved. `checkChipConservation` is the tripwire, not the
    enforcement - see §5.
15. `TimeoutAction` must return a move the same package's `ValidateAction`
    accepts.
16. A rules panic must end the table, not the process - `recoverRulesPanic`
    stays a **direct** defer in `onTurnTimeout`, `SubmitAction`'s recover stays a
    direct defer, and neither asks the rules for standings.
17. The same seat with the same turn length keeps its deadline (10 s floor), and a
    miss is charged once per seat-turn.
18. Every TUI feed message carries the channel that delivered it; a view drops one
    stamped with any other.

---

## 13. Reading the call graph

The repo is indexed by GitNexus (`.gitnexus/`, local, not committed). Two seams
matter more than any single query result, because channel receives and
`time.AfterFunc` do not become CALLS edges.

**There is no static path from `BoundEngine.Submit` to `finalizeFinishedGame`.**
The engine broadcasts `EventGameEnded`; a different goroutine
(`handleBroadcasterEvents`) picks it up.

```
Lobby.startGameLocked -> watchGameLocked -> handleBroadcasterEvents
  -> releaseFinishedGame        (reopen first)
  -> requestFinalize -> Manager.finalizeFinishedGame
      -> registerFinalizer / shutdownCtx
      -> persistFinishedMatch -> Engine.StandingsWithPlaces
                              -> rating gate
                              -> recordFinishedMatch -> MatchRepository
```

**Generation fencing and grace fencing are two different mechanisms that must
agree.** The owning generation in `ReleaseWith` on the SSH side, `pending` -> `expiring` on the lobby
side. A reconnect race is what happens when they disagree.

```
releaseSession
  -> SessionTracker.ReleaseWith (owning generation only, under t.mu)
  -> Manager.DisconnectPlayer
      -> disconnectGrace.arm (mid-game)
      -> OR LeaveLobby / expireLeave -> Lobby.notifyEngineAndBroadcast
          -> Engine.RemovePlayer -> Manager.RemoveLobby (empty table)
```

Re-index after a large merge:

```bash
node .gitnexus/run.cjs analyze --index-only
```

Absent edges across channels and timers mean "dynamic hop", not "dead code".

---

## 14. Doc map

| Doc | Use |
|---|---|
| [`README.md`](../README.md) | What it is, how to play, how to run |
| [`docs/README.md`](README.md) | The index and the recommended path |
| [`reading-guide.md`](reading-guide.md) | Ordered bottom-up file tour, tooling, "where is X?" |
| [`decisions.md`](decisions.md) | One record per non-obvious choice |
| [`onboarding.md`](onboarding.md) | Product story, annotated tree, local development |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | How to add a game, test and PR norms |
| [`SECURITY.md`](SECURITY.md) | Disclosure, scope, deployment hardening |
| [`data-inventory.md`](data-inventory.md) | Per-field data inventory |
| [`../CLAUDE.md`](../CLAUDE.md) | Short operational brief for agents |
