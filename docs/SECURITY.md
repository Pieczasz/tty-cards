# Security policy

tty cards is a hobby project run by volunteers. There is no bounty, no
dedicated security team, and no SLA. What there is: a private channel, an honest
answer, and a fix in the open.

## Reporting a vulnerability

**Use GitHub's private vulnerability reporting:**
<https://github.com/Pieczasz/tty-cards/security/advisories/new>

That opens a draft advisory only the maintainers can see. Please do not open a
public issue, a pull request, or a discussion for a vulnerability - a public
report is a public exploit while the fix is being written.

Include, as far as you can:

- what you did, against which host or which commit;
- what happened, and what you expected;
- the impact you believe it has (read someone else's hand, take someone else's
  seat, move someone else's Elo, take the process down, read the database);
- a proof of concept, if you have one.

If the report needs a test account, register one the ordinary way - the first
connection with a new SSH key claims a username.

## What to expect

| Step | Target |
|---|---|
| Acknowledgement that a human has read it | 5 working days |
| First assessment (accepted / not a vulnerability / need more) | 14 days |
| Fix on `main` for something exploitable | as fast as the maintainers can manage |
| Credit in the advisory and the changelog | on request, unless you ask not to be named |

We will tell you when the fix lands and when the advisory is published. If a
report goes quiet for more than 30 days, escalate by commenting on your own
advisory draft. There is no embargo period we will hold you to; disclosing after
a fix ships is welcome.

**No bounty.** There is no money, no swag, and no paid triage. This is a card
game.

## In scope

- **The SSH server** (`cmd/server`, `internal/ssh`, `internal/lobby`,
  `internal/game`, `internal/tui`) - authentication and identity
  (`internal/ssh/auth.go`), session ownership and displacement
  (`ssh.SessionTracker`), the rate limiters (`internal/ratelimit`), anything that
  lets a player see another player's hand, act as another player, take a seat
  that is not theirs, or move rating they did not earn. Also one session holding
  memory past its bound: the terminal is clamped to 120x40 (about 1.3 MiB a
  session) and stdin is budgeted (`internal/ssh/input.go`: 1 KiB/s after a 16 KiB
  burst, 32 KiB held by the input decoder, a paste included), so a single
  connection that grows the process is a finding even though volume DoS is not.
- **The stats API** (`internal/httpapi`) - it is read-only and unauthenticated on
  purpose. In scope: anything that turns it into a write path, leaks per-user
  data beyond the leaderboard the TUI already shows, or lets a client evade
  `API_REQUESTS_PER_MINUTE` by forging `X-Forwarded-For`.
- **The compose stack and the proxy config** (`compose.yaml`,
  `internal/config/nginx.conf`, `internal/config/*`) - a default that exposes a
  service, a credential with a default value, a container privilege that is not
  needed.
- **The persistence layer** (`internal/db`, `internal/repository`,
  `internal/db/migrations`) - SQL injection, a transaction that can be raced into
  inconsistent Elo, a missing constraint that lets bad rows in.

Reports against a **default configuration** are the most useful. If you had to
change a default to make it exploitable, say which one.

## Out of scope

These are known, deliberate, and documented; a report saying only this will be
closed as "working as intended":

- **Anyone can register.** Any SSH public key is accepted and the first
  connection claims a username (`internal/ssh/auth.go`). That is the point of a
  public demo. Running a private instance? Put it behind a firewall, a VPN, or an
  allowlist. Separate limiters cap new accounts per `REGISTRATION_WINDOW` (1h) at
  `REGISTRATION_LIMIT` (5) per client network, `REGISTRATION_LIMIT_WIDE` (20) per
  IPv6 /48 - one free tunnel - and `REGISTRATION_GLOBAL_LIMIT` (600) across every
  network, so this is a rate problem, not an open door. An attacker with enough
  networks can spend the global ceiling and hold off sign-ups (never logins) for
  the window; that is the trade for a bounded users table.
- **The SSH key fingerprint is the only credential.** Lose the key, lose the
  account. There is no password reset and no second factor.
- **Usernames are public**, on the in-game leaderboard and through
  `/v1/leaderboard`. So is the fact that two accounts played each other.
- **Denial of service by volume** against a host you do not operate. Please do
  not load-test the public server; `make loadtest` exists for your own.
- **Missing security headers on the static site**, or findings from an automated
  scanner with no demonstrated impact.
- **Self-XSS, clickjacking on a page with no state, and anything requiring a
  compromised client machine** - if the attacker already has the player's SSH
  key, the game is over by definition.
- **The reported version of a dependency** with no reachable call path.
  `govulncheck` runs in CI and is reachability-based; if you have a reachable
  path it missed, that *is* in scope - show the path.

## Hardening the deployment you run

If you self-host, the properties below are what the shipped configuration
assumes. Breaking one is how a safe deployment becomes an unsafe one.

- **Never publish `:6969`.** The backend trusts the PROXY protocol header for the
  client address, so a client that can reach it directly can forge that address
  and defeat every per-network limit. `compose.yaml` keeps it on the internal
  network; only nginx may reach it. Same argument for `:6970` and
  `API_TRUST_PROXY=true`: nginx sets `X-Forwarded-For` from `$remote_addr`, not
  from `$proxy_add_x_forwarded_for`, so a client cannot prepend its own value -
  but only while the client cannot reach the port.
- **Keep `PROXY_TRUSTED_CIDRS` matched to the proxy's network.** With it set, the
  backend honors a PROXY header only from those networks and refuses every other
  connection, and believes `X-Forwarded-For` only from them, so another container
  on the network cannot forge a client address either. Compose sets it to the
  `edge` subnets, where only nginx and the backend sit; change the list and the
  subnets together.
- **The only ports the stack publishes to the world are 22, 80 and 443**, all
  on nginx. Grafana is the single exception and is bound to `127.0.0.1:3000`; reach
  it with `ssh -L 3000:127.0.0.1:3000 <host>` and never bind it wider.
- **Grafana has no login at all** - anonymous Admin, login form off. That is safe
  only because the port is published on loopback and CI asserts it stays there:
  reaching it means an SSH session on the host, which already owns everything.
  Widening the binding without adding authentication is a vulnerability. It
  answers only to `Host: localhost` (`GF_SERVER_ENFORCE_DOMAIN`), which is what
  stops a DNS-rebinding page in the operator's browser from reaching it through
  the tunnel; open it as `http://localhost:3000`.
- **Set a strong, unique `DB_PASSWORD`.** Compose refuses to start without one,
  and with `ENV=production` the server refuses to boot without one. `ENV` itself
  must be `production`, `staging` or `development`; a typo fails the boot rather
  than running with every production check off. Boolean settings are parsed as
  strictly (`true`/`false`, `1`/`0`, `t`/`f`), so a mistyped `PROXY_PROTOCOL` or
  `API_TRUST_PROXY` - `off`, `yes` - fails the boot instead of quietly meaning one
  or the other. Outside `ENV=development`
  `DB_SSLMODE` must be `require`, `verify-ca` or `verify-full` for every host,
  compose's own `db` included (it serves TLS from the `db-certs` certificate, and
  its `pg_hba.conf` rejects a plaintext connection server-side).
