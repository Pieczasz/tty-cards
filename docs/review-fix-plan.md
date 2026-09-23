# Review fix plan

The plan for every finding from the 2026-09-23 eight-region code review, including
the lows, the code smells and the SOLID items. It is a working document: delete it
once the last PR lands, and move anything still true into `decisions.md` or
`architecture.md`.

*Baseline: commit `6526fb8`. Impact risk is GitNexus upstream impact
(`node .gitnexus/run.cjs impact <symbol> --direction upstream --repo .`) run on that
commit. Re-run it before each edit, as `CLAUDE.md` requires.*

## Rules for every item

These come from `CLAUDE.md` and `AGENTS.md` and are not negotiable per item:

1. **Impact first.** Run upstream impact on each symbol before editing it. Items
   marked HIGH or CRITICAL below need the caller list read, not just the count.
2. **A test that fails on the old code.** Write it first, watch it fail, then fix.
   Config-only items get a CI assertion instead (the Grafana-loopback step in
   `test.yml` is the pattern).
3. **Split, never raise a lint threshold.**
4. **Change the record too.** Any item that alters a documented decision updates
   `decisions.md` and `architecture.md` in the same PR.
5. **`make ci` green and `detect-changes --scope all` clean before each commit.**

## Decisions this plan takes

Eight findings need a policy choice. The plan takes the choice marked below. Each
one gets a decision record in the PR that implements it.

| # | Question | Choice taken | Alternative |
|---|---|---|---|
| D-1 | A Hearts match ended by one leaver: rated? | **Seated players unrated, the leaver still loses rating**, new `EndReasonInterrupted` (decided by the maintainer 2026-09-23) | Keep it rated, record the collusion surface |
| D-2 | Poker fold-out after the top bettor leaves: who gets dead money? | **The winner**, matching the showdown path | Refund each folder their excess |
| D-3 | Several short all-ins that add up to a full raise | **Reopen betting** (the TDA rule) | Keep today's rule, record the deviation |
| D-4 | Usernames | **Unique case-insensitively**, display case kept | Keep case-sensitive, record it |
| D-5 | Registration error messages | **Say "invalid" and why; keep "taken" folded.** Revises #17 | Keep both folded |
| D-6 | Turn clock when the same seat keeps the turn | **Keep the deadline, floor of 10 s; count one miss per seat-turn** | Keep resetting, record it |
| D-7 | IPv6 | **Enable IPv6 on the compose network** | Publish no AAAA record, document it |
| D-8 | Leaving mid-game | **esc asks for confirmation** while the game is playing | Keep one-key forfeit |

---

## PR 1 - Crash, DoS and privacy (fix first)

### S1. Memory DoS through unstarted session channels and `env` flooding - HIGH

- **Where:** `internal/ssh/server.go` `SetupServer` (impact LOW), `acquireChannelSlot` (LOW).
- **Fix:** after `wish.NewServer`, set `server.ChannelHandlers["session"]` to a
  wrapper around `ssh.DefaultSessionHandler`:
  - count open session channels per connection on the connection `ssh.Context`,
    and `newChan.Reject(gossh.ResourceShortage, ...)` beyond
    `maxSessionsPerConnection` **before** `Accept`;
  - wrap `gossh.NewChannel` so `Accept` returns a proxied request channel that
    replies `false` to `env` after 32 requests or 8 KiB total. Keep a small cap
    rather than refusing `env` outright: the Bubble Tea middleware reads the
    environment for colour detection;
  - decrement the counter when the channel's request stream closes.
- **Then delete** `acquireChannelSlot`/`releaseChannelSlot` and the check in
  `sessionLifecycle`: the limit now lives where the channel is opened.
- **Test:** `internal/ssh/server_test.go`, a real client over the in-process server:
  opening a third session channel without `shell` is rejected; 1,000 `env` requests
  on one channel leave at most 32 stored. Both fail today.

### S2. Displacement closes the channel, not the connection - MEDIUM

