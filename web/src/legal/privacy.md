tty.cards is an SSH server that runs card games in your terminal, plus a small static
website. It is open source under the MIT licence, so every line that touches your data
is readable at <https://github.com/Pieczasz/tty-cards>. This policy describes what
that code actually does.

---

## 1. Who is responsible

The controller for the personal data described here is:

**Bartlomiej Piekarz**, a private individual based in the Netherlands.
Email for privacy requests and anything else in this policy: **bartekp854@gmail.com**.

This is a personal, non-commercial project run by one person. There is no company
behind it and no data protection officer, because processing at this scale does not
require one. If a postal address is needed for a formal request, ask by email and one
will be provided.

The project is far below the thresholds that require a Data Protection Officer, so
there is none. Write to the address above.

---

## 2. What we store, in one box

| What | Where | Why | How long |
|---|---|---|---|
| Username (1-16 characters, `A-Z a-z 0-9 _`), chosen by you | Database | It is your identity at the table and on the leaderboard | Until the account is deleted |
| SHA256 fingerprint of your SSH public key | Database | Logging you back in. **The key itself is never stored** | Until the account is deleted |
| Account created | Database | Support and abuse handling | Until the account is deleted; `created_at` stays on the anonymised row |
| Last seen / key last used | Database | Support and abuse handling | Last seen is cleared on deletion; keys are hard-deleted |
| Elo rating and matches played, per game | Database | Ranked play and the leaderboard | Until the account is deleted |
| Match history: game, ranked or casual, your placement, your Elo change, timestamps | Database | Your profile, and recomputing ratings if something goes wrong | Kept after deletion, anonymised (section 10) |
| Your IP address | In memory only | Rate limiting, so one network cannot flood the server | One window: 1 second for SSH auth, 1 hour for new-account registration, 60 seconds for the API |
| Which accounts have a live session right now | In memory only | The connection cap, and letting you reconnect to your seat | Until you disconnect |
| Server logs and traces (network prefix - full IP only when a connection is refused - player id, lobby codes; traces carry the username on the session span, not the address) | Log/trace store on the server | Diagnosing crashes and abuse | Logs 14 days, traces 48 hours |
| Backups of the database (everything above stored in it) | Archive files on the server | Restoring the service after a disk failure or a mistake | 14 days |

We do not store your email address, your real name, your SSH private key, your
passwords (there are none), your chat messages (there is no chat), or any payment
details (the service is free).

---

## 3. The website (www.tty.cards)

The website is a set of static pages served by GitHub Pages behind Cloudflare.

- **It sets no cookies.** None, first- or third-party. It also uses no
  `localStorage`, no `sessionStorage`, no analytics, no tag manager, no advertising
  pixels, no social embeds, and no web fonts fetched from a third party - the page
  asks for whatever monospace font your device already has.
- The "who is on right now" panel calls `https://tty.cards/api/v1/stats` and
  `https://tty.cards/api/v1/leaderboard` from your browser, once when the panel
  scrolls into view and then every 30 seconds while the tab is visible. That request
  goes to our own server. Like any HTTP request it carries your IP address, which the
  server's rate limiter sees (in memory, for one minute). The nginx access log in
  front of that API does not record the address. The response contains only public
  leaderboard data and sends nothing about you.
- GitHub and Cloudflare keep their own operational logs (IP address, requested URL,
  user agent) in order to deliver the page. We do not control what they log and we do
  not receive analytics from either of them.

---

## 4. The game server (`ssh tty.cards`)

### 4.1 Your account

There is no sign-up form. Your SSH key is your account.

On your first connection the server stores the **SHA256 fingerprint** of the public
key your client offered, together with the **username** you connected as. The
username must be 1-16 characters from `A-Z`, `a-z`, `0-9` and `_`; you choose it, and
nothing checks whether it resembles your real name - a pseudonym is fine and
recommended. Your private key never leaves your machine, and the public key itself is
not stored either: only its fingerprint, which is what later connections are matched
against.

The server also records when the account was created, when it was last seen, and when
each key was last used.

### 4.2 Matches and ratings

