# Data inventory

What this deployment collects about a person, where each item lives, and for how
long. Written for whoever drafts the privacy notice: every claim below cites the file
that makes it true, so it can be re-checked rather than believed.

Scope: the SSH game server (`cmd/server`), its stats API (`internal/httpapi`), the
observability stack (`compose.yaml` + `internal/config/`), and the marketing site
(`web/`). Accurate as of the tree this file is committed in.

## 1. Stored indefinitely - the database

One Postgres instance, `db` in `compose.yaml`, on an internal network with no
published port. Schema is `internal/db/migrations/`.

| Table.column | What it is | Why it exists |
|---|---|---|
| `users.username` | chosen names are 1-16 chars, `[A-Za-z0-9_]`, unique case-insensitively (`idx_users_username_lower`, migration `000006`; the display case is kept), **publicly displayed** on the in-game leaderboard and on the website. After erasure it reads `deleted_` plus 32 hex digits of the UUID (`db.AnonymisedUsername`, 40 chars) | identity |
| `users.id` | UUIDv7 primary key (`uuidv7()`); **this is also the `player_id` that appears in logs** (`internal/lobby/player.go`). Time-ordered, not a sequence. | joins |
| `users.last_seen_at` | last connection timestamp | activity |
| `users.created_at` / `updated_at` / `deleted_at` | account lifecycle; `deleted_at` is a GORM **soft** delete | lifecycle |
| `public_keys.fingerprint` | `SHA256:…` of the player's SSH public key, `NOT NULL UNIQUE` | the credential; the only thing authentication matches on |
| `public_keys.last_used_at` | last login timestamp | activity |
| `public_keys.name` | always the literal `"auto-generated key"` | nothing |
| `rankings.{user_id,game_id,elo,matches_played}` | per-game skill and volume | leaderboard, matchmaking |
| `matches.{game_id,ranked,created_at}` | one row per finished game | history |
| `match_participants.{match_id,user_id,placement,elo_delta}` | who played, where they placed | history |

Two things to say plainly in a notice:

- **The fingerprint is a cross-service correlator.** It is a hash, not the key, and
  the raw public key is never stored - `internal/ssh/auth.go` turns it into a digest
  at the door and the repository interface (`internal/db/repository.go`) only accepts
  a string. But the same SSH key is usually the one on GitHub, so the fingerprint is
  stable and linkable across services. It is authentication data, not a pseudonym.
- **`matches` + `match_participants` is a social graph.** Joined on `created_at` it
  answers who played with whom and when.

**Retention: none is configured.** Account rows, keys and ratings have no expiry.
They go when the account is erased. Match history is kept, anonymised: other
players at those tables still have a name to resolve.

**Erasure: `db.Profiles.DeleteAccount(ctx, userID)`**, implemented by
`eraseUser` in `internal/repository/user.go`, one transaction. Reached from the
Profile screen with `x` and a typed `DELETE`; refused while the player is seated at a
table, and the session ends afterwards.

| Row | What happens | Why |
|---|---|---|
| `public_keys` | **hard**-deleted (`Unscoped`) | the fingerprint column is unique, so a soft-deleted row would lock the person out of ever registering again. With the keys gone, nothing can authenticate as the account |
| `rankings` | **hard**-deleted (`Unscoped`) | a soft-deleted ranking still holds the `(user_id, game_id)` primary key |
| `users` | **kept, anonymised**: `username` -> `db.AnonymisedUsername(userID)` (`deleted_` + 32 hex), `last_seen_at` -> NULL | it is the parent of `match_participants` rows belonging to *other* players, whose history has to keep resolving to a name |
| `match_participants` | untouched | same reason; the erased player shows as `deleted_` plus hex |

Two details a notice should state plainly: erasure is **not** reversible and does not
remove the person from other players' match history, only their name from it; and the
leaderboard cache is cleared wholesale on deletion, because its 5-minute TTL would
otherwise be five more minutes of an erased name on screen.

A ranking is never re-created for an erased account. The erasure takes the same
per-seat advisory lock a ranked finalize holds, and a finalize that ran alongside it
skips the anonymised seat (`unerasedSeats`, `internal/repository/match.go`): that
seat's participant row is written with a delta of 0 and no ranking. The cache also
carries a generation the erasure bumps, so a leaderboard read that started before it
cannot store the erased name again. The `users` update is `Unscoped`, so an account an
operator soft-deleted is anonymised too.