- **Where:** `sessionModel` (LOW), `SessionTracker.Connect` (LOW).
- **Fix:** pass the connection, not the session:
  `conn, _ := s.Context().Value(ssh.ContextKeyConn).(gossh.Conn)`, then
  `tracker.Connect(user.ID, conn)`. `ServerConn.Close` closes the socket without
  waiting on channel writes, and it takes down every channel of that connection
  (at most two). Correct the `Connect` doc comment ("Count under-reports" is wrong:
  Count counts accounts).
- **Test:** replace the `countingCloser` test with one over a real connection: after
  a second login, the first client's `Wait()` returns within 1 s.

### E1. A rules panic on the player path leaves the table running - MEDIUM

- **Where:** `internal/game/engine.go` `SubmitAction` (caller of `submitActionLocked`, HIGH).
- **Fix:** a direct `defer` in `SubmitAction`, after `Lock`, that recovers, logs with
  `debug.Stack()` and calls `finishAfterPanicLocked` (E2). Return an error to the
  view. Do not re-panic: the table is ended, and the player's program keeps running
  to show the result.
- **Test:** `engine_test.go`, rules whose `ApplyAction` panics through `SubmitAction`:
  no panic escapes, `IsFinished()` is true, and `EventGameEnded` carries
  `EndReasonRulesError`.

### E2. The panic recovery can panic again and kill the process - MEDIUM

- **Where:** `turnclock.go` `recoverRulesPanic` (LOW), `engine.go` `finishGameLocked` (CRITICAL, 11 flows: do not change its behaviour).
- **Fix:** add `finishAfterPanicLocked()`. It sets `Phase = Finished`, stops the
  clock and broadcasts `EventGameEnded{Reason: EndReasonRulesError}` **without**
  calling `Rules.Standings` on the corrupted state. Use it from both recoveries.
  `finishGameLocked` is left alone.
- **Same exposure one layer up:** `Engine.StandingsWithPlaces`, called by the lobby
  watcher's finalize, also calls `Rules.Standings`. Recover there and return
  `nil, nil`. Finalize already counts empty standings as `dropped`.
- **Test:** the existing `TestEngine_TurnTimeout_RulesPanicEndsOnlyThisTable`, plus a
  case where `Standings` also panics. The panic must not escape `onTurnTimeout`.

### P1. Tempo stores every website visitor's IP and User-Agent - HIGH

- **Where:** `internal/httpapi/httpapi.go` `Handler` (LOW).
- **Fix:** pass `otelhttp.WithTracerProvider(tracenoop.NewTracerProvider())`. The
  stats API keeps its request metrics, which carry no client address, and stops
  producing spans. That also removes the 15 s `/healthz` span noise.
- **Test:** `httpapi_test.go` with an in-memory span recorder installed globally: a
  request produces zero spans. Extend `metrics_test.go`'s attribute allow-list check
  to the `http.server.*` instruments (fixes the overstated claim in P4).

---

## PR 2 - Session, lobby and account integrity

### S3. Teardown race can forfeit a reconnected player's seat - MEDIUM (plausible)

- **Where:** `releaseSession` (LOW), `Manager.DisconnectPlayer` (HIGH), `tui.Model` (the `ResumePlayer` call).
- **Fix, two parts:**
  - Add `SessionTracker.ReleaseWith(userID, gen, fn func()) bool`. It runs `fn`
    (the `DisconnectPlayer` call) and then deletes the entry, all under `t.mu`, only
    if `gen` is live. This introduces the lock order **tracker, then manager**.
    Nothing takes them the other way; record it in `architecture.md` §4.6.
  - Move the `ResumePlayer` block out of `tui.Model` into an exported
    `tui.ResumeSeat(r *router.Router, ...)`. `sessionModel` calls it **after**
    `tracker.Connect` succeeds. That also fixes the `ErrServerFull` path: a refused
    session no longer cancels the grace timer.
- **Test:** `lifecycle_test.go`: session A's teardown interleaved with B's connect
  never leaves a grace timer armed for B's seat. Run it with `-race`.

### L1. Changing a setting keeps everyone ready - MEDIUM

- **Where:** `lobby.go` `withLeaderSettings` (LOW, 4 callers).
- **Fix:** `clear(l.ready)` after a successful `mutate()`, then broadcast
  `EventPlayersUpdated` as well as `EventSettingsUpdated`.
