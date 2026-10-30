# Contributing to tty cards

The project is open source so it can outlive whoever is currently maintaining it.
That goal drives most of the rules below: every one of them exists so the next
person can change the code without reading all of it first.

Start with [`README.md`](../README.md) to run it, then
[`architecture.md`](architecture.md) for the contracts you must not break.

Security bugs do **not** go in an issue - see [`SECURITY.md`](SECURITY.md).

## Setup

Go **1.27.3** (see `go.mod`; CI pins the same). PostgreSQL 18 for anything that
touches the database. Docker for the integration suite.

```bash
cp .env.example .env
export DB_DSN='postgres://postgres:PASSWORD@localhost:5432/terminal_card?sslmode=disable'
make install-tools   # golang-migrate
make migrate-up
make test-short
make build
```

### Linting locally

CI runs golangci-lint **v2.15.0** via `golangci/golangci-lint-action@v7` with
`install-mode: goinstall` - prebuilt binaries lag the module's Go version and
refuse `go 1.27.x`. Match it locally the same way:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
~/go/bin/golangci-lint run      # or `make lint` if it is on your PATH
```

### What each check guards

- **`make lint`** - golangci-lint over the default and the `integration` build:
  correctness, style, the size gates below, and `depguard`'s layer rules (nothing
  but `cmd/server` imports `internal/repository`; `internal/game` imports from an
  allow-list).
- **`make deadcode`** - code reachable from neither `main` nor any test. Tests count
  as roots, so a test-only hook is fine; a helper nothing calls is not.
- **`make semgrep`** - the `CLAUDE.md` invariants a linter cannot express, in
  `.semgrep/tty-cards.yml`: the turn cursor moves only through `SetTurn` /
  `OverrideTurn`; TUI code never calls `Engine.WithState`, `SubmitAction` or the
  two-argument `Frame`; rules code never names, builds, binds or drives an
  `Engine`, and game code logs through `State.Logger()`, never bare `slog`; recover helpers are deferred directly; no client address
  on a span or metric, and none in an info/debug log; the DSN and DB password
  never reach a log, print or error; a `Subscribe` error is never dropped; boolean
  env values go through `strconv.ParseBool`. Every rule has `// ruleid:` / `// ok:`
  cases in `.semgrep/tty-cards.go`, and the target runs them before scanning, so a
  rule that stopped matching fails rather than passing as a clean tree. A new rule
  ships with its cases and at zero findings on the tree.
- **`make cover`** - the whole suite, unit and integration, with `-race` and
  `-coverpkg` over the production packages, failing under 95.0% of statements.
  `internal/testutil`, `internal/game/gametest`, `internal/tui/tuitest` and
  `cmd/loadtest` are left out of the denominator: test support and a measurement
  tool, not the product. `go tool cover -func=coverage.out` shows where the gaps
  are. CI's integration job runs exactly this.

## Adding a game

Five games exist; a sixth touches the engine in no way at all. Crazy Eights is
the smallest reference implementation, Poker the most demanding.

### 1. Rules - `internal/game/<name>/`

Implement `game.Rules` and pin it:

```go
var _ game.Rules = (*Rules)(nil)
```

`TimeoutAction` is one of its methods, not an option: it is the move the turn clock
plays for a player who stopped playing, and without it a player who walks away
would freeze the table. `Standings` returns places, not just an order: rank every
seated player, then every leaver below them, and let a draw share a place within
either group but never across it. A game ranked on one number gets all of that
from `game.RankByScore(state, score)`.

Add the optional interfaces you actually need, each with its own compile-time
assertion:

