# Reading guide

An ordered walk through the Go code. It is **bottom-up**: dependencies in this
repo point inward, so the leaves come first and every later file only uses what
you already know. Nothing in step 7 needs a forward reference to step 9.

Read with the code open. Each step names the files and their size, what to
understand, one invariant to check while you read, and one test that teaches the
step better than prose can.

| Companion | Role |
|---|---|
| [`../README.md`](../README.md) | Run it, play it, the make targets |
| [`architecture.md`](architecture.md) | The canonical design document - contracts, topology, invariants |
| [`decisions.md`](decisions.md) | One record per non-obvious choice, with its context |
| [`onboarding.md`](onboarding.md) | Product story, annotated file tree, day-one tasks |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | How to add a game, test and commit norms |
| This file | The order you read them in |

**Pace.** Steps 1-4 are the engine and the games; that is the part with real
design in it and is worth two days. Steps 5-8 are the spine: persistence, the
lobby, the TUI, the SSH server. Steps 9-12 are the edges and the deployment, and
are mostly confirmation.

---

## Before you start: read the branch by commit

The history is written to be read. Commits are small and each behaviour change
ships with the test that fails without it.

```bash
git log --reverse --oneline main..<branch>   # the story in the order it happened
git show <sha>                               # one concern at a time
git log -S'symbolName' --format='%ad %h %s' --date=short --reverse | head -1
```

That last one is how [`decisions.md`](decisions.md) is dated: `-S` finds the
commit that introduced a string, and its message usually says why.

---

## 1. The leaves: cards and fan-out

**Files.** `internal/deck/card.go` (136), `deck.go` (102);
`internal/broadcaster/broadcaster.go` (158). Under 400 lines total and nothing
else in the repo is a prerequisite.

**Understand.**

*Three rank scales that must never be swapped* (`card.go`):

| Function | Answers | Ace | Courts | Used by |
|---|---|---|---|---|
| `RankValue` | comparison value, Ace high | 14 | 11/12/13 | poker, hearts |
| `RunOrder` | run position, Ace low | 1 | 11/12/13 distinct | gin rummy runs |
| `PipValue` | card cost | 1 | all 10 | gin rummy deadwood |

All three answer **0** outside Ace..King - including the Joker and the Uno
block - so a misuse loses loudly instead of quietly tying the ace. Standard ranks
are 1-based (`Ace Rank = iota + 1`) so the zero `deck.Card` is detectably empty
rather than the ace of spades; Uno's extra ranks start at `Zero Rank = iota + 20`
so neither block renumbers the other. `AllRanks` exists only so a `Rank`-keyed
map can be tested for exhaustiveness - a map has no compiler check, unlike a
switch.

*Copies, not aliases* (`deck.go`): `New`, `Cards`, `DrawN` and
`RemoveOne`/`RemoveEach` all clone. A hand that aliased the pile would mutate the
deck when a player played a card.

*`Shuffle()` returns nothing.* It seeds a `math/rand/v2` ChaCha8 generator once
per call from `crypto/rand`, which since Go 1.24 cannot fail - it aborts the
process instead. The error every caller used to plumb through was an unreachable
branch dressed as resilience.

*Latest-wins fan-out* (`broadcaster.go`): `Broadcast` holds the mutex, tries a
non-blocking send, and on a full 256-deep buffer drops the **oldest** and
enqueues the newest. Correct here because every event is a cue to re-read a
snapshot, not a delta that must be applied in order. `Subscribe` returns
`ErrClosed` / `ErrAtCapacity` rather than a closed channel: a closed channel is
indistinguishable from a finished game, so the caller could not tell "you will
never receive anything" from "the stream ended".

**Invariant to check.** No function in `internal/deck` returns a slice that
aliases the pile.

**Test.** `internal/deck/deck_test.go` `TestPile_DrawN_DoesNotAliasThePile`,
then `internal/broadcaster/broadcaster_test.go` `TestBroadcaster_LatestWins` and
`TestBroadcaster_ASlowSubscriberDoesNotBlockTheOthers`.

---

## 2. The engine core

**Files, in this order.** `internal/game/rules.go` (100, `Action`, `Rules` and the
optional hooks), `event.go` (86, `Event`, `EventType`, `EndReason` and their
`String` labels), `player.go` (44), `state.go` (75, `State` and the snapshots),
`turn.go` (54, `SetTurn`/`OverrideTurn`, `SeatAt`, `NextSeat`, `ValidateNextHand`),
`engine.go` (611) top to bottom - constructor, public API, then the locked helpers -
`turnclock.go` (227), `bound.go` (70), `registry.go` (63).

**Understand.**

*The contract.* `Rules` (nine methods) plus four optional hooks probed by type
assertion: `TurnTimeoutHandler` (`TimeoutAction`), `TurnDurationHandler`
(`TurnDuration`), `PlayerLeaveHandler` (`OnPlayerLeave` / `AfterPlayerRemoved`)
and `StandingScorer` (`StandingScore`, so equal results share a place).

