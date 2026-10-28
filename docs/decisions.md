# Decisions

The choices that shape this codebase and would look like mistakes without their reason.
Each record is short: the situation, what was decided, what it costs, where it lives.
Contracts are in [`architecture.md`](architecture.md); this file is only the *why*.

Numbers are **stable ids**, not a count: the series started at 62, and what is not here was
either folded into the comment beside the code it explains or retired. Code comments
cite these numbers, so do not renumber. If you change a decision, change its record -
a stale reason is worse than none.

| # | Decision |
|---|---|
| 1 | One mutex per engine, and rules never call back into it |
| 3 | The event feed is latest-wins, and `Subscribe` returns an error |
| 4 | A generation counter fences the turn clock |
| 5 | `BoundEngine` is a façade, not a capability |
| 6 | `catalog.Games` is the one registration point |
| 7 | A game's identity in the database is its slug |
| 8 | Account ids are UUIDv7 from Postgres 18 |
| 10 | The anti-farm cap counts pairs, across every game |
| 11 | Three end states are recorded without Elo |
| 12 | Players who left rank strictly below players who stayed |
| 14 | A mid-game seat survives 90 seconds, and a second session displaces the first |
| 16 | Identity is an SSH key fingerprint |
| 19 | The backend speaks PROXY protocol and `:6969` is never published |
| 21 | Grafana is loopback-only with no login |
| 22 | Erasure anonymises the `users` row |
| 29 | There is no outbox for match finalization |
| 37 | A rules panic ends one table, not the process |
| 39 | An interrupted match charges only its leavers |
| 45 | The session tracker's lock comes before the manager's |
| 59 | Backups hold the database and the host key, go off-host, and are drilled |
| 60 | Capacity is measured: 20 FPS, a fixed 120x40 terminal, 1250 sessions |
| 62 | The public-lobby cache stays |

**Retired labels still cited in code comments.** D-2: fold-out dead money goes to the
winner (poker). D-3: short all-ins that add up to a full raise reopen the betting.
D-4: usernames are unique case-insensitively (`idx_users_username_lower`). D-6: a turn that
carries on keeps its running deadline. D-7: the edge network runs IPv6. D-8: esc at a live
table asks before forfeiting. D-1 and D-5 are #39 and the registration limiter in
`internal/ssh/auth.go`.

---

## 1. One mutex per engine, and rules never call back into it

**Context.** The turn cursor, timer, deadline, missed-turn map and `*State` are read together;
separate locks would mean an ordering to get right on every path.

**Decision.** `Engine.mu` is the only lock and covers the clock fields and `State`. Every
`Rules` method, `WithState` and `Frame` callback runs with it held, so rules must never call the
engine. That is structural, not by review: `Rules` methods receive only `*State`, which carries
no engine handle. The semgrep rule `rules-package-reaches-the-engine` closes the other route
(a rules package naming `game.Engine`, `NewEngine`, `Bind`, `WithState`, `SubmitAction`).

