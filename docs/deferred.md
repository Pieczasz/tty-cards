# Deferred design work

Held here, not as `TODO` comments (`godox` fails the lint). Delete an entry in the same change that does the work.

| What | Trigger |
|---|---|
| Split domain types from the GORM models. The session identity is already mapped once (architecture invariant 20); the profile and leaderboard rows and `lobby.NewPlayer`'s argument are still GORM types | A second store, or a TUI bug from a missed `Preload` |
| Move presence and the session lifecycle out of `internal/ssh` | A second transport |
| A template-method framework for game views | A sixth game whose view is mostly hearts/gin plumbing |
| Split `internal/game` into an SPI package and the engine | Rules packages start needing engine internals, or a second engine |
| `Engine[S]` generics, or a typed `Extra` adapter | A sixth game |
| A write deadline on session output. A peer that stops granting ssh window holds its session's event loop in a render, unbounded while it keeps sending (each read restarts the idle timeout); resizes and the retier no longer wait on it | Sessions seen parked in a renderer write, or an account slot held by a peer that sends but never reads |
| Move test.yml's compose "Deployment checks" into something runnable locally (a script or `make` target); today they run only in CI | A deployment check that fails in CI and has to be debugged by pushing |
