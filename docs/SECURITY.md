# Security policy

Terminal Card is a hobby project run by volunteers. There is no bounty, no
dedicated security team, and no SLA. What there is: a private channel, an honest
answer, and a fix in the open.

## Reporting a vulnerability

**Use GitHub's private vulnerability reporting:**
<https://github.com/Pieczasz/terminal-card/security/advisories/new>

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
  that is not theirs, or move rating they did not earn.
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
  allowlist. A separate limiter caps new accounts at `REGISTRATION_LIMIT` per
  `REGISTRATION_WINDOW` (5 per hour by default) per client network, so this is a
  rate problem, not an open door.
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
- **The only ports the stack publishes to the world are 22 and 80**, both on
  nginx. Grafana is the single exception and is bound to `127.0.0.1:3000`; reach
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
  than running with every production check off. In production `DB_SSLMODE` must
  be `require`, `verify-ca` or `verify-full` for any host outside the compose
  network.
- **Do not mount the Docker socket into anything.** Alloy reads container logs
  through `docker-socket-proxy`, which answers GET only for containers and
  networks, on an internal network it shares with Alloy alone: `:ro` on a socket
  restricts nothing, and container inspect includes every container's
  environment, `DB_PASSWORD` among it.
- **Keep the container restrictions.** Every service runs with
  `no-new-privileges`; the backend is `FROM scratch`, `read_only` and
  `cap_drop: [ALL]`; nginx keeps only the four capabilities its master process
  needs. Images are pinned by digest and CI actions by commit SHA, with the CI
  token read-only and Dependabot moving the pins.
- **Protect your backups.** `scripts/backup.sh` writes each dump `0600`, keeps
  `RETENTION_DAYS` (14) days of them, reads only the keys it needs from `.env`
  rather than sourcing it, and `backups/` never enters the image build context. A
  dump is the whole user base, fingerprints included.
- **Keep the host's own `sshd` off port 22** (the game proxy owns it) and keep
  your admin SSH port firewalled to trusted addresses.
- **Keep the `ssh-keys` volume** across redeploys, or every client sees a
  host-key change - which is indistinguishable from an attack.

## Supported versions

There are no releases and no backports. The supported version is `main`; fixes
land there and are deployed from there.

## See also

- [`privacy.md`](privacy.md) - what is stored about a player and for how long
- [`data-inventory.md`](data-inventory.md) - the same
  question answered per file, for auditors
- [`architecture.md`](architecture.md) §2 - the layered limits and where each one
  is enforced