For every match that finishes, the server stores the game, whether the lobby was
ranked or casual, each participant's placement, each participant's Elo change, and
the timestamps. It keeps one Elo rating and a played-match count per account per
game.

### 4.3 Held only in memory, never written down

- **Your IP address**, in the rate limiter: sliding-window counters keyed by your
  network (IPv6 addresses are collapsed to their /64, because a single subscriber is
  routinely handed 2^64 addresses). Each entry lasts one window (1 second for SSH
  auth, 1 hour for new-account registration, 60 seconds for the API) and everything
  is lost on restart.
- **Who is connected**: a map of which account IDs currently have a live session. It
  exists so the server can enforce its connection cap and so that a reconnect finds
  your seat instead of forfeiting your match. It stores nothing besides that - no
  history, no addresses - and it is empty again after a restart.
- **The live game itself**: the table, the hands, the chips. Discarded when the match
  ends; only the result described in 4.2 is written down.

### 4.4 Logs, traces and metrics

The server emits structured logs and OpenTelemetry traces to a self-hosted
Loki/Tempo/Prometheus stack on the same machine. These are for finding bugs and
abuse, and they are not published.

- **Logs** can contain your network prefix (the full address only when a connection
  is refused), a numeric player id, lobby codes, and what your session did. They do
  not contain your username or SSH fingerprint.
- **Traces** carry the SSH client version, terminal size, and the username on the
  session span. They do not carry your IP address.
- **Metrics** are aggregate counters only (games started, sessions active, errors per
  endpoint). They carry no username and no address.

Server logs are retained for **14 days** and traces for **48 hours**, then deleted.

---

## 5. Lawful bases (GDPR Article 6)

- **Article 6(1)(b), performance of a contract**: your account, your match history
  and your ratings. You asked to play a ranked multiplayer game; those records are
  the game.
- **Article 6(1)(f), legitimate interests**: rate limiting, the connection cap, logs,
  traces and metrics. The interest is keeping a free service available and abuse-free
  for everyone playing on it. The data involved is minimal, short-lived and never
  used to build a profile of you, which is why we consider it balanced against your
  interests. You can object - see section 10.

We do not rely on consent for anything, because nothing here is optional
tracking - which is also why there is no cookie banner: there is nothing to consent
to.

---

## 6. What is public

By design, and for anyone, with no account:

- The in-game leaderboard shows **username, game and Elo rating**.
- The read-only JSON API at `https://tty.cards/api/v1/leaderboard` returns the same
  thing, and `https://tty.cards/api/v1/stats` returns aggregate counts (players
  online, hands in play, tables open).
- Other players at your table see your username and how you played.

Pick a username you are happy to be seen under. Changing it later means contacting us
(see section 10).

---

## 7. Recipients

We do not sell personal data, and we do not share it for advertising. It reaches:

- **Contabo GmbH** (Germany) - hosts the virtual server that runs the game, its
  database, and the log, trace and metric stores, in a data centre in Germany, inside
  the EU. Acting as a processor under Contabo's data processing agreement.
- **GitHub, Inc.** - hosts the static website (GitHub Pages) and the source code.
- **Cloudflare, Inc.** - DNS and CDN in front of the website.

The database, the logs and the traces all run on that one server. No third-party
analytics or monitoring service receives them.

---

## 8. Transfers outside the EEA

GitHub and Cloudflare are US companies and may process the request data described in
section 3 outside the EEA. Both offer data processing terms incorporating the European
Commission's **Standard Contractual Clauses**, which is the transfer mechanism we rely
on, alongside their own supplementary measures. No game data - accounts, matches,
ratings, logs - is transferred to them; it stays on the server in Germany and never
leaves the EU.

---

## 9. Retention

| Data | Kept for |
|---|---|
| Account, ratings, keys | Until you delete the account (section 10), or the service shuts down |
| Match history | Kept after deletion, anonymised: it is the other players' history too |
| Server logs | 14 days |
| Database backups | 14 days: one is taken a day and the newest 14 are kept. A deleted account stays in older ones until they rotate out (section 10) |
| Traces | 48 hours |
| Aggregate metrics (no personal data) | 30 days |
| Rate-limiter counters | One window in memory: 1 second SSH auth, 1 hour registration, 60 seconds API |
| Live session and game state | Until you disconnect / the match ends |

