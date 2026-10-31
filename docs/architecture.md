# Architecture

Topology and the invariant list. The policies a later change has to keep are
in [`decisions.md`](decisions.md).

## Topology

```
        ssh client (port 22)           browser (ports 80, 443)
                 |                                |
                 v                                v
   +----------------------------------------------------------+
   |  nginx           internal/config/nginx.conf               |
   |  stream{}  limit_conn 8 / $client_net, proxy_protocol on  |
   |  http{}    :443 /api/ -> backend:6970, else -> www        |
   +----------------------------------------------------------+
                 |  PROXY header (real client IP)
                 v
   +-----------------------------------------+
   |  backend :6969 (Go)      cmd/server      |
   |   Wish SSH + Bubble Tea                  |
   |   lobby.Manager (in memory)              |
   |   game.Engine per match                  |
   |   stats API :6970       internal/httpapi |
   +-----------------------------------------+
          |                        |
          v                        v
   +---------------+   +---------------------------+
   | Postgres 18   |   | Alloy -> Loki/Tempo/Prom  |
   | users, keys   |   |        -> Grafana         |
   | games, ranks  |   +---------------------------+
   | matches       |
   +---------------+
```

Published ports are 22, 80 and 443, all on nginx (IPv4 and IPv6). Grafana binds
`127.0.0.1:3000`. Postgres, the telemetry pipeline, and the backend's `6969` and
`6970` are not published. Publishing `6969` lets a client forge the PROXY header;
publishing `6970` lets it forge `X-Forwarded-For`.

`edge` holds nginx and the backend, with IPv6 and fixed subnets
`172.29.69.0/24` and `fd6b:1e37:9a52:6969::/64`. `PROXY_TRUSTED_CIDRS` names
those networks: a PROXY header, and the stats API's `X-Forwarded-For`, are
honored only from them. `default` carries everything else. `docker-api` is
internal and holds only Alloy and the docker socket proxy.

| Layer | Limit | Where |
|---|---|---|
| nginx (stream) | 8 concurrent conns / client network, 64 / IPv6 /48 | `nginx.conf` |
| nginx (http) | 16 concurrent / network, 64 /48; 2 r/s + burst 10 on `/api/` | `nginx.conf` |
| PROXY header | only from `PROXY_TRUSTED_CIDRS` | `proxyListener` |
| listener | `2 x MAX_CONNECTIONS` | `netutil.LimitListener` |
| account slots | `MAX_CONNECTIONS`; a second connect displaces the first | `ssh.SessionTracker` |
| auth | `RATE_LIMIT_CONNECTIONS` / `RATE_LIMIT_WINDOW` per network, 16x that per IPv6 /48 | `ssh.rateLimitAuth` |
| registration | per window: 5 /64, 20 /48, 600 global; refunded if no account is created | `ssh.allowRegistration` |
| session input | 1 KiB/s after a 16 KiB burst; 32 KiB without a pause | `ssh.inputGuard` |
| terminal size | tiers 250x80 down to a 120x40 floor inside a 5M-cell budget | `internal/ssh/termsize.go` |
| stats API | `API_REQUESTS_PER_MINUTE` per network | `httpapi.withRateLimit` |
| disconnect grace | 90s mid-game; waiting seats leave immediately | `lobby.DisconnectPlayer` |

Network limits key on `ratelimit.NetKey` (IPv6 /64). Auth and registration also
spend `ratelimit.WideKey` (IPv6 /48).

## Invariants

1. Never publish `:6969` or `:6970`.
2. `sessionLifecycle` is outermost. Recover is a direct defer. The bubbletea path is `reportingModel`, and the notice goes to `s.Stderr()`.
3. A displaced session must not `LeaveLobby` or free the replacement's tracker slot. `Connect` closes the old connection outside the account lock, or, when the new session arrived on that same connection, only the old session: its program is ended and its channel closed. Teardown gives up the seat and the slot together under that lock (`ReleaseWith`). `ResumeSeat` runs only once the slot is owned, and the slot's user and generation are stored before it runs.
4. A mid-game drop calls `DisconnectPlayer`, not `LeaveLobby`. The hold is released on hand end and on shutdown, not only by its timer. A grace callback from an older arm does not expire a newer hold.
5. Finalize lives on the Manager and uses the snapshot taken at game start. Rules error, abandoned and shutdown are history without Elo. Interrupted writes only the leavers' losses.
6. Soft-deleted rankings must be revived before the seed, or finalize aborts.
7. Advisory locks are per seat, sorted by folded key, two-int4, and taken by finalize and erasure alike. The 24-hour damp count is per pair, across games.
8. Any view that subscribes implements `router.Closer`. The router and `releaseSession` call it.
9. Anything kept after `Frame` or `Sync` must be copied, not aliased.
10. Lock order is tracker account, manager, lobby, engine. `State` has no lock of its own.
11. A catalog entry is rules and view. The slug is persisted, so changing one is a data migration.
12. An accept-loop or API failure still runs `drainServer`.
13. Every screen fits 64x20. Measure with `AvailableContentHeight`.
14. Chips are conserved. A failure ends the match as a rules error; the next hand does not zero a pot that never paid out.
15. `TimeoutAction` must return a move the same package's `ValidateAction` accepts.
16. A rules panic ends the table, not the process. The recovers in `Start`, `SubmitAction`, `RemovePlayer` and `expireTurn` are one helper, `recoverRulesPanic`, deferred directly under the lock, and none asks the rules for standings. The idle-removal hook, the game watcher and the disconnect-grace timer recover on their own goroutines.
17. A seat keeps its deadline (10s floor) only while its turn carries on: `State.ContinueTurn` (gin's draw then discard), a refused auto-play, a leave elsewhere. Any other move starts a full clock, even for the same seat, and a miss is charged once per seat-turn.
18. Every TUI feed message carries the channel that delivered it. A view drops one stamped with any other.
19. The floor tier (120x40) times `MAX_CONNECTIONS`, plus `extraCellBudget`, has to fit `GOMEMLIMIT`, and `cellBudget` has to fit the cores. Raise one only with the others, after a load test.
