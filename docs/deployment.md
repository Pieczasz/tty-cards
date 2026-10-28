# Deploying tty cards on a VPS

A step-by-step runbook from a fresh Ubuntu 24.04 VPS to `ssh <domain>` working for
everyone, hardened. Every command is meant to be pasted after you replace the
placeholders:

| Placeholder | Meaning |
|---|---|
| `<domain>` | the apex name players `ssh` to (`TTY_DOMAIN`; this project's own is `tty.cards`) |
| `<admin>` | the sudo-capable account you administer the VPS with |
| `<acme-email>` | where Let's Encrypt warns you if renewals stop (optional) |
| `<ipv4>`, `<ipv6>` | the VPS's addresses, from the provider's panel |

It assumes the packages and Docker are installed and `<admin>` has sudo; steps 1, 2 and 5 check
that and fix only what is missing. The reasoning behind each setting is in
[`SECURITY.md`](SECURITY.md) (threat model and checklist) and [`../README.md`](../README.md)
("Self-hosting", every environment variable). This page is the order to do things in.

## Hardening checklist

Tick these before announcing the server. Each is a step below; each is also a check in
[`SECURITY.md`](SECURITY.md).

- [ ] The host's own `sshd` is on another port (2222 here), key-only, root login off. **Port 22 is the
      game's**: a system sshd still on 22 makes the proxy fail to bind (step 3).
- [ ] `ufw` allows only 22, 80, 443 and your admin port; the admin port is rate-limited or
      restricted to your address (step 4). Docker bypasses ufw for published ports, so ufw is not
      what protects a container port.
- [ ] `docker compose config` publishes exactly 22, 80, 443 and `127.0.0.1:3000`. **Never publish
      `6969` or `6970`**: the PROXY header / `X-Forwarded-For` they trust would be forgeable and
      every per-network limit defeated (step 11 checks the open ports from outside).
- [ ] Grafana stays on loopback (it has no login) and is reached over an SSH tunnel.
- [ ] `.env` is `0600`, holds a strong unique `DB_PASSWORD`, and is backed up in a password manager.
      Nothing secret is committed.
- [ ] The stack runs as a dedicated user that nobody logs in as; the checkout cannot push (step 6, 7).
- [ ] The apex is **DNS only (grey cloud)** if your DNS is on Cloudflare; a proxied record cannot
      carry ssh (step 10).
- [ ] Alertmanager has its Telegram token and chat id on the host, outside the checkout, and a test
      alert arrived (step 8, 11).
- [ ] Backups run from cron, a copy leaves the host through an rclone `crypt` remote, and the restore
      drill passed once. **The archive holds the SSH host private key**: whoever has it can
      impersonate your server (step 12).
- [ ] The `ssh-keys` volume survives redeploys, or every player sees a host-key change that looks like an
      attack. Never `docker compose down -v`.

**Known limits to plan around.** Compose only *marks* a container unhealthy; nothing restarts it for that
(`restart: unless-stopped` covers crashes, not hangs), so watch `docker compose ps` or run an auto-healer.
`internal/config/nginx.conf` answers any `Host` but still redirects the apex's non-API paths to
`https://www.tty.cards`: change that one line (marked `SELF-HOSTERS`) to your own site, or to
`return 404;`. Nothing serves `web/`; the website is a static Astro site you host separately (GitHub
Pages, for the project's own).

---

## What you end up with

| Name | Points at | Cloudflare proxy | Serves |
|---|---|---|---|
| `www.<domain>` (optional) | wherever your website is hosted (GitHub Pages for the project's own) | proxied is fine | the website; the apex redirects to it |
| `<domain>` | the VPS | **DNS only (grey)** | `ssh <domain>` on 22, the stats API on 443, ACME and redirects on 80 |

On the VPS:

| Port | Who | Open to |
|---|---|---|
| 22 | nginx in Docker -> the game | everyone |
| 80, 443 | nginx in Docker -> ACME, redirect to www, `/api/` | everyone |
| 2222 | the host's own sshd, for you | you (key only) |
| 127.0.0.1:3000 | Grafana | loopback only, reached through your SSH tunnel |

Everything else - Postgres, the backend's 6969/6970, Loki, Tempo, Prometheus,
Alertmanager - stays on Docker's internal networks and is never published.

The apex has to be DNS only. A proxied record resolves to Cloudflare's addresses,
which forward only HTTP, so `ssh <domain>` would fail.

---

## 1. Check what is already there

Log in as `<admin>` and run:

```sh
lsb_release -ds                                  # Ubuntu 24.04.x LTS ("No LSB modules" is harmless)
docker --version; docker compose version         # Docker 27 or newer, Compose v2 or newer
git --version; make --version | head -1; command -v ssh-keygen
ufw version | head -1
systemctl is-enabled unattended-upgrades         # enabled
ip -6 addr show scope global                     # lists <ipv6>
timedatectl show -p Timezone --value             # UTC
```

Fix only what a line shows is missing:

- **A tool is missing:** `sudo apt install -y git make openssh-client ufw unattended-upgrades`.
- **unattended-upgrades is not `enabled`:** `sudo dpkg-reconfigure -f noninteractive unattended-upgrades`
  for security updates, with no automatic reboot.
- **No IPv6 address:** enable IPv6 for the VPS in the provider's panel. The `edge`
  network gives IPv6 players their real address, and the AAAA record below needs one.
- **The timezone is not UTC:** `sudo timedatectl set-timezone UTC`, so log and backup
  timestamps line up with the containers'.
- **Docker is missing or older than 27:** see step 5.

Then bring the system up to date, and reboot if that installed a new kernel:

```sh
sudo apt update && sudo apt full-upgrade -y
[ -f /var/run/reboot-required ] && sudo reboot
```

## 2. Your admin account

`<admin>` is the account you administer the VPS with. Before step 3 turns off root
and password logins, make sure it does not need either:

```sh
id <admin>                          # groups include sudo
sudo -v                              # works with <admin>'s password
cat ~/.ssh/authorized_keys           # holds your laptop's public key
```

If `authorized_keys` is missing or holds a different key, add yours. On your laptop,
if you have none yet: `ssh-keygen -t ed25519 -f ~/.ssh/tty-cards-admin`. Then, on the
VPS as `<admin>`:

```sh
install -d -m 700 ~/.ssh
nano ~/.ssh/authorized_keys          # paste the contents of tty-cards-admin.pub
chmod 600 ~/.ssh/authorized_keys
```

**Keep your current session open**, and from a second terminal on your laptop check
that the key alone gets you in:

```sh
ssh -i ~/.ssh/tty-cards-admin -o PasswordAuthentication=no <admin>@<ipv4>
```

If you log in as root today, do this before going further: after step 3, root can
no longer log in over SSH, and `<admin>` with its key is the only way in.

## 3. Move sshd to 2222 and lock it down

Port 22 belongs to the game. This also turns password logins off, so `<admin>` must
already have a working key - without one, the next login needs the provider's
console. As `<admin>`, in a session you logged into **with your key**, check first:

```sh
grep -c '^ssh-' ~/.ssh/authorized_keys 2>/dev/null || echo "NO KEY - stop, and do step 2 first"
```

It must print a number of 1 or more. Only then:

```sh
sudo tee /etc/ssh/sshd_config.d/10-tty-cards.conf >/dev/null <<'EOF'
Port 2222
PermitRootLogin no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
AllowUsers <admin>
MaxAuthTries 3
X11Forwarding no
EOF
sudo sshd -t                      # prints nothing when the config is valid
sudo ufw allow 2222/tcp           # before anything listens there, so you cannot lock yourself out
sudo systemctl daemon-reload      # Ubuntu 24.04 starts sshd from ssh.socket; this re-reads Port
sudo systemctl restart ssh.socket 2>/dev/null || sudo systemctl restart ssh
sudo ss -tlnp | grep -E ':(22|2222)\b'   # only 2222 should be sshd now
```

From a **new** laptop terminal, before closing the old ones:

```sh
ssh -p 2222 -i ~/.ssh/tty-cards-admin <admin>@<ipv4>
```

Then add this to `~/.ssh/config` on your laptop, so admin access is `ssh tty-admin`
and never collides with the game on `<domain>:22`:

```sshconfig
Host tty-admin
    HostName <ipv4>
    Port 2222
    User <admin>
    IdentityFile ~/.ssh/tty-cards-admin
    IdentitiesOnly yes
```

## 4. Firewall

```sh
sudo ufw default deny incoming
sudo ufw default allow outgoing
sudo ufw limit 2222/tcp comment 'admin ssh'   # rate-limits repeated connects
sudo ufw allow 22/tcp  comment 'game ssh'
sudo ufw allow 80/tcp  comment 'acme + redirect'
sudo ufw allow 443/tcp comment 'stats api'
sudo ufw enable
sudo ufw status verbose
```

If your home address is static, tighten the admin rule to it:
`sudo ufw delete limit 2222/tcp && sudo ufw allow from <your-ip> to any port 2222 proto tcp`.

Know one thing about Docker and ufw: ports Docker publishes skip ufw's rules,
because Docker writes its own iptables chain ahead of them. That is fine here
because `compose.yaml` publishes exactly 22, 80 and 443 (plus Grafana on loopback),
and CI fails if that changes. But it means **ufw is not what protects a container
port** - never add a `ports:` entry you would not open to the whole internet.

## 5. Docker

If step 1 showed Docker 27 or newer with Compose v2, skip the install and only add
the daemon settings below. Otherwise install Docker from Docker's own repository,
not Ubuntu's older `docker.io` package:

```sh
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null
sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
```

Daemon settings, in both cases. Look at `/etc/docker/daemon.json` first: if it already
exists, merge these keys into it rather than overwrite it.

```sh
sudo cat /etc/docker/daemon.json 2>/dev/null
sudo tee /etc/docker/daemon.json >/dev/null <<'EOF'
{
  "live-restore": true,
  "log-driver": "json-file",
  "log-opts": { "max-size": "10m", "max-file": "3" }
}
EOF
sudo systemctl restart docker
docker info --format '{{.LiveRestoreEnabled}} {{.LoggingDriver}}'   # true json-file
```

`live-restore` keeps containers running while the Docker daemon itself restarts
(an upgrade from unattended-upgrades, say). The log limits match the ones every
compose service already sets, for anything started outside compose.

Keep `<admin>` out of the `docker` group (`groups` should not list it): the stack
runs as its own user, next.

## 6. A service account that only runs the stack

The stack runs as `ttycards`, which has no password, no sudo and no SSH login
(`AllowUsers <admin>` above). You switch to it with `sudo`.

```sh
sudo adduser --system --group --home /home/ttycards --shell /bin/bash ttycards
sudo usermod -aG docker ttycards
sudo install -d -o ttycards -g ttycards -m 750 /srv/tty-cards
```

The checkout's directory name is compose's project name, which prefixes every
container and volume (`tty-cards-backend-1`, `tty-cards_db-data`), so keep it
`/srv/tty-cards`.

Being in the `docker` group is root-equivalent - anyone in it can start a privileged
container. That is why nothing else runs as this user and nobody logs in as it.

## 7. Clone the repository, read-only

The repository is public, so the VPS clones it over HTTPS with **no credentials at
all**. With nothing to authenticate with, the machine can pull but cannot push. Make
a push fail loudly too:

```sh
sudo -iu ttycards
git clone https://github.com/Pieczasz/tty-cards.git /srv/tty-cards   # or your fork
cd /srv/tty-cards
git remote set-url --push origin no-push-from-this-host
git log -1 --oneline
```

**Already downloaded the repository somewhere else** - in your home directory, say,
or with a GitHub token or key that can push? Do not deploy from that copy. Clone the
fresh one above. Then check what the old copy could do and remove it:

```sh
git -C ~/tty-cards remote -v         # a token in the URL, or an SSH remote, can push
rm -rf ~/tty-cards                   # once nothing in it is yours to keep
```

If that copy held a GitHub token or key, revoke it on GitHub as well (Settings ->
Developer settings -> Personal access tokens, or Settings -> SSH keys).

If the repository ever becomes private, use a **read-only deploy key**. Run
`ssh-keygen -t ed25519 -f ~/.ssh/deploy -N ""` as `ttycards`. On GitHub, go to
the repository's Settings -> Deploy keys -> Add, paste `~/.ssh/deploy.pub`, and
leave "Allow write access" **unticked**. Then clone `git@github.com:Pieczasz/tty-cards.git`
with `GIT_SSH_COMMAND='ssh -i ~/.ssh/deploy -o IdentitiesOnly=yes'`.

Deploy a tag rather than whatever `main` is at that moment, once you have tagged
one: `git fetch --tags && git checkout v0.1.0`.

## 8. Secrets

Still as `ttycards`, in `/srv/tty-cards`. Compose reads `.env` only to fill in
its own variables; the backend's settings are fixed in `compose.yaml` (`ENV=production`,
`DB_SSLMODE=require`, the limits). So `.env` needs only:

```sh
umask 077
cat > .env <<EOF
DB_PASSWORD=$(openssl rand -base64 36 | tr -d '/+=\n')
TTY_DOMAIN=<domain>
ACME_EMAIL=<acme-email>
EOF
chmod 600 .env
exit                      # back to <admin>
```

`TTY_DOMAIN` defaults to `tty.cards`: **set `TTY_DOMAIN=<domain>` in `.env` if yours differs** (it names the
certificate certbot requests and the placeholder one). `ACME_EMAIL` is where Let's Encrypt warns
you if renewals ever stop. Back up `.env` somewhere safe (a password manager): the
database password is needed to restore.

**Telegram alerts:** write the bot token and chat id now, as `<admin>`. Follow
[`SECURITY.md`](SECURITY.md), "Alert delivery", steps 1-3 (BotFather, `getUpdates`,
the two `install` commands into `/etc/tty-cards/alertmanager`). Alertmanager starts
without them, but then nothing reaches you.

## 9. Start the stack

As `ttycards`:

```sh
sudo -iu ttycards
cd /srv/tty-cards
docker compose config --quiet && echo config ok
SERVICE_VERSION=$(git rev-parse --short HEAD) docker compose up -d --build
docker compose ps         # wait until db, backend and proxy report (healthy)
```

`SERVICE_VERSION` stamps the commit on every log line, trace and metric, so you can
tell deploys apart in Grafana.

Every service has a hard `mem_limit` and a `cpus` ceiling: 7.75 GiB and 4 cores
together, out of the 12 GiB and 6 cores the stack is sized for. That leaves the host
its own headroom. Check what they actually use with `docker stats --no-stream`.

nginx starts with a self-signed placeholder certificate. The real one needs the DNS
change below.

## 10. Point the domain at the VPS

At your DNS provider (if it is Cloudflare: dashboard -> your zone -> DNS -> Records):

1. Remove any `A`/`AAAA` records the apex had (a previous host, GitHub Pages' addresses).
2. Add `A`, name `@`, content `<ipv4>`, proxy status **DNS only (grey cloud)**, TTL Auto.
3. Add `AAAA`, name `@`, content `<ipv6>`, proxy status **DNS only (grey cloud)**, TTL Auto.
4. Leave `www` (if you have a website) wherever it points; it may be proxied.

**The apex must not be proxied.** A proxied record resolves to Cloudflare's addresses, which forward
only HTTP, so `ssh <domain>` times out. Do not add a CAA record naming only Let's Encrypt if a
proxied `www` is issued by other authorities: it would break their renewals.

Then, on the VPS, ask certbot to try straight away instead of at its next hourly retry:

```sh
docker compose restart certbot
docker compose logs -f certbot      # until: installed the certificate
```

nginx reloads on the new certificate within a minute. If certbot fails, check that
`dig +short <domain> @1.1.1.1` already returns `<ipv4>` before restarting it again.
Let's Encrypt allows five failed validations per hour for one name, and on its own
certbot retries hourly.

## 11. Test it

From your laptop:

```sh
# DNS points at the VPS, over Cloudflare's resolver
dig +short <domain> A @1.1.1.1; dig +short <domain> AAAA @1.1.1.1

# a Let's Encrypt certificate, not the placeholder
openssl s_client -connect <domain>:443 -servername <domain> </dev/null 2>/dev/null \
  | openssl x509 -noout -issuer -enddate

curl -s https://<domain>/api/v1/stats                  # JSON counts
curl -s 'https://<domain>/api/v1/leaderboard?limit=5'  # JSON, empty at first
curl -sI http://<domain>/ | head -3                    # 301 -> https://<domain>/
curl -sI https://<domain>/ | grep -i location          # -> your www (or 404 if you changed it)
curl -s -o /dev/null -w '%{http_code}\n' https://<domain>/api/healthz   # 404, by design

# only 22, 80, 443 and your 2222 answer
for p in 22 80 443 2222 3000 5432 6969 6970 9090; do nc -z -w 3 <ipv4> $p && echo "$p open" || echo "$p closed"; done
```

The last loop must print `open` for 22, 80, 443 and 2222 only. Anything else open
means a port is published that should not be: stop and find it before going further.

Then the game itself:

1. `ssh <domain>`. The first time, ssh asks you to trust the host key. Note its
   fingerprint: it never changes as long as the `ssh-keys` volume survives, and a
   change later means something is wrong.
2. Register, open a lobby, and play a hand with a second account: a second key,
   `ssh -i ~/.ssh/other_key -o IdentitiesOnly=yes <domain>`.
3. Drop one connection mid-hand, reconnect within 90 seconds, and check you are back
   in the seat. Then check the profile and leaderboard.
4. If you host the website: its live panel shows players online and the leaderboard (it reads
   `https://<domain>/api/v1/...`; the build's `PUBLIC_API_BASE` points it at your domain).
5. Telegram: send the test alert from [`SECURITY.md`](SECURITY.md), "Alert delivery",
   step 4. It arrives about 30 seconds later.
6. Grafana: `ssh -N -L 3000:127.0.0.1:3000 tty-admin`, then open
   <http://localhost:3000>. The dashboards should already show your sessions.

## 12. Backups

As `ttycards`, in `/srv/tty-cards`:

```sh
make backup             # writes backups/tty-cards-<stamp>.tar
make restore-drill      # proves it restores, without touching the live database
crontab -e
```

```cron
30 3 * * *  cd /srv/tty-cards && ./scripts/backup.sh >> backups/backup.log 2>&1
0  5 * * 0  cd /srv/tty-cards && ./scripts/restore-drill.sh >> backups/drill.log 2>&1
```

A backup on the same disk as the database protects against mistakes, not against
losing the VPS. To keep a copy off the host, configure an rclone `crypt` remote and
add `BACKUP_REMOTE=<remote>:tty-cards` to the backup line (README, "Backups"). That
provider then receives personal data (usernames, key fingerprints, match history, the host
key), so name it in your own privacy policy first.

## 13. Load test (before you announce it)

`cmd/loadtest` needs to reach the backend directly: through nginx, one network gets
eight connections. Don't publish 6969 to the world. Bind it to the VPS's loopback
with a temporary override, then tunnel to it from a **different machine** than the VPS:

```yaml
# /srv/tty-cards/compose.load.yaml - delete it when you are done
services:
  backend:
    ports: !override
      - "127.0.0.1:6969:6969"
    environment:
      - PROXY_PROTOCOL=false
      - RATE_LIMIT_CONNECTIONS=50
      - RATE_LIMIT_WINDOW=1s
      - REGISTRATION_LIMIT=100000
      - REGISTRATION_LIMIT_WIDE=100000
      - REGISTRATION_GLOBAL_LIMIT=100000
```

```sh
# on the VPS, as ttycards
docker compose -f compose.yaml -f compose.load.yaml up -d backend

# on the load machine
ssh -N -L 6969:127.0.0.1:6969 tty-admin &
go run ./cmd/loadtest -addr 127.0.0.1:6969 -sessions 1000 -play-fraction 0.4 \
  -table-size 4 -menu-size 120x40 -ramp 120s -hold 10m -prefix vps1000
# meanwhile, on the VPS: docker stats --no-stream tty-cards-backend-1 (every few seconds)

# afterwards, on the VPS: back to the production settings
rm compose.load.yaml && docker compose up -d backend
```

The 1,000 bot accounts it registers stay in the database, so run it before players
arrive and then start the database empty. This keeps the `ssh-keys` volume, so the
host key players will pin stays the same:

```sh
docker compose down
docker volume rm tty-cards_db-data
docker compose up -d
```

 On the cell-budget and capacity numbers behind `MAX_CONNECTIONS`, see
[`decisions.md`](decisions.md) #60 and #61.

## 14. Deploying an update

As `ttycards`, in `/srv/tty-cards`:

```sh
make backup                                  # before anything that might migrate
git fetch --tags && git checkout v0.2.0      # or: git pull --ff-only
SERVICE_VERSION=$(git rev-parse --short HEAD) docker compose up -d --build
docker compose ps
```

The backend gets up to 80 seconds to shut down (`stop_grace_period`), so matches
that just finished are still recorded before it exits. Tables in progress end, as
on any restart. `migrate` applies new migrations before the backend starts. To roll back, check out the previous tag
and run the same `up`. A migration that changed data needs `make restore` from the
backup you just took.

## 15. Later

- **About 60 days in**, once certbot's first automatic renewal has gone through
  (`docker compose logs certbot`), raise HSTS in `internal/config/nginx.conf` from
  `max-age=300` to `604800`, then later `31536000`.
- **Reboots:** unattended-upgrades does not reboot on its own here. Check
  `/var/run/reboot-required` now and then and reboot at a quiet hour; every service
  is `restart: unless-stopped`, so the stack comes back by itself.
- **Disk:** the `DiskFilling` alert reaches Telegram before the disk fills. Logs stay
  14 days, traces 48 hours and metrics 30 days, so steady-state use is flat.