| Interface | When |
|---|---|
| `game.PlayerLeaveHandler` | mid-hand disconnects change your state (`OnPlayerLeave` before removal, `AfterPlayerRemoved` after seat indices shift) |
| `game.TurnDurationHandler` | one phase needs longer than 30s (hearts' pass, the between-hands deal) |

Two rules that have each cost a bug:

- **`TimeoutAction` must return something your own `ValidateAction` accepts.**
  Otherwise the turn re-arms with only the 10-second floor left and every refused
  expiry still costs a miss, so the absent seat is taken early. Gin rummy's
  `autoDiscard` skips the card the upcard rule forbids for exactly this reason.
- **Anything checkable up front belongs in `ValidateAction`.** An error from
  `ApplyAction` ends the game as `EndReasonRulesError`, with
  state possibly half-applied and the match recorded unrated.

`AfterPlayerRemoved` returns a `game.LeaveOutcome`. If one seat leaving means the
match cannot go on (hearts is four-handed or nothing), return
`game.LeaveInterruptsMatch`: the game then ends `EndReasonInterrupted`, and finalize
charges only the leavers.
If a lone remaining seat still has a hand to play out (poker's all-in leaver still
contests the pot), return `game.LeaveKeepsHandOpen`, or the engine hands that seat a
forfeit win; otherwise `game.LeaveSettled`.
Deal the opening hands with `State.Deal`. A shedding game should reuse package
`internal/game/shed` (embed `shed.State`, and call `shed.ValidatePlay`,
`shed.DrawInto`, `shed.OpenDiscard`, `shed.HandEmptyOrAllPassed`, `shed.Leave`,
`shed.Standings`) rather than copy it.
Name who acts next with `State.SetTurn` / `State.OverrideTurn`, never by writing
`CurrentTurn` and `OverrideNextTurn` by hand, and keep a seat on turn for the rest
of the same turn (gin's draw before its discard) with `State.ContinueTurn`, the one
way a turn keeps its clock; refuse a foreign action with
`game.ErrUnknownAction` - from `ValidateAction` and, through a `default:` arm, from
`ApplyAction` too, so a move added to one switch and forgotten in the other is not a
silent no-op - and a move between hands with `game.ErrHandOver`, and gate
the between-hands deal with `game.ValidateNextHand` and clock it with
`game.NextHandTurnDuration`. `internal/game` imports from an
allow-list (`depguard` `game-is-pure`: stdlib, `deck`, `broadcaster`,
`internal/game/...`, `uuid`), so a rules package that needs anything else is a
decision to discuss first.

Read `State.Extra` through one per-package `extraOf(state) *State` that asserts
without `ok`, as every game does: the wrong type there is a wiring bug.
A helper a view calls inside `Frame` keeps the `ok` form, because `Frame` does not recover.

Use `internal/deck` rather than writing your own: `RankValue` / `RunOrder` /
`PipValue` answer three different questions and must not be swapped, and
`RemoveOne` / `RemoveEach` never alias.

### 2. View - `internal/tui/views/gameview/<name>/`

Expose `New(global router.GlobalContext, table *game.BoundEngine, slug string) tea.Model`
(the seat the lobby bound to the session's player - a view never holds a
`*game.Engine`, and semgrep fails one that does), keep the model itself unexported,
embed `gameview.Session` (built with `gameview.NewSession(global, table, slug)`), and
implement your rules rendering and nothing else. The slug is the catalog's, and it is
the `game_type` label your view's metrics carry, the same one the lobby's carry for
this game. Session already owns subscribing, `Init` (the feed listener and the
turn-clock tick - do not write your own), the `Update` loop (`HandleFrame`), the last rejected move
(`ActionErr`), the hand cursor, the forfeit prompt, leaving, idle removal and the
idle-quit exemption, and `Close`. A suit or colour choice is `gameview.ChoicePicker`
(crazy eights and uno share it); a between-hands result screen is
`gameview.RenderHandOver(global, gameview.HandOver{...})` (hearts, gin rummy and
poker share it). Draw opponents from `BaseState.Opponents`, which already runs
clockwise from the hero's left. Wire it the way `crazyeight` does:
- Your key handler closes any prompt of your own on esc first, then calls
  `m.HandleLeaveKey(key)` before its own bindings and returns if it consumed the
  key - mid-game esc must ask before it forfeits.
- `View` returns `m.LeaveConfirmScreen()` before anything else when it is armed.
- Submit through `m.Submit(action)`, which keeps the result in `m.ActionErr`, and
  render `m.ActionErr` in the hero band (`gameview.RenderHeroBand`).

Copy anything you keep past `Sync` (`maps.Clone`, `HandResult.Clone`), in one pure
`readTable(state, ...)` that returns a value you assign once `Sync` returns, as
poker, hearts and gin rummy do. The
`*State` you get is live and unredacted - filtering what the player may see is
your job, and it should be a named function so a reviewer can find it. Where
what a seat may see is a rule of the game, that function lives in the rules
package (`poker.SeatsFor`), not the view.

### 3. Register both together - `internal/catalog/catalog.go`

One entry in `Games`. This is the only registration point; `cmd/server/main.go`
and `internal/tui/app.go` both read it.

```go
{
    Name:       "My Game",
    Slug:       "my_game",
    MinPlayers: mygamerules.MinPlayers,
    MaxPlayers: mygamerules.MaxPlayers,
    Factory:    func() game.Rules { return &mygamerules.Rules{} },
    View:       mygameview.New,
},
```

`catalog.Entry` embeds `game.Module` (`Name`, `Slug`, `MinPlayers`, `MaxPlayers`,
`Factory`) beside `View`; export the seat range as `MinPlayers` / `MaxPlayers`
constants from the rules package. `catalog_test.go` fails on a missing field or a
duplicate slug, and `game.NewRegistry` panics on either, or on seat bounds no table
can meet, at boot. `internal/catalog/rules_test.go` runs the shared Rules contract
over every entry, and fails until its `moves` map has your game's candidate moves (see
below). **The slug is
persisted** (`games.slug`), so treat it as permanent: changing it later means a
data migration, not a rename. The display `Name` is free to change.

### 4. Tests the game does not ship without

- **Rules unit tests**, table-driven, covering every action the rules can reject.
- **Candidate moves for the Rules contract.** Add your slug to `moves` in
  `internal/catalog/rules_test.go`: every move worth trying for the seat on turn,
  legal or not (the suite keeps the ones `ValidateAction` accepts). With it,
  `gametest.RunRules` proves a match played by `TimeoutAction` alone ends, another
  game's move is refused with `game.ErrUnknownAction` by `ValidateAction` and
  `ApplyAction`, and `Standings` ranks everyone who sat down - leavers last, never
  sharing a seated player's place - and `gametest.SoakAmongRandomMoves` checks
  `TimeoutAction` against `ValidateAction` on every turn of random play with seats
  walking out.
  That is the timeout-action soak; do not write your own. Without it the auto-play
  path is untested until it strands a real table.
- **A shedding game runs the shared suite.** Describe it as a `gametest.Shed` and
  call `gametest.RunShed(t, suite)`, as `internal/game/uno/helpers_test.go` does,
  rather than copying the draw, reshuffle and deadlock tests.
- **A fit test.** `TestView_FitsTheTerminal`-style, over `tuitest.FitSizes`
  (`{styles.MinWidth, styles.MinHeight}`, `{80, 24}`, `{120, 50}`), driving keys
  with `tuitest.Key`. Copy `internal/tui/views/gameview/uno/view_test.go`.
- **Nothing per view for `Close`.** `TestGames_CloseReleasesTheEngineSubscription`
  in `internal/catalog/close_test.go` checks every entry in `Games` releases its
  subscription (counted with `Engine.SubscriberCount`).
- **`goleak_test.go`** with `goleak.VerifyTestMain(m)` in both new packages. 26
  packages have one; a view that subscribes and forgets to `Close` is exactly
  what it catches.

You do **not** need to seed a `games` row: `getOrCreateGame` reads by slug at
finalize time and upserts on the slug the first time it is missing.

## Test conventions

- **Table-driven with named subtests**, `t.Parallel()` wherever it is safe
  (`paralleltest` and `tparallel` are enabled linters).
- **`pgregory.net/rapid`** for properties - Elo's invariants, poker's "nobody
  loses chips nobody matched" (`streets_test.go`), the timeout soaks.
- **Fuzz targets** for anything that parses or searches untrusted or
  combinatorial input. Eight exist today: `FuzzBestMeldSplit`,
  `FuzzClassifyHand`, `FuzzEvaluateHand`, `FuzzJoinLobbyByCode`, `FuzzNetKey`,
  `FuzzToUint32`, `FuzzPile_DrawN`, `FuzzValidateUsername`. They run over
  their seed corpus in the ordinary suite; commit any crasher the fuzzer finds as
  a `testdata/fuzz` seed.
- **`go.uber.org/goleak`** `TestMain` in every package that starts a goroutine.
- **Benchmarks** for render paths and hot evaluators - 20 exist; add one when you
  touch a `View()` or the poker evaluator.
- **Integration tests** behind `//go:build integration`. `testutil.SetupTestDB`
  applies the real migrations and skips when Docker is absent. Note that
  `internal/systemtest` is mostly *not* tagged - only `persistence_test.go` needs
  Docker; the rest drives the real components through their public APIs in the
  ordinary suite.

**Every fix ships a test that fails on the old code.** Not a test that passes
afterwards - one you have actually watched fail first. Most of the hardening in
this repo was invisible for months precisely because the behaviour looked fine;
`git log` is full of `fix(...)` commits paired with the test that would have
caught them. If you cannot construct that test, say so in the PR and explain
why.

Combined unit + integration coverage of the production packages stays at 95% or
better; `make cover` and CI enforce it. Assert on outcomes: a test that only
executes a line buys the number and none of the safety.

## Code style

- `make fmt` and `make lint` clean. `goimports` grouping.
- Wrap errors with `%w`, lowercase messages (`wrapcheck` is on).
- **Comments explain *why*, not *what*.** The codebase is intentionally light on
  them; the ones that exist mostly record a failure mode or a rejected
  alternative. If a comment would restate the code, delete it. If a guard looks
  unnecessary, the comment must say what it is guarding against - and if it is
  genuinely unreachable, delete the guard and say *that* instead.
- Colours live in `theme.go`. `TestNoRawColoursOutsideTheme` enforces it.
- Match key names, not key literals: `tea.KeyPressMsg.String()` normalises the
  spacebar to `"space"`, and `" "` silently never matches.

### Size gates - split, do not raise

`.golangci.yml` sets `funlen` 75 lines / 55 statements, `cyclop` 21, `gocognit`
30, `nestif` 9, `lll` 140. Each sits **just above** the worst surviving function,
so a new violation means your function is the worst one in the repo. Split it.
Raising a threshold to land a change is how the gate stops meaning anything, and
it is not accepted in review. (Tests are excluded from all five: a table-driven
test is legitimately long and repetitive, and its branchiness is not a
maintenance signal.)

## Database migrations

Schema changes are SQL files in `internal/db/migrations/`, applied with
[golang-migrate](https://github.com/golang-migrate/migrate). `000001_init` is
the squashed base schema; every change since is a new numbered pair on top of it.

- **Up *and* down, always.** `make migrate-create` writes both.
- **No GORM AutoMigrate.** `testutil.SetupTestDB` replays these same files, up,
  down and up again, so the tested schema cannot drift from the deployed one -
  and a broken down fails the suite rather than production.
- **Say so when a down loses data.** Mark it `-- LOSSY.` and say what is lost.
- Compose runs them automatically before the backend starts.
- Put the *reason* in the file as a SQL comment.
- Never edit an applied migration: a database already past it would never see
  the change. Add the next number with `make migrate-create`.

## Commits and pull requests

- **Conventional commits**, imperative, lowercase, **at most 100 characters** on
  the subject line: `fix(lobby): snapshot finalize settings at game start`.
  Scopes are package names (`tui`, `ssh`, `poker`, `nginx`, `ci`, `docs`).
  Types in use: `feat`, `fix`, `refactor`, `test`, `docs`, `style`, `chore`.
- **Small batches.** One concern per commit and, where you can manage it, per
  PR. A behaviour change and its test belong in the same commit; a rename belongs
  in its own.
- Describe *what* and *why* in the PR body. The *what* is readable from the diff;
  the *why* is the thing that is gone in six months.
- CI must be green: unit tests, integration tests and the coverage floor, lint,
  semgrep, `govulncheck`, the
  Docker image build (amd64 + arm64), and `docker compose config`.
- If you changed a contract - `SessionTracker`, finalize ownership, the grace
  state machine, the `Frame` signature, the catalog shape, a repository interface
  - update `CLAUDE.md` and `docs/architecture.md`, and `docs/decisions.md` if the
  reason changed. Update `docs/onboarding.md` only if the day-one story changed.

## Reporting bugs and suggesting features

Issues: what you did, what you expected, what happened, plus terminal emulator,
OS and SSH client. For a feature, say who it helps - a player or an operator -
and which of the invariants in `docs/architecture.md` §12 it would touch.

Happy coding.
