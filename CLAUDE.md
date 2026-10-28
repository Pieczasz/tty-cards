# CLAUDE.md

Operational brief for coding agents. The reasoning is in [`docs/architecture.md`](docs/architecture.md)
(contracts, invariants) and [`docs/decisions.md`](docs/decisions.md) (the *why*, cited as `#N`).

## Commands

```bash
make test-short       # unit tests, no Docker
make test             # go test -race -shuffle=on ./...
make test-integration # -tags=integration, needs Docker (testcontainers)
make lint             # golangci-lint, both build tags
make semgrep          # project-invariant rules: their tests, then a scan
make cover            # unit + integration with -race; fails under the coverage floor
make ci               # fmt, fix, lint, deadcode, semgrep, cover, build (Docker and uv)
go test -race -run 'TestX/sub' ./internal/lobby/   # one test
make migrate-create   # new migration in internal/db/migrations/ (up and down)
./scripts/dev-session.sh   # three tmux SSH clients, throwaway keys; TC_PORT=6969 needs PROXY_PROTOCOL=false
```

## Shape

SSH -> wish middleware -> Bubble Tea `Router` -> view -> `game.BoundEngine` -> `game.Engine` -> `Rules`.
`internal/catalog/catalog.go` `Games` is the only place a game is declared (rules + view, #6).

## Layer rules (lint-enforced where marked)

- `internal/db` is the GORM models **and** the repository interfaces; `internal/repository` implements
  them and only `cmd/server` imports it (`depguard`). Consumers ask for the smallest interface.
- `internal/game` is pure: stdlib, `deck`, `broadcaster`, `internal/game/...`, `uuid` (`depguard`
  `game-is-pure`). Seats are scalars on `game.Player`, never a `*db.User`. No routes, no db, no tui.
- `internal/rating` and `internal/elo` are pure (`rating-is-pure`). `internal/tui` never names
  `game.Engine`, `NewEngine`, `Bind` or `ActiveGame` (semgrep): views get a `*game.BoundEngine`
  from the lobby.
- Views that subscribe implement `router.Closer`. Anything kept after `Frame`/`Sync` is copied.

## Invariants that bite

- `Engine.mu` is the only engine lock and `Rules` run under it: rules never call the engine, and
  `State` carries no engine handle (#1). Move the turn only through `State.SetTurn`/`OverrideTurn`.
- Lock order: tracker account -> `Manager.mu` -> `Lobby.mu` -> `Engine.mu` (#45). A `Lobby` is a leaf.
- Every goroutine you start recovers its own panic (`Manager.recoverGoroutine` is a direct defer);
  a rules panic ends one table, not the process (#37).
- `ApplyAction` ends in `default: return ErrUnknownAction`; checkable-up-front belongs in
  `ValidateAction`; `TimeoutAction` must return something `ValidateAction` accepts.
- Never publish `:6969` or `:6970` (PROXY / `X-Forwarded-For` trust, #19). `PROXY_TRUSTED_CIDRS`
  is the only set of networks whose header is believed.
- The slug is the persisted game identity; changing one is a data migration (#7).
- The finalize snapshot is taken when a game starts. Rules error, abandoned and a forced hang-up
  (`BeginTeardown`) record history without Elo (#11); `NewManager` panics on a nil repository.
- Terminals are capped at 120x40 (`internal/ssh/termsize.go`, #60): `MAX_CONNECTIONS` x that must fit
  `GOMEMLIMIT`; re-measure with `cmd/loadtest` before raising either.
- A lobby refusal the player caused matches `lobby.ErrRefused`; views render errors through
  `views.PlayerError`. Every screen fits 64x20.

## Conventions

- Wrap errors with `%w`, lowercase messages. Comments say *why*; the codebase is light on them.
- Table-driven tests with named subtests, `t.Parallel()` where safe; `rapid` for properties,
  `goleak` for leaks. Integration tests are behind `//go:build integration`.
- Every catalog game runs `gametest.RunRules` and `SoakAmongRandomMoves` (`catalog/rules_test.go`).
- `.golangci.yml` size gates (`funlen`, `cyclop`, `gocognit`, `nestif`, `lll`): split the function,
  never raise a threshold. Use `styles.JoinVertical/JoinHorizontal/MarginTop`, not lipgloss's
  (semgrep `tui-uses-lipgloss-join`).
- Env booleans go through `strconv.ParseBool`; a plaintext database is development-only.
- Deferred design work lives in `docs/deferred.md`, not `TODO` markers (`godox` fails them).
- `code-review-graph` (MCP, `.mcp.json`) answers call-graph questions; refresh with
  `uvx code-review-graph update`. A channel receive or `time.AfterFunc` is not a call edge.
