# Changelog

Notable changes, in terms a player or an operator would care about. Format loosely
follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). There are no
tagged releases yet; `main` is what runs.

## [Unreleased]

A hardening pass across rating, payouts, sessions, the deployment and the UI.
Most of it is invisible when everything goes right, which is exactly why it went
unnoticed for so long.

### Added

- **Delete your own account.** Profile -> `x` -> type `DELETE`. Your SSH keys and
  your ratings are erased outright; your username becomes `deleted_` plus the hex
  of your account id so the other players at your old tables keep a readable match
  history. Refused while you are seated at a table, and the session ends once it
  is done. Closing the terminal after confirming does not cancel it.
- **A separate limit on new accounts** - 5 per hour per client network by
  default, set with `REGISTRATION_LIMIT` and `REGISTRATION_WINDOW`. Playing on an
  existing account never touches that budget, and neither does a name that fails
  validation. `make loadtest` needs the limit raised on the server under test.
- **esc asks before you forfeit.** Mid-game, esc shows "Leave and forfeit this
  game?" and only `y` leaves; any other key keeps you playing. One stray esc used
  to cost a ranked match. After the game, esc and enter go back to the lobby as
  before.
- **`PROXY_TRUSTED_CIDRS`** (operators). A comma-separated list of the networks
  the proxy sits on: a PROXY header is honored only from them and any other
  connection is refused, and the stats API believes `X-Forwarded-For` only from
  them. Compose sets it to the new `edge` network's subnets. Empty keeps the old
  behaviour in development only; anywhere else it fails the boot while either
  trust is on. One malformed entry fails the boot, and so does an IPv4-mapped
  one (`::ffff:0.0.0.0/96` read as the whole IPv4 internet on the ssh listener
  and as nothing on the stats API): write the IPv4 network.
- **IPv6 players get their own rate-limit bucket** (operators). nginx and the
  backend sit on an IPv6-enabled `edge` network and nginx listens on both
  families; before, every IPv6 player arrived from the Docker gateway and shared
  one limit. The host's Docker needs IPv6 enabled for user networks.
- **Explicit data retention.** Logs 14 days, traces 48 hours, metrics 30 days. It
  used to be "until the disk fills".
- Terms of Service and a Privacy Policy, on the site
  (`web/src/legal/`).
- A security policy with a private disclosure channel (`docs/SECURITY.md`).

### Documentation

- **Every document except `README.md`, `LICENSE`, `CLAUDE.md` and `AGENTS.md` now
  lives in [`docs/`](README.md)**, with `docs/README.md` as the index.
  `ARCHITECTURE.md` is `docs/architecture.md`, `ONBOARDING.md` is
  `docs/onboarding.md`, `CHANGELOG.md` is `docs/changelog.md`, and
  `internal/observability/DATA.md` is `docs/data-inventory.md`.
  The published privacy policy and terms are `web/src/legal/`.
  `docs/CONTRIBUTING.md` and `docs/SECURITY.md` keep their upper-case names so
  GitHub's own UI still links them.
- **New: [`docs/decisions.md`](decisions.md)** - the policies a later change
  has to keep, from "one mutex per engine" to the rating and proxy rules.
- `docs/architecture.md` is the canonical design document and is readable top to
  bottom by someone who has never seen the code. `docs/onboarding.md` keeps the
  product story, the annotated tree and local development, and links out for
  everything else instead of repeating it.

### Changed - identity

- **Account ids are UUIDv7.** Postgres 18 generates them with `uuidv7()`; the
  Go side uses the stdlib `uuid` package. Recreate the Postgres volume after a
  schema rewrite: golang-migrate will checksum-fail otherwise.
- **Usernames are unique regardless of case.** Once `Alice` exists, `alice` is
  taken; your name keeps the case you chose (`idx_users_username_lower`).
- **Registration tells you why a name is invalid** (too long, a character that
  is not allowed) instead of a generic refusal, and checking it no longer spends
  one of your network's new-account slots. A *taken* name still gets the same
  answer as any other refusal.