- **Test:** the reviewer's scratch case `TestScratch_RankedFlipKeepsGuestReady`,
  inverted: after a ranked flip no guest is ready, and the leader's toggle does not
  start a game.

### L5. A roster change can leave a table all-ready that never starts - LOW

- **Where:** `detachPlayerLocked` (called only by `LeaveLobby`, confirmed by text search), `Manager.Kick` (LOW).
- **Fix:** clear ready flags on any roster change in a `Waiting` lobby, the same rule
  as L1. One rule, "any change un-readies the table", replaces the special case.
- **Test:** A, B and C ready, D unready and leaves: nobody is ready and no game started.

### L2. `RemoveLobby` can delete a newer lobby's mapping - MEDIUM

- **Where:** `manager.go` `RemoveLobby` (**CRITICAL**, 12 flows, 8 modules).
- **Fix:** delete a `playerLobby` entry only when it still points at `l`:
  `if m.playerLobby[id] == l { delete(m.playerLobby, id) }` for the leader and every
  guest. The change is inside the function, so no caller changes.
- **Test:** the reviewer's `TestScratch_RemoveLobbyWipesNewMapping`: a player who
  created a new table between `LeaveLobby` and `RemoveLobby` is still found by
  `FindLobbyByPlayer`.

### L3. A finished match can be dropped at shutdown - LOW

- **Where:** `handleBroadcasterEvents` (LOW), `requestFinalize` (LOW), `finalizeFinishedGame` (LOW).
- **Fix:** call `registerFinalizer` first in `handleBroadcasterEvents`, then
  `releaseFinishedGame`, then `requestFinalize(..., registered)`.
  `finalizeFinishedGame` takes the registration rather than making it. The table
  still reopens before the write.
- **Test:** `finalize_test.go`: a game that ends while `releaseHeldSeats` is blocked
  on `m.mu` is still waited on by `WaitForFinalizers`.

### L6. Kicking a held guest leaks its grace timer - LOW

- **Where:** `Manager.Kick` (LOW).
- **Fix:** `m.grace.clear(target.ID)` under `m.mu`.
- **Test:** kick a disconnected guest: no pending grace entry remains.

### L4. Browse can list a table that just went private or started - LOW

- **Where:** `browse.go` `browseEntry` (text search: one caller, `BrowseLobbies`).
- **Fix:** `browseEntry` returns `(BrowseEntry, bool)`. It is `false` when the lobby
  is private or not `Waiting`, and `BrowseLobbies` skips it. Delete the comment
  claiming concurrent misses cannot produce a wrong list.
- **Test:** a cache holding a lobby that is then made private does not return it.

### R1. An erased account comes back on the leaderboard - MEDIUM

- **Where:** `repository/match.go` `updateRankingsTx`, `seedRankingRows` (LOW); `user.go` `eraseUserLocked`.
- **Fix, root cause:**
  - `eraseUserLocked` takes `pg_advisory_xact_lock` on the user's seat key, so an
    erasure and a finalize for that user serialize.
  - After `lockPairing`, `updateRankingsTx` drops the seats whose `users` row is
    anonymised (`username LIKE 'deleted\_%'`). It computes Elo among the rest and
    writes history for everyone. An erased seat keeps its participant row, which is
    what decision #22 wants, and gets no ranking.
- **Test (integration):** finalize a ranked match after one seat's `DeleteAccount`:
  that user has no `rankings` row and does not appear in `BestPlayers`.

### T4. Account deletion is not modal - MEDIUM

- **Where:** `profile.go` `confirmKey`, `accountDeleted` (LOW).
- **Fix:** a `deleteRunning` phase entered when the command is issued. It swallows
  every key except ctrl+c until `accountDeletedMsg` arrives, so the player cannot
  navigate away and keep playing on an erased account.
- **Test:** `profile_test.go`: after enter, esc and q are ignored until the result arrives.

### T2. One esc forfeits a ranked game - MEDIUM (D-8)

- **Where:** `gameview.Session.Leave` (**CRITICAL**, 10 callers: one per game view plus the finished-enter paths).
- **Fix:** add `Session.HandleLeaveKey(key string) (tea.Cmd, bool)` to the Session.
  While `Phase == Playing`, the first esc arms `confirmLeave` and shows "Leave and
  forfeit? y/n", `y` calls `Leave`, and anything else disarms. Once `Finished`,
  esc leaves as today. Each view's esc branch calls the helper after closing its own
  prompt. That is also T10's first deduplication.