*One mutex.* `Engine.mu` covers the clock fields **and** `*State`; `State` has no
lock of its own. Every `Rules` method and every `WithState`/`Frame` callback runs
with it held. So rules may mutate `*State` freely and must **never** call back
into the engine. That holds structurally today: `Rules` methods receive only
`*State`, which carries no engine handle.

*The order inside `SubmitAction`.* `checkTurnLocked` (closed, phase, turn,
`ValidateAction`) -> clear the missed-turn count -> `applyActionLocked`:
`ApplyAction` -> `AfterAction` -> broadcast `EventActionApplied` ->
`CheckWinCondition` -> `advanceTurnLocked` -> broadcast `EventTurnAdvanced`. The
timer's auto-play runs the same two halves, `submitTimedOutAction` minus the
clear. An error from `ApplyAction` or `AfterAction`
finishes the game as `EndReasonRulesError` with state possibly half-applied, so
**anything checkable up front belongs in `ValidateAction`**.

*The turn cursor.* `advanceTurnLocked` steps `CurrentTurn` on unless
`OverrideNextTurn` is set, then calls `settleTurnLocked`, which is the whole rule:
`OverrideNextTurn` wins and is cleared, else `CurrentTurn` stays where it is. A
leave settles without advancing (`settleAfterLeaveLocked`). Then `clampTurnLocked`
forces the cursor into `[0, len(Players))` with `SeatAt`, because a leave handler
can compute an index against the pre-removal seat count. Rules name the next seat
with `State.SetTurn` (on turn now, and after the action) or `State.OverrideTurn`
(after the action only).