### Changed - ratings and payouts

- **Quitting no longer helps.** A player who leaves mid-match now ranks strictly
  below everyone still at the table, and can never tie with them.
- **Tables nobody finished are not rated.** If every seat leaves, the match is
  recorded as history with no Elo change; standings for an abandoned table are
  just reverse leave order, and rating that paid the last person to quit.
- **The new-account rule is now per opponent, not per table.** A brand-new
  account (fewer than 5 ranked matches) still pays nothing to an established
  player - but the established player can still *lose* to one. Previously an alt
  at the table froze the whole result, which turned an anti-farm rule into a
  shield.
- **The anti-farm cap counts pairs of players, across all five games.** The same
  two accounts stop moving each other's rating after three ranked matches in 24
  hours, whatever game they switch to, and whoever else they seat alongside
  themselves. The old cap counted exact player sets, so rotating a third account
  through the table reset it.
- **Poker pays out correctly in five situations it previously did not:**
  - everyone folds to a bet nobody called - the uncalled part comes back;
  - an orphaned side pot no longer disappears;
  - you can no longer raise more than any opponent can call (the raise is
    refused, rather than staged and handed straight back at showdown);
  - a big blind too short to post in full no longer drags the opening bet, and
    the first legal raise, below a full blind;
  - a tripwire logs loudly if a hand ever finishes with chips unaccounted for.
- **Gin rummy defenders get the arrangement they are owed.** The defender's hand
  is now split for the lowest deadwood *after* laying off, which can be the
  difference between a loss and an undercut.
- **Hearts counts the live hand once** in the standings of a match that ended
  mid-hand - not twice, and not zero times.
- **Uno gives the last player standing their forfeit win**, including on a
  reversed table where it used to be swallowed.
- **A Hearts match cut short by a leave only costs the leaver.** Hearts cannot
  go on three-handed, so one seat leaving ends it for everyone. It used to count
  as a full rated result, which let a friend quit on cue to lock in the leader's
  win. Now the players still seated keep their rating and their match count, and
  the leaver still takes their loss (never a gain).
- **Poker fold-outs pay like a showdown.** When everyone else folds or leaves,
  only the one slice nobody matched goes back; the rest, folders' chips included,
  goes to the winner. A player who called and then folded used to get part of it
  back.
- **Short all-ins that add up to a full raise reopen the betting**, as the
  standard (TDA) rule has it. A player who had already acted can raise again.
- **Poker raises you can actually make.** A deep stack can put a short one
  all-in, and the raise prompt now shows the legal range, `(min X, max Y)`,
  taken from the same rule the table enforces.
- **Poker ties on chips are broken by cards only after a real showdown** between
  players still seated. A pot won face-down, or a leaver's hand, no longer splits
  a draw by cards nobody showed, and a final pot won face-down stays face-down on
  the result screen.
- **A poker player who left stays folded for the rest of the match.** They used
  to come back unfolded each hand and appear at every later showdown as an extra
  seat, showing the cards of the hand they walked out of.
- **A chopped poker pot's odd chip goes to the first winner left of the button**,
  the standard rule, so it moves round the table. It used to go to the lowest
  account id, and ids are time-ordered, so the older account took it every time;
  with stacks otherwise level, that one chip decided first and second place.
- **An erased account can never reappear on the leaderboard**, not even from a
  match that was finishing while it was deleted, and not from a leaderboard read
  that started just before.
- A game's identity in the database is now its slug, not its display name, so a
  game can be renamed without orphaning every rating attached to it. Scalar
  columns that used to scan a SQL NULL into a silent Go zero (Elo 0, game 0)
  are `NOT NULL`.

### Changed - sessions and security

- **A second connection to the same account now closes the first one.** A
  half-open TCP session used to keep you out of your own seat for the whole
  90-second reconnect window.
- **A taken username gets a fixed answer**, not the database's error text. It
  still says the name is taken: a free name would have registered, and ranked
  names are public on the leaderboard anyway. (An invalid name now says why; see
  identity above.)