- **Test:** `session_test.go`: one esc while playing does not call `LeaveLobby`, and
  esc then y does. The per-view tests for picker cancellation still pass.

---

## PR 3 - Deployment, privacy and config

Config-only items. Each gets a CI assertion in `test.yml` next to "Grafana stays on
loopback", because none of them has a Go test to fail.

| ID | Fix | Where | CI check |
|---|---|---|---|
| P2 | `limit_req_log_level warn; limit_conn_log_level warn;` and `error_log stderr error;` in both contexts. Add an Alloy `stage.replace` that rewrites `client: <addr>` to `client: redacted` on the proxy container's lines, keeping upstream errors diagnosable | `nginx.conf`, `alloy/config.alloy` | grep both directives |
| D1 | `nginx:1.28-alpine` pinned by digest. Pin every compose image by digest, and pin CI's `nginx -t` to the same | `compose.yaml`, `test.yml` | every `image:` has `@sha256:` |
| D2 | (D-7) An explicit compose network with `enable_ipv6: true` and fixed subnets. `listen [::]:22` and `listen [::]:80` | `compose.yaml`, `nginx.conf` | `nginx -t`, grep `listen [::]` |
| D3 | `GF_SERVER_DOMAIN=localhost`, `GF_SERVER_ENFORCE_DOMAIN=true`; current Grafana 11.x digest | `compose.yaml` | grep `ENFORCE_DOMAIN=true` |
| D4 | `limit_conn_zone $client_net zone=http_addr:10m; limit_conn http_addr 16; client_header_timeout 10s; client_body_timeout 10s;` | `nginx.conf` http | `nginx -t` |
| D5 | `upstream` blocks with `zone` and `server backend:<port> resolve`, plus `resolver 127.0.0.11 valid=10s` (open-source nginx has had `resolve` in upstreams since 1.27.3) | `nginx.conf` | `nginx -t` |
| D6 | `limit_req_status 429;` | `nginx.conf` | grep |
| D7 | Stream `proxy_connect_timeout 5s`; `proxy_timeout 1h` stays | `nginx.conf` | grep |
| D8 | `${DB_PASSWORD:?set DB_PASSWORD}` at all three sites | `compose.yaml` | `docker compose config -q` with it unset **fails** |
| D9 | Alloy reads the Docker API through a `docker-socket-proxy` service allowing only `CONTAINERS=1`; Alloy's socket mount goes. Update the memory arithmetic in the compose header (currently 5632 MiB) | `compose.yaml`, `config.alloy` | grep: no `docker.sock` on alloy |
| D10 | `security_opt: [no-new-privileges:true]` on every service. On the proxy, `cap_drop: [ALL]` and add back `NET_BIND_SERVICE`, `CHOWN`, `SETUID`, `SETGID` | `compose.yaml` | a script asserting each service |
| D11 | Top-level `permissions: contents: read` in both workflows; actions pinned by commit SHA; `.github/dependabot.yml` for `github-actions` and `docker` | `.github/` | the workflow itself |
| D12 | `paths-ignore: ['web/**']` on `test.yml`, which makes the comment in `web.yml` true | `test.yml` | - |
| P3 | `backup.sh`: `umask 077`; write to `"$out.part"` and `mv` on success; read `DB_*` from `.env` with a parser, not `. ./.env`. Add `backups/` to `.dockerignore` | `scripts/backup.sh`, `.dockerignore` | `shellcheck`; grep `.dockerignore` |

### S6. A mistyped `ENV` silently disables the production checks - LOW

- **Where:** `config.go` `resolveEnv`, `Validate` (both LOW).
- **Fix:** an unrecognised `ENV` is an error, not a fallback. In production,
  `DB_SSLMODE` must be `require`, `verify-ca` or `verify-full`, unless the existing
  `ALLOW_INSECURE_DB` or internal-host exception applies.
- **Test:** `config_test.go`: `ENV=prod` and `DB_SSLMODE=prefer` in production are both errors.

