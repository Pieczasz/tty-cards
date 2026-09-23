# Contributing to Terminal Card

The project is open source so it can outlive whoever is currently maintaining it.
That goal drives most of the rules below: every one of them exists so the next
person can change the code without reading all of it first.

Start with [`README.md`](../README.md) to run it, then
[`reading-guide.md`](reading-guide.md) to learn it, then
[`architecture.md`](architecture.md) for the contracts you must not break.

Security bugs do **not** go in an issue - see [`SECURITY.md`](SECURITY.md).

## Setup

Go **1.27.1** (see `go.mod`; CI pins the same). PostgreSQL 18 for anything that
touches the database. Docker for the integration suite.

```bash
cp .env.example .env
export DB_DSN='postgres://postgres:PASSWORD@localhost:5432/terminal_card?sslmode=disable'
make install-tools   # golang-migrate
make migrate-up
make test-short
make build
```

### Make targets

| Target | Purpose |
|---|---|
| `make test-short` | Unit tests, no Docker |
| `make test` | `go test -race ./...` |
| `make test-integration` | `-tags=integration`, needs Docker (testcontainers) |
| `make lint` | golangci-lint |
| `make fmt` / `make fix` | `go fmt` / `go fix` |
| `make build` | `bin/server` |
| `make ci` | fmt, fix, lint, test, build - run this before you push |
| `make migrate-create` / `migrate-up` / `migrate-down` | SQL migrations via `$DB_DSN` |
| `make loadtest` | SSH concurrency harness against a **running** server |

### Linting locally

