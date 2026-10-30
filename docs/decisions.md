# Decisions

The policies below are the ones a later change has to keep. The invariant list
is in [`architecture.md`](architecture.md).

1. **One mutex per engine.** It covers the clock and `State` for the whole of `Start`, `SubmitAction` and `RemovePlayer`. Rules run under it and never call back into the engine.
2. **A failed watch does not open the table.** `Subscribe` returns an error. If the lobby's watcher cannot subscribe, the game is not marked in play and `EventGameStarted` is not sent.
3. **A panic ends one table, or one goroutine.** Rules panics in `Start`, `SubmitAction`, `RemovePlayer` and the turn timer end that table as a rules error and do not ask for standings. The idle-removal hook, the game watcher and the disconnect-grace timer recover so one of them cannot take the process down.
4. **Views hold `BoundEngine`.** They submit as one player and read that player's hand. There is no escape hatch back to `*game.Engine`.
5. **The slug is the game's identity.** It is the registry key, the route and `games.slug`. The display name is cosmetic.
6. **Leavers rank strictly below players who stayed.** An interrupted match (a leave that ends it for everyone) writes only the leavers' losses. Rules error, abandoned and shutdown are history without Elo. The finalize snapshot is taken when the game starts.
7. **A mid-game seat survives 90 seconds.** `DisconnectPlayer` holds it and the engine auto-plays. The timer callback carries a generation; a stale callback does not drop a newer hold. If expiry already owns the seat, `ResumePlayer` waits until that leave finishes.
8. **A second session displaces the first and closes its connection** (only that session, its program ended and its channel closed, when both share one connection, as under OpenSSH's ControlMaster). Teardown releases the seat and the tracker slot together, and only for the generation it owns. The session stores user, generation and model before `ResumeSeat`.
9. **Never publish `:6969` or `:6970`.** PROXY and `X-Forwarded-For` are believed only from `PROXY_TRUSTED_CIDRS`. Outside development, turning either trust on with an empty list fails boot. `0.0.0.0/0`, `::/0` and IPv4-mapped prefixes (`::ffff:a.b.c.d/n`) are rejected; the PROXY listener's `net.ParseCIDR` and the API's `netip` read a mapped prefix differently.
10. **Network limits key on the IPv6 /64, and the wide budget on the /48.** Auth and registration spend both; a /48 refusal gives the /64 its attempt back. Registration is refunded when `RegisterUserWithSSHKey` does not create an account. An address that cannot be keyed is refused.
11. **Identity is the SSH key fingerprint.** Usernames are unique case-insensitively. Erasure anonymises the user row rather than deleting the match history.
12. **A plaintext database is development-only.** An unknown `ENV` fails the boot. `ENV` comes from the process environment only; `.env` setting it fails the boot.
13. **Leaving asks first.** Esc on a game that is not finished arms a confirmation. Esc and enter leave immediately only once the game is finished.
14. **Chips are conserved.** A mismatch ends the match as a rules error. At showdown, an all-in player who left still has their hole cards shown.
15. **Lock order is account, then manager, then lobby, then engine.** The rating policy is a pure function; the repository does the locks and the writes.