- **Reconnecting while your old session is still closing is safe.** The old
  session gives up the seat and the slot in one step, so it can no longer start a
  disconnect timer on the seat your new session is playing; and a reconnect
  refused because the server is full no longer cancels the timer holding your
  seat.
- **A terminal resize can no longer wedge a connection.** Two resizes sent before
  the shell started, after the session ended, or while the session's screen was
  stuck stopped the server reading that connection for good: closing it ended
  nothing, its listener slot (and, for a live session, its account slot) never
  came back, and enough of them shut everyone out until a restart. A resize
  before the shell is now refused (the pty request already carries the size), and
  later ones never wait for the screen; a session that falls behind gets the
  latest size.
- **The displaced session's whole connection is closed**, not just its channel,
  and a connection can no longer open more session channels than the cap (or
  flood the environment) before being counted. The exception is a second session
  on the same connection, as OpenSSH's `ControlMaster` opens: only the first
  session is ended, where hanging up the connection used to drop both. Its screen
  now stops on the server too, not just its channel: one connection opening shell
  after shell used to pile up running screens, and every one of them shrank the
  screen size the server grants to all players.
- **A typo in `ENV` fails the boot** (operators). `ENV=prod` used to start a
  production server in development mode with every production check off. In
  production, `DB_SSLMODE` must also be `require`, `verify-ca` or `verify-full`
  for any database outside the compose network; `prefer` and `allow` can fall
  back to plaintext.
- **The stats API is no longer traced** (operators). Every website visitor polls
  it, and a trace per request put each visitor's address and browser into Tempo.
  Its request metrics remain, with a fixed server label and no client-chosen
  port.
- **nginx's error log no longer ships client addresses** (operators): it logs at
  `error` only, rate-limit refusals stay below that, and Alloy redacts the
  `client:` field. nginx now also re-resolves the backend, so recreating the
  backend container no longer strands the proxy; the HTTP side caps concurrent
  connections per network, times out slow clients after 10 s, and answers a
  limit with 429 rather than 503.
- **Grafana answers only to `http://localhost:3000`** (operators), which stops a
  DNS-rebinding page from reaching it through your tunnel.
- **Alloy no longer mounts the Docker socket** (operators); it reads container
  logs through a GET-only socket proxy on a network of its own. Every service
  runs with `no-new-privileges`, every image is pinned by digest, and CI actions
  are pinned by commit with a read-only token.
- **Backups are private and atomic** (operators). `scripts/backup.sh` writes
  each dump readable by its owner only, never leaves a half-written file that
  looks like a good backup, and reads `.env` as data instead of sourcing it.
  Compose refuses to start without `DB_PASSWORD`.
- **A crash in the UI now tells you so** instead of dropping the connection
  silently.
- **We log a lot less about you.** Connect and disconnect records keep only your
  network prefix (an IPv6 /64), not your address; nginx logs no client addresses
  at all; and the session trace no longer carries your address next to your
  username.
- Grafana is reachable only through an SSH tunnel to the host (`127.0.0.1:3000`),
  which CI now asserts; behind that gate it needs no login of its own.
- The deployment publishes only ports 22 and 80. Grafana stays on
  `127.0.0.1:3000` behind an SSH tunnel.
- nginx's connection and request limits key on the IPv6 /64, so one customer with
  a /64 no longer has 2^64 ways around them.
- **SSH sign-in has a budget per IPv6 /48 too** (operators), 16 times
  `RATE_LIMIT_CONNECTIONS`. A free tunnel is one /48 of 65,536 /64s, and each /64
  had a fresh sign-in budget of its own.
- Stats-API metrics no longer take their server label from the client's own
  `Host` header.
- A session turned away because the server is shutting down is now counted, as
  `outcome="rejected_draining"` on `ttycards.ssh.sessions` (operators).
- The stack is sized to fit 5.5 GiB on a 12 GB / 6-core VPS, with a per-service
  limit that adds up (see the header comment in `compose.yaml`).

### Changed - configuration and metrics (operators)

