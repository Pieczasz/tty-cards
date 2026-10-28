# CLAUDE.md

## Commands

```bash
make test-short # unit tests, no Docker
make test # go test -race ./...
make test-integration # -tags=integration, needs Docker (testcontainers)
make lint # golangci-lint, both build tags
make semgrep # .semgrep/ project-invariant rules: their tests, then a scan
make cover # unit + integration with -race; fails under 95% combined coverage
make build # -> bin/server
make ci # fmt, fix, lint, deadcode, semgrep, cover, build (needs Docker and uv)

go test -race -run TestName ./internal/game/poker/ # single test
go test -race -run 'TestX/subtest_name' ./internal/lobby/ # single subtest

make migrate-create # prompts for name, writes internal/db/migrations/
make migrate-up # needs $DB_DSN exported
```

Local multi-client testing: `./scripts/dev-session.sh` (`TC_PORT=6969` needs `PROXY_PROTOCOL=false`). `PROXY_TRUSTED_CIDRS` is the only set of networks whose PROXY header the ssh listener honors.

## Contracts

Topology, the invariant list and the policies are `docs/architecture.md` and
`docs/decisions.md`. The index is `docs/README.md`. These are the ones those
lists do not already say:

- Hearts and gin show the next-hand enter hint only on the seat whose turn it is. A gin forfeit shows the scoreboard.
- `lobby.ErrRefused` is shown as written. Any other lobby error is logged once in `views.PlayerError` and shown as a generic line.
- Do not raise a `.golangci.yml` size gate; split the function. Comments say why. Deferred work is `docs/deferred.md`, not `TODO` comments.