CI runs golangci-lint **v2.13.2** via `golangci/golangci-lint-action@v7` with
`install-mode: goinstall` - prebuilt binaries lag the module's Go version and
refuse `go 1.27.x`. Match it locally the same way:

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
~/go/bin/golangci-lint run      # or `make lint` if it is on your PATH
```

## Adding a game

Five games exist; a sixth touches the engine in no way at all. Crazy Eights is
the smallest reference implementation, Poker the most demanding.

### 1. Rules - `internal/game/<name>/`

Implement `game.Rules` and pin it:

```go
var _ game.Rules = (*Rules)(nil)
```

Add the optional interfaces you actually need, each with its own compile-time
assertion:

| Interface | When |
|---|---|
| `game.PlayerLeaveHandler` | mid-hand disconnects change your state (`OnPlayerLeave` before removal, `AfterPlayerRemoved` after seat indices shift) |
| `game.TurnTimeoutHandler` | **almost always.** No handler means no turn clock, and a player who walks away freezes the table forever |
| `game.TurnDurationHandler` | one phase needs longer than 30s (hearts' pass, the between-hands deal) |
| `game.StandingScorer` | equal results should share a place rather than break the tie by seat order |

Two rules that have each cost a bug:

- **`TimeoutAction` must return something your own `ValidateAction` accepts.**
  Otherwise the turn re-arms with only the 10-second floor left and every refused
  expiry still costs a miss, so the absent seat is taken early. Gin rummy's
  `autoDiscard` skips the card the upcard rule forbids for exactly this reason.
- **Anything checkable up front belongs in `ValidateAction`.** An error from
  `ApplyAction` or `AfterAction` ends the game as `EndReasonRulesError`, with
  state possibly half-applied and the match recorded unrated.

If one seat leaving means the match cannot go on (hearts is four-handed or
nothing), set `State.Interrupted` in `OnPlayerLeave`: the removal that ends the
game then reports `EndReasonInterrupted`, and finalize charges only the leavers
([`decisions.md` #39](decisions.md#39-an-interrupted-match-charges-only-its-leavers)).
A shedding game should reuse `internal/game/shed.go` (`ValidateShedPlay`,
`DrawWithReshuffle`, `ShedStandings`) rather than copy it.

Use `internal/deck` rather than writing your own: `RankValue` / `RunOrder` /
`PipValue` answer three different questions and must not be swapped, and
`RemoveOne` / `RemoveEach` never alias.

### 2. View - `internal/tui/views/game/<name>/`

Expose `New(router.GlobalContext, *game.Engine) tea.Model`, embed
`gameview.Session`, and implement your rules rendering and nothing else. Session
already owns binding, subscribing, the `Update` loop (`HandleFrame`), the turn
clock tick, the last rejected move (`ActionErr`), the hand cursor, the forfeit
prompt, leaving, idle removal and the idle-quit exemption, and `Close`. Wire it the
way `crazyeight` does:

- `Init` returns `tea.Batch(m.Listen(), m.ClockTick())`.
- Your key handler closes any prompt of your own on esc first, then calls
  `m.HandleLeaveKey(key)` before its own bindings and returns if it consumed the
  key - mid-game esc must ask before it forfeits
  ([`decisions.md` #44](decisions.md#44-leaving-a-live-game-asks-first)).
- `View` returns `m.LeaveConfirmScreen()` before anything else when it is armed.
- Submit through `m.Submit(action)`, which keeps the result in `m.ActionErr`, and
  render `m.ActionErr` in the hero band (`gameview.RenderHeroBand`).

Copy anything you keep past `Sync` (`maps.Clone`, `HandResult.Clone`). The
`*State` you get is live and unredacted - filtering what the player may see is
your job, and it should be a named function so a reviewer can find it
(`buildSeats` in poker).

### 3. Register both together - `internal/catalog/catalog.go`

One entry in `All`. This is the only registration point; `cmd/server/main.go`
and `internal/tui/app.go` both read it.

```go
{
    Name:  "My Game",
    Slug:  "my_game",
    Rules: func() game.Rules { return &mygamerules.Rules{} },
    View:  mygameview.New,
},
```

`catalog_test.go` fails on a missing field or a duplicate slug. **The slug is
persisted** (`games.slug`), so treat it as permanent: changing it later means a
data migration, not a rename. The display `Name` is free to change.

### 4. Tests the game does not ship without

- **Rules unit tests**, table-driven, covering every action the rules can reject.
- **A timeout-action soak.** Four games have
  `TestSoak_TimeoutActionIsAlwaysLegal` (rapid-driven; see
  `internal/game/uno/rules_test.go`); poker has the deterministic equivalent,
  `TestRules_TimeoutAction_IsAcceptedByValidateAction`. Either shape is fine.
  Without one, the auto-play path is untested until it strands a real table.
- **A fit test.** `TestView_FitsTheTerminal`-style, at 64x20, 80x24 and 120x50 -
  `{styles.MinWidth, styles.MinHeight}`, `{80, 24}`, `{120, 50}`. Copy
  `internal/tui/views/game/uno/view_test.go`.
- **`goleak_test.go`** with `goleak.VerifyTestMain(m)` in both new packages. 22
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
  `FuzzToUint32`, `FuzzPile_DrawNCards`, `FuzzValidateUsername`. They run over
  their seed corpus in the ordinary suite; commit any crasher the fuzzer finds as
  a `testdata/fuzz` seed.
- **`go.uber.org/goleak`** `TestMain` in every package that starts a goroutine.
- **Benchmarks** for render paths and hot evaluators - 21 exist; add one when you
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

Coverage sits at 90% or better in every package that is not pure wiring
(`cmd/server` is ~42% and that is fine - it is `main`).

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
[golang-migrate](https://github.com/golang-migrate/migrate). Seven pairs exist.

- **Up *and* down, always.** `make migrate-create` writes both.
- **No GORM AutoMigrate.** `testutil.SetupTestDB` replays these same files, up,
  down and up again, with rows seeded before the down pass
  (`seedRoundTripData`), so the tested schema cannot drift from the deployed one -
  and a broken migration, or a down that breaks on real data, fails the suite
  rather than production. Add a seed row when your down file rewrites data.
- **Say so when a down loses data.** `000005_game_slug.down.sql` opens with
  `-- LOSSY.` and what is lost
  ([`decisions.md` #48](decisions.md#48-migration-000005s-down-is-lossy)).
- **Refuse rather than guess** when existing rows break the new constraint and
  fixing them is an operator's decision: `000006_username_ci.up.sql` names every
  case collision and stops.
- Compose runs them automatically before the backend starts.
- Put the *reason* in the file as a SQL comment. `000004_not_null_scalars.up.sql`
  and `000005_game_slug.up.sql` both do, and both are worth reading before you
  write your first one.
- Backfill before you constrain - but only where a resting value is honest. 000004
  backfills `rankings.elo` to 1500 and deliberately does *not* invent a parent for
  a row with no owner.

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
- CI must be green: unit tests, integration tests, lint, `govulncheck`, the
  Docker image build (amd64 + arm64), and `docker compose config`.
- If you changed a contract - `SessionTracker`, finalize ownership, the grace
  state machine, the `Frame` signature, the catalog shape, a repository interface
  - update the docs in this order: `CLAUDE.md`, `docs/architecture.md`,
  `docs/decisions.md` if the *reason* changed, the `docs/reading-guide.md` step
  that covers the path, and `docs/onboarding.md` only if the day-one story
  changed.

## Reporting bugs and suggesting features

Issues: what you did, what you expected, what happened, plus terminal emulator,
OS and SSH client. For a feature, say who it helps - a player or an operator -
and which of the invariants in `docs/architecture.md` §12 it would touch.

Happy coding.