- **The `game_type` metric label is now the game's catalog slug** (`crazy_eights`,
  `poker`, `uno`, `hearts`, `gin_rummy`) on every game metric. The lobby used to
  label a game by its display name (`Crazy Eights`) and the game views by a
  lower-case spelling of their own (`crazy eights`), so every game was split
  across two series. **Update any dashboard, recording rule or alert that
  matches on a `game_type` value.**
- **`RATE_LIMIT_WINDOW`** is a Go duration (`1s`, `500ms`; default `1s`) like
  `REGISTRATION_WINDOW`.
- **Boolean variables are parsed strictly.** `PROXY_PROTOCOL` and
  `API_TRUST_PROXY` take `true`/`false`, `1`/`0` or `t`/`f`; anything else fails
  the boot. `PROXY_PROTOCOL=off` used to be read as something, and `yes` meant
  false for some variables and true for others: write `false`. OTel plaintext is
  decided by the endpoint URL scheme (`http://` vs `https://`); database
  plaintext is accepted only with `ENV=development`.
- **`ENV` is read from the process environment only.** It is settled before
  `.env` is read, so an `ENV` line in `.env` was silently ignored: `ENV=staging`
  there booted as development with every fail-closed check off, and `ENV=prod`
  there booted at all. An `ENV` line in `.env` now fails the boot. **Remove it
  from an `.env` copied from an older `.env.example` and export `ENV` instead.**
- **Every invalid variable is reported at once**, not just the first, and a boot
  failure is printed once instead of twice.
- **The stats API refuses to start with a missing dependency** instead of
  serving zeros and an empty leaderboard forever.
- **An IPv6 literal `SERVER_HOST`** (`::`, say) now binds; the address used to
  be built without brackets.
- Every SQL statement is logged in development only; staging now logs like
  production (warnings and slow queries).
- A failed interrupted-match finalize now logs as `finalize interrupted match`,
  once, instead of `finalize ranked match: finalize ranked match: ...`.
- **`scripts/restore.sh` no longer drops the live database first.** It restores
  into `<DB_NAME>_restore` and swaps that in only once `pg_restore` succeeds; a
  truncated or corrupt archive used to leave an empty database behind.
- **`make migrate-down` asks first and rolls back one migration** (developers).
  It ran `down -all` with no prompt against whatever `DB_DSN` named.
- **The observability sidecars drop every Linux capability** (`cap_drop: [ALL]`
  on alloy, loki, tempo, prometheus, alertmanager and grafana); alloy keeps only
  `DAC_OVERRIDE` for its data directory. CI fails any long-running service
  without it.
- **The image build context is an allowlist** (`go.mod`, `go.sum`, `cmd/`,
  `internal/`), so `.env`, secrets, backups and anything else in the checkout
  can no longer reach the Docker daemon.
- govulncheck also runs weekly on `main`, and Dependabot now updates the Go
  modules.

### Changed - tables and lobbies

- **What a match is recorded as is decided when it starts.** Reconfiguring a
  lobby after the hand ended used to rewrite the finished match's game and ranked
  flag.
- A seat taken by the idle timer now leaves the roster properly, so the table can
  reach all-ready again.
- A held seat is released when the hand ends and when the server shuts down, not
  only when its 90-second timer runs out. Previously it could keep you out of
  every other table in the meantime.
- **Changing a setting, or someone leaving or being kicked, un-readies the
  table.** A ready is agreement to the table as it was; the leader could flip a
  setting after everyone readied and start a match nobody agreed to, and dropping
  the one unready player could leave a table all-ready with nothing to start it.
- Kicking a player whose seat was being held for a reconnect stops that hold, so
  it can no longer pull them out of the next table they join.
- The table browser no longer offers a table that has just gone private or
  started.
- A match that ends just as the server begins shutting down is no longer dropped
  while the table reopens.
- **A ranked match finished during a deploy's 30-second drain is rated.** The
  server used to mark itself as shutting down before that window, so a game that
  ended there by play was stored without Elo. Only the tables the drain tears down
  at its end are recorded unrated now. No new game starts inside the window: a
  table that readies up there stays waiting, rather than starting a match the
  drain would cut short.