If the service is shut down for good, the database is deleted.

---

## 10. Your rights

Under the GDPR you have the right to **access** your data, to **rectification** of
anything wrong, to **erasure**, to **restriction** of processing, to **data
portability**, and to **object** to processing based on legitimate interests
(section 5). You also have the right to lodge a **complaint with a supervisory
authority**. The operator is based in the Netherlands, so the lead authority is the
Dutch **Autoriteit Persoonsgegevens** (<https://autoriteitpersoonsgegevens.nl>); you may
equally complain to the data protection authority of the EU country you live or work
in, or where the issue happened.

**Deleting your account yourself.** Open your profile in the game (`p` from any
screen), press **`x`**, type `DELETE` and press enter. The confirmation spells out
what will happen and `esc` backs out of it. You have to leave any table you are
seated at first. The deletion runs there and then, and ends your SSH session.

It removes, permanently:

- **every SSH key fingerprint registered to the account.** Connecting again with the
  same key does not reopen it - it registers a brand-new account, from zero.
- **every rating**: the Elo and matches-played row for each game, so the account
  leaves the leaderboard immediately rather than at the next refresh.
- **your username and your last-seen timestamp.** The account row is renamed to
  `deleted_` plus the 32 hex digits of your account id, and the timestamp is cleared.

What stays is the **matches and their participation rows**, including yours. Those
same rows are the other players' match history and the record of the Elo they have
already won or lost, which is their data, not yours to erase. Your row is anonymised
instead of deleted: it stays attached to the renamed `deleted_<hex>` account, and
that is what those players' profiles show from then on. Nothing left in it names you
- no username, no key fingerprint, no rating.

**Backups.** A deletion does not reach the backups taken before it: your account
stays in those until they rotate out, at most 14 days later (section 9). Backups are
used only to restore the service after data loss, and if one is ever restored, every
account deleted since it was taken is deleted again.

**If you can no longer log in** - key lost, account already unreachable - email
**bartekp854@gmail.com** and say what you want done. The same address is the
route for every other right in this section: access, rectification, restriction,
portability, objection. To prove that an account is yours, sign a short message we
send you with the same SSH key - that is the only credential the account has.
Requests are answered within one month, as Article 12(3) requires.

---

## 11. Automated decision-making

There is none that produces legal effects or similarly significantly affects you
(Article 22). Nothing here decides anything about you outside the game.

The one automated calculation is your **Elo rating**, and it is worth stating plainly:

- Every account starts at **1500** per game.
- Only **ranked** lobbies move it. New lobbies are casual by default, and casual
  matches never change a rating.
- When a ranked match ends, players are ordered by placement and each neighbouring
  pair is scored against each other with the standard Elo formula (K-factor 32). The
  rating is clamped to the range 100-4000.
- An account with very few ranked matches is treated as **provisional**: beating one
  earns an established player nothing, because a fresh SSH keypair is free and would
  otherwise be a free source of rating points. A provisional account's own rating
  still moves, so it settles onto a real number as it plays.

That is the whole of it. There is no scoring of you as a person, no advertising
profile, and no sharing of your rating beyond the public leaderboard.

---

## 12. Children

The service is not aimed at children. You must be at least **16 years old** to use
it, which matches the default age of consent under GDPR Article 8. If we learn that an
account belongs to someone younger, we will delete it.

---

## 13. Security

Everything between your terminal and the server goes over SSH, encrypted and
authenticated with your own key. There are no passwords to leak. The database and the
observability stack are not reachable from the internet; only the SSH port and the
read-only JSON API are exposed. Being a hobby project run by one person, this is a
best-effort promise and not a warranty - see the [Terms of Service](https://www.tty.cards/terms/).

If you find a vulnerability, please report it privately to the address above rather
than demonstrating it at a table.

---

## 14. Changes to this policy

If this policy changes materially, the date at the top changes and the previous
version stays in the repository's git history, which is public. Continuing to play
after a change means the new version applies.

---

*This document is a careful, good-faith description of what the software does. It is
not legal advice. If you operate this service, have it reviewed by a lawyer in your
jurisdiction before relying on it.*
