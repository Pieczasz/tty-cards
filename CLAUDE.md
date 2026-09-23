# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make test-short # unit tests, no Docker
make test # go test -race ./...
make test-integration # -tags=integration, needs Docker (testcontainers)
make lint # golangci-lint
make build # -> bin/server
make ci # fmt, fix, lint, test, build

go test -race -run TestName ./internal/game/poker/ # single test
go test -race -run 'TestX/subtest_name' ./internal/lobby/ # single subtest

make migrate-create # prompts for name, writes internal/db/migrations/
make migrate-up # needs $DB_DSN exported
```

Local multi-client testing: `./scripts/dev-session.sh` opens three tmux-attached SSH clients against a running server (`TC_PORT=6969` bypasses the nginx proxy; the server then needs `PROXY_PROTOCOL=false`, since a bare ssh client sends no PROXY header). `PROXY_TRUSTED_CIDRS`, when set, is the only set of networks whose PROXY header the ssh listener honors - every other peer is refused - and the stats API's `X-Forwarded-For` is believed only from the same networks; compose sets it to its `edge` subnets.

## Architecture

SSH connection -> wish middleware -> Bubble Tea `Router` -> view -> `game.BoundEngine` -> `game.Engine` -> `Rules`.

### Game registration is a single point

`internal/catalog/catalog.go` `All` is the only place a game is declared, and each entry carries both the rules factory and the TUI view constructor. `cmd/server/main.go` builds the `game.Registry` from it; `internal/tui/app.go` registers routes from it. A game cannot be registered without a view (`catalog_test.go` fails on a missing field or duplicate slug); copying an entry and changing only the rules still compiles, so keep the pair in lockstep by hand.

Two identifiers, two consumers: `Module.Name` (display name) is the registry key and the lobby option, and `db.Game.Name` stores it for display only; `Slug` is what the TUI derives routes from (`router.GameRoute(slug)` -> `"game_<slug>"`) **and** the persisted game identity, `games.slug` (migration 000005). The pair travels as `db.GameRef{Slug, Name}`, built at game start in `lobby.go`. Changing a slug migrates data; changing a name does not. `internal/game` deliberately knows nothing about routes.

### Shared helpers, not per-game copies

`internal/deck` owns card mechanics every game needs: `RemoveOne`/`RemoveEach` (hand
removal that never aliases), and three distinct rank questions that must not be
swapped - `RankValue` (Ace high, 14, for poker and Hearts), `RunOrder` (Ace low 1..13,
courts distinct, for Gin Rummy runs) and `PipValue` (courts count 10, for deadwood).
All three answer 0 outside Ace..King, Joker included - losing loudly beats tying the ace.
`IsSuit` is the guard for a named suit (refuses `NoSuit`, the zero value, client garbage).
Standard ranks are 1-based, so a zero `deck.Card` is detectably empty rather than the
ace of spades; Uno's extra ranks sit in their own block at 20+. `AllRanks` is what
makes `Rank`-keyed maps testable for exhaustiveness. `Pile.Shuffle()` returns nothing:
crypto/rand cannot fail on Go 1.24+, so the plumbed error was an unreachable branch.

`game.AnyScoreAtLeast` is the shared match-target check for Hearts and Gin Rummy.

### Layer boundaries

- `internal/db` - GORM models **and** the `UserRepository` / `MatchRepository` interfaces. It defines the contract.
- `internal/repository` - the GORM implementations. Everything else depends on the `db` interfaces, never on this package (except `cmd/server`, which is the composition root; `depguard` enforces it). The auth sentinels `internal/ssh` maps live in `internal/db/errors.go`.
- `internal/game` - pure rules/engine, no db, no TUI, no routes. Seat identity is the scalars on `game.Player` (`UserID`, `Name`, `Ratings`), not a `*db.User`.
- `internal/tui` - presentation only; reaches state through `router.GlobalContext`, which carries `*lobby.Manager` directly (the one-implementation `lobby.SessionAPI` interface is gone) and never a `MatchRepository`.

### Engine and Rules contract

`Engine` owns a single mutex covering its clock fields and the `State`, held for the whole of `Start`, `SubmitAction`, and `RemovePlayer`. `Rules` methods (and `WithState`/`Frame` callbacks) run with it held, so they must never call back into `Engine` (deadlock) and may mutate `*State` freely. The no-callback rule holds structurally today - `Rules` methods receive only `*State`, which carries no engine handle - so do not add one.

Per-game state lives in `State.Extra` (`crazyeight.State`, `poker.State`, `uno.State`, `hearts.State`, `ginrummy.State`). The turn cursor is settled by `applyNextTurnLocked`: `State.OverrideNextTurn` wins, else advance, else honor `State.CurrentTurn` - that is how poker picks the next actor from `AfterAction`. `ApplyAction` runs then `AfterAction`; an error from either finishes the game (the state may be half-applied), so anything checkable up front belongs in `ValidateAction`. `EventGameEnded` carries an `EndReason` (win / rules error / forfeit / abandoned / interrupted) so observers can tell them apart. Interrupted is a match one seat's leave ended early for everyone: hearts' `OnPlayerLeave` sets `State.Interrupted`, and `removePlayerLocked` then reports it instead of a win; the engine knows nothing about what finalize does with it (only the leavers' losses are written). A rules panic, on the player path or the timer, ends the table through `finishAfterPanicLocked` as a rules error without asking the rules for standings.

Mid-hand disconnects: implement the optional `game.PlayerLeaveHandler` (`OnPlayerLeave` before removal, `AfterPlayerRemoved` after seat indices shift).

### Turn clock

`applyNextTurnLocked` also arms a per-turn timer (`DefaultTurnTimeout`, 30s). The same seat with the same turn length is the same turn carrying on (gin's draw then discard, a re-armed auto-play, somebody else leaving): `armTurnTimerLocked` keeps its running deadline, floored at `minTurnRemaining` (10s), and a miss is charged once per seat-turn (`turnMissCharged`), not per expiry. On expiry the engine plays the move from the optional `game.TurnTimeoutHandler` (`TimeoutAction`) and broadcasts `EventTurnTimedOut` on the same lock hold that charged the miss - outside it, a player whose action lands in the gap gets the miss refunded while the "timed out" they disproved still ships; after `MaxMissedTurns` (3) consecutive missed turns it re-checks under the engine lock and only then broadcasts `EventPlayerIdle` and removes the seat. A player's own *accepted* action clears their count - a move the rules reject does not, or spamming garbage would dodge removal forever - so this only fires on someone who stopped playing.

Rules opt in: no `TurnTimeoutHandler` means no clock. Poker checks when free, calls when no opponent can cover more than it already has out, folds otherwise, and deals between hands (an absent dealer would otherwise freeze the table); crazy eights and uno draw; hearts passes its three most dangerous cards (Q♠, A♠, K♠, then the highest hearts), plays its first legal card, and deals the next hand; gin rummy draws, knocks when a discard leaves it gin, otherwise sheds its priciest deadwood, and deals. `TimeoutAction` must return something `ValidateAction` accepts, or the turn re-arms (on the same lock hold, as a fresh chargeable turn with the 10s floor) and each refused expiry still costs a miss - gin rummy's `autoDiscard` skips the card the upcard rule forbids for exactly this reason.

`game.TurnDurationHandler` lets a rules set stretch a particular turn: hearts gives the pass phase 45s and the between-hands prompt a minute; poker and gin rummy stretch the between-hands deal the same way. Returning zero keeps the engine default; it cannot resurrect a clock `WithTurnTimeout` disabled.

`turnSeq` is what makes this safe: every cursor change invalidates timers already in flight, and an auto-play carries the generation it was computed for, so a player who acted as their clock ran out is neither charged a miss nor double-played. `stopTurnTimerLocked` runs from `finishGameLocked`, the last-player-standing path, and `Close`; the engine's `closed` flag is what stops a concurrently-resolved timeout from re-arming a timer on a closed engine.

The game view quits its bubbletea program on its own `EventPlayerIdle`, which is what ends the ssh session through the ordinary `releaseSession` path - the engine never reaches into the session layer.

A dropped session calls `Manager.DisconnectPlayer`, not `LeaveLobby`: a mid-game seat survives for `DisconnectGrace` (90s, with the engine auto-playing and its idle removal as the backstop) so a reconnect - `Manager.ResumePlayer`, reached through `tui.ResumeSeat`, which the ssh layer calls only once the session owns its tracker slot - resumes the match instead of forfeiting it. A waiting-lobby seat and any seat during shutdown still leave immediately.

### BoundEngine, not Engine, in views

`game.Bind(engine, playerID)` gives a session-scoped handle that only submits as that player and only returns that player's hand. TUI views use `BoundEngine` / `Session.Sync` (one `Frame` lock hold: snapshot, own hand, clock and `Extra` cannot describe different moments); `Engine.WithState` and `SubmitAction` are for the server side. `Subscribe`/`Unsubscribe` join the feed without handing out the broadcaster.

It is a façade, not a capability: `Frame`'s callback still hands the view the live, unredacted `*State`, and poker reads every seat from it because rendering a table means rendering every seat. There is no `Engine()` escape hatch any more. The value is that the default path is the safe one, so the redaction is the view's stated job (`buildSeats`).

### gameview.Session is the view baseline

Every game view embeds `gameview.Session` (`internal/tui/views/game/session.go`). It owns the parts that are the same in all five games: binding to the engine, subscribing (`NewSession`), reading the feed and the whole `Update` loop (`HandleFrame`), losing a seat to the idle timer (`IdleRemoved`), the turn-clock tick (`ClockTick`), the last rejected move (`ActionErr`, which `Submit` keeps and the hero band renders), the hand cursor (`MoveCursor` / `SelectDigit` / `SelectedCard`), the forfeit prompt (`HandleLeaveKey` / `LeaveConfirmScreen`: mid-game esc asks before leaving), the idle-quit exemption for a live table (`IdleExempt`), leaving the table (`Leave`), and `Close` - which is what satisfies `router.Closer`. The shared layout frame (`gameview.RenderBands`, the compact breakpoints, the width-budgeted hand renderers) lives in `internal/tui/views/game`; a new game implements its own rules rendering and nothing else.

Read per-game state through the `extra` callback of `Session.Sync` (unredacted table state the view has to filter itself; `BoundEngine.Frame` is the standalone form) - and seat order, display names and stock size through `BaseState` (`Seats`, `SeatOrder()`, `SeatNames()`, `DeckSize`) rather than re-deriving them from the live `*State` - `PlayerSnapshot.Username` already falls back to the player ID.

Anything a view keeps after releasing the engine lock must be copied, not aliased (`maps.Clone`, `HandResult.Clone`).

### Subscription lifecycle

`broadcaster.Broadcaster[T]` is latest-wins (drops the oldest on a full buffer) and `Subscribe` returns `ErrAtCapacity` / `ErrClosed` rather than a pre-closed channel, so a caller cannot mistake "you will never receive anything" for "the stream ended". Engines size it `len(players)+8` for the ranked-finalize watcher and reconnect overlap. Views surface a failure in their own error line; the lobby logs it loudly because it means a match result will not be persisted.

Any view holding a subscription must implement `router.Closer`. The router closes the active view on navigation; `ssh.closeSessionModel` closes the whole model on disconnect. Skipping `Close()` parks a listener goroutine and burns a subscriber slot until the engine closes.

Lock order is `SessionTracker.mu` -> manager (`m.mu`) -> lobby (`l.mu`) -> engine (`Engine.mu`) - see `Manager.Kick` / `RemoveLobby`. The tracker comes first because `ReleaseWith` runs `DisconnectPlayer` under it, so a session's teardown and a reconnect's resume cannot interleave.

### SSH server

Middleware in `wish.WithMiddleware` runs **last-first**, so `sessionLifecycle` is listed last to be outermost. charm.land/ssh (v0.4.3) recovers on every goroutine it spawns, so `recoverSession` is a second layer - a metric and a clean lobby leave - and it must stay a **direct** `defer` (a `recover()` inside a function called *by* a deferred function returns nil). It is not what the player sees, though: nothing panics *out of* bubbletea, so `reportingModel` wraps Init/Update/View and `notifySessionPanic` writes `panicNotice` to `s.Stderr()`. Per-session state (user, model, span) lives in a `sessionRegistry` that `SetupServer` makes per server, never on `s.Context()` - that context is per-**connection** and shared by every channel. Session channels are capped per connection (`maxSessionsPerConnection`, 2) in the `session` `ChannelHandler`, which rejects the extra one with `ResourceShortage` before `Accept`; `env` requests are capped at 32 / 8 KiB per channel. Auth accepts any public key; identity is the SHA256 fingerprint, first connection registers the username. `SessionTracker.Connect` displaces an existing session for the account **and closes its conn**, outside the tracker lock. Two limiters, both keyed by `ratelimit.NetKey` (IPv6 /64): the auth one counts *attempts* (an ssh-agent offers each key it holds), and `REGISTRATION_LIMIT`/`REGISTRATION_WINDOW` (default 5 per 1h) gates only the `user == nil` branch of `LoadOrRegisterUser`, after `db.ValidateUsername` - a typo does not spend the budget, and the player is told why the name is invalid. `NetKey` is total; `netKeyFor` is what fails closed on an unkeyable address. `mapRegisterError` folds only taken into `ErrNameUnavailable` - a distinguishable "taken" is an account-existence oracle; an invalid name is a fixed rule, not a fact about other accounts. Usernames are unique case-insensitively (`idx_users_username_lower`, migration 000006). Connect/disconnect log `client_net` (the /64), not `remote_addr`; the session span carries no client address at all.

### Stats API

`internal/httpapi` serves a read-only JSON feed the website reads: `/v1/stats`
(players online, hands in play, tables open) and `/v1/leaderboard?limit=N`. It is
mounted by `cmd/server/main.go` on `API_PORT` (6970) and reached only through the
proxy's `/api/` location - never published to the host.

It is deliberately narrow: no writes, no auth, no per-user data, nothing the TUI
leaderboard does not already show any visitor. That is what makes it safe
unauthenticated. Live counts come from `ssh.SessionTracker.Count` and
`lobby.Manager.Stats`, so `SetupServer` requires the `Tracker` it shares with the stats API
(`ErrNoTracker` without one).

`API_TRUST_PROXY` makes the per-network limiter read `X-Forwarded-For`, and with
`PROXY_TRUSTED_CIDRS` set only from a peer inside those networks (6970 is reachable from
every container on the network, not just nginx). It **defaults
to false** and compose opts in explicitly: a directly exposed listener that trusts the
header can be evaded by forging it, so the unsafe direction has to be chosen. nginx
sets it from `$remote_addr`, not `$proxy_add_x_forwarded_for`, so a client cannot
prepend its own value.

Both limiters key on `ratelimit.NetKey`, which collapses IPv6 to its /64. Keying on the
full address is meaningless there: one customer is routinely handed 2^64 of them.

The backend listens on `:6969` behind nginx speaking PROXY protocol. Publishing that port lets clients spoof source IPs and defeat the per-IP rate limiter. Compose publishes 22 and 80 only, plus Grafana on `127.0.0.1:3000`. The stats API makes no spans (a noop tracer provider: every site visitor polls it), and `otelhttp.WithServerName("stats-api:80")` pins `server.address` and `server.port`, keeping the client's `Host` header out of the metric labels; `internal/httpapi/telemetry_test.go` asserts both.

Retention is explicit and set in three places: Loki 14d (`internal/config/loki/loki.yaml`), Tempo 48h (`internal/config/tempo/tempo.yaml`), Prometheus 30d (`compose.yaml`). Per-field inventory in `docs/data-inventory.md`.

## Conventions

- Schema changes are SQL migrations in `internal/db/migrations/` (up **and** down). `testutil.SetupTestDB` applies those same files, so the tested schema cannot drift from the deployed one.
- Integration tests are behind `//go:build integration`; `testutil.SetupTestDB` skips when Docker is absent.
- Table-driven tests with named subtests, `t.Parallel()` where safe. `pgregory.net/rapid` for property tests, `goleak` for goroutine leaks.
- Wrap errors with `%w`, lowercase messages.
- The `.golangci.yml` size gates (`funlen` 75/55, `cyclop` 21, `gocognit` 30, `nestif` 9, `lll` 140) sit just above the worst surviving function. Split the function rather than raising a threshold.
- Comments explain *why*, not *what*; the codebase is intentionally light on them.