- **Do not mount the Docker socket into anything.** Alloy reads container logs
  through `docker-socket-proxy`, which passes only the GETs Alloy makes (list,
  inspect, logs, networks, version), answers Alloy's address alone, and sits on an
  internal network: `:ro` on a socket restricts nothing, container inspect
  includes every container's environment, `DB_PASSWORD` among it, and a
  section-level allow (`CONTAINERS=1`) also serves `/containers/{id}/archive` and
  `/export` - any container's files. For the same reason Alloy does not mount the
  host's `/`.
- **Loki, Tempo, Prometheus, Alertmanager and Alloy have no authentication.** They
  publish no port; anything on the compose `default` network - every service but
  nginx and the socket proxy - can read every log and trace and write into them.
  Adding a service to that network is granting it that.
- **Keep the container restrictions.** Every service runs with
  `no-new-privileges`; the backend is `FROM scratch`, `read_only` and
  `cap_drop: [ALL]`; nginx keeps only the four capabilities its master process
  needs. Images are pinned by digest and CI actions by commit SHA, with the CI
  token read-only and Dependabot moving the pins.
- **Protect your backups, and keep one off the host.** `scripts/backup.sh` writes
  each archive `0600` and keeps the newest `BACKUP_KEEP` (14); `backups/` never
  enters the image build context. An archive is the whole user base, fingerprints
  included, **and the ssh host's private key**: whoever holds it can impersonate the
  server to every player. Send the off-host copy (`BACKUP_REMOTE`) to an rclone
  `crypt` remote, and run `scripts/restore-drill.sh` on a schedule - it proves an
  archive restores without touching the live database.