## 2. Processed transiently - never written to disk

| Item | Where | Lifetime |
|---|---|---|
| Client IP, as a rate-limit key | `internal/ratelimit` - an in-memory map, IPv6 collapsed to its /64 by `NetKey` | one window: 1s for SSH auth, **1 hour for new-account registration** (`REGISTRATION_WINDOW`, `internal/config/config.go`), 60s for the API. Swept every 64 calls, capped at 10 000 keys |
| Client IP, from the PROXY protocol header | `cmd/server` -> `github.com/pires/go-proxyproto` | the TCP connection |
| Client IP, from `X-Forwarded-For` | `internal/httpapi` `clientIPFunc`, only when `API_TRUST_PROXY=true`, and with `PROXY_TRUSTED_CIDRS` set only from a peer inside it | the request |

Nothing here is persisted or exported. The limiter's map is the whole of it.

## 3. Reaches the observability stack

Everything below is on the compose networks; none of these services publishes a
port (`docker compose config` - only `proxy` 22/80 and Grafana on `127.0.0.1:3000`).

### Logs -> Loki. **14 days** (`internal/config/loki/loki.yaml`, `retention_period: 336h`, compactor retention enabled)

Two sources reach Loki:

1. **The application**, via `slog` -> the OTLP bridge (`cmd/server/main.go` installs a
   MultiHandler: JSON to stderr *and* `otelslog`). The stderr copy is dropped in Alloy
   (`internal/config/alloy/config.alloy`, `stage.drop`) so structured records are not
   stored twice.
2. **Container stdout/stderr** for everything that is not a structured record -
   panic dumps, Postgres, nginx.

Personal data in those logs today:

- **Client network** under `client_net`, at **INFO on every connect and every
  disconnect**: `internal/ssh/server.go` (`clientNet`, which is `ratelimit.NetKey` with
  `"unknown"` as the fallback). This is a complete connection log of the user base with
  durations, but at /64 granularity for IPv6 and full-address granularity for IPv4 -
  where a single address is already what a household shares.
- **Full `remote_addr` on WARN and ERROR only** - an unkeyable address, a rate-limit
  rejection, a failed session, a panic. Abuse investigation keeps what it needs; the
  routine path does not.
- **`client_version`** alongside both - the SSH client string, a weak fingerprinting
  signal.
- **`player_id` = `users.id`**, at INFO and above, across `internal/lobby/disconnect.go`
  (disconnect, grace, resume), `internal/game/turnclock.go`, `internal/elo/elo.go`,
  `internal/tui/views/gameview/session.go` and `internal/tui/views/lobby/lobby.go`, and as
  map keys in the per-hand summaries in `internal/game/hearts/trick.go` and
  `internal/game/ginrummy/rules.go`.
- **Lobby codes** in `internal/lobby/finalize.go`, `manager.go` and `watch.go` - a live
  join code for a private room, which is a secret more than it is personal data.
- **Not logged anywhere: the SSH fingerprint and the username.** `internal/ssh/auth.go`
  logs neither, including on its failure paths.

nginx's access logs carry no address: `internal/config/nginx.conf` sets a
`log_format privacy` that omits `$remote_addr` and the User-Agent (the compiled-in
default was `combined`, which has both), and the `stream` block states
`access_log off`. Its **error log** does name the client on every line
(`client: 203.0.113.7`). It goes to stderr at `error` and above only (`error_log stderr
error`); the `limit_*` rejections, the one high-volume source of such lines, are
logged at `warn`, below that threshold; and Alloy rewrites the `client:` field on the
proxy's lines to `redacted` before they reach Loki (`stage.replace`,
`internal/config/alloy/config.alloy`). What is left is which upstream failed and how.

### Traces -> Tempo. **48 hours** (`internal/config/tempo/tempo.yaml`, `block_retention: 48h`)

Deliberately the shortest retention here, because Tempo used to hold the strongest
link in the system: the `ssh.session` span carried the client address and the username
together, so a trace joined an IP to an account to a session duration. **It no longer
carries the address.** `startSession` (`internal/ssh/server.go`) sets only
`client_version` and the terminal size; `finishSession` adds `user` at the end. The
comment in `startSession` states the reason.

What remains: the username on `ssh.session` for 48h, and `user_id` on repository spans
(`internal/repository/user.go`, including `db.DeleteAccount`).

