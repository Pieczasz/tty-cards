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
  is done.
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
  behaviour. One malformed entry fails the boot.
- **IPv6 players get their own rate-limit bucket** (operators). nginx and the
  backend sit on an IPv6-enabled `edge` network and nginx listens on both
  families; before, every IPv6 player arrived from the Docker gateway and shared
  one limit. The host's Docker needs IPv6 enabled for user networks.
- **Explicit data retention.** Logs 14 days, traces 48 hours, metrics 30 days. It
  used to be "until the disk fills".
- Terms of Service and a Privacy Policy, in the repo and on the site
  (`docs/privacy.md`, `docs/terms.md`).
- A security policy with a private disclosure channel (`docs/SECURITY.md`).

### Documentation

- **Every document except `README.md`, `LICENSE`, `CLAUDE.md` and `AGENTS.md` now
  lives in [`docs/`](README.md)**, with `docs/README.md` as the index.
  `ARCHITECTURE.md` is `docs/architecture.md`, `READING_GUIDE.md` is
  `docs/reading-guide.md`, `ONBOARDING.md` is `docs/onboarding.md`,
  `CHANGELOG.md` is `docs/changelog.md`, `PRIVACY.md` and `TERMS.md` are
  `docs/privacy.md` and `docs/terms.md`, and
  `internal/observability/DATA.md` is `docs/data-inventory.md`.
  `docs/CONTRIBUTING.md` and `docs/SECURITY.md` keep their upper-case names so
  GitHub's own UI still links them.
- **New: [`docs/decisions.md`](decisions.md)** - one record per non-obvious
  choice, with context, consequences and the code it lives in. Forty-eight of
  them, from "one mutex per engine" to "the odd chip goes to the lowest-sorted
  player id".
- The reading guide is now a **bottom-up** walk: leaves first, so every later
  file only uses what you already know. Per step it names the files and their
  size, the invariant to check, and the one test that teaches it.
- `docs/architecture.md` is the canonical design document and is readable top to
  bottom by someone who has never seen the code. `docs/onboarding.md` keeps the
  product story, the annotated tree and local development, and links out for
  everything else instead of repeating it.

### Changed - identity

- **Account ids are UUIDv7.** Postgres 18 generates them with `uuidv7()`; the
  Go side uses the stdlib `uuid` package. Recreate the Postgres volume: 16->18
  is a major upgrade, and the rewritten `000001` will checksum-fail otherwise.
- **Usernames are unique regardless of case.** Once `Alice` exists, `alice` is
  taken; your name keeps the case you chose. Migration `000006` refuses to run,
  and names the accounts, if an existing database already holds two names that
  differ only by case - pick which one keeps it and rename the other first.
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
- **An erased account can never reappear on the leaderboard**, not even from a
  match that was finishing while it was deleted, and not from a leaderboard read
  that started just before.
- A game's identity in the database is now its slug, not its display name, so a
  game can be renamed without orphaning every rating attached to it
  (migration `000005`). Migration `000004` makes several columns `NOT NULL` that
  could previously read back as a silent zero - an unrated player at 0 Elo, a
  match attached to game 0.

### Changed - sessions and security

- **A second connection to the same account now closes the first one.** A
  half-open TCP session used to keep you out of your own seat for the whole
  90-second reconnect window.
- **A taken username gets the same answer as any other refusal.** The login
  banner had become a way to ask "does this account exist". (An invalid name now
  says why; see identity above.)
- **Reconnecting while your old session is still closing is safe.** The old
  session gives up the seat and the slot in one step, so it can no longer start a
  disconnect timer on the seat your new session is playing; and a reconnect
  refused because the server is full no longer cancels the timer holding your
  seat.
- **The displaced session's whole connection is closed**, not just its channel,
  and a connection can no longer open more session channels than the cap (or
  flood the environment) before being counted.
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
- Stats-API metrics no longer take their server label from the client's own
  `Host` header.
- The stack is sized to fit 5.5 GiB on a 12 GB / 6-core VPS, with a per-service
  limit that adds up (see the header comment in `compose.yaml`).

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
- Closing a table no longer unseats a player who had already moved to another
  one.

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
- The create form no longer offers fewer seats than the chosen game needs.
- Account deletion cannot be backed out of, or issued twice, once it is running.
- Switching screens no longer doubles up game events or the turn clock.
- Key hints stay visible at 80x24.
- The welcome banner no longer renders your username as ASCII art (it also
  stopped being a way to fill a shared render cache).

### Fixed - internals worth knowing about

- `deck.Pile.Shuffle()` no longer returns an error nobody could trigger. Deals
  are a uniform permutation from a ChaCha8 generator seeded per call from
  `crypto/rand`.
- The turn-timeout event is now broadcast on the same lock hold that charges the
  miss, so a player whose move lands in the gap is not told they timed out after
  the miss was refunded.
- **A turn that carries on keeps its clock.** Gin's draw then discard, an Uno
  skip that comes back to you, a Hearts trick winner leading again, or someone
  else leaving no longer hands the same player a fresh 30 seconds; at least 10
  are always left. A turn costs one miss, not one per expiry, so an absent Gin
  player loses the seat after three turns instead of one and a half.
- **Smarter auto-play for an absent player:** poker calls when nobody can make
  the call cost anything instead of folding a covered bet; Hearts passes the
  queen of spades and the high spades and hearts instead of its three lowest
  cards; Gin knocks when it holds gin instead of discarding it.
- A bug in a game's rules on a player's own move now ends that one table as an
  unrated match, as it already did on the turn timer, rather than leaving it
  running on half-applied state.
- Migration `000007` indexes the day's ranked matches, so the anti-farm check no
  longer reads a veteran's whole history on every ranked result. Migration
  `000005`'s down is marked lossy: take a backup before rolling back past it.
- The `lobby.SessionAPI` interface is gone; views take the manager directly. It
  had one implementation and one consumer.
- Unreachable guards removed across the engine, poker and the game packages, each
  with a comment saying why it could not fire.

### Testing

- Coverage is 90% or better in every package that is not pure wiring.
- Property tests (`rapid`) for Elo's invariants, poker's "nobody loses chips
  nobody matched", and the auto-play move in four of the five games; poker has a
  deterministic equivalent.
- Eight fuzz targets, including `FuzzBestMeldSplit` and `FuzzClassifyHand`.
- `goleak` in 22 packages, fit tests per screen, and `-race` tests pinning that
  hand manipulation never aliases the pile.
- CI additionally builds the Docker image for amd64 and arm64 and validates
  `compose.yaml` and `nginx.conf`. Lint is pinned to golangci-lint v2.13.2.