### S7. `make loadtest` is broken by the registration limit - LOW

- **Where:** `config.go`, `SetupServer`.
- **Fix:** `REGISTRATION_LIMIT` and `REGISTRATION_WINDOW` env settings, defaulting to
  5 and 1 h and validated at 1 or more. Document the loadtest settings in `README.md`
  and in the `make loadtest` comment.
- **Test:** `config_test.go` default and override.

### S8-proxy. The PROXY listener trusts a header from anyone - info

- **Where:** `cmd/server/main.go` `serve`.
- **Fix:** `PROXY_TRUSTED_CIDRS`. When set, a `proxyproto.ConnPolicy` accepts a header
  only from those networks and rejects everything else. When empty, today's
  behaviour. Compose sets it to the fixed subnet D2 introduces.
- **Test:** `main_test.go`: a header from outside the trusted CIDR is refused.

---

## PR 4 - Poker

All in `internal/game/poker`. Every impact is LOW except `awardUncontested`
(**CRITICAL**, 13 flows): read all of its callers before K4.

| ID | Fix | Where | Test (fails today) |
|---|---|---|---|
| K1 | `TimeoutAction` returns `ActionCall{}` when `largestCallableBet(...) <= PlayerBets[p]`, i.e. the call is refunded in full | `TimeoutAction` | heads-up, p1 short blind at 10: timeout calls, stacks 1000/10 intact before showdown |
| K2 | (D-3) Track `LastBetLevel[p]`, the `CurrentBet` when p last acted. A raise is allowed when `CurrentBet - LastBetLevel[p] >= MinRaise`. It replaces `ActedThisRound` as the reopen test in `checkBettingReopened` | `applyBetIncrease`, `checkBettingReopened` | A bets 100, B shoves 150, C shoves 220: A may raise |
| K3 | Add `RaiseBounds(state, playerID) (lo, hi uint, ok bool)`, the one source of truth. `validateRaiseTo` accepts `amount == callable` when `callable < CurrentBet+MinRaise`. The view's `canRaise`/`clampRaise` call `RaiseBounds` instead of re-deriving | `rules.go`, `views/game/poker/model.go` | A 1000 vs B 30 on the flop: raise to 30 is legal, and the view offers exactly it |
| K4 | (D-2) `awardUncontested` refunds only the top contributor's excess over the second-highest contribution. Everything else goes to the winner, matching `buildSidePots`' orphan rule | `streets.go` | the reviewer's W/A/C scenario: C gets nothing back |
| K5 | `resultLevel` applies the hand-score tiebreak only when `ReachedShowdown` and both players are seated | `streets.go` | two leavers at 1000 share a place |
| K7 | `reveal` only when `ReachedShowdown`, not on `Phase == Finished` | `views/game/poker/model.go` | redaction test: fold-out final hand shows no winner cards |
| K6 | Smells: delete the second `StandingScorer` assertion; extract the shared "one contender left, award, finish, else settle, else next actor" into `resolveAfterChange` used by both `afterBettingAction` and `AfterPlayerRemoved`, with one error policy; delete `isFolded`; `beginHand`'s error path refunds blinds; split `rules.go` into `hand.go`, `betting.go`, `leave.go` (moves only) | `rules.go` | existing suite plus `TestChipsAreConservedAcrossRandomHands` |

K6 lands last in the PR, as pure moves after the behaviour fixes, so the diff
reviewers read for K1 to K5 stays small.

---

## PR 5 - The other games and the engine clock