**Cost.** Coarse: one session's `Frame` briefly blocks another's `Submit`. Fine, because
nothing under the lock does I/O. In return there is one lock order (see #45). A rules set cannot
ask the engine anything; what it needs goes on `State` or `State.Extra`. Do not add an engine
pointer to `State`.

**Where.** `internal/game/engine.go`, `state.go`, `rules.go`.

---

## 3. The event feed is latest-wins, and `Subscribe` returns an error

**Context.** One engine fans events out to every seated session. A slow reader must not stall
the table, and a reader must be able to tell "nothing will arrive" from "the stream ended".

**Decision.** `broadcaster.Broadcaster[T]` drops the oldest event on a full buffer (the end of a
game is always the last event, so it is never the one dropped). `Subscribe` returns
`ErrAtCapacity` / `ErrClosed` instead of a pre-closed channel. Engines size it `len(players)+8`;
a lobby's is `10+8`, because a roster can grow after the broadcaster exists and a reconnecting
player briefly holds two subscriptions.

**Cost.** A view can miss an intermediate event, so views re-read the whole state on each event
rather than apply deltas. A failed subscribe is shown to the player and counted
(`SubscribeFailure`); for the lobby's own watcher it is logged loudly, because the match result
would not be persisted.

**Where.** `internal/broadcaster`, `internal/game/engine.go`, `internal/lobby/watch.go`.

---

## 4. A generation counter fences the turn clock

**Context.** A fired `time.AfterFunc` cannot be stopped. One that was waiting on the engine lock
while the player moved would charge a miss and double-play.

**Decision.** Every cursor change bumps `clock.seq`; a timer captures it when armed and does
nothing on a mismatch. An expiry is settled in one lock hold (charge the miss, then auto-play or
take the seat). A rejected action changes nothing, so spamming garbage cannot dodge idle
removal; only an accepted move clears the miss count. The same seat with the same turn length
keeps its running deadline (floored at 10s), so gin's draw-then-discard is one turn missed once.

**Where.** `internal/game/turnclock.go`, `engine.go` (`turnClock`).

---

## 5. `BoundEngine` is a façade, not a capability

**Decision.** `game.Bind(engine, playerID)` is a session-scoped handle: it submits only as that
player and `Frame` returns only that player's hand. The lobby hands one out per seat; views
never hold an `*Engine`, and the engine does not expose its broadcaster.

**Cost.** It is not a security boundary: `Frame`'s callback gives a view the live, unredacted
`*State`, because rendering a table means rendering every seat. What it buys is that the default
path is safe and redaction is a named, testable function that belongs to the game
(`poker.SeatsFor`). `internal/catalog/render_test.go` renders every seat of every game and
asserts no other seat's card faces appear.

**Where.** `internal/game/bound.go`, `internal/game/poker/seats.go`.

---

## 6. `catalog.Games` is the one registration point

**Decision.** One slice whose `Entry` embeds the `game.Module` (name, slug, seat range,
`Factory`) and adds the view constructor. `game.NewRegistry` panics on a half-declared module,
a duplicate name or slug, or seat bounds no table can meet, so a wiring slip fails the boot.

**Cost.** Copying an entry and changing only the rules still compiles. Accepted: a registry keyed
by type parameter or codegen costs more than the one line of attention. The tests that guard it:
`catalog_test.go`, `requireSameGame` in `render_test.go`, and the slug-vs-migration backfill test.

**Where.** `internal/catalog/catalog.go`, `internal/game/registry.go`.

---

## 7. A game's identity in the database is its slug

**Decision.** `games.slug` is the identity (`ON CONFLICT (slug)`); `name` is refreshed on every
write and is cosmetic. The pair travels as `db.GameRef{Slug, Name}`. The slug is also the
registry key, the `Player.Ratings` key, the route (`game_<slug>`) and the `game_type` metric label.

**Cost.** Changing a slug is a data migration; changing a name is not.

**Where.** `internal/db/games.go`, `internal/repository/match.go`, migration `000005`.

---

## 8. Account ids are UUIDv7 from Postgres 18

**Decision.** `users.id UUID DEFAULT uuidv7()`: unguessable like v4, but time-ordered, so inserts
land at the edge of the primary-key B-tree. Go also sets one in `BeforeCreate`, so neither side
depends on the other.

**Cost.** Postgres 18 is a hard floor (an older volume needs `pg_upgrade` or recreation). The Go
side uses the stdlib `uuid` package, which has no `database/sql` Scanner, hence a GORM serializer
(`stduuid`) and `uuid.String()` for raw query parameters. This id is also the `player_id` in logs.

**Where.** `internal/db/users.go`, `uuid_sql.go`, migration `000001`.

---

## 10. The anti-farm cap counts pairs, across every game

**Context.** Two accounts trading ranked wins is farming or a private rivalry; either way the
ladder should stop paying. Counting repeats of the exact participant set let two accounts farm
forever by rotating a third alt through the table.

**Decision.** `rating.Rate` (a pure package: no I/O, so the whole ladder policy is table-testable)
counts, over ranked matches in the last 24h, the highest co-occurrence of any *pair* at this
table, across all five games. At `MaxSamePairingPerDay` (3) the table is damped: history and
`matches_played` are written, `elo` is not. A player's first `ProvisionalMatches` (5) are rated
per pair too. `internal/repository/match.go` keeps the locks and the reads and calls `Rate`.

**Cost.** One over-budget pair damps every seat at the table. The cap bounds a Sybil farm, it does
not rule one out: the ceiling is linear in the graduated alts someone will run. The pairing lock
must be held before the count is read, or two concurrent finalizes both see an undamped count.

**Where.** `internal/rating/rating.go`, `internal/repository/match.go`.

---

## 11. Three end states are recorded without Elo

**Decision.** A ranked match is written as casual history, with no rating movement, when it ends
in a rules error (the state may be half-applied), when every seat left (standings are reverse
leave order, so rating them pays the last to quit), or when the server is hanging up on its
sessions (`Manager.BeginTeardown`: teardown order, not play, decides who is left holding cards).
A last-player-standing forfeit is rated.

**Cost.** A player cannot tell damped from unrated; `unratedReason` logs which and the
`EndReason` metric label distinguishes them. A match played out normally *while the server
drains* (`BeginShutdown`, before `BeginTeardown`) is still rated. A match with no standings
writes nothing and counts as `dropped`.

**Where.** `internal/lobby/finalize.go` (`unratedReason`), `cmd/server/main.go` (`stopServer`).

---

## 12. Players who left rank strictly below players who stayed

**Decision.** `Rules.Standings` returns places (`[]game.Standing`). `game.RankByScore` ranks the
seated, then the leavers last-to-leave first, and lets a tie group share places only within
itself; poker splits its tie groups at the same boundary.

**Cost.** A rage-quit cannot become a rated draw. Two leavers with the same score still share a
place, because splitting them would mint Elo between two people who both quit.

**Where.** `internal/game/rules.go`, `internal/game/poker/streets.go`.

---

## 14. A mid-game seat survives 90 seconds, and a second session displaces the first

**Decision.** A dropped session calls `Manager.DisconnectPlayer`, not `LeaveLobby`: a seat in a
running game is held for `disconnectGrace` (90s) while the turn clock plays for the absent
player (three missed turns remove the seat sooner). A reconnect cancels the grace and resumes
the match. A second `Connect` for an account displaces the first and *closes its connection*,
or half-open TCP would block a reconnect for the whole grace window and the per-account limit
would be advisory.

**Cost.** A waiting-lobby seat and any seat during shutdown still leave at once. The grace timer
runs on its own goroutine, so its body recovers panics (`Manager.recoverGoroutine`).

**Where.** `internal/lobby/disconnect.go`, `internal/ssh/tracker.go`.

---

## 16. Identity is an SSH key fingerprint

**Decision.** Auth accepts any public key; the SHA256 fingerprint is the account, and the first
connection registers the login name. No passwords, no email, no second factor.

**Cost.** Lose the key, lose the account. Registration is therefore rate-limited on its own
(per /64, per /48 and server-wide, refunded when the registration then fails), and an unavailable
name is reported as "could not register that name" so the banner is not an account-existence oracle.

**Where.** `internal/ssh/auth.go`, `server.go`.

---

## 19. The backend speaks PROXY protocol and `:6969` is never published

**Decision.** nginx owns port 22 and forwards to the backend with a PROXY header, which is how the
backend learns the client address. So a client that can reach `:6969` can forge it and defeat every
per-network limit. Compose never publishes it; `PROXY_TRUSTED_CIDRS` (set to the `edge` network)
makes the listener refuse a PROXY header from anywhere else, and `X-Forwarded-For` on the stats
API is believed only from the same networks. Every network limit keys on the IPv6 /64 (a customer
is routinely handed 2^64 addresses), with a /48 and a global budget behind it.

**Cost.** Local testing needs `PROXY_PROTOCOL=false` and a direct port. A self-hoster who
publishes `6969` voids the rate limiting (see `SECURITY.md`).

**Where.** `cmd/server/main.go` (`proxyListener`), `internal/ratelimit`, `internal/config/nginx.conf`.

---

## 21. Grafana is loopback-only with no login

**Decision.** Anonymous Admin, login form off, published on `127.0.0.1:3000` only, answering only
`Host: localhost`. Reaching it means an SSH session on the host, which already owns everything.

**Cost.** Widening the binding without adding authentication is a vulnerability; CI asserts the
published ports. Loki, Tempo, Prometheus and Alertmanager have no auth either and publish no port.

**Where.** `compose.yaml`, `internal/config/grafana/`.

---

## 22. Erasure anonymises the `users` row

**Decision.** `DeleteAccount` hard-deletes keys and rankings and rewrites `username` to
`deleted_` + 32 hex digits of the UUID; `match_participants` stays, because it is other players'
history. `ValidateUsername` reserves the prefix.

**Cost.** Erasure is irreversible and does not remove the person from others' history, only their
name from it; the privacy notice says so. The leaderboard cache is cleared wholesale afterwards.

**Where.** `internal/repository/user.go`, `internal/db/users.go`, `docs/data-inventory.md`.

---

## 29. There is no outbox for match finalization

**Decision.** A finished match is written by the game's watcher goroutine. Shutdown has two
bounded drain windows (15s, then another 15s with a warning) rather than a durable outbox. Each
game's watcher is counted from the game's *start*, and `BeginShutdown` closes a gate on new games,
so a match ending anywhere in the drain is written.

**Cost.** compose's `stop_grace_period` (80s) must exceed the whole sequential drain
(30 SSH + 5 session teardown + 5 API + 2x15 finalize + 6 OTel = 76s), or the runtime SIGKILLs the
process mid-write. Giving up is logged at ERROR. Residual risk, stated rather than removed: a
write that blocks for 30s is lost from history. A transient failure *before* anything is sent to
Postgres is retried with backoff (`repository.isRetryable`); one that may have committed is not.

**Where.** `cmd/server/main.go` (`waitForFinalizers`), `internal/lobby/finalize.go`.

---

## 37. A rules panic ends one table, not the process

**Decision.** Every place rules code runs under the engine lock has a deferred `recover`
(`SubmitAction`, `RemovePlayer`, `Start`, the turn timer, `Standings`), and goroutines the lobby
starts (watcher, grace timer) recover at their own frame. The engine ends the table through
`endGameLocked(nil, EndReasonRulesError)` without asking the rules to rank a state that may be
half-applied; the match is recorded unrated.

**Cost.** A backstop, not a licence: `gametest.SoakAmongRandomMoves` over every catalog game and a
gin-melds fuzz test exist to keep rules from panicking. `Start` refuses (the table stays
Waiting) rather than ending, since it never reached Playing.

**Where.** `internal/game/engine.go`, `turnclock.go`, `internal/lobby/manager.go`.

---

## 39. An interrupted match charges only its leavers

**Decision.** When one seat's leave ends a hearts match for everyone
(`game.LeaveInterruptsMatch`), Elo is computed over the table with leavers ranked last, but only
the leavers' rows are written, and only downwards. Seated players keep rating and
`matches_played`. With no leaver left to charge, the match is stored unranked so it does not
spend the stayers' pair budget.

**Cost.** Quitting a losing match still costs you and pays nobody.

**Where.** `internal/game/hearts/rules.go`, `internal/repository/match.go`.

---

## 45. The session tracker's lock comes before the manager's

**Decision.** Lock order: account (tracker) -> `Manager.mu` -> `Lobby.mu` -> `Engine.mu`.
`ReleaseWith` runs the teardown (`DisconnectPlayer`) under the account's lock and frees the slot
in a defer, so a panic cannot strand it; a reconnect's `Connect` waits for it. A `Lobby` is a leaf:
it takes only its own lock and holds no pointer to its manager. `ResumeSeat` runs only once the
session owns its slot.

**Cost.** One account's reconnect waits behind its own teardown (in-memory work). Nothing below
the tracker may call back into it; `internal/lobby` does not import `internal/ssh`.

**Where.** `internal/ssh/tracker.go`, `internal/lobby/manager.go`.

---

## 59. Backups hold the database and the host key, go off-host, and are drilled

**Decision.** One archive per run: `pg_dump --format=custom` plus the backend's SSH host key
volume. Retention is a count (`BACKUP_KEEP`, 14), locally and on the remote; `BACKUP_REMOTE` copies
off-host via rclone. `restore.sh` drops and recreates the database, and `restore-drill.sh`
proves an archive restores without touching the live one.

**Cost.** An archive carries the host's private key: a leaked copy lets someone impersonate the
server, so the remote must be an rclone `crypt` remote and archives are `0600`. The drill proves
the dump loads, not that its rows are right.

**Where.** `scripts/backup.sh`, `restore.sh`, `restore-drill.sh`.

---

## 60. Capacity is measured: 20 FPS, a fixed 120x40 terminal, 1250 sessions

**Decision.** bubbletea compares the whole frame every tick, so an idle session costs
fps x terminal cells. `sessionFPS = 20`; each session is drawn at no more than 120x40 whatever it
asks for (the renderer's start size and every resize are capped; 120x40 clears every layout
breakpoint); the backend has `mem_limit: 4g`, `GOMEMLIMIT=2560MiB`, `MAX_CONNECTIONS=1250`, the most
that held on 3 cores in `cmd/loadtest`. Past it a player is told the server is full.

**Cost.** Big terminals get no bigger picture. An earlier version scaled the size with server
load (tiers up to 250x80 and a pool for renderer memory, ~300 lines); it was removed as not worth
its bookkeeping. If it is wanted back, raise `maxTermWidth/Height` together with a loadtest run.
The measurements ran on an 8-core laptop's Docker VM, not the VPS: re-run `make loadtest` there.

**Where.** `internal/ssh/termsize.go`, `server.go`, `compose.yaml`, `cmd/loadtest`.

---

## 62. The public-lobby cache stays

**Decision.** Uncached, a browse refresh at 1000 tables under churn is ~123µs and a join waits two
to three times as long for `m.mu` behind the scans. The cache halves a browse, and the manager
lock with it. If the cached path (~70µs) ever shows in a profile, make a per-lobby entry that the
lobby's own writes keep current; do not remove the cache.

**Where.** `internal/lobby/browse.go`, `browse_bench_test.go`.