- **A taken `API_PORT` refuses the boot** (operators). The stats API used to bind
  in the background, so the server logged "starting stats api", opened SSH to
  players and only then failed. It now binds before the SSH listener opens.
- Closing a table no longer unseats a player who had already moved to another
  one.
- **A new turn gets the whole clock, even when it comes straight back to you.**
  A Hearts trick winner leading the next trick, the heads-up poker big blind
  opening the flop, and a heads-up Uno Skip, Reverse or Draw now start a fresh
  30 seconds instead of the 10 left over from the turn before, and an absent
  seat that keeps getting the turn back is charged a miss for each turn, so it
  is removed after three. Only Gin's draw then discard is one turn on one clock.
  This supersedes the earlier note that an Uno skip or a Hearts trick winner
  kept the clock.
- A player who readied and then dropped from a waiting table is un-readied before
  they leave, so the last ready-up in that moment can no longer start the game
  with them in it and forfeit their seat without the 90-second hold.
- The release of held seats at hand end only gives up the holds it found. A
  player who reconnected, started the next game and dropped again in that moment
  keeps their new 90-second hold instead of being forfeited at once.

### Changed - terminal UI

- **Every screen now fits an 80x24 terminal**, and a 64x20 one. Menus, the
  leaderboard, the profile and the join browser all size themselves to the space
  they actually have instead of assuming it.
- **The spacebar works in the Hearts pass phase and in the join browser.** It
  never did; the pass phase was playable only by letting the 45-second
  auto-pass fire.
- Pressing Home while seated takes you back to your table instead of leaving your
  hand to be auto-played until the idle timer takes the seat.
- The leaderboard pages properly and ignores a response for a filter you have
  already cycled past; after a filter change it no longer declares a long board
  finished after its first screenful.
- **An idle game-over screen now times out** like any menu. Only a live table is
  spared the 5-minute idle quit.
- A reconnect is no longer routed into a table that has just finished.
- The create form no longer offers fewer seats than the chosen game needs. For a
  game the server does not know, both the create screen and the lobby now fall
  back to the same 2-6 seats (the create screen used to allow 8).
- **Opponents sit clockwise from your left in Crazy Eights, Uno and Gin Rummy**,
  as they already did in Poker and Hearts, so the player who acts after you is
  drawn on your left. They used to be drawn in seat order, which could put the
  next player on your right.
- **The lobby shows the rating matchmaking uses.** A stored rating of 0 now shows
  as the starting 1500, which is what the table browser ranks it by, instead of
  as 0.
- Hearts, Gin Rummy and Poker share one between-hands result screen, so the
  three lay out their standings the same way.
- **The result screen keeps its headline on a small terminal.** At 80x24 Gin
  Rummy used to cut off the hand number, the GIN/KNOCK banner and who scored,
  showing only the bottom of the card art. The cards are now drawn small there,
  and dropped before the headline when even that does not fit. Poker's leave
  warning is on its own line so 64 columns no longer cut it short.
- **A table that ends on an error says so.** It used to read "MATCH COMPLETE"
  over " wins" in Poker and Gin Rummy, and "Game Over! Winner: " with no name in
  Hearts, as if somebody had won a match that is recorded unrated. It now reads
  "MATCH ENDED - table error", and Hearts keeps its scoreboard on screen.
- Your own turn clock starts counting the moment the turn reaches you. It used
  to sit on the full time for up to five seconds first.
- Hearts and Gin Rummy no longer offer the play keys to a seat that is waiting
  for its turn. Space and enter in the Hearts pass, and draw, discard and knock
  in Gin, did nothing off-turn; the waiting seats now see only the cursor keys
  and esc.
- A failed join no longer pushes the join browser past the edge of an 80x24 or
  64x20 terminal: the error line is wrapped and the table list gives up a row
  for it.
- An empty leaderboard says no one has ranked yet instead of staying on
  "Loading leaderboard." forever.