| ID | Fix | Where / impact | Test |
|---|---|---|---|
| G1 | (D-1) Add `EndReasonInterrupted`. Hearts `OnPlayerLeave` sets `State.Interrupted`, and the engine uses that reason when a removal ends the game. Finalize applies only the leavers' negative Elo deltas: seated players move nothing, so a friend quitting cannot bank a lead, and a losing player cannot quit for free | `game/action.go`, `engine.go` `removePlayerLocked` (**CRITICAL**), hearts `OnPlayerLeave` (**CRITICAL**, 10 flows) | hearts: a leave ends the match with `EndReasonInterrupted`; lobby: recorded unrated |
| G2 | `ReshuffleDiscardIntoStock` becomes a no-op on a non-empty stock and returns nothing. Add `game.DrawWithReshuffle(state) (deck.Card, bool)` and use it in crazy eights and uno. Delete the stale "crypto/rand failure" comment and the unreachable error branches | `shed.go` (**HIGH**, 2 callers, 4 modules) | card-conservation soak unchanged; a new shed test for an empty stock with a one-card discard |
| G3 | Delete the second `var _ game.StandingScorer` in all five rules packages | rules.go x5 | compile |
| G4 | `HandComplete` becomes a method derived from `Stage`/`HandPhase` in hearts and gin rummy | `state.go` x2 | existing suites |
| G5 | Extract the shared crazy eights/uno `ValidateAction` skeleton into `shed.go` (`ValidateShedAction(state, action, playable)`) | `shed.go` | both suites |
| G6 | Gin `autoDiscard` knocks when a legal discard leaves 0 deadwood | `ginrummy` `autoDiscard` (LOW) | soak stays legal; new: a gin hand auto-knocks |
| G7 | Hearts auto-pass sends the three most dangerous cards (Q♠, A♠, K♠, then the highest hearts), not the lowest | hearts `TimeoutAction` | soak stays legal; unit test on a fixed hand |
| G8 | Comment nits: `deck.Shuffle`'s two first sentences, `uno/deck.go` names `InitialDeck`, gin names `MaxHandTurns` | - | - |
| G9 | Document the deliberate deviations (crazy eights deals 7, uno ranks by cards and has no UNO call, gin has no big-gin or box bonuses) in the README game table | `README.md` | - |
| E3 | `rearmTurnTimer(seq)` re-arms only while `seq == e.turnSeq`. Split the log: a `ValidateAction` refusal (`errActionRefused`, wrapped) warns; an apply failure is already an ended game | `turnclock.go` (LOW) | an accepted player move during the refusal window keeps the next seat's deadline |
| E4 | `NewState` copies each `Player` struct with `Cards` nil, so engines never share seat objects. First confirm by text search that nothing depends on pointer identity between lobby and engine seats: the lobby compares with `Player.Equal` | `state.go` `NewState` (LOW) | `-race` test: reading a finished engine's seat while the next engine deals |
| E5 | (D-6) `applyNextTurnLocked` keeps the running deadline when the seat on turn is unchanged, with a floor of 10 s. A leave by someone else no longer resets it. Misses count once per seat-turn, not per expiry, so gin's draw-then-discard costs one miss. New decision record #38 | `applyNextTurnLocked` (**CRITICAL**, 13 flows), `resolveTurnTimeout` | engine timeout tests: the same seat keeps its deadline; an absent gin player loses the seat after 3 turns, not 1.5 |

E5 is the riskiest item in the plan: every flow that plays a move passes through
`applyNextTurnLocked`. It gets its own commit, and its test lands before the change.

---

## PR 6 - Persistence and identity

