# tty.cards

[![Go Version](https://img.shields.io/github/go-mod/go-version/Pieczasz/tty-cards)](https://go.dev/)
[![License](https://img.shields.io/github/license/Pieczasz/tty-cards)](./LICENSE)
[![Build Status](https://img.shields.io/github/actions/workflow/status/Pieczasz/tty-cards/test.yml?branch=main)](https://github.com/Pieczasz/tty-cards/actions)

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

Where the tables differ from the rules you may know: Crazy Eights deals seven
cards at every table size; Uno ranks the other seats by fewest cards left and has
no "UNO" call; Gin Rummy scores the gin and undercut bonuses (25 each) and nothing
else - no big gin, no box or game bonus. In Hearts one seat leaving ends the match
for everyone: only the leaver's rating moves, and only down.

### Rules that apply at every table

- **30 seconds a turn** (`game.DefaultTurnTimeout`). When it runs out the rules
  play a safe move for you - poker checks, or calls when nobody can bet more than
  you already have in, and otherwise folds; Uno and Crazy Eights draw; Hearts
  passes its most dangerous cards and plays its first legal card; Gin Rummy knocks
  when it holds gin and otherwise sheds its priciest deadwood. A turn that carries
  on - Gin's draw then discard, an Uno skip that comes back to you - keeps its
  clock, with at least 10 seconds left.
- **Three missed turns in a row loses the seat** (`game.MaxMissedTurns`). Acting
  clears the count; a move the rules *reject* does not.
- **esc asks before you forfeit.** Mid-game, esc shows "Leave and forfeit this
  game?" and only `y` leaves; any other key keeps you playing.
- **A dropped connection holds your seat for 90 seconds**
  (`disconnectGrace` in `internal/lobby`). Reconnect inside that and you land back at the
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
client sees a host-key change. Locally nginx serves `https://localhost` with a
self-signed placeholder, and the `certbot` service fails to validate `tty.cards`
and retries hourly; `docker compose stop certbot` if that noise bothers you.

**Ports.** The stack publishes exactly three to the world, all on nginx: **22**
(SSH), **443** (`/api/` over TLS; everything else redirects to the site on `www`)
and **80** (the ACME challenge and a redirect to https). Grafana is the one exception and is bound
to `127.0.0.1:3000` - reach it with `ssh -L 3000:127.0.0.1:3000 <host>`. The
backend's `6969` (SSH) and `6970` (stats API) stay on the compose network; see
[`docs/SECURITY.md`](docs/SECURITY.md) for why publishing them is a real hole and
not a nitpick.

**Sizing.** Every service carries an explicit `mem_limit` and they add up to
7936 MiB (7.75 GiB) and 4 cores of a 12 GB / 6-core VPS, leaving the rest to the host.
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
`FuzzPile_DrawN`, `FuzzValidateUsername`) run as ordinary tests over their
seed corpus; to actually fuzz one:

```bash
go test -run='^$' -fuzz=FuzzBestMeldSplit -fuzztime=60s ./internal/game/ginrummy/
```

Benchmarks (rendering, evaluator, broadcaster, Elo, rate limiter):

```bash
go test -run='^$' -bench=. -benchmem ./internal/tui/views/gameview/poker/
```

`make loadtest` drives N concurrent SSH sessions at a **running** server and
prints connect / first-frame latency percentiles. It asserts nothing; it is a
measurement tool. Point it at your own server, never a public one. With `PLAY=0.4`
(`-play-fraction`) 40% of the sessions sit at Crazy Eights tables of `TABLE` (4)
seats and play through the real TUI: the host creates a public lobby, the guests
join by the code read off its screen, each bot moves 1-4 s after its turn comes up,
and a finished table plays again until the hold ends. It then also reports moves,
move round-trip (keypress to the frame showing it applied), games completed and
output frames per second per session. The rest park in the join browser. Every
session registers a fresh account and every run from one address trips the
per-network limits, so start the server under test with `REGISTRATION_LIMIT`,
`REGISTRATION_LIMIT_WIDE` and `REGISTRATION_GLOBAL_LIMIT` above the session count
and `RATE_LIMIT_CONNECTIONS` above sessions-per-second of the ramp, and give each run
on the same database a new `PREFIX`. Refusals at auth and at registration are
counted separately in the report.

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
  ratelimit/       sliding window + IPv6 /64 and /48 keying
  systemtest/      end-to-end through public APIs only

web/               Astro marketing site (not part of the Go module)
docs/              architecture, decisions, and the doc index
```

## Configuration

Full list with comments in [`.env.example`](.env.example).

| Variable | Default | Notes |
|---|---|---|
| `ENV` | `development` | `production`, `staging` or `development`; anything else fails the boot. `production` requires `DB_PASSWORD` |
| `SERVER_HOST` / `SERVER_PORT` | `0.0.0.0` / `6969` | never publish `6969`; an IPv6 literal host (`::`) binds |
| `PROXY_PROTOCOL` | `true` | `false` for a bare local `ssh` client |
| `PROXY_TRUSTED_CIDRS` | empty | comma-separated; when set, a PROXY header is honored only from these networks and every other connection is refused. Empty trusts any peer |
| `MAX_CONNECTIONS` | `1000` | concurrent sessions (one per account; compose sets 1250, the most measured to hold on its 3 cores); the listener's backstop is twice this. Each session costs memory (`docs/architecture.md` §3.2), so raise it only with `GOMEMLIMIT` and the backend's `mem_limit` |
| `SSH_KEY_PATH` | `.wishlist/server` | host key |
| `SERVICE_VERSION` | the build's module version | stamped on `service.version` so dashboards can tell deploys apart |
| `RATE_LIMIT_CONNECTIONS` / `RATE_LIMIT_WINDOW` | `5` / `1s` | SSH **auth attempts** per client network. The window is a Go duration (`1s`, `500ms`); the old `RATE_LIMIT_WINDOW_MS` fails the boot |
| `REGISTRATION_LIMIT` / `REGISTRATION_WINDOW` | `5` / `1h` | new accounts per client network (IPv6 /64); raise for `make loadtest` |
| `REGISTRATION_LIMIT_WIDE` | `20` | new accounts per IPv6 /48 per `REGISTRATION_WINDOW` (a free tunnel is one /48 of 65,536 /64s); IPv4 stays per address. Raise with `REGISTRATION_LIMIT` for `make loadtest` |
| `REGISTRATION_GLOBAL_LIMIT` | `600` | new accounts across every network per `REGISTRATION_WINDOW`; returning players never spend it. Raise for `make loadtest` |
| `DB_*` | see `.env.example` | Postgres; outside `ENV=development` `DB_SSLMODE` defaults to `require` and must be `require`, `verify-ca` or `verify-full`, whatever the host |
| `DB_MAX_OPEN_CONNS` | `25` | pool size, independent of the SSH cap |
| `API_PORT` | `6970` | stats API; reached only through nginx `/api/` |
| `API_ALLOW_ORIGIN` | `*` | the stats API's CORS origin |
| `API_REQUESTS_PER_MINUTE` | `120` | per client network |
| `API_TRUST_PROXY` | `false` | compose opts in; only safe behind a proxy that sets `X-Forwarded-For` itself |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://localhost:4317` (`https://` in production) | logs, metrics, traces. A URL: `http://` exports in plaintext, `https://` over TLS; a bare `host:port` and the removed `OTEL_EXPORTER_OTLP_INSECURE` fail the boot |
| `LOG_LEVEL` | `INFO` | stderr and OTLP both |

Boolean variables (`PROXY_PROTOCOL`, `API_TRUST_PROXY`) take what Go's
`strconv.ParseBool` does: `1`, `t`, `true`, `0`, `f`, `false` (also `True`/`TRUE`).
Anything else - `yes`, `off` - fails the boot rather than meaning one or the other,
and the error lists every invalid variable at once.

Compose itself reads two more, for the certificate: `TTY_DOMAIN` (default
`tty.cards`), the name `proxy-certs` and `certbot` issue for, and `ACME_EMAIL`
(optional), the contact on the ACME account; unset registers without one. The backup script's
`BACKUP_*` variables are under [Backups](#backups).

New accounts are separately capped at `REGISTRATION_LIMIT` per
`REGISTRATION_WINDOW` per client network, `REGISTRATION_LIMIT_WIDE` per IPv6 /48
and `REGISTRATION_GLOBAL_LIMIT` across all of them; a sign-up refused by one of the
three spends none of the others. Returning players never spend that budget, and
neither does a name that fails validation - that player is told why.
Usernames are unique regardless of case.

## Observability

Alloy -> Loki / Tempo / Prometheus -> Grafana, all in the same compose file, none
of it published beyond Grafana's loopback binding. The app **pushes** OTLP and
nothing scrapes it; Alloy exports only the host's metrics, and Prometheus scrapes
the pipeline itself (Alloy, Loki, Tempo, Alertmanager) so a dead component alerts
instead of going quiet. Alerts go to Alertmanager, which delivers them to Telegram
once the bot token and chat id are on the host ([`docs/SECURITY.md`](docs/SECURITY.md), "Alert delivery").

Retention is explicit: **logs 14 days** (`internal/config/loki/loki.yaml`),
**traces 48 hours** (`internal/config/tempo/tempo.yaml`), **metrics 30 days**
(`compose.yaml`). What is in them, per field, is in
[`docs/data-inventory.md`](docs/data-inventory.md).

## Self-hosting

1. A VM with Docker, with IPv6 enabled for user-defined networks (the `edge`
   network sets `enable_ipv6: true`, so an IPv6 player keeps their own address).
   12 GB / 6 cores is what `compose.yaml` is sized against; the stack itself wants
   7.75 GiB and 4 cores.
2. Move the host's own `sshd` off port 22 (`Port 2222` in `/etc/ssh/sshd_config`)
   and reconnect there - the game proxy owns 22.
3. Firewall: allow 22, 80 and 443, plus your admin SSH port from trusted
   addresses only. Do not open Postgres, Grafana, `6969` or `6970`.
4. DNS: point the apex (`TTY_DOMAIN`, default `tty.cards`) at the VM with A and
   AAAA records, **DNS only** (grey cloud) - see [TLS](#tls-on-the-apex).
5. `cp .env.example .env` and set `DB_PASSWORD`; compose refuses to start
   without it. Set `TTY_DOMAIN` if it is not `tty.cards` (and the two
   `server_name` lines and the `www` redirect in `internal/config/nginx.conf`),
   and optionally `ACME_EMAIL`. Compose sets `ENV=production` on the backend.
6. `docker compose up -d --build`, then `docker compose logs -f certbot` until it
   prints `installed the certificate`; nginx picks it up within a minute. Check
   with `curl -sI https://<domain>/api/v1/stats`.
7. Backups: schedule `scripts/backup.sh` and the drill ([Backups](#backups)),
   with an off-host `BACKUP_REMOTE`, and run `make backup restore-drill` once by
   hand now.
8. Smoke test: register a new key -> create a lobby -> play each game -> drop
   mid-hand -> reconnect -> check profile and leaderboard.

### TLS on the apex

The apex serves the stats API over https (the website on `www` calls it from an
https page) and answers the ACME challenge on http. nginx cannot start without a
certificate and Let's Encrypt cannot issue one before nginx answers, so:

- `proxy-certs` writes a self-signed placeholder into the `proxy-tls` volume on
  first start, and nginx boots with it;
- `certbot` (`internal/config/certbot/renew.sh`) requests the real certificate
  over the webroot, copies it into `proxy-tls`, and re-checks twice a day
  (renewing 30 days before expiry, retrying hourly after a failure);
- the `proxy` service reloads nginx when the certificate file changes.

Keep the `letsencrypt` volume: it holds the ACME account and the certificates, and
losing it means re-issuing against Let's Encrypt's weekly duplicate limit. HSTS
starts at `max-age=300`; once the first automatic renewal has gone through (about
60 days in) raise it in `nginx.conf` to `604800`, then `31536000`
(`docs/decisions.md`).

### Backups

`scripts/backup.sh` writes `backups/tty-cards-<UTC stamp>.tar`, holding the
database (`pg_dump` custom format) and the contents of the `ssh-keys` volume - the
host key every returning player's `known_hosts` pins. It keeps the newest
`BACKUP_KEEP` (14) archives, and with `BACKUP_REMOTE` set (an rclone `remote:path`,
configured in `RCLONE_CONFIG`, default `~/.config/rclone/rclone.conf`) copies each
one off the host through the pinned `rclone` image and keeps the same number there.
An archive on the same disk as the database is not a backup of it: set a remote,
and make it an rclone `crypt` remote - an archive is the whole user base plus the
host's private key. Needs only Docker on the host (and `ssh-keygen` for the drill).

```bash
make backup                                        # ./scripts/backup.sh
make restore-drill                                 # newest archive, throwaway container
make restore BACKUP=backups/tty-cards-<stamp>.tar  # replaces the live database and key
```

`scripts/restore-drill.sh` restores an archive into a throwaway Postgres (the db
service's image, no network) and fails unless the dump restores cleanly,
`schema_migrations` is clean and no newer than the checkout, `users` has rows
(`DRILL_MIN_USERS`) and the archive holds the host key, whose fingerprint it
prints. `scripts/restore.sh` is the real thing, on a fresh host or a running one:
it stops the backend, drops and recreates the database, restores the dump, puts
the key back and starts the stack, whose `migrate` then applies any newer
migrations. On a new host, clone the repo, write `.env`, copy the archive over and
run `scripts/restore.sh` before the first `up`.

Schedule both, from the repo root as the user that runs Docker. cron:

```cron
30 3 * * *  cd /srv/tty-cards && BACKUP_REMOTE=b2crypt:tty-cards ./scripts/backup.sh >> backups/backup.log 2>&1
0  5 * * 0  cd /srv/tty-cards && ./scripts/restore-drill.sh >> backups/drill.log 2>&1
```

or a systemd timer (`/etc/systemd/system/tty-cards-backup.{service,timer}`):

```ini
[Service]
Type=oneshot
WorkingDirectory=/srv/tty-cards
Environment=BACKUP_REMOTE=b2crypt:tty-cards
ExecStart=/srv/tty-cards/scripts/backup.sh
ExecStartPost=/srv/tty-cards/scripts/restore-drill.sh

[Timer]
OnCalendar=*-*-* 03:30:00
Persistent=true

[Install]
WantedBy=timers.target
```

`systemctl enable --now tty-cards-backup.timer`; a failed run shows in
`systemctl --failed` and the journal.

Notes worth reading before you deploy:

- **Registration is open on purpose.** Any public key is accepted. For a private
  community put it behind a firewall, a VPN or an allowlist -
  [`docs/SECURITY.md`](docs/SECURITY.md).
- Compose's Postgres serves TLS with a self-signed certificate the one-shot
  `db-certs` service makes on first start (remove the `db-tls` volume to rotate it),
  and the backend connects with `DB_SSLMODE=require`. Its `pg_hba.conf`
  (`internal/config/postgres/`) rejects plaintext TCP outright. For an external managed
  database prefer `verify-full` with its CA. Plaintext is accepted only with
  `ENV=development`; the removed `ALLOW_INSECURE_DB` fails the boot if set.
- The `edge` network's subnets (`172.29.69.0/24`, `fd6b:1e37:9a52:6969::/64`) are
  what the backend's `PROXY_TRUSTED_CIDRS` trusts. If they collide with a network
  on your host, change both in `compose.yaml` together.
- Grafana answers only to `Host: localhost` (DNS-rebinding protection): tunnel
  with `ssh -L 3000:127.0.0.1:3000 <host>` and open `http://localhost:3000`.

## Docs

Start at [`docs/README.md`](docs/README.md) - it says what each document is for
and the order to read them in.

| Doc | For |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | Topology, contracts, invariant list |
| [`docs/decisions.md`](docs/decisions.md) | The policies a later change has to keep |
| [`docs/onboarding.md`](docs/onboarding.md) | Product story, annotated file tree, day one |
| [`docs/CONTRIBUTING.md`](docs/CONTRIBUTING.md) | How to add a game, test conventions, PR norms |
| [`docs/SECURITY.md`](docs/SECURITY.md) | Reporting a vulnerability; what is in scope |
| [`docs/data-inventory.md`](docs/data-inventory.md) | Per-field personal-data inventory |
| [`docs/changelog.md`](docs/changelog.md) | What changed, in user-facing terms |
| [`web/src/legal/`](web/src/legal/) | The published privacy policy and terms |
| [`CLAUDE.md`](CLAUDE.md) / [`AGENTS.md`](AGENTS.md) | Terse briefs for coding agents |

## License

MIT - see [LICENSE](LICENSE).