*A turn that carries on keeps its clock.* `armTurnTimerLocked` keeps the running
deadline when the seat on turn and the turn length are unchanged - gin's draw then
discard, a re-armed auto-play, somebody else leaving - floored at
`minTurnRemaining` (10 s), and `resolveTurnTimeout` charges one miss per seat-turn
(`clock.missCharged`), not per expiry
([`decisions.md` #38](decisions.md#38-the-turn-clock-keeps-a-seats-deadline-while-its-turn-carries-on)).

*`clock.seq` is the fence.* The clock fields live together in one `turnClock`
struct under `Engine.mu`. `stopTurnTimerLocked` increments `seq`, and
`armTurnTimerLocked` calls `stopTurnTimerLocked` first - so every cursor change
invalidates timers already in flight. An auto-play carries the generation it was
computed for (`submitTimedOutAction` refuses on a mismatch with `errStaleTurn`),
and `removeIfStillIdle` re-checks the same generation under the lock before it
takes a seat. `resolveTurnTimeout` says what an expiry means as a `timeoutOutcome`:
ignored, auto-play or take the seat. That is why a player who acted as their clock ran out is neither
charged a miss nor double-played.

*Only accepted actions clear a miss.* `delete(e.clock.missed, playerID)` sits
after `checkTurnLocked`, not before. Clearing on any keypress would let a client
dodge removal by spamming rejected actions forever.

*The clock is opt-in.* No `TurnTimeoutHandler` means `armTurnTimerLocked` returns
without arming. There is nothing safe to play for an absent player, so they get
no clock rather than a silent removal.

*A rules panic ends one table.* `onTurnTimeout` runs on a `time.AfterFunc`
goroutine with nothing above it to recover, so `recoverRulesPanic` is a direct
`defer` there; `SubmitAction` has its own direct deferred `recover`. Both end the
game through `endOnRulesPanicLocked` -> `endGameLocked`, the one primitive every
end goes through, as `EndReasonRulesError` - without asking the
rules for standings, since a second panic inside a recover would take the process
down with every other table on it.

*`BoundEngine` is a façade, not a capability.* `Bind(engine, playerID)` submits
only as that player and `Frame` returns only that player's hand, the snapshot and
the clock in one lock hold. But `Frame`'s callback hands over the live,
unredacted `*State` - rendering a card table means rendering every seat. There is
no `Engine()` accessor; the value is that the default path is the safe one, and
what reaches the screen is the view's job.

*The feed is lent, not handed out.* `Engine.Subscribe`/`Unsubscribe`/`Dropped`/
`SubscriberCount` are the whole of it; there is no `Broadcaster()` accessor, so no
view can publish on a table's feed or close it
([`decisions.md` #51](decisions.md#51-the-engine-hands-out-subscriptions-not-its-broadcaster)).

*Seats are copies.* `NewEngine` copies each `*Player` with `Cards` cleared, so two
engines never share a seat, and `Start` has no rollback: the lobby builds a new
engine per attempt.

*The registry is fixed at construction.* `game.NewRegistry(mods...)` panics on a
half-declared module or a display name declared twice, so a wiring bug fails the
boot rather than the first table.

**Invariant to check.** Nothing inside a `Rules` method can reach the `Engine`.
Grep the five rules packages for `*game.Engine` and find nothing.

**Test.** `internal/game/engine_timeout_test.go`, all of it, but especially
`TestEngine_TurnTimeout_StaleTimerIsIgnored`,
`TestEngine_TurnTimeout_MovingBeforeRemovalKeepsTheSeat`,
`TestEngine_TurnTimeout_EventShipsWithTheMiss` and
`TestEngine_TurnTimeout_RulesPanicEndsOnlyThisTable`. Then
`internal/game/bound_test.go` `TestBoundEngine_HandBelongsToTheBoundPlayerOnly`.

---

## 3. One game vertically: Crazy Eights

**Files.** `internal/game/crazyeight/rules.go` (164), `state.go` (14), then
`internal/game/shed/shed.go` (157), what crazy eights and Uno share. Read them
against the interface you just read, method by method.

**Understand.** This is the smallest complete implementation of `Rules`. Match
rank or `CurrentSuit`; an eight is wild and carries the suit choice **inside**
`ActionPlayCard` (`ChosenSuit`, the same field name Uno uses), so one action makes
one state change and there is no half-applied "wild played, suit not yet named".
`ActionDrawCard` is always legal, so an exhausted board cannot soft-lock the turn
loop. The win check (`shed.HandEmptyOrAllPassed`) and the standings
(`shed.Standings`, `shed.Score`) come from package `internal/game/shed`, shared
with Uno, and so do the opening card (`shed.OpenDiscard`), the play check
(`shed.ValidatePlay`), the draw (`shed.DrawInto`, which reshuffles the discard
under the card in play when the stock runs out) and the leave (`shed.Leave`). The
per-game `State` embeds `shed.State`.

**Invariant to check.** `TimeoutAction` returns something this package's own
`ValidateAction` accepts. If it does not, the turn re-arms and the seat is taken
with only the 10-second floor and each refused expiry still costs a miss - the
seat goes early and the clock stops being a clock.

**Test.** `internal/game/crazyeight/helpers_test.go`
`TestSoak_TimeoutActionIsAlwaysLegal` - a `rapid`-driven soak
(`gametest.SoakTimeoutIsAlwaysLegal`, `internal/game/gametest/shed.go`, 284) that
plays random tables and asserts the auto-play move at every reachable state. Four
of the five games have one; poker has the deterministic equivalent
`TestRules_TimeoutAction_IsAcceptedByValidateAction`. `TestShedContract` in the
same file runs `gametest.RunShed`, the one suite crazy eights and Uno share.

---

## 4. The other four games, hardest last

Same shape each time. Read the rules package, then its view package later (step
7) if you want the loop closed.

| Game | Rules | Lines | What is new |
|---|---|---|---|
| Uno | `internal/game/uno/` | 257 + 51 + 33 (`rules`, `deck`, `state`) | Skip, Reverse and the draw cards name the next seat with `State.OverrideTurn`, so a reversed table honours `Direction` rather than the engine's +1 |
| Hearts | `internal/game/hearts/` | 353 + 167 + 143 (`rules`, `trick`, `state`) | A pass phase with its own clock (`TurnDurationHandler`), a `Phase` enum (`PhasePassing`, `PhaseTrickPlay`, `PhaseHandOver`), trick resolution, shooting the moon, `game.AnyScoreAtLeast` for the match target, and a leave that ends the match as `EndReasonInterrupted` (`State.Interrupted`) |
| Gin Rummy | `internal/game/ginrummy/` | 487 + 249 + 145 + 67 (`rules`, `melds`, `state`, `layoffs`) | A search problem; see below |
| Poker | `internal/game/poker/` | 289 + 232 + 205 + 76 + 417 + 251 + 131 (`rules`, `hand`, `betting`, `leave`, `streets`, `evaluator`, `state`) | Money, kept per player on one `Seat` in `State.Seats`; see below |

### Poker - the place where a bug is a payout

Read `streets.go` before `betting.go`. Four functions carry the argument:

- **`refundUncalled`** - the slice of the biggest bet nobody matched leaves the
  pot *before* side pots are cut. Only the single largest contributor can have
  one. Fold it into the live pot and you pay one player's uncalled chips to their
  opponents.
- **`buildSidePots`** and its `orphan` accumulator - dead money from folded
  players rides forward and joins the last live layer, rather than vanishing when
  a layer has no eligible player.
- **`awardUncontested`** - a fold-out pays like a showdown: `refundUncalled`, then
  everything else, folders' dead money included, to the winner
  ([`decisions.md` #40](decisions.md#40-fold-out-dead-money-goes-to-the-winner)).
- **`validateRaiseTo`** (`betting.go`) with `largestCallableBet` - a raise past
  what any opponent can call is **refused**, not staged and handed back at
  showdown. `checkBettingReopened` refuses a raise from a player facing the
  uncalled part of a sub-minimum all-in, unless the short all-ins since they last
  acted (`Seat.LastBetLevel`) add up to a full raise
  ([#41](decisions.md#41-short-all-ins-that-add-up-to-a-full-raise-reopen-the-betting)).
  `RaiseBounds` is the one legal band, and the view builds its prompt from it.

After an action or a leave, `resolveAfterChange` moves the hand on, and a street
that cannot be dealt unwinds through `settleOrUnwind` on either path.

`checkChipConservation` is the tripwire, not the enforcement: by the time it
fires the hand is closed out, so the value is the log line.

**Test.** `internal/game/poker/streets_test.go`, the `handLedger` property test -
it remembers what each player was worth when the hand was dealt and asserts over
`rapid`-generated hands that nobody loses chips nobody matched
(`checkNoUnmatchedLoss`).

### Gin Rummy - the place where a bug is an arrangement

`melds.go`: `bestSplitBy` searches every disjoint set of candidate melds
(`uint16` index masks, so `maskBits = 16` bounds the hand) and keeps whichever
split a scoring function rates lowest. Two callers:

- `bestMeldSplit` scores raw deadwood - what a knocker gets.
- **`bestMeldSplitAgainst`** scores deadwood *after laying off onto the knocker's
  melds* - what a defender is entitled to. Minimising raw deadwood first can
  strand a card that would have attached to a knocker meld, which overcharges the
  defender and can cost them an undercut they had earned.

A hand longer than 16 cards returns everything as deadwood rather than panicking,
because `TimeoutAction` runs inside `time.AfterFunc` with no recover.

**Test.** `internal/game/ginrummy/melds_test.go` `FuzzBestMeldSplit` - the
search's contract on a hand nobody designed.

---

## 5. Rating and persistence

**Files, in this order.** `internal/elo/elo.go` (160); then `internal/db` -
`repository.go` (79, the interfaces), `games.go` (25, `GameRef`), `users.go`
(106), `matches.go` (27), `uuid_sql.go` (69), `errors.go` (17), `migrations.go`
(9); then `internal/db/migrations/*.sql` **in number order**; then
`internal/repository/connect.go` (53), `match.go` (634) and `user.go` (404).

**Understand.**

*`internal/db` defines the contract, `internal/repository` implements it.*
Nothing outside `cmd/server` (the composition root) may import
`internal/repository`; the error sentinels callers compare against live in
`internal/db/errors.go`. That rule is enforced by
`depguard` in `.golangci.yml`, not by memory. `repository.Connect` takes a DSN and
pool settings, so `internal/db` imports nothing of ours. The account interface is
three consumer-sized ones - `db.Authenticator` (the SSH login), `db.Profiles` (the
profile screen) and `db.Leaderboard` (the boards and the stats API) - and
`db.UserRepository` is only their union, for the composition root
([`decisions.md` #50](decisions.md#50-consumers-ask-for-the-smallest-repository-interface)).

*Identity is the slug.* `db.GameRef{Slug, Name}` carries both halves together:
`Slug` is what a rating hangs off (`games.slug`, read first and upserted
`ON CONFLICT (slug)` only on a miss, a rename or a soft-deleted row), `Name` is a
display column refreshed whenever it differs. Migration `000005_game_slug`
exists because renaming a game used to create a second `games` row and orphan
every ranking on the first.

*Account ids are UUIDv7.* `users.id UUID PRIMARY KEY DEFAULT uuidv7()` in
`000001_init.up.sql` - Postgres 18, time-ordered, not `gen_random_uuid()`. The Go
side uses the **stdlib** `uuid` package (`import "uuid"`, no module dependency);
`db.uuid_sql.go` registers a GORM serializer named `stduuid` because
`uuid.UUID` has no `database/sql` Scanner or Valuer of its own.

*Erasure anonymises.* `users.username` is `VARCHAR(40)` with a CHECK that allows
either a chosen name of at most 16 characters or exactly
`^deleted_[0-9a-f]{32}$`. `db.AnonymisedUsername` produces the second form, and
`ValidateUsername` refuses the `deleted_` prefix so nobody can squat it.

*Read `updateRankings` top to bottom*, then `DeleteAccount` /
`eraseUser`. The order inside the ranked transaction matters: seat advisory
locks first (sorted by folded key and deduplicated, so two overlapping tables
cannot deadlock), drop the seats erased meanwhile (`unerasedSeats`), revive
soft-deleted ranking rows and seed only the rows about to be written,
`SELECT … FOR UPDATE`, then the maths. An interrupted match
(`FinalizeInterruptedMatch`) writes only the leavers' rows, and only downwards.
Erasure takes the same per-seat lock.

**Invariant to check.** Every Elo write happens inside one transaction that holds
a two-int4 `pg_advisory_xact_lock` per seat, taken in folded-key order.

**Test.** `internal/repository/user_test.go`
`TestUserRepository_BestPlayers_FiltersBySlugAfterRename` for slug identity, and
`internal/db/schema_test.go` `TestSchemaNullabilityMatchesStructs`, which derives
what the SQL must guarantee from the GORM structs so a new scalar field fails CI
until it is pinned `NOT NULL`.

---

## 6. The lobby

**Files, in this order.** `internal/lobby/errors.go` (28, the sentinels callers
match on), `manager.go` (404), `disconnect.go` (174), `lobby.go` (626) -
specifically `CreateLobby`'s table and `startGameLocked` - `watch.go` (100) for
`watchGameLocked`, `handleGameEvents` and `requestFinalize`, `finalize.go` (242),
`browse.go` (193), `player.go` (27).

**Understand.**

*The lobby is the only thing that knows both a table and a database exist.* The
engine broadcasts; the lobby's watcher goroutine listens and persists.

*The finalize snapshot is taken when the game **starts**.* `watchGameLocked`
builds `finalizeRequest{lobbyCode, game, isRanked, startedAt}` at subscribe time.
By the time the game ends the lobby may have reopened and been reconfigured, and
the result would be written under the new ranked flag, the new game and the next
hand's start time.

*The 90-second hold is released at three points, not one.* Its timer, yes - but
also `releaseHeldSeats` when the hand ends (a still-armed hold keeps the player
out of every other table and this one unable to reach all-ready) and
`BeginShutdown` (they are not coming back to a process that is exiting).

*A ready is consent to the table as it was.* A settings change or any roster
removal clears every ready flag; a join does not.

*Lock order is manager (`m.mu`) then lobby (`l.mu`), never inverted* - below the
ssh layer's `SessionTracker.mu`, which comes first. The browse
cache uses an `atomic.Bool` dirty flag specifically so a `Lobby` can mark it while
holding its own lock without reaching for the manager's.

**Invariant to check.** A mid-game drop calls `DisconnectPlayer`, never
`LeaveLobby`. A waiting-lobby seat and any seat during shutdown still leave
immediately.

**Test.** `internal/lobby/concurrency_test.go` - the whole file is the lock
order and the grace state machine under race.
`TestConcurrent_GraceExpiryRacesReconnect` and
`TestConcurrent_MatchEndsDuringTheGraceWindow` are the two that teach most.
Then `internal/lobby/finalize_test.go`
`TestFinalize_RulesErrorIsRecordedWithoutElo`.

---

## 7. The terminal UI

**Files, in this order.** `internal/tui/router/router.go` (250),
`internal/tui/app.go` (102), `internal/tui/views/common.go` (160),
`internal/tui/styles/common.go` (221) - `layoutHeights` above all -
`internal/tui/views/gameview/session.go` (294), `layout.go` (357), `state.go` (89),
`zones.go` (133), `frame.go` (105), `handover.go` (51), `choice.go` (68), then
`internal/tui/views/gameview/poker/model.go` (219) for `buildSeats`. Skim the
rest: `views/home`, `views/lobby`, `views/leaderboard`, `views/profile`,
`styles/theme.go`, `components/card.go`, and `internal/tui/tuitest/tuitest.go`
(64), the fit sizes and key helpers the view tests share.

**Understand.**

*The router owns navigation and closing.* Views hold a `router.GlobalContext` by
value. Routes are a typed `router.Route`, navigation is a message (`ChangeViewMsg`,
built by `router.Navigate`), not a call, and `Goto` closes
the outgoing view if it implements `router.Closer`. Any view holding a
subscription **must** implement it, or a listener goroutine parks and a
subscriber slot burns until the engine closes.

*One function owns the fit arithmetic.* `layoutHeights(w, h, header, footer)`
wraps the header and footer to `innerWidth` **before** measuring them, and
returns `hContent = max(innerHeight - hHeader - hFooter, 0)`.
`AvailableContentHeight` is that minus `opticalPadding` (2, the blank lines the
renderer appends); `Theme.RenderMainLayout` calls the same function, so the
promise and the render cannot drift. Every screen is tested at
`{MinWidth, MinHeight}` = 64x20, plus 80x24 and 120x50.

*`gameview.Session` is the view baseline.* It owns the engine binding,
`NewSession`'s subscribe, `Init` (the feed listener and the first clock tick), the
whole `Update` loop (`HandleFrame`), the last rejected move (`ActionErr`), the hand
cursor,
`IdleRemoved`, the forfeit prompt (`HandleLeaveKey` / `LeaveConfirmScreen`: esc
mid-game asks first), `IdleExempt` (a live table is spared the router's idle quit),
`Leave` and `Close`. Every feed message carries the channel that delivered it
(`EventMsg.Source`, `ClockTickMsg.Source`), and a view drops one armed by another.
`BaseState.Opponents` runs clockwise from the hero's left, the order `SplitZones`
lays the table out in. The hand-over screen (`RenderHandOver`) and the wild-card
picker (`ChoicePicker`) are shared too. A new game writes its rules rendering
and nothing else. Read per-game state through `Sync`'s callback and **copy**
anything you keep - the `*State` you get is live and unredacted.

*`buildSeats` is the one redaction point.* Poker reads whole-table state through
`Sync`'s callback because a showdown needs every seat, and so redaction becomes
the view's named, testable job.

**Invariant to check.** Nothing kept after `Sync` returns aliases engine state
(`maps.Clone`, `HandResult.Clone`).

**Test.** `internal/tui/styles/layout_test.go`
`TestRenderMainLayout_HonoursAvailableContentHeight`, and
`internal/tui/views/gameview/poker/redaction_test.go`
`TestBuildSeats_RevealsHoleCardsOnlyWhereTheRulesDo`.

---

## 8. The SSH server

**Files.** `internal/ssh/auth.go` (93) first, then `internal/ssh/tracker.go` (93)
for `SessionTracker`, then `internal/ssh/server.go` (633) in this order:
`NewServer` -> `limitSessionChannels` -> `sessionLifecycle` -> `sessionModel` ->
`releaseSession` -> `recoverSession` / `reportingModel`.

**Understand.**

*Identity is the key fingerprint.* Any public key is accepted;
`SessionFingerprint` turns it into `SHA256:…` and `LoadOrRegisterUser` resolves
it. The SSH login name becomes the username on first connect only.
`allowRegister` is consulted **only** on the `user == nil` branch, after
`db.ValidateUsername`, so neither a returning player nor a typo spends the
registration budget (`REGISTRATION_LIMIT` / `REGISTRATION_WINDOW`).

*"Taken" is uninformative on purpose.* `mapRegisterError` folds
`db.ErrUsernameTaken` into `ErrNameUnavailable`, because a distinguishable message
turns the login banner into a "does this account exist" oracle. An invalid name is
a fixed rule, not a fact about other accounts, so the player is told why.

*Middleware runs last-first.* The slice in `wish.WithMiddleware` executes in
reverse, so `sessionLifecycle` is listed **last** to be outermost.
`charm.land/ssh` recovers on every goroutine it spawns, so `recoverSession` is a
second layer - and it must stay a **direct** `defer`, because a `recover()`
inside a function called *by* a deferred function returns nil.

*Per-session state lives in a per-server `sessionRegistry`*, never on
`s.Context()`, which is per-**connection** and shared by every channel. The
per-connection channel cap lives in the `session` channel handler and refuses
before `Accept`.

*A second session displaces the first* and closes its connection, outside the
tracker lock - a wedged peer must not hold every other account's `Connect` behind
it. Only the owning generation may free the slot and the seat.

**Invariant to check.** `releaseSession` gives up the lobby seat and the tracker
slot as one step under the tracker lock (`ReleaseWith`), and `tui.ResumeSeat` runs
only once `Connect` has handed the session its slot, so a reconnect can neither
land between the two nor have a refused session cancel its grace timer.

**Test.** `internal/ssh/lifecycle_test.go`
`TestReleaseSession_GivesUpTheSeatBeforeTheSlot`,
`TestSessionState_IsPerChannelNotPerConnection`,
`TestSessionTracker_ReleaseWithHoldsOffTheReconnect` and
`TestSessionTracker_ConnectClosesTheDisplacedSession`; then
`internal/ssh/teardown_race_test.go` and `channel_test.go`.

---

## 9. The edges

**Files.** `internal/httpapi/httpapi.go` (313), `internal/ratelimit/limiter.go`
(105) + `netkey.go` (24), `internal/observability/otel.go` (139) +
`metrics.go` (198), `internal/config/config.go` (349).

**Understand.** The stats API is read-only, unauthenticated, and returns nothing
the in-game leaderboard does not already show any visitor - that is what makes it
safe. `API_TRUST_PROXY` defaults to **false**; a directly exposed listener that
trusts `X-Forwarded-For` can be evaded by forging it, so the unsafe direction has
to be chosen explicitly, and with `PROXY_TRUSTED_CIDRS` set the header is
believed only from the proxy's networks. The API makes no spans. Both limiters key
on `ratelimit.NetKey`, which collapses IPv6 to its /64, because one customer is
routinely handed 2^64 addresses. `httpapi.NewServer` refuses to build without its
session counter, lobby counter and leaderboard (`ErrMissingDeps`) rather than
serving zeros. `config.Load` refuses an unknown `ENV` and, in production, a
`DB_SSLMODE` that can fall back to plaintext; it reads booleans with
`strconv.ParseBool`, so `PROXY_PROTOCOL=off` fails the boot
([#53](decisions.md#53-boolean-environment-values-are-parsed-strictly)), takes
`RATE_LIMIT_WINDOW` as a Go duration and refuses the old `RATE_LIMIT_WINDOW_MS`
by name ([#52](decisions.md#52-rate_limit_window-is-a-duration-and-the-old-name-fails-the-boot)),
and reports every bad variable at once. The session gauge reads
`SessionTracker.Count` through `observability.RegisterSessionGauge`.

**Invariant to check.** No metric attribute carries personal data.

**Test.** `internal/observability/metrics_test.go` collects every instrument and
fails if any attribute key falls outside a fixed allow-list - enforced, not
asserted. `internal/catalog/metrics_test.go`
`TestAll_LobbyAndViewShareOneGameTypeLabel` pins the `game_type` label to the
catalog slug ([#49](decisions.md#49-the-game_type-metric-label-is-the-catalog-slug)).
Then `internal/ratelimit/netkey_test.go`
`TestNetKey_AdjacentAllocationsDoNotShareAKey`.

---

## 10. The composition root

**Files.** `internal/catalog/catalog.go` (74), then `cmd/server/main.go` (421).

**Understand.** `catalog.All` is the only place a game is declared, and each
entry embeds a `game.Module` (name, slug, rules `Factory`) **and** carries the
TUI view constructor, which is handed the slug. `catalog.NewRegistry` builds the
`game.Registry` from it; `internal/tui/app.go` registers routes from it. `main.go`
now reads as a summary of everything above: `config.Load` -> OTel
(`observability.Setup`) -> `repository.Connect` -> repositories ->
`lobby.NewManager` -> tracker -> `ssh.NewServer` (with `catalog.NewRegistry`) ->
`httpapi.NewServer` -> `serve`, with the `defer`s ordered so LIFO unwinding drains match writes before
closing the DB handle they write through.

**Invariant to check.** A catalog entry without a view does not compile past
`catalog_test.go`; but copying an entry and changing only the rules **does**
compile, so the pair is kept in lockstep by hand.

**Test.** `internal/catalog/catalog_test.go` `TestAll_EntriesComplete`,
`TestAll_NamesArePersistedAndFrozen` and `TestAll_SlugsMatchTheMigrationBackfill`;
then `close_test.go` `TestAll_CloseReleasesTheEngineSubscription`, one table over
`catalog.All` that proves every view gives its subscriber slot back.

---

## 11. The deployment

**Files.** `compose.yaml`, `internal/config/nginx.conf`,
`internal/config/alloy/config.alloy`, `internal/config/loki/loki.yaml`,
`internal/config/tempo/tempo.yaml`, `internal/config/prometheus/prometheus.yml`,
`internal/config/grafana/provisioning/**`.

**Understand.** Exactly two ports are published to the world, 22 and 80, both on
nginx; Grafana is the single exception on `127.0.0.1:3000`. The comments in
`compose.yaml` and `nginx.conf` state why each one is what it is - PROXY
protocol, the `$client_net` map duplicated across `stream` and `http` because the
two contexts cannot share one, the `log_format privacy` with no `$remote_addr`,
and the commented-out `443` block with the reason TLS is not terminated yet.

**Invariant to check.** `:6969` and `:6970` are `expose`, never `ports`.

**Test.** `.github/workflows/test.yml`, the `compose` job: `docker compose
config` plus `nginx -t`, plus a step that asserts Grafana publishes nothing but
`127.0.0.1:3000`.

---

## 12. Now re-read the policies

**Files.** [`data-inventory.md`](data-inventory.md),
[`privacy.md`](privacy.md), [`SECURITY.md`](SECURITY.md).

Read each sentence and ask: **does the code I just read make this true?** These
three documents are the ones that go stale silently, because nothing compiles
against them. The data inventory cites a file for every claim precisely so it can
be re-checked rather than believed.

---

## Tooling

You can read all of this by hand. These cut the search tax.

### The layer rules are a linter

`.golangci.yml` `depguard` is the authoritative statement of who may import what:

| Rule | Refuses |
|---|---|
| `repository-only-from-root` | anything but `cmd/server` and `internal/repository` importing `internal/repository` |
| `game-is-pure` | `internal/game/**` (bar the `gametest` test support) importing anything outside its allow-list: the standard library, `deck`, `broadcaster`, `game/**` and `uuid` ([#54](decisions.md#54-the-game-packages-import-from-an-allow-list)) |

If you are unsure whether a dependency is allowed, add it and run `make lint`.

### Go's own tools

```bash
go doc ./internal/game                 # package overview from the comments
go doc ./internal/game Rules           # one symbol
go list -deps ./cmd/server | sort -u   # everything the binary pulls in
go list -f '{{.ImportPath}} {{.Imports}}' ./... | grep lobby
```

### GitNexus

[GitNexus](https://github.com/abhigyanpatwari/GitNexus) indexes the repo into a
local knowledge graph and exposes it over MCP, so an agent can ask "what calls
`finalizeFinishedGame`?" instead of grepping.

```bash
node .gitnexus/run.cjs analyze --index-only     # re-index after a large merge
node .gitnexus/run.cjs impact finalizeFinishedGame --direction upstream --repo .
node .gitnexus/run.cjs context DisconnectPlayer --repo .
```

**Two seams it cannot see.** Channel receives and `time.AfterFunc` do not become
CALLS edges, so a `no_path` result across a broadcaster or a timer means "dynamic
hop", not "dead code". The two that matter:

```
Engine.endGameLocked --broadcast--> Lobby.handleGameEvents
  -> requestFinalize -> Manager.finalizeFinishedGame -> MatchRepository

Engine.armTurnTimerLocked --time.AfterFunc--> onTurnTimeout
  -> resolveTurnTimeout -> submitTimedOutAction / removeIfStillIdle
```

### Habits that work on this repo

1. Search for the **type name** (`finalizeRequest`, `disconnectGrace`,
   `trackedSession`, `handLedger`) rather than the English phrase.
2. Read one vertical slice end to end before reading a second game package.
3. Tests are the documentation of record for behaviour. The comment above a test
   function usually states the failure mode it was written for.

---

## "Where is X?"

| Question | Start here |
|---|---|
| How does a key become a user? | `ssh/auth.go`, `repository/user.go` |
| Second SSH session for the same account? | `ssh/tracker.go` `SessionTracker.Connect` - it displaces |
| Mid-game wifi drop? | `lobby/disconnect.go`, `DisconnectPlayer`, `ResumePlayer` |
| Who writes Elo? | `lobby/finalize.go` -> `repository/match.go` |
| Why did Elo not move? | shutdown / `EndReasonRulesError` / `EndReasonAbandoned` / `EndReasonInterrupted` (only leavers move) / pairing damp / provisional |
| Soft-deleted ranking broke finalize? | `seedRankingRows` revive |
| How is a game registered? | `catalog/catalog.go` `All`; `game.NewRegistry` panics on a duplicate |
| What do crazy eights and Uno share? | `game/shed/shed.go`; their shared tests in `game/gametest/shed.go` |
| Who acts next? | `game/engine.go` `advanceTurnLocked` / `settleTurnLocked`; rules set it with `State.SetTurn` / `OverrideTurn` (`game/turn.go`) |
| Turn auto-play / idle kick? | `game/turnclock.go` (`armTurnTimerLocked`, `resolveTurnTimeout`, `timeoutOutcome`) |
| Why is a game split in two on a dashboard? | it should not be: `game_type` is the catalog slug everywhere (`catalog/metrics_test.go`) |
| Why did esc not leave the table? | `views/gameview/session.go` `HandleLeaveKey` - mid-game it asks first |
| TUI reading engine state? | `gameview.Session.Sync` -> `BoundEngine.Frame` |
| Where is state redacted for the screen? | `views/gameview/poker/model.go` `buildSeats` |
| Browse list sorting? | `lobby/browse.go` |
| Lock order? | tracker -> manager -> lobby -> engine; `State` has no lock |
| Rate limiting on IPv6? | `ratelimit/netkey.go`; fail-closed in `ssh.netKeyFor` |
| Why can I not register? | `REGISTRATION_LIMIT` / `REGISTRATION_WINDOW` (`config/config.go`), `ssh.allowRegistration`; `db.ValidateUsername`; `mapRegisterError` |
| Add a migration? | `make migrate-create`, files under `db/migrations/` |
| Why is the game row keyed on a slug? | `db/games.go` `GameRef`, migration `000005` |
| Why are account ids UUIDs? | `db/users.go`, `db/uuid_sql.go`, migration `000001` |
| Why did my screen not fit? | `styles/common.go` `layoutHeights` |
| Where does an account get erased? | `repository/user.go` `eraseUser` |
| Where do poker chips get refunded? | `poker/streets.go` `refundUncalled`, `awardUncontested` |
| What does a connect log record? | `client_net`, the /64 - `ssh/server.go` `clientNet` |
| Who may import what? | `.golangci.yml` `depguard` |

---

## Prove you understand it

Against a local server (`make build`, Postgres 18, `PROXY_PROTOCOL=false` for a
bare `ssh` client, or `./scripts/dev-session.sh`):

1. Register two keys, play a casual Crazy Eights table to the end, and find the
   `matches` row with zero Elo deltas.
2. Start a ranked hand, kill one SSH client mid-hand, reconnect within 90
   seconds. Confirm resume, not forfeit.
3. Connect a second session for the same key while the first is half-open.
   Confirm displacement, not "already connected".
4. Force a rules-error end in a test (`internal/lobby/finalize_test.go`) and
   confirm a ranked lobby takes the casual record path.
5. On paper, write the lock order for `Manager.Kick` and for
   `Manager.BrowseLobbies`, and say why the browse cache uses an atomic.
6. Add a sixth game's catalog entry with the rules of an existing one and a new
   slug. Confirm the route appears and the engine needed no change.

---

## Keeping this guide honest

When you change a contract - `SessionTracker`, finalize ownership, the grace
state machine, the `Frame` signature, the catalog shape, a repository interface -
update in this order:

1. [`../CLAUDE.md`](../CLAUDE.md) (agents load it first)
2. [`architecture.md`](architecture.md)
3. [`decisions.md`](decisions.md), if the *reason* changed rather than the code
4. the step above that covers the path
5. [`changelog.md`](changelog.md), if a player or an operator would notice

`architecture.md` owns contracts and invariants. `decisions.md` owns the reasons.
`onboarding.md` owns the product story and the annotated tree. `README.md` owns
commands and configuration. This file owns the order.