| ID | Fix | Where / impact | Test |
|---|---|---|---|
| S4/R5 | (D-4) Migration `000006_username_ci`. It checks for existing case collisions first and fails loudly, naming them, rather than guessing. Then `CREATE UNIQUE INDEX idx_users_username_lower ON users (lower(username))`. `RegisterUserWithKey` checks `lower(username) = lower(?)`. The down migration drops the index | `migrations/`, `user.go` | integration: `alice` after `Alice` is `ErrUsernameTaken` |
| S5 | (D-5) `LoadOrRegisterUser` runs `db.ValidateUsername` **before** spending the registration budget and shows the validation message. `mapRegisterError` keeps folding only `ErrUsernameTaken`. Revise decision #17 | `auth.go` `LoadOrRegisterUser`, `mapRegisterError` (LOW) | `auth_test.go`: an invalid name does not consume the limiter and gets the reason |
| R2 | `getOrCreateGame` selects by slug first and upserts only on a miss, a name change or a soft-deleted row, so the hot path takes no row lock | `match.go` (**HIGH**, 2 callers) | integration: two concurrent casual records for one game do not serialize (a lock-wait assertion via `pg_locks`) |
| R3 | Migration: `CREATE INDEX idx_matches_ranked_created ON matches (created_at) WHERE ranked AND deleted_at IS NULL`. `repeatedPairCountLast24h` drives from `matches` in the window, then joins participants. Fix the "bounded by" comment | `match.go` (LOW) | `EXPLAIN` in an integration test uses the new index |
| R4 | A generation counter on the leaderboard cache. `DeleteAccount` bumps it, and `BestPlayers` stores only if it is unchanged since the query started | `user.go` `BestPlayers` (**HIGH**), `DeleteAccount` (LOW) | a slow query racing an erasure does not re-cache the erased row |
| R10a | `BestPlayers` misses go through `singleflight` (`golang.org/x/sync` is already an indirect dependency; promote it) | `user.go` | N concurrent misses run 1 query |
| R6 | Correct the "a pumped alt cannot carry rating" comment. Record the Sybil ceiling in decision #10 | `user.go`, `decisions.md` | - |
| R7 | Mark `000005` down as lossy in the file. `SetupTestDB` seeds one row before the down pass, so the data-dependent migrations run in CI | `migrations/`, `testutil/db.go` | the round trip fails today on a renamed game |
| R8 | `repository.go`: equal places are "scored as a draw", not "no rating moves" | comment | - |
| R9 | `lockPairing` sorts and dedupes by folded key, and uses the two-argument `pg_advisory_xact_lock(<app classid>, key)` so it cannot share a namespace with golang-migrate | `match.go` (LOW) | unit: two ids with the same folded key lock once |
| R10b | Delete `default:uuidv7()` from `User.ID` (decision #30); a nil `*uuid.UUID` serializes to NULL; `eraseUserLocked` updates the users row `Unscoped`, so an operator soft-delete does not block erasure | `users.go`, `uuid_sql.go`, `user.go` | `gorm_test.go`, `uuid_sql` test, integration erase of a soft-deleted user |

---

## PR 7 - Terminal UI

| ID | Fix | Where / impact | Test |
|---|---|---|---|
| T1 | `loadedMsg` carries the limit it asked for, and `exhausted` is computed from that. `cycleFilter` asks for `maxRowsPerPage` like `Init` | `leaderboard.go` `cycleFilter` (LOW) | at 80x24, a filter change on 100 players still pages |
| T3 | Cache banners by `(text, font)` with their measured size. `RenderFigureASCII` picks the first cached font that fits, so no size is in the key and the cap goes | `styles/common.go` `RenderFigureASCII` (**CRITICAL**, 9 flows; signature unchanged) | rendering one title at 61x6 sizes stores at most 3 entries; every fit test still passes |
| T5 | Optional `router.IdleExempt` interface. `Session` implements it as `Phase == Playing`, and the router exempts only then | `router.go` `Update` (LOW) | a finished game screen quits after 5 min idle |
| T6 | `refreshMsg` carries its model's id; a stale tick is dropped without re-arming. `ClockTickMsg` gets the same stamp | `join.go`, `views/game/layout.go` | esc then f twice leaves one refresh chain |
| T7 | The create form bounds max players by the selected game's `MinPlayers()` | `create.go` | Hearts cannot go below 4 |
| T8 | The lobby `Init` redirect also requires `!engine.IsFinished()` | `views/lobby/lobby.go` | a finished engine does not bounce the player back |
| T9 | `lobbyMsg` and `EventMsg` carry their source channel; a view drops messages from any other | `views/common.go`, `session.go` | a rebuilt lobby view runs one listener |
| T11 | `home.go` uses `views.GlobalRoute`, not its own copy of the key map | `home.go` | `home_test.go` |

---

## PR 8 - Structure (SOLID and smells)

Pure moves and deletions, after every behaviour fix above has landed. Each is one
commit so `git log --follow` stays readable.

- **T10.** `Session` owns `lastActionErr` (`Submit` stores it), the finished-enter
  leave and the esc handling (T2). The five views lose their copies. The pickers
  already share `components.GridPicker`, so they are not touched.
- **L9.** `internal/lobby` file moves, with no new types:
  - grace orchestration (`DisconnectPlayer`, `ResumePlayer`, `expireLeave`,
    `releaseHeldSeats`) into `disconnect.go`;
  - the finalizer registry and drain into `finalize.go`;
  - the browse cache into `browse.go`;
  - `kickableGuestLocked` into `lobby.go`;
  - the watcher (`watchGameLocked`, `handleBroadcasterEvents`, `requestFinalize`)
    into `watch.go`.
- **L8.** One rating fallback, missing or zero means the default, replacing
  `playerEloForGame` and `ratingFor`. Delete the dead `gameName` block in
  `browseEntry`. `ActiveGame` takes `RLock`. `startGameLocked` returns only `error`.
  Delete `SetCardGame` and `IsWaiting`, whose only callers are tests. **Keep**
  `IsReady` and `GameNames`: text search shows production callers in
  `views/lobby/lobby.go`, `create.go` and `join.go`.
- **L7.** `JoinLobbyByCode` returns the `*Lobby`, and `join.go` drops the second lookup.
- **S8.** Move `SessionTracker` into `internal/ssh/tracker.go`. Delete
  `SessionTracker.Disconnect` (its tests use `Release`) and the `Tracker == nil`
  fallback: `SetupServer` requires a tracker. Replace the package-global
  `sessionStates` with a registry `SetupServer` creates and hands to its middleware.
- **E6.** Delete `BoundEngine.Engine()` (2 test uses) and `Engine.StandingsIDs`
  (7 test uses, migrated to `StandingsWithPlaces`). **Keep** `WithState`,
  `CurrentPlayerID`, `TurnDeadline` and `MissedTurns`. They back 85 to 89 test call
  sites, and the `deadcode -test` gate treats tests as roots on purpose. Say so in a
  one-line comment on each.
- **E7.** Delete `Start`'s rollback: the lobby builds a new engine per attempt, so
  it has no caller. `cryptoIntN` becomes `rand.IntN` from `math/rand/v2`. The dealer
  seat is not a secret, and the error was unreachable.
- **httpapi.** Delete the second copy of the defaults (120 per minute and `*`):
  `config.Load` already sets and validates them. Tests pass explicit values.

---

## PR 9 - Documentation sweep

Most documentation changes ride with the PR that makes them true. This PR is what is
left:

- `CLAUDE.md` and `architecture.md` §4.3: the poker view no longer uses
  `BoundEngine.Engine()`, which E6 deletes; the lock order gains the tracker (S3).
- `architecture.md` §3.3: the seated-player redirect covers five routes, not three.
- `data-inventory.md`: stats-API spans are gone (P1), nginx client addresses are
  redacted (P2), a Backups row with its retention (P3), and the metrics test covers
  `http.server.*` (P4).
- `decisions.md`: new records for D-1 to D-8, the tracker lock order, and 000005
  being lossy; revised #10, #17 and #37.
- Refresh `codebase-map.md` §8 and the GitNexus counts in `CLAUDE.md`/`AGENTS.md`
  (`node .gitnexus/run.cjs analyze`, no `--index-only`, rewrites the generated block).
- Delete this file.

---

## Order and size

| PR | Items | Risk to watch | Rough size |
|---|---|---|---|
| 1 | S1, S2, E1, E2, P1 | `finishGameLocked` stays untouched | M |
| 2 | S3, L1-L6, R1, T2, T4 | `RemoveLobby`, `Session.Leave`, the new tracker lock order | L |
| 3 | P2, P3, D1-D12, S6, S7, S8-proxy | the nginx `resolve` syntax needs 1.28; test `docker compose up` locally | M |
| 4 | K1-K7 | `awardUncontested` | M |
| 5 | G1-G9, E3-E5 | `applyNextTurnLocked` (E5) gets its own commit | L |
| 6 | S4/R5, S5, R2-R10 | migration 000006 on real data with collisions | M |
| 7 | T1, T3, T5-T9, T11 | `RenderFigureASCII` fit tests at all three sizes | M |
| 8 | T10, L7-L9, S8, E6, E7, httpapi | pure moves; `detect-changes` should show no new flows | M |
| 9 | docs | - | S |

PRs 1 and 2 carry every high and medium finding and should go first. PRs 3 to 7 are
independent of each other and can run in parallel. PR 8 waits for all of them, so
its moves do not conflict with behaviour fixes.