- A player kicked, or whose table closed, in the moment between joining and the
  table screen opening is sent home instead of being left on a roster they are
  no longer in.
- **A kicked guest is sent home at once.** The kick used to leave them on a
  frozen roster that still listed them, every key answering "player not in
  lobby", until they pressed x then y.
- Account deletion cannot be backed out of, or issued twice, once it is running.
- Switching screens no longer doubles up game events or the turn clock.
- Key hints stay visible at 80x24.
- **Keys do nothing while the "Terminal too small" notice is up**, except ctrl+c.
  They used to reach the hidden screen, so an enter while resizing could play a
  card you could not see.
- **Enter on a poker table never leaves it while the game is still running.** A
  match result that reached the screen before the table closed made enter leave
  without the forfeit confirmation. Esc still asks first, and enter leaves once
  the table has finished.
- The welcome banner no longer renders your username as ASCII art (it also
  stopped being a way to fill a shared render cache).

### Fixed - internals worth knowing about

- `deck.Pile.Shuffle()` no longer returns an error nobody could trigger. Deals
  are a uniform permutation from a ChaCha8 generator seeded per call from
  `crypto/rand`.
- The turn-timeout event is now broadcast on the same lock hold that charges the
  miss, so a player whose move lands in the gap is not told they timed out after
  the miss was refunded.
- **A turn that carries on keeps its clock.** Gin's draw then discard, or
  someone else leaving, no longer hands the same player a fresh 30 seconds; at
  least 10 are always left. A turn costs one miss, not one per expiry, so an
  absent Gin player loses the seat after three turns instead of one and a half.
  A new turn that comes straight back to you is a full turn: see "A new turn
  gets the whole clock" under tables and lobbies.
- **Smarter auto-play for an absent player:** poker calls when nobody can make
  the call cost anything instead of folding a covered bet; Hearts passes the
  queen of spades and the high spades and hearts instead of its three lowest
  cards; Gin knocks when it holds gin instead of discarding it.
- A bug in a game's rules on a player's own move now ends that one table as an
  unrated match, as it already did on the turn timer, rather than leaving it
  running on half-applied state.
- `idx_matches_ranked_created` indexes the day's ranked matches, so the
  anti-farm check no longer reads a veteran's whole history on every ranked
  result.
- The `lobby.SessionAPI` interface is gone; views take the manager directly. It
  had one implementation and one consumer.
- **Registering a game twice, or a half-declared one, panics at boot**
  (developers). `game.NewRegistry` names the game; it used to surface as a
  missing route or a nil factory when somebody started a table.
- Unreachable guards removed across the engine, poker and the game packages, each
  with a comment saying why it could not fire.
- **One stuck session no longer stops the others resizing.** When the server's
  size tier moved, the sweep that tells each session its new size waited on each
  in turn, so a session whose screen was stuck held every later one at a size the
  tier no longer grants. Each session now gets its nudge on its own, at most one at
  a time.
- **A big terminal held back for want of room gets it when room frees up.** A
  session that left without moving the size tier gave back its extra cells, but
  nobody was told, so a session held at a smaller size kept it until it resized.

### Testing

- Coverage is 90% or better in every package that is not pure wiring.
- Property tests (`rapid`) for Elo's invariants, poker's "nobody loses chips
  nobody matched", and the auto-play move in four of the five games; poker has a
  deterministic equivalent.
- Eight fuzz targets, including `FuzzBestMeldSplit` and `FuzzClassifyHand`.
- `goleak` in 26 packages, fit tests per screen, and `-race` tests pinning that
  hand manipulation never aliases the pile.
- The bot end-to-end test in `cmd/loadtest` fails when its table stops moving
  or a bot reports an error, not after a fixed minute: on a busy CI runner two
  random-length games under `-race` outlived the minute while still playing, and
  a bot's own error was hidden behind the timeout message.
- CI additionally builds the Docker image for amd64 and arm64 and validates
  `compose.yaml` and `nginx.conf`. Lint is pinned to golangci-lint v2.15.0.