**The stats API makes no spans at all** (`internal/httpapi/httpapi.go`: `otelhttp` runs
with a noop tracer provider). Every website visitor polls it, and a span per request
put each visitor's address and User-Agent into Tempo. `internal/httpapi/telemetry_test.go`
`TestHandler_ProducesNoSpans` asserts it.

### Metrics -> Prometheus. **30 days** (`compose.yaml`, `--storage.tsdb.retention.time=30d`, plus an 8GB size cap)

**No personal data**, and that is enforced rather than asserted:
`internal/observability/metrics_test.go` collects every instrument and fails if any
attribute key is outside `{outcome, limiter, game_type, ranked, reason, stream}`.
otelhttp's `http.server.*` metrics for the stats API get the same treatment in
`internal/httpapi/telemetry_test.go`: only the method, status, route, scheme and
protocol keys and `server.address`, which `otelhttp.WithServerName("stats-api:80")`
pins to `stats-api` rather than the client's `Host` header, with no `server.port`
series from it either. Alloy's host exporter adds machine metrics only.

### Docker's own log files

`compose.yaml` caps every container at 3 × 10 MB of JSON (`x-logging`). That is a size
bound, not a time bound; Loki's 14 days is the retention that counts.

## 4. The website

`web/` is a static Astro site on GitHub Pages behind Cloudflare.

- **No cookies, no localStorage, no analytics or tracking script of any kind** - no
  Google Analytics, Plausible, Umami, Fathom, Sentry, PostHog, Matomo, Clarity or
  Hotjar. Dependencies are build-time only.
- **No third-party fonts or CDN**: `web/src/styles/global.css` uses a local system
  font stack; icons and images are self-hosted.
- **Exactly one external origin at runtime**: `https://tty.cards/api`.
  `web/src/components/LiveStats.astro` fetches `/v1/stats` and
  `/v1/leaderboard?limit=5` - GET, no credentials - lazily on scroll, then every 30s
  while the tab is visible.
- Consequences to state anyway: that fetch sends the visitor's IP to our API on a 30s
  cadence (used only as a rate-limit key, §2), the leaderboard response contains
  **usernames**, and GitHub Pages and Cloudflare see visitor IPs under their own
  policies regardless of anything in this repo.

## 5. What the public API exposes

`internal/httpapi` is read-only, unauthenticated, and returns only what any player
already sees in the TUI: aggregate counts (`/v1/stats`) and the top of the leaderboard
- rank, username, game, Elo (`/v1/leaderboard`, `limit` 1-200, default 5). No
per-user endpoint, no writes, no auth.

## 6. Retention, in one table

| Data | Store | Kept | Set in |
|---|---|---|---|
| Account, keys, ratings | Postgres | until erasure | no expiry job exists; erasure is §1 |
| Match history | Postgres | kept, anonymised on erasure | `match_participants` is not deleted |
| Application and container logs | Loki | **14 days** | `internal/config/loki/loki.yaml` (`retention_period: 336h`, compactor retention on) |
| Traces | Tempo | **48 hours** | `internal/config/tempo/tempo.yaml` (`block_retention: 48h`) |
| Metrics | Prometheus | **30 days**, 8 GB cap | `compose.yaml` (`--storage.tsdb.retention.time=30d`) |
| Container stdout on disk | Docker json-file | 3 × 10 MB, size-bound not time-bound | `compose.yaml` `x-logging` |
| Database backups (optional) | the host's `backups/` | **14 days** (`RETENTION_DAYS`), written `0600` (`umask 077`), never in the image build context | `scripts/backup.sh`, `.dockerignore` |
| Rate-limit counters | process memory | one window (1s SSH auth, 1h registration, 60s API) | `internal/ratelimit` |
| Live session and table state | process memory | until disconnect / match end | - |

## 7. Known gaps, for whoever picks them up

1. **`player_id` (= `users.id`) at INFO across lobby, engine and TUI logs**, for 14
   days. It survives account deletion, because the `users` row does. Correlating it
   back to a person needs database access, so this is a lower-grade identifier than
   what was here before - but it is still one. (lobby / game)
2. **Lobby codes at INFO** - `internal/lobby/finalize.go`, `manager.go`, `watch.go`. A live join code
   for a private room is a secret more than it is personal data. (lobby)
3. **The username on the `ssh.session` span** for 48h. Nothing joins it to an address
   any more, so this is the weakest of the three and may be worth keeping. (ssh)
