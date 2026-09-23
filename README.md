# tty.cards

[![Go Version](https://img.shields.io/github/go-mod/go-version/Pieczasz/terminal-card)](https://go.dev/)
[![License](https://img.shields.io/github/license/Pieczasz/terminal-card)](./LICENSE)
[![Build Status](https://img.shields.io/github/actions/workflow/status/Pieczasz/terminal-card/test.yml?branch=main)](https://github.com/Pieczasz/terminal-card/actions)

An SSH server that deals cards. You `ssh` in, you get a full terminal UI, and you
play against other people connected to the same process. No download, no browser,
no account form - the first connection with your SSH key claims your username.

```
ssh tty.cards
```

Go, [Charm](https://charm.sh/) (Bubble Tea + Wish), PostgreSQL for what has to
outlive the process. One process holds every table.

**Documentation lives in [`docs/`](docs/README.md)** - start there for the design,
the reading order and the reasoning behind every non-obvious choice. This file is
orientation, commands and configuration.

## Play

```
ssh tty.cards
```

Any SSH client works. Your public-key fingerprint is your identity; the name you
log in with the first time becomes your username (`ssh -l yourname tty.cards` to
pick it).

Create a table or browse the public ones, set it **Casual** or **Ranked**, and
press Ready. Ranked tables move Elo; casual tables only record history.

### The five games

| Game | Seats | In one line |
|---|---|---|
| **Crazy Eights** | 2-6 | Match the rank or the current suit; an eight is wild and names the next suit. First empty hand wins. |
| **Poker** (No-Limit Hold'em) | 2-9 | A 10-hand match. 1000 chips, blinds 25/50, real side pots. Most chips at the end wins. |
| **Uno** | 2-10 | Match colour, number or symbol. Skip, Reverse and the draw cards pick the next actor explicitly. |
| **Hearts** | exactly 4 | Pass three, 2♣ leads, follow suit, avoid hearts and the queen. Shooting the moon charges everyone else 26. Match ends at 100. |
| **Gin Rummy** | exactly 2 | Draw, meld, discard. Knock at 10 deadwood or less, gin at 0, undercut on a tie. Match ends at 100. |

Seat limits come from each game's `MinPlayers`/`MaxPlayers`
(`internal/game/*/rules.go`); the lobby clamps its own capacity to them.

### Rules that apply at every table

- **30 seconds a turn** (`game.DefaultTurnTimeout`). When it runs out the rules
  play a safe move for you - poker checks or folds, Uno and Crazy Eights draw,
  Hearts plays its first legal card, Gin Rummy sheds its priciest deadwood.
- **Three consecutive misses loses the seat** (`game.MaxMissedTurns`). Acting
  clears the count; a move the rules *reject* does not.
- **A dropped connection holds your seat for 90 seconds**
  (`lobby.DisconnectGrace`). Reconnect inside that and you land back at the
  table mid-hand. A waiting-lobby seat leaves at once.
- **One live session per account.** A second connection displaces the first and
  closes it, so a half-open TCP session cannot lock you out of your own seat.

## Run it locally

### Docker Compose (the whole stack)

Requires Docker, Docker Compose and an SSH client.

```bash
cp .env.example .env
# set DB_PASSWORD - a strong unique password; it has no usable default

docker compose up -d --build
ssh -p 22 yourname@localhost
```

Migrations run automatically (the `migrate` service) before the backend starts.
SSH host keys persist in the `ssh-keys` volume - keep it across redeploys or every
client sees a host-key change.

**Ports.** The stack publishes exactly two to the world, both on nginx: **22**
(SSH) and **80** (the site and `/api/`). Grafana is the one exception and is bound
to `127.0.0.1:3000` - reach it with `ssh -L 3000:127.0.0.1:3000 <host>`. The
backend's `6969` (SSH) and `6970` (stats API) stay on the compose network; see
[`docs/SECURITY.md`](docs/SECURITY.md) for why publishing them is a real hole and
not a nitpick.

**Sizing.** Every service carries an explicit `mem_limit` and they add up to
5632 MiB (5.5 GiB), sized for a 12 GB / 6-core VPS with half left for the host.
The arithmetic is in the header comment of `compose.yaml`.

### Without Docker

```bash
# prerequisites: Go 1.27.1, PostgreSQL 18

cp .env.example .env
export DB_DSN='postgres://postgres:PASSWORD@localhost:5432/terminal_card?sslmode=disable'
make install-tools     # golang-migrate
make migrate-up
make build
PROXY_PROTOCOL=false ./bin/server
```

`PROXY_PROTOCOL=false` is required when you connect a bare `ssh` client: the
default assumes nginx in front, speaking PROXY protocol, and a bare client sends
no PROXY header.

```bash
ssh -p 6969 yourname@localhost
```

`./scripts/dev-session.sh` opens three tmux-attached clients with distinct keys
against a running server, which is the fastest way to test anything multiplayer.

## Tests

```bash
make test-short        # unit tests, no Docker
make test              # go test -race ./...
make test-integration  # -tags=integration, needs Docker (testcontainers)
make lint              # golangci-lint (v2.13.2 in CI)
make ci                # fmt, fix, lint, test, build

go test -race -run TestName ./internal/game/poker/
go test -race -run 'TestX/subtest_name' ./internal/lobby/
```

Fuzz targets (8 of them: `FuzzBestMeldSplit`, `FuzzClassifyHand`,
`FuzzEvaluateHand`, `FuzzJoinLobbyByCode`, `FuzzNetKey`, `FuzzToUint32`,
`FuzzPile_DrawNCards`, `FuzzValidateUsername`) run as ordinary tests over their
seed corpus; to actually fuzz one:

```bash
go test -run='^$' -fuzz=FuzzBestMeldSplit -fuzztime=60s ./internal/game/ginrummy/
```

Benchmarks (rendering, evaluator, broadcaster, Elo, rate limiter):

```bash
go test -run='^$' -bench=. -benchmem ./internal/tui/views/game/poker/
```

`make loadtest` drives N concurrent SSH sessions at a **running** server and
prints connect / first-frame latency percentiles. It asserts nothing; it is a
measurement tool. Point it at your own server, never a public one. Every session
registers a fresh account, and the server allows only `REGISTRATION_LIMIT` (5) new
accounts per network per `REGISTRATION_WINDOW` (1h), so start the server under test
with `REGISTRATION_LIMIT=10000`, and give each run on the same database a new
`PREFIX`.

## Layout

```
cmd/server/        composition root, Dockerfile
cmd/loadtest/      SSH concurrency harness

internal/
  catalog/         the one place a game is declared (rules + view, together)
  config/          env parsing + nginx/alloy/loki/tempo/prometheus/grafana assets
  db/              GORM models, repository interfaces, SQL migrations
  repository/      the GORM implementations
  ssh/             Wish server, auth, session ownership
  tui/             Bubble Tea router, views, styles
  lobby/           tables, disconnect grace, match finalization
  game/            engine + Rules contract; crazyeight|poker|uno|hearts|ginrummy
  deck/            shared card mechanics
  elo/             rating math
  broadcaster/     latest-wins fan-out
  httpapi/         read-only public stats JSON
  observability/   OpenTelemetry setup and instruments
  ratelimit/       sliding window + IPv6 /64 keying
  systemtest/      end-to-end through public APIs only

web/               Astro marketing site (not part of the Go module)
docs/              the design document, reading guide, decisions, policies
```

## Configuration

Full list with comments in [`.env.example`](.env.example).

| Variable | Default | Notes |
|---|---|---|
| `ENV` | `development` | `production`, `staging` or `development`; anything else fails the boot. `production` requires `DB_PASSWORD` |
| `SERVER_HOST` / `SERVER_PORT` | `0.0.0.0` / `6969` | never publish `6969` |
| `PROXY_PROTOCOL` | `true` | `false` for a bare local `ssh` client |
| `PROXY_TRUSTED_CIDRS` | empty | comma-separated; when set, a PROXY header is honored only from these networks and every other connection is refused. Empty trusts any peer |
| `MAX_CONNECTIONS` | `1000` | concurrent TCP connections |
| `SSH_KEY_PATH` | `.wishlist/server` | host key |
| `RATE_LIMIT_CONNECTIONS` / `RATE_LIMIT_WINDOW_MS` | `5` / `1000` | SSH **auth attempts** per client network |
| `REGISTRATION_LIMIT` / `REGISTRATION_WINDOW` | `5` / `1h` | new accounts per client network; raise for `make loadtest` |
| `DB_*` | see `.env.example` | Postgres; in production `DB_SSLMODE` defaults to `require` and must be `require`, `verify-ca` or `verify-full` unless the host is internal or `ALLOW_INSECURE_DB=true` |
| `DB_MAX_OPEN_CONNS` | `25` | pool size, independent of the SSH cap |
| `API_PORT` | `6970` | stats API; reached only through nginx `/api/` |
| `API_REQUESTS_PER_MINUTE` | `120` | per client network |
| `API_TRUST_PROXY` | `false` | compose opts in; only safe behind a proxy that sets `X-Forwarded-For` itself |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | logs, metrics, traces |
| `LOG_LEVEL` | `INFO` | stderr and OTLP both |

New accounts are separately capped at 5 per hour per client network
(`registrationLimit` in `internal/ssh/server.go`); returning players never spend
that budget.

## Observability

Alloy -> Loki / Tempo / Prometheus -> Grafana, all in the same compose file, none
of it published beyond Grafana's loopback binding. The app **pushes** OTLP; Alloy
scrapes only the host.

Retention is explicit: **logs 14 days** (`internal/config/loki/loki.yaml`),
**traces 48 hours** (`internal/config/tempo/tempo.yaml`), **metrics 30 days**
(`compose.yaml`). What is in them, per field, is in
[`docs/data-inventory.md`](docs/data-inventory.md).

## Self-hosting

1. A VM with Docker. 12 GB / 6 cores is what `compose.yaml` is sized against; the
   stack itself wants 5.5 GiB.
2. Move the host's own `sshd` off port 22 (`Port 2222` in `/etc/ssh/sshd_config`)
   and reconnect there - the game proxy owns 22.
3. Firewall: allow 22 and 80, plus your admin SSH port from trusted addresses
   only. Do not open Postgres, Grafana, `6969` or `6970`.
4. `cp .env.example .env` and set `DB_PASSWORD`.
   Compose sets `ENV=production` on the backend.
5. `docker compose up -d --build`.
6. Optional: install `zstd` and cron `./scripts/backup.sh` (see its header).
   Protect `backups/`.
7. Smoke test: register a new key -> create a lobby -> play each game -> drop
   mid-hand -> reconnect -> check profile and leaderboard.

Notes worth reading before you deploy:

- **Registration is open on purpose.** Any public key is accepted. For a private
  community put it behind a firewall, a VPN or an allowlist -
  [`docs/SECURITY.md`](docs/SECURITY.md).
- Compose sets `DB_SSLMODE=disable` for the internal Postgres network. For an
  external managed database set `DB_SSLMODE=require` and supply CA-trusted TLS.

## Docs

Start at [`docs/README.md`](docs/README.md) - it says what each document is for
and the order to read them in.

| Doc | For |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | The canonical design document: contracts, topology, invariants |
| [`docs/decisions.md`](docs/decisions.md) | One record per non-obvious choice, with its reasoning |
| [`docs/reading-guide.md`](docs/reading-guide.md) | Ordered bottom-up code tour, "where is X?" index |
| [`docs/onboarding.md`](docs/onboarding.md) | Product story, annotated file tree, day one |
| [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md) | How to add a game, test conventions, PR norms |
| [`docs/SECURITY.md`](docs/SECURITY.md) | Reporting a vulnerability; what is in scope |
| [`docs/data-inventory.md`](docs/data-inventory.md) | Per-field personal-data inventory |
| [`docs/changelog.md`](docs/changelog.md) | What changed, in user-facing terms |
| [`docs/privacy.md`](docs/privacy.md) / [`docs/terms.md`](docs/terms.md) | The published policies |
| [`CLAUDE.md`](CLAUDE.md) / [`AGENTS.md`](AGENTS.md) | Terse briefs for coding agents |

## License

MIT - see [LICENSE](LICENSE).