<!-- gitnexus:start -->
# GitNexus - Code Intelligence

This project is indexed by GitNexus as **terminal-card** (4149 symbols, 18961 relationships, 353 execution flows).

> Index stale? Run `node .gitnexus/run.cjs analyze --index-only` from the project root - it auto-selects an available runner. No `.gitnexus/run.cjs` yet? Bootstrap with `npx`, `bunx`, or `pnpm dlx` - e.g. `bunx gitnexus@latest analyze` (npm 11 npx crash; #1939).

## Always Do

- **MUST run impact before editing.** Use `impact({target: "symbolName", direction: "upstream"})` or `node .gitnexus/run.cjs impact "symbolName" --direction upstream --repo .`; report callers, processes, and risk. Never substitute grep for graph analysis.
- **MUST analyze graph changes before committing.** Use `detect_changes({scope: "all"})` (MCP) or `node .gitnexus/run.cjs detect-changes --scope all --repo .` (CLI fallback). `partial: true` or `truncated: true` is not a clean check - a zero means unseen, not unaffected; re-run it. For regression review: `detect_changes({scope: "compare", base_ref: "main"})` or `node .gitnexus/run.cjs detect-changes --scope compare --base-ref "main" --repo .`.
- MUST warn on HIGH/CRITICAL `risk` pre-edit; never use `riskSharedAxes` to waive a HIGH/CRITICAL `risk` warning. Compare File/symbol: MCP File omits axes; Graph-RAG expands File.
- **MUST treat `risk: UNKNOWN` as unresolved, not as low.** An empty caller set is not evidence the symbol is unused - it can also mean the callers are not resolvable by the index (plain-object property access, dynamic dispatch, cross-language calls). `impact` pairs `UNKNOWN` with a `riskNote` saying so. Confirm with a text search before treating the symbol as safe to change or delete; do not proceed on the strength of a zero.
- **MUST use `query({search_query: "concept"})` for concepts/flows, `context({name: "symbolName"})` for a named symbol, or `impact` for blast radius, on read-only callers, dependencies, imports, or execution flow.** Graph first; text search only for empty/`UNKNOWN`/literals.
- For security review, `explain({target: "fileOrSymbol"})` lists taint findings (source->sink flows; needs `analyze --pdg`).

## Never Do

- NEVER edit a function, class, or method before MCP/CLI impact analysis.
- NEVER ignore HIGH or CRITICAL risk warnings from impact analysis, and never read `UNKNOWN` as an all-clear - it means the walk could not answer, which is the one verdict that requires confirming by other means.
- NEVER rename symbols with find-and-replace - use `rename` which understands the call graph.
- NEVER commit before MCP/CLI graph change analysis.

## Resources

| Resource | Use for |
| --- | --- |
| `gitnexus://repo/terminal-card/context` | Codebase overview, check index freshness |
| `gitnexus://repo/terminal-card/clusters` | All functional areas |
| `gitnexus://repo/terminal-card/processes` | All execution flows |
| `gitnexus://repo/terminal-card/process/{name}` | Step-by-step execution trace |

## CLI

| Task | Read this skill file |
| --- | --- |
| Understand architecture / "How does X work?" | `.claude/skills/gitnexus-exploring/SKILL.md` |
| Blast radius / "What breaks if I change X?" | `.claude/skills/gitnexus-impact-analysis/SKILL.md` |
| Trace bugs / "Why is X failing?" | `.claude/skills/gitnexus-debugging/SKILL.md` |
| Rename / extract / split / refactor | `.claude/skills/gitnexus-refactoring/SKILL.md` |
| Tools, resources, schema reference | `.claude/skills/gitnexus-guide/SKILL.md` |
| Index, status, clean, wiki CLI commands | `.claude/skills/gitnexus-cli/SKILL.md` |

<!-- gitnexus:end -->