- **TLS on the apex.** nginx serves only TLS 1.2 and 1.3 with forward-secret AEAD
  ciphers and no session tickets, and sends HSTS (`max-age=300` to start; raise it
  once https has held). Plain http answers the ACME challenge and `/healthz` and
  redirects everything else. Until certbot has a certificate nginx serves a
  self-signed placeholder, which browsers reject - so a failed issuance shows, it
  does not downgrade. Keep the apex **DNS only** at Cloudflare: proxied, `ssh`
  would follow it to Cloudflare's addresses and fail.
- **No password in argv.** `migrate` gets the database password as `PGPASSWORD`,
  not in its URL, so it is not in `ps` or `docker inspect`'s `Args`; CI asserts it.
- **Keep the host's own `sshd` off port 22** (the game proxy owns it) and keep
  your admin SSH port firewalled to trusted addresses.
- **Keep the `ssh-keys` volume** across redeploys, or every client sees a
  host-key change - which is indistinguishable from an attack.

## Alert delivery

Alertmanager delivers every alert to Telegram. The bot token and chat id are files
on the host, outside the checkout so neither can be committed, and Alertmanager
reads them only when it sends. Until they are in place it still runs and shows
alerts at `alertmanager:9093`, logs each failed send, and Prometheus fires
`AlertmanagerNotificationsFailing`. To set it up:

1. Message [@BotFather](https://t.me/BotFather), send `/newbot`, and keep the token
   it gives you.
2. Send your new bot any message (or add it to a group), then read the chat id
   from `https://api.telegram.org/bot<token>/getUpdates`, under `"chat":{"id":...}`.
   A group's id is negative.
3. On the host, write both files where compose mounts them
   (`ALERTMANAGER_SECRETS_DIR`, default `/etc/tty-cards/alertmanager`). Alertmanager
   runs as `nobody` (65534):

   ```sh
   sudo install -d -m 0750 -g 65534 /etc/tty-cards/alertmanager
   printf '%s' '<token>' | sudo install -m 0440 -g 65534 /dev/stdin /etc/tty-cards/alertmanager/telegram_bot_token
   printf '%s' '<chat id>' | sudo install -m 0440 -g 65534 /dev/stdin /etc/tty-cards/alertmanager/telegram_chat_id
   ```

4. Restart it and send a test alert. It arrives after `group_wait` (30s), and
   again as resolved when it expires:

   ```sh
   docker compose up -d alertmanager
   docker compose exec alertmanager amtool --alertmanager.url=http://127.0.0.1:9093 alert add TestAlert severity=warn
   ```

   Nothing arriving? `docker compose logs alertmanager` names the failure: a file
   it cannot read, a chat id that is not a number, or Telegram refusing the token.

## Supported versions

There are no releases and no backports. The supported version is `main`; fixes
land there and are deployed from there.

## See also

- [`../web/src/legal/privacy.md`](../web/src/legal/privacy.md) - what is stored about a player and for how long
- [`data-inventory.md`](data-inventory.md) - the same
  question answered per file, for auditors
- [`architecture.md`](architecture.md) §2 - the layered limits and where each one
  is enforced
